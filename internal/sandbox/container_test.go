package sandbox

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// fakeContainer models a container with a known set of binaries, and records
// that the probe actually ran inside it.
func fakeContainer(present map[string]string, calls *[]string) ContainerRunner {
	return func(ctx context.Context, argv ...string) (string, error) {
		*calls = append(*calls, strings.Join(argv, " "))
		// argv is ("sh", "-c", "command -v '<bin>'"), so the binary is
		// whatever follows the prefix, minus the shell quoting.
		bin := ""
		if len(argv) >= 3 {
			bin = strings.Trim(strings.TrimPrefix(argv[2], "command -v "), "'")
		}
		if path, ok := present[bin]; ok {
			return path, nil
		}
		return "", errors.New("exit status 1")
	}
}

func TestAbsentInsideReportsAContainerMiss(t *testing.T) {
	var calls []string
	p, err := AbsentInside(context.Background(),
		fakeContainer(map[string]string{"git": "/usr/bin/git"}, &calls), "ripgrep")
	if err != nil {
		t.Fatalf("AbsentInside: %v", err)
	}
	if p.Found {
		t.Error("Found = true for a tool the container does not have")
	}
	if p.Where != "container" {
		t.Errorf("Where = %q, want container — a result must name the machine it came from", p.Where)
	}
	if len(calls) != 1 {
		t.Errorf("probe ran %d times, want 1: %v", len(calls), calls)
	}
}

func TestAbsentInsideReportsAContainerHit(t *testing.T) {
	var calls []string
	p, err := AbsentInside(context.Background(),
		fakeContainer(map[string]string{"jq": "/opt/homebrew/bin/jq"}, &calls), "jq")
	if err != nil {
		t.Fatalf("AbsentInside: %v", err)
	}
	if !p.Found {
		t.Error("Found = false for a tool the container does have")
	}
	if p.Evidence == "" {
		t.Error("Evidence is empty; a presence claim with no output is not checkable")
	}
}

// The negative control, and the reason this function exists. The HOST has the
// tool; the container does not. A host-side check would call this present and
// the whole run would be theatre. Measuring inside is the only thing that
// catches it.
func TestAbsentInsideIgnoresTheHostEntirely(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on this machine, so the contrast cannot be demonstrated")
	}
	var calls []string
	p, err := AbsentInside(context.Background(),
		fakeContainer(map[string]string{}, &calls), "git")
	if err != nil {
		t.Fatalf("AbsentInside: %v", err)
	}
	if p.Found {
		t.Error("Found = true because the HOST has git; the probe must only see the container")
	}
}

// A container that failed to start must not read as "the tool is absent".
// That false absence is the precondition for every later step being
// meaningless, so it is called out on its own.
func TestAbsentInsideRefusesToCallABrokenContainerAbsent(t *testing.T) {
	broken := func(ctx context.Context, argv ...string) (string, error) {
		return "docker: Cannot connect to the Docker daemon", errors.New("exit status 1")
	}
	p, err := AbsentInside(context.Background(), broken, "ripgrep")
	if err == nil {
		t.Fatalf("a dead container was reported as an absent tool: %+v", p)
	}
	if p.Found {
		t.Error("Found = true from a dead container")
	}
	if !strings.Contains(err.Error(), "Docker") {
		t.Errorf("error %q does not carry the underlying reason", err)
	}
}

func TestAbsentInsideValidatesItsArguments(t *testing.T) {
	if _, err := AbsentInside(context.Background(), nil, "git"); err == nil {
		t.Error("a nil runner was accepted")
	}
	if _, err := AbsentInside(context.Background(),
		func(context.Context, ...string) (string, error) { return "", nil }, "  "); err == nil {
		t.Error("an empty binary name was accepted")
	}
}

// A manifest is data, but data still should not be concatenated into a shell.
func TestShellQuoteNeutralisesQuoting(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"git", `'git'`},
		{"a b", `'a b'`},
		{"it's", `'it'\''s'`},
		{"; rm -rf /", `'; rm -rf /'`},
	} {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}
