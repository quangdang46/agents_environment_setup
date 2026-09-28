package installer

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ulikunitz/xz"
)

// ast-grep ships only .zip, and aadc only .tar.xz. The extractor handled
// gzip-tar only, which meant those two tools were uninstallable by any strategy.
// archive/zip is in the stdlib, so supporting zip costs no dependency and no
// new shell-out.

func TestExtractZip(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create("sg")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := f.Write([]byte("#!/bin/sh\n")); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "tool.zip")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write zip: %v", err)
	}

	dest := filepath.Join(dir, "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := extractArchive(archivePath, dest, "sg")
	if err != nil {
		t.Fatalf("extract zip: %v", err)
	}
	if filepath.Base(got) != "sg" {
		t.Errorf("extracted %q, want the file named sg", got)
	}
}

// A zip that slips a "../" entry past the extractor must be refused before
// anything is written. The tar path already checks names; zip must check them
// too, because a different container is not a different threat model.
func TestExtractZipRejectsTraversal(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create("../escape")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := f.Write([]byte("pwned")); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "tool.zip")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write zip: %v", err)
	}

	if _, err := extractArchive(archivePath, filepath.Join(dir, "dest"), "sg"); err == nil {
		t.Fatal("a zip entry escaping the destination was accepted")
	}
}

// A zip entry must not be able to reach outside destDir via an absolute path,
// and the extraction must not silently ignore a missing wanted binary.
func TestExtractZipMissingWant(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create("other")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := f.Write([]byte("x")); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "tool.zip")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write zip: %v", err)
	}

	if _, err := extractArchive(archivePath, filepath.Join(dir, "dest"), "sg"); err == nil {
		t.Fatal("a zip without the wanted binary was accepted")
	}
}

// aadc ships only .tar.xz. The extractor handled gzip-tar and zip, so aadc
// was uninstallable by any strategy. xz is not in the Go stdlib, so this
// needs a dependency — ulikunitz/xz, pure Go, no cgo, no transitive deps.

func TestExtractTarXz(t *testing.T) {
	t.Parallel()

	// Build a real .tar.xz in memory.
	var buf bytes.Buffer
	xw, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatalf("xz writer: %v", err)
	}
	tw := tar.NewWriter(xw)
	body := []byte("#!/bin/sh\n")
	if err := tw.WriteHeader(&tar.Header{Name: "aadc", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := xw.Close(); err != nil {
		t.Fatalf("xz close: %v", err)
	}

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "aadc.tar.xz")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write xz: %v", err)
	}

	dest := filepath.Join(dir, "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := extractArchive(archivePath, dest, "aadc")
	if err != nil {
		t.Fatalf("extract tar.xz: %v", err)
	}
	if filepath.Base(got) != "aadc" {
		t.Errorf("extracted %q, want the file named aadc", got)
	}
}

// A tar.xz that slips a "../" entry past the extractor must be refused.
func TestExtractTarXzRejectsTraversal(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	xw, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatalf("xz writer: %v", err)
	}
	tw := tar.NewWriter(xw)
	if err := tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o755, Size: 5, Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write([]byte("pwned")); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := xw.Close(); err != nil {
		t.Fatalf("xz close: %v", err)
	}

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "aadc.tar.xz")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write xz: %v", err)
	}

	if _, err := extractArchive(archivePath, filepath.Join(dir, "dest"), "aadc"); err == nil {
		t.Fatal("a tar.xz entry escaping the destination was accepted")
	}
}

// A tar.xz without the wanted binary must be refused, not silently accepted.
func TestExtractTarXzMissingWant(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	xw, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatalf("xz writer: %v", err)
	}
	tw := tar.NewWriter(xw)
	if err := tw.WriteHeader(&tar.Header{Name: "other", Mode: 0o755, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := xw.Close(); err != nil {
		t.Fatalf("xz close: %v", err)
	}

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "aadc.tar.xz")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write xz: %v", err)
	}

	if _, err := extractArchive(archivePath, filepath.Join(dir, "dest"), "aadc"); err == nil {
		t.Fatal("a tar.xz without the wanted binary was accepted")
	}
}
