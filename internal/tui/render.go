package tui

import (
	"fmt"
	"io"
	"strings"
)

// Render writes the current screen to w.
//
// Rendering is a pure function of the model so it can be tested without a
// terminal: the golden-style assertions below are what caught the first
// version's misaligned columns.
func (m *Model) Render(w io.Writer) {
	switch m.Screen {
	case ScreenTools:
		m.renderTools(w)
	case ScreenMenu:
		m.renderMenu(w)
	case ScreenProfiles:
		m.renderMenu(w)
	case ScreenDoctor:
		m.renderMessage(w, "Doctor reports on the environment. Run `aes doctor` for the full report.")
	case ScreenEnvironment:
		m.renderMessage(w, "Environment file. Run `aes env --write` to regenerate it.")
	}
	m.renderFooter(w)
}

func (m *Model) renderMenu(w io.Writer) {
	fmt.Fprintf(w, "\n  aes — agents environment setup\n\n")
	for i, item := range Menu {
		cursor := "  "
		if i == m.Cursor {
			cursor = "> "
		}
		fmt.Fprintf(w, "%s%-12s %s\n", cursor, item.Label, screenHint(item.Screen))
	}
}

func screenHint(s Screen) string {
	switch s {
	case ScreenMenu:
		return "the North Star: install and verify a complete environment"
	case ScreenTools:
		return "search and tick tools, then install the selection"
	case ScreenProfiles:
		return "browse the shipped profiles"
	case ScreenDoctor:
		return "environment health and drift"
	case ScreenEnvironment:
		return "the generated environment file"
	default:
		return ""
	}
}

func (m *Model) renderTools(w io.Writer) {
	fmt.Fprintf(w, "\n  Tools — %d of %d shown", len(m.Tools), len(m.All))
	if m.Filter != "" {
		fmt.Fprintf(w, "   filter: %s", m.Filter)
	}
	fmt.Fprintf(w, "\n\n")

	if len(m.Tools) == 0 {
		fmt.Fprintf(w, "  no tools match %q\n", m.Filter)
	} else {
		// The tick column is fixed width so the columns do not jump when
		// something is selected.
		const tickWidth = 2
		width := 0
		for _, t := range m.Tools {
			if len(t.Name) > width {
				width = len(t.Name)
			}
		}
		for i, t := range m.Tools {
			cursor := " "
			if i == m.Cursor {
				cursor = ">"
			}
			tick := " "
			if m.Selected[t.Name] {
				tick = "x"
			}
			fmt.Fprintf(w, " %s [%s] %-*s  %-10s %s\n",
				cursor, tick, width, t.Name, t.Category, truncate(t.Description, 48))
		}
	}
}

func (m *Model) renderMessage(w io.Writer, msg string) {
	fmt.Fprintf(w, "\n  %s\n\n  %s\n", m.Screen, msg)
}

func (m *Model) renderFooter(w io.Writer) {
	if m.Err != "" {
		fmt.Fprintf(w, "\n  error: %s\n", m.Err)
	} else if m.Result != "" {
		fmt.Fprintf(w, "\n  %s\n", m.Result)
	}

	if m.Screen == ScreenTools {
		fmt.Fprintf(w, "\n  %d selected: %s\n", len(m.Selected), m.Selection().String())
	}

	keys := make([]string, 0, len(KeyMap))
	for _, k := range KeyMap {
		keys = append(keys, k.Help)
	}
	fmt.Fprintf(w, "\n  %s\n", strings.Join(keys, "   "))
	fmt.Fprintf(w, "  esc back/clear search   q quit\n")
}

// truncate shortens s to at most n runes, marking that it was cut. A hard
// byte cut can split a UTF-8 rune and produce mojibake in the middle of a
// table, which is worse than losing the tail.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}
