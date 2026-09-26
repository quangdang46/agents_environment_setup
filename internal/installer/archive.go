package installer

import (
	"archive/tar"
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

// extractArchive unpacks a gzipped tar into destDir and returns the path of
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
func extractArchive(archivePath, destDir, want string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

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
