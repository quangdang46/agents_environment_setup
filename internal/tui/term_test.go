package tui

import (
	"bufio"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A single keypress must not wait for bytes that will never come.
//
// readKey used to Peek(3) unconditionally, so a lone `q` sat in the buffer
// while Peek blocked for two more bytes — and the call loop only checks
// cancellation BETWEEN completed reads, so a cancelled context waited with
// it. This is what makes the TUI feel stuck rather than responsive.
func TestReadKeyDoesNotBlockOnASingleKeypress(t *testing.T) {
	t.Parallel()

	r := bufio.NewReader(strings.NewReader("q"))

	done := make(chan struct{})
	var got Key
	var gotR rune
	var n int
	go func() {
		got, gotR, n = readKey(r)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readKey blocked on a single keypress; it is waiting for bytes " +
			"that were never sent")
	}
	if n == 0 {
		t.Fatal("readKey consumed nothing")
	}
	if got != KeyRune || gotR != 'q' {
		t.Errorf("readKey = (%v, %q, %d), want (KeyRune, 'q', 1)", got, gotR, n)
	}
}

// A bare ESC is a real keypress. It must resolve to KeyEsc rather than
// blocking on the two bytes that would make it an arrow key — which may never
// arrive, because a user pressing Escape expects the TUI to react now.
func TestReadKeyResolvesABareEscape(t *testing.T) {
	t.Parallel()

	r := bufio.NewReader(strings.NewReader("\x1b"))

	done := make(chan struct{})
	var got Key
	var n int
	go func() {
		got, _, n = readKey(r)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readKey blocked on a bare Escape; it is waiting for a sequence " +
			"that was never sent")
	}
	if n == 0 {
		t.Fatal("readKey consumed nothing for a bare Escape")
	}
	if got != KeyEsc {
		t.Errorf("readKey = %v, want %v for a bare Escape", got, KeyEsc)
	}
}

// An arrow key is the case the Peek(3) was FOR. It must still decode, so the
// fix did not break the thing it replaced.
func TestReadKeyStillDecodesArrowKeys(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   string
		want Key
	}{
		{"up", "\x1b[A", KeyUp},
		{"down", "\x1b[B", KeyDown},
		{"application-mode up", "\x1bOA", KeyUp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := bufio.NewReader(strings.NewReader(tc.in))
			got, _, n := readKey(r)
			if n == 0 {
				t.Fatalf("readKey consumed nothing for %q", tc.in)
			}
			if got != tc.want {
				t.Errorf("readKey = %v, want %v for %q", got, tc.want, tc.in)
			}
		})
	}
}

// runStty must be bounded. These were bare exec.Command calls with no context
// and no deadline, so a wedged stty hung `aes` startup — and bare `aes`
// attempts the TUI before falling back to help, so it made every command
// unusable rather than just the interactive one.
func TestRunSttyIsBounded(t *testing.T) {
	if _, err := exec.LookPath("stty"); err != nil {
		t.Skipf("stty is not installed: %v", err)
	}
	// A healthy stty answers immediately; the assertion is that this returns
	// at all, and well inside the budget rather than blocking on the terminal.
	start := time.Now()
	if _, err := runStty("-g"); err != nil {
		t.Skipf("no controlling terminal here: %v", err)
	}
	if elapsed := time.Since(start); elapsed > sttyTimeout {
		t.Errorf("runStty took %s, past its %s budget", elapsed, sttyTimeout)
	}
}
