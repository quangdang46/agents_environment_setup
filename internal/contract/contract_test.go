// Package contract holds tests that enforce invariants spanning several
// packages.
//
// These do not belong to any one package because they are about the whole
// tool: a claim like "aes never writes outside ~/.aes" is a property of the
// code as a whole, and putting it in one package would make it look like
// that package's job when it is nobody's.
package contract

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// forbiddenDir is the directory invariant I7 forbids aes from touching: it
// belongs to the ACFS project, and this project is explicitly not allowed to
// become ACFS 2.0.
const forbiddenDir = ".agents"

// repoRoot is the module root, two levels up from this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

// TestNoSourceReferencesTheForbiddenDir is the static half of I7.
//
// Nothing in the tree currently names this directory, which is exactly why
// the invariant has held: by absence. A behavioural test cannot distinguish
// "nothing writes there" from "nothing was ever going to", and a regression
// would be invisible until it shipped. So the cheap structural check comes
// first — if a future contributor reaches for it, this fails immediately and
// names the file.
//
// It parses rather than greps, for the reason the TUI import guard does: a
// comment mentioning the directory is as likely as code, and only one of them
// is a violation.
func TestNoSourceReferencesTheForbiddenDir(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	fset := token.NewFileSet()
	checked := 0

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// .beads is task data, not source. .git and dist are not ours.
			switch info.Name() {
			case ".git", ".beads", "dist", "tools", "profiles", "docs":
				return filepath.SkipDir
			}
			return nil
		}
		name := info.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			return nil
		}
		checked++
		rel, _ := filepath.Rel(root, path)

		// An import path is the other way the directory could be named.
		for _, imp := range f.Imports {
			if v, err := strconv.Unquote(imp.Path.Value); err == nil && strings.Contains(v, forbiddenDir) {
				t.Errorf("%s imports %q; invariant I7 forbids aes from touching it", rel, forbiddenDir)
			}
		}

		// Check string literals and comments by reading the source: a path
		// can be built from a constant rather than an import.
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue // prose may legitimately mention it
			}
			if strings.Contains(line, forbiddenDir) {
				t.Errorf("%s:%d references %q; invariant I7 forbids aes from touching it",
					rel, i+1, forbiddenDir)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if checked == 0 {
		t.Fatal("no source files were checked; the guard is vacuous")
	}
}

// TestCommandsLeaveTheForbiddenDirUntouched is the behavioural half: build
// the real binary, run every command against a throwaway HOME that has the
// forbidden directory seeded, and assert it comes out byte-identical.
//
// The static check proves no one *names* the directory. This proves the
// property that actually matters — that a run does not create, modify, or
// delete anything there — which survives a path assembled at runtime.
func TestCommandsLeaveTheForbiddenDirUntouched(t *testing.T) {
	// Not parallel: it builds a binary and needs a stable temp HOME.
	if testing.Short() {
		t.Skip("builds the binary; skipped under -short")
	}

	root := repoRoot(t)
	bin := filepath.Join(t.TempDir(), "aes")
	build := exec.Command("go", "build", "-o", bin, "./cmd/aes")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	home := t.TempDir()
	// Seed the directory with something a run would be tempted to clobber.
	forbidden := filepath.Join(home, forbiddenDir)
	if err := os.MkdirAll(forbidden, 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sentinel := filepath.Join(forbidden, "preexisting.txt")
	const contents = "this file belongs to another project\n"
	if err := os.WriteFile(sentinel, []byte(contents), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before := snapshot(t, forbidden)

	// Every command that can write, with a catalog present so the resolve
	// path actually runs.
	catalogRoot := filepath.Join(home, "catalog")
	writeTool(t, catalogRoot)

	for _, args := range [][]string{
		{"list", "--json"},
		{"verify", "--json"},
		{"doctor", "--json"},
		{"env", "--write"},
		{"setup", "--dry-run", "--only", "demo"},
		{"setup", "--non-interactive", "--only", "demo"},
		{"forget", "demo"},
		{"uninstall", "demo"},
	} {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "HOME="+home)
		// A nil TTY so nothing prompts or blocks.
		cmd.Stdin = nil
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		// The exit code is irrelevant: some of these are expected to fail.
		// What matters is that none of them touched the other directory.
		_ = cmd.Run()

		if after := snapshot(t, forbidden); after != before {
			t.Errorf("aes %s changed %s:\nbefore: %q\nafter:  %q",
				strings.Join(args, " "), forbidden, before, after)
		}
	}

	// And the sentinel is still exactly as written.
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("sentinel was removed: %v", err)
	}
	if string(got) != contents {
		t.Errorf("sentinel contents changed:\ngot:  %q\nwant: %q", got, contents)
	}
}

// TestAESHomeDefaultsUnderDotAES is the other half of the boundary claim:
// every write aes makes lives under $AES_HOME, and the default for that is
// ~/.aes — never ~/.agents.
func TestAESHomeDefaultsUnderDotAES(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join(repoRoot(t), "cmd", "aes", "main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	src := string(data)

	if !strings.Contains(src, `".aes"`) {
		t.Error("main.go no longer names the default .aes home; the boundary has moved")
	}
	// A home built from the user's home directory must be a single path
	// component below it. Joining with a bare directory name is how a
	// sibling project would get clobbered.
	if !strings.Contains(src, `filepath.Join(home, ".aes")`) &&
		!strings.Contains(src, `filepath.Join(userHome, ".aes")`) {
		t.Error("the default home is not a literal .aes component; it may be configurable to anything")
	}
}

// snapshot records a tree's paths and contents, so any change shows.
func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		b.WriteString(rel)
		if info.IsDir() {
			b.WriteString("/\n")
			return nil
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			b.WriteString(" <unreadable>\n")
			return nil
		}
		b.WriteString(" " + string(data) + "\n")
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return b.String()
}

// writeTool puts one valid manifest in the catalog so commands that resolve
// have something to resolve.
func writeTool(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "utility", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const body = `name: demo
description: fixture tool
category: utility
provides: [demo-absent]
verify:
  command: demo-absent --version
install:
  darwin:
    strategy: go
    go_package: example.com/demo@v1.0.0
  linux:
    strategy: go
    go_package: example.com/demo@v1.0.0
`
	if err := os.WriteFile(filepath.Join(dir, "tool.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
