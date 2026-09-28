package installer

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxArchiveEntryBytes caps a single decompressed entry. A release asset that
// expands to gigabytes is a decompression bomb, not a tool.
const maxArchiveEntryBytes = 512 << 20 // 512 MiB

// extractArchive unpacks a release asset into destDir and returns the path of
// the extracted file named want.
//
// Every entry is checked before anything is written, and the check is on the
// entry name rather than on the resolved destination. An archive that can
// write outside destDir is a code-execution vector, not a cosmetic problem:
// "../" in a release asset is the whole attack.
//
// want is matched against the base name, so an archive that wraps its
// contents in a version directory ("aes-1.0.0-linux-amd64/aes") extracts the
// same as a flat one.
//
// Format is detected from the content, not the filename. ast-grep ships only
// .zip and aadc only .tar.xz; the strategy set is closed, so a tool AES cannot
// unpack is a tool it cannot install, and renaming the asset in the manifest
// would not change what the bytes are. archive/zip is stdlib; the xz case is
// still refused, because a pure-Go xz decoder is a dependency AES does not
// have, and a shell-out to `tar` is the arbitrary-shell thing I3 forbids.
func extractArchive(archivePath, destDir, want string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	if isZip(archivePath) {
		return extractZip(archivePath, destDir, want)
	}
	return extractTarGz(f, destDir, want)
}

// isZip reports whether the file is a zip container.
//
// The name is checked first as a fast path, and the magic bytes decide when
// the name says nothing. A manifest can name the asset; it cannot lie about
// what the bytes are, and the bytes are what we extract.
func isZip(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	// "PK\x03\x04" for a local file header, "PK\x05\x06" for an empty archive.
	return magic[0] == 'P' && magic[1] == 'K' &&
		(magic[2] == 0x03 || magic[2] == 0x05 || magic[2] == 0x07)
}

// extractZip unpacks a zip, refusing any entry that would escape destDir.
func extractZip(archivePath, destDir, want string) (string, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()

	// Every entry is validated BEFORE the first byte is written, so a hostile
	// archive is refused whole rather than unpacked until it reaches the bad one.
	for _, f := range zr.File {
		if err := validateArchiveEntry(f.Name); err != nil {
			return "", err
		}
	}

	found := ""
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || filepath.Base(f.Name) != want {
			continue
		}
		target := filepath.Join(destDir, filepath.Clean(f.Name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("read zip entry %s: %w", f.Name, err)
		}
		// LimitReader for the same reason the tar path uses one: a lying
		// header must not fill the disk.
		// The zip store mode (0) is the only one archive/zip will decompress in
		// pure Go; deflate is handled by the stdlib, everything else is refused
		// by f.Open below.
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			rc.Close()
			return "", fmt.Errorf("create %s: %w", target, err)
		}
		_, copyErr := io.Copy(out, io.LimitReader(rc, maxArchiveEntryBytes+1))
		rc.Close()
		closeErr := out.Close()
		if copyErr != nil {
			return "", fmt.Errorf("extract %s: %w", f.Name, copyErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close %s: %w", target, closeErr)
		}
		if info, err := os.Stat(target); err == nil && info.Size() > maxArchiveEntryBytes {
			return "", fmt.Errorf("archive entry %s exceeds %d bytes", f.Name, maxArchiveEntryBytes)
		}
		found = target
	}

	if found == "" {
		return "", fmt.Errorf("%w: %q", ErrBinaryMissing, want)
	}
	return found, nil
}

// extractTarGz unpacks a gzipped tar.
func extractTarGz(f *os.File, destDir, want string) (string, error) {
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()

	if err := checkArchiveEntries(tar.NewReader(gz)); err != nil {
		return "", err
	}

	// Re-open: the safety pass consumed the stream, and tar readers are
	// strictly forward-only. Two passes over a few hundred KB is cheaper
	// than buffering the whole thing to memory.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind archive: %w", err)
	}
	gz, err = gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()

	found := ""
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read archive: %w", err)
		}
		// Already validated above; re-checked so this function is safe to
		// call on its own.
		if err := validateArchiveEntry(hdr.Name); err != nil {
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.Base(hdr.Name) != want {
			continue
		}

		target := filepath.Join(destDir, filepath.Clean(hdr.Name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return "", fmt.Errorf("create %s: %w", target, err)
		}
		// LimitReader keeps a lying header from filling the disk. The
		// extra byte turns an exactly-at-limit entry into a clean error
		// rather than a silent truncation.
		_, copyErr := io.Copy(out, io.LimitReader(tr, maxArchiveEntryBytes+1))
		closeErr := out.Close()
		if copyErr != nil {
			return "", fmt.Errorf("extract %s: %w", hdr.Name, copyErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close %s: %w", target, closeErr)
		}
		if info, err := os.Stat(target); err == nil && info.Size() > maxArchiveEntryBytes {
			return "", fmt.Errorf("archive entry %s exceeds %d bytes", hdr.Name, maxArchiveEntryBytes)
		}
		found = target
	}

	if found == "" {
		return "", fmt.Errorf("%w: %q", ErrBinaryMissing, want)
	}
	return found, nil
}

// checkArchiveEntries walks the whole archive and rejects it if any entry is
// unsafe. Doing this as a separate pass means a hostile archive is refused
// before a single byte reaches the filesystem.
func checkArchiveEntries(tr *tar.Reader) error {
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if err := validateArchiveEntry(hdr.Name); err != nil {
			return err
		}
	}
}

// validateArchiveEntry rejects any entry that is absolute, escapes the
// destination, or looks like a tar option.
func validateArchiveEntry(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: empty entry name", ErrUnsafeArchive)
	case strings.HasPrefix(name, "/"):
		return fmt.Errorf("%w: %q is an absolute path", ErrUnsafeArchive, name)
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("%w: %q begins with a dash", ErrUnsafeArchive, name)
	}
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == ".." {
			return fmt.Errorf("%w: %q contains a parent-directory component", ErrUnsafeArchive, name)
		}
	}
	return nil
}
