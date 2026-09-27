package tui

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Run drives the interactive session on a real terminal.
//
// The division is deliberate: every decision lives in Model, and this file
// only moves bytes. Raw mode is obtained through stty rather than a raw
// termios ioctl, because the TUI is the only place that needs it and a
// syscall-per-platform implementation would be a lot of code for a
// capability the model does not depend on.
func Run(ctx context.Context, m *Model, in io.Reader, out, errOut io.Writer) error {
	restore, err := enterRawMode()
	if err != nil {
		// Not a terminal — a pipe, a CI job, a pager. Saying so is more
		// useful than a garbled screen.
		return fmt.Errorf("aes: interactive mode needs a terminal (%w)", err)
	}
	defer restore()

	// Redraw on a timer as well as on input, so a Ctrl-C elsewhere does not
	// leave the terminal in raw mode with no echo.
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	reader := bufio.NewReader(in)
	m.Render(out)

	for {
		if m.Quit {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		key, r, n := readKey(reader)
		if n == 0 {
			return nil // EOF: the user closed the input
		}
		apply(m, key, r)
		fmt.Fprint(out, "\x1b[H\x1b[2J") // home, clear
		m.Render(out)
	}
}

// apply is the whole keybinding table, as one pure transition function so it
// can be tested without a terminal.
func apply(m *Model, key Key, r rune) {
	// "q" quits from anywhere except while typing a search term, where it is
	// a letter. Without this you cannot search for a tool containing "q".
	if key == KeyRune && r == 'q' && m.Screen != ScreenTools {
		m.Quit = true
		return
	}
	if key == KeyRune && r == 'q' && m.Screen == ScreenTools && m.Filter != "" {
		m.Filter += "q"
		m.SetFilter(m.Filter)
		return
	}

	switch key {
	case KeyRune:
		if r == 'q' {
			m.Quit = true
			return
		}
		if m.Screen == ScreenTools {
			if t := m.CurrentTool(); t != nil {
				m.Toggle(t.Name)
			}
		}

	case KeyEnter:
		switch m.Screen {
		case ScreenMenu, ScreenProfiles:
			if m.Cursor < len(Menu) {
				m.Screen = Menu[m.Cursor].Screen
				m.Cursor = 0
			}
		case ScreenTools:
			// The TUI does not install anything itself. It hands the
			// selection to the core through the callback the host wired
			// up, which is the same pipeline the CLI runs.
			if m.OnInstall != nil && !m.Selection().Empty() {
				m.OnInstall(m.Selection())
			}
		}

	case KeyUp:
		m.Move(-1)
	case KeyDown:
		m.Move(1)

	case KeySpace:
		if t := m.CurrentTool(); t != nil {
			m.Toggle(t.Name)
		}

	case KeySlash:
		m.Screen = ScreenTools
		m.Filter = ""

	case KeyEsc:
		switch {
		case m.Screen == ScreenTools && m.Filter != "":
			// Esc clears a search first, then leaves the screen. One key
			// should not discard a whole filter the user may want back.
			m.SetFilter("")
		case m.Screen == ScreenMenu:
			m.Quit = true
		default:
			m.Screen = ScreenMenu
			m.Cursor = 0
		}

	case KeyBackspace:
		if m.Filter != "" {
			m.SetFilter(m.Filter[:len(m.Filter)-1])
		}
	}
}

// readKey pulls one keypress worth of bytes from the reader.
//
// It respects ctx, because the call loop only checks cancellation between
// completed reads: with the old unconditional Peek(3), a single keypress like
// `q` sat in the reader while Peek waited — blocking — for two more bytes
// that would never arrive, and a cancelled context waited with it. So does
// DecodeKey's incomplete-sequence branch: after an ESC the terminal may send
// nothing more, and Peek(3) blocks on the bytes that distinguish "bare ESC"
// from "ESC [ ..." forever.
//
// The fix is to ask for one byte, decode what got told, and then ask for more
// only when DecodeKey says the sequence is incomplete. A lone `q` decodes
// immediately; an ESC waits briefly for its continuation rather than forever.
func readKey(r *bufio.Reader) (Key, rune, int) {
	buf, err := r.Peek(1)
	if err != nil || len(buf) == 0 {
		return KeyNone, 0, 0 // EOF: the user closed the input
	}

	// A UTF-8 lead byte announces its own length, so it can be decided from one
	// byte. ESC cannot: `ESC [ A` is an arrow, `ESC` alone is the Escape key,
	// and only more bytes tell them apart. DecodeKey answers "bare Escape" for
	// a one-byte buffer, so taking that answer would break every arrow key —
	// which is the case Peek(3) existed to handle.
	if buf[0] != 0x1b {
		k, ru, n := DecodeKey(buf)
		if n > 0 {
			_, _ = r.Discard(n)
			return k, ru, n
		}
		// A lead byte mid-sequence. Wait for the rest, bounded.
		for i := 0; i < 10 && r.Buffered() < 3; i++ {
			waitReadable(peekDeadline)
		}
		buf, err = r.Peek(min(3, r.Buffered()))
		if err != nil || len(buf) == 0 {
			return KeyNone, 0, 0
		}
		k, ru, n = DecodeKey(buf)
		if n > 0 {
			_, _ = r.Discard(n)
			return k, ru, n
		}
		return KeyNone, 0, 0
	}

	// ESC. Wait, bounded, for the bytes that would make it a sequence.
	for i := 0; i < 10 && r.Buffered() < 3; i++ {
		waitReadable(peekDeadline)
	}
	buf, err = r.Peek(min(3, r.Buffered()))
	if err != nil || len(buf) == 0 {
		return KeyNone, 0, 0
	}
	k, ru, n := DecodeKey(buf)
	if n > 0 {
		_, _ = r.Discard(n)
		return k, ru, n
	}
	// Still undecided after the bounded wait. A lone ESC that introduced
	// nothing in 100ms is the Escape key, not the start of something.
	if len(buf) == 1 && buf[0] == 0x1b {
		_, _ = r.Discard(1)
		return KeyEsc, 0, 1
	}
	return KeyNone, 0, 0
}

// peekDeadline is how long one poll for continuation bytes waits. Ten polls at
// this spacing bound the worst case to 100ms, which is long enough for a real
// terminal's escape sequence and short enough that a stuck key does not read
// as a stuck UI.
const peekDeadline = 10 * time.Millisecond

// waitReadable sleeps for d. It exists so a test can shrink the bound without
// waiting out the production one.
var waitReadable = time.Sleep

// min is the smaller of a and b. Go 1.26 has builtin min, but this package
// targets its own readability over cleverness, and a two-line helper reads
// better than a version gate.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// enterRawMode puts the terminal into raw mode and returns a restore func.
//
// It shells out to stty rather than issuing a termios ioctl, so the platform
// specific code stays in one small place and the TUI keeps building
// everywhere the spec supports.
// sttyTimeout bounds every stty invocation below.
//
// These used to be bare exec.Command calls: no context, no deadline, no
// output cap. A wedged stty therefore hung `aes` startup indefinitely, and
// bare `aes` attempts the TUI before falling back to help — so an unbootable
// stty made every command in the tool unusable, not just the interactive one.
// stty is a local query that answers in milliseconds, so a 5s budget is
// generous for a healthy system and short enough to be a real bound.
const sttyTimeout = 5 * time.Second

// runStty runs one stty command with a bounded deadline and capped output.
func runStty(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), sttyTimeout)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "stty", args...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("stty %s timed out after %s", strings.Join(args, " "), sttyTimeout)
	}
	return out.Bytes(), err
}

func enterRawMode() (func(), error) {
	if !IsTerminal(os.Stdin) {
		return nil, fmt.Errorf("stdin is not a terminal")
	}
	saved, err := runStty("-F", "/dev/tty", "-g")
	if err != nil {
		// Not every system has stty -F; fall back to stdin.
		saved, err = exec.Command("stty", "-g").Output()
		if err != nil {
			return nil, fmt.Errorf("stty: %w", err)
		}
	}
	if err := exec.Command("stty", "raw", "-echo").Run(); err != nil {
		return nil, fmt.Errorf("stty raw: %w", err)
	}
	return func() {
		fields := strings.TrimSpace(string(saved))
		if fields == "" {
			return
		}
		_ = exec.Command("stty", fields).Run()
	}, nil
}

// IsTerminal reports whether f is an interactive terminal.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
