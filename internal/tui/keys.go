package tui

// Key is a decoded keypress. The TUI's keybindings are locked by the spec, so
// they are decoded here rather than compared as raw bytes at each call site.
type Key int

const (
	KeyNone Key = iota
	KeyUp
	KeyDown
	KeyEnter
	KeySpace
	KeySlash
	KeyEsc
	KeyBackspace
	KeyRune
)

// KeyMap documents the bindings. It is exported so `--help` and the rendered
// footer cannot disagree with the decoder.
var KeyMap = []struct {
	Key  Key
	Help string
}{
	{KeyUp, "↑/↓ navigate"},
	{KeySpace, "space toggle"},
	{KeySlash, "/ search"},
	{KeyEnter, "enter install"},
	{KeyEsc, "esc back"},
	{KeyRune, "q quit"},
}

// DecodeKey decodes one keypress from a byte slice.
//
// It returns the number of bytes consumed, so a caller can read an escape
// sequence and a multi-byte rune correctly. Terminal input is a byte stream
// with no framing, and the two ambiguous cases are handled explicitly:
//
//   - ESC alone versus the start of an arrow-key sequence. A lone ESC is
//     returned immediately; CSI sequences (ESC [ A) are consumed whole.
//   - A leading byte with the high bit set is a UTF-8 rune, not a control
//     character, so "é" does not decode as a modifier.
func DecodeKey(buf []byte) (Key, rune, int) {
	if len(buf) == 0 {
		return KeyNone, 0, 0
	}

	b := buf[0]
	switch {
	case b == 0x1b: // ESC
		// ESC [ A/B/C/D are the arrows. A bare ESC, or ESC followed by
		// something else, is the Escape key.
		if len(buf) >= 3 && buf[1] == '[' {
			switch buf[2] {
			case 'A':
				return KeyUp, 0, 3
			case 'B':
				return KeyDown, 0, 3
			}
			// Another CSI final byte: consume it so it is not decoded as
			// runes on the next pass.
			return KeyNone, 0, 3
		}
		if len(buf) >= 2 && buf[1] == 'O' && len(buf) >= 3 {
			// ESC O A — application cursor mode, some terminals send this.
			switch buf[2] {
			case 'A':
				return KeyUp, 0, 3
			case 'B':
				return KeyDown, 0, 3
			}
			return KeyNone, 0, 3
		}
		return KeyEsc, 0, 1

	case b == '\r' || b == '\n':
		return KeyEnter, 0, 1
	case b == ' ':
		return KeySpace, 0, 1
	case b == '/':
		return KeySlash, 0, 1
	case b == 0x7f || b == 0x08:
		return KeyBackspace, 0, 1

	case b >= 0x20 && b < 0x7f:
		return KeyRune, rune(b), 1

	case b >= 0x80:
		// UTF-8 continuation or lead byte. A lead byte announces its own
		// length, so consume the right number of bytes rather than
		// treating them as separate keys.
		n := 1
		switch {
		case b&0xe0 == 0xc0:
			n = 2
		case b&0xf0 == 0xe0:
			n = 3
		case b&0xf8 == 0xf0:
			n = 4
		}
		if len(buf) < n {
			return KeyNone, 0, len(buf) // incomplete; wait for more
		}
		r := []rune(string(buf[:n]))
		if len(r) == 1 {
			return KeyRune, r[0], n
		}
		return KeyRune, 0, n

	default:
		return KeyNone, 0, 1
	}
}
