package tui

import (
	"bufio"
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
func readKey(r *bufio.Reader) (Key, rune, int) {
	// Peek so a partial escape sequence is not mistaken for a bare Escape.
	buf, err := r.Peek(3)
	if err != nil && len(buf) == 0 {
		return KeyNone, 0, 0
	}
	k, ru, n := DecodeKey(buf)
	if n == 0 {
		return KeyNone, 0, 0
	}
	_, _ = r.Discard(n)
	return k, ru, n
}

// enterRawMode puts the terminal into raw mode and returns a restore func.
//
// It shells out to stty rather than issuing a termios ioctl, so the platform
// specific code stays in one small place and the TUI keeps building
// everywhere the spec supports.
func enterRawMode() (func(), error) {
	if !IsTerminal(os.Stdin) {
		return nil, fmt.Errorf("stdin is not a terminal")
	}
	saved, err := exec.Command("stty", "-F", "/dev/tty", "-g").Output()
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
