package progress

import (
	"bytes"
	"strings"
	"testing"
)

// A non-TTY run must be pure text: no carriage returns, no clear-line
// sequences, no colour. This is what a log file contains, and the property
// is what makes `grep '\[7/17\]' setup-*.log` answer correctly.
func TestNonTTYIsPlainText(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := New(&buf, Options{TTY: false})

	p.Section("Installing %d tools", 2)
	p.Step(1, 2, "Installing %s", "ripgrep")
	p.Step(2, 2, "Installing %s", "fzf")
	p.Success("%s installed", "ripgrep")

	got := buf.String()
	for _, ctrl := range []string{"\r", "\033[", "\033"} {
		if strings.Contains(got, ctrl) {
			t.Errorf("non-TTY output contains %q:\n%q", ctrl, got)
		}
	}
	want := []string{"═══ Installing 2 tools ═══", "[1/2] Installing ripgrep", "[2/2] Installing fzf", "✓ ripgrep installed"}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("output missing %q:\n%s", w, got)
		}
	}
}

// On a TTY the progress line is rewritten in place, which is the only reason
// a 40-tool run does not scroll the terminal.
func TestTTYBarRendersInPlace(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := New(&buf, Options{TTY: true})

	p.Step(7, 17, "Installing %s", "vault")
	got := buf.String()
	for _, want := range []string{"\r\033[K", "7/17", "(41%)", "██████"} {
		if !strings.Contains(got, want) {
			t.Errorf("TTY step output missing %q:\n%q", want, got)
		}
	}

	// A Success clears the bar before printing, so the final line is what
	// stays on screen.
	buf.Reset()
	p.Success("%s installed", "vault")
	if !strings.HasPrefix(buf.String(), "\r\033[K") {
		t.Errorf("Success did not clear the in-place line:\n%q", buf.String())
	}
}

func TestBarCells(t *testing.T) {
	t.Parallel()
	if got := bar(17, 17); got != strings.Repeat("█", barWidth) {
		t.Errorf("bar(17,17) = %q, want full", got)
	}
	if got := bar(0, 17); got != strings.Repeat("░", barWidth) {
		t.Errorf("bar(0,17) = %q, want empty", got)
	}
	if got := bar(7, 17); len([]rune(got)) != barWidth {
		t.Errorf("bar(7,17) width = %d, want %d", len([]rune(got)), barWidth)
	}
	if got := bar(1, 0); got != strings.Repeat("░", barWidth) {
		t.Errorf("bar(1,0) = %q, want empty (no divide by zero)", got)
	}
}

// Colour is opt-out in three independent ways, and each must win. The
// assertion is on a colour escape specifically: a TTY with colour disabled
// still writes \r\033[K to manage the in-place line, which is layout rather
// than decoration.
func TestColourIsOptOut(t *testing.T) {
	cases := []struct {
		name    string
		options Options
		envNO   string
		envTERM string
	}{
		{name: "piped", options: Options{TTY: false}},
		{name: "no_color", options: Options{TTY: true}, envNO: "1"},
		{name: "dumb_term", options: Options{TTY: true}, envTERM: "dumb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tc.envNO)
			t.Setenv("TERM", tc.envTERM)

			var buf bytes.Buffer
			New(&buf, tc.options).Success("done")
			for _, code := range []string{ansiReset, ansiBlue, ansiGreen, ansiYellow, ansiRed, ansiGray} {
				if strings.Contains(buf.String(), code) {
					t.Errorf("colour code %q emitted for %s:\n%q", code, tc.name, buf.String())
				}
			}
		})
	}
}

func TestTTYUsesColour(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	var buf bytes.Buffer
	New(&buf, Options{TTY: true}).Success("done")
	if !strings.Contains(buf.String(), ansiGreen) {
		t.Errorf("TTY output should carry colour:\n%q", buf.String())
	}
}

// Detail is silenced unless verbose asked for it. A default run must not
// become noisy because a caller logged a detail unconditionally.
func TestDetailRequiresVerbose(t *testing.T) {
	t.Parallel()
	for _, verbose := range []bool{false, true} {
		var buf bytes.Buffer
		New(&buf, Options{Verbose: verbose}).Detail("fetching %s", "vault")

		got := buf.String()
		if verbose != (got != "") {
			t.Errorf("verbose=%v produced %q", verbose, got)
		}
	}
}

func TestQuietSuppressesEverything(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := New(&buf, Options{TTY: true, Verbose: true, Quiet: true})

	p.Section("s")
	p.Step(1, 1, "x")
	p.Detail("d")
	p.Spin("sp")
	p.Success("ok")
	p.Warn("w")
	p.Fail("f")
	p.Info("i")
	p.Box("t", "b")

	if buf.Len() != 0 {
		t.Errorf("quiet wrote %q, want nothing", buf.String())
	}
}

// A caller with no writer is the common case (a library user, or a test), and
// must not panic on any method.
func TestNilWriterIsNoOp(t *testing.T) {
	t.Parallel()
	p := New(nil, Options{TTY: true, Verbose: true, Quiet: false})

	p.Section("s")
	p.Step(1, 2, "x")
	p.Detail("d")
	p.Spin("sp")
	p.Success("ok")
	p.Warn("w")
	p.Fail("f")
	p.Info("i")
	p.Box("t", "b")
}

// Detail and Warn clear an in-place line before printing, otherwise the bar
// is stranded mid-terminal.
func TestDetailClearsInPlaceLine(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := New(&buf, Options{TTY: true, Verbose: true})

	p.Step(3, 10, "Installing %s", "vault")
	buf.Reset()
	p.Detail("attempt %d of %d", 2, 3)

	if !strings.HasPrefix(buf.String(), "\r\033[K") {
		t.Errorf("Detail did not clear the in-place line:\n%q", buf.String())
	}
}

func TestSpinReportsState(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := New(&buf, Options{TTY: true})

	p.Spin("installing %s", "tmux")
	if !p.Spinning() {
		t.Error("Spinning() = false after Spin, want true")
	}
	p.Success("done")
	if p.Spinning() {
		t.Error("Spinning() = true after Success, want false")
	}
}

// A long tool name must not shift the whole line, which is what fixed-width
// truncation is for.
func TestFormatNameTruncates(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", nameMax+10)
	if got := formatName(long); len(got) > nameMax {
		t.Errorf("formatName len = %d, want <= %d", len(got), nameMax)
	}
	if got := formatName("ripgrep"); got != "ripgrep" {
		t.Errorf("formatName(ripgrep) = %q", got)
	}
}
