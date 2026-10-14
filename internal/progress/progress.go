// Package progress renders human-readable install progress on stderr.
//
// The contract this serves is in docs/COMPREHENSIVE_FOR_AES.md: stdout
// carries exactly one JSON document under --json, so nothing here ever
// writes to stdout. Everything a human watches while a multi-minute
// install runs goes to stderr, and the fallback shape is what lands in a
// log file: plain line-by-line text with no control codes.
//
// The design mirrors the reference bash installer's log_step/log_detail/
// log_success (agentic_coding_flywheel_setup/install.sh), specifically its
// degradation rule: on a TTY the progress line is updated in place, and
// without one the same information is printed as a line. A run piped to a
// file or read by an agent gets the second form, so logs stay greppable.
package progress

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	// barWidth is the cell count of the in-place bar. Fixed rather than
	// terminal-width-derived so the rendering is deterministic and testable.
	barWidth = 20

	// nameMax is the truncation length for a tool name in the bar line.
	nameMax = 35
)

// Options configures a Printer.
type Options struct {
	// Verbose enables Detail(). Without it, Detail is a no-op rather than a
	// different level of the same output, so a default run cannot be made
	// noisy by a caller that forgets to check.
	Verbose bool
	// TTY reports whether the writer is an interactive terminal. It is
	// passed in rather than discovered here: the CLI already computes this
	// from an injected IsTTY, and a second computation is a second thing
	// to disagree with.
	TTY bool
	// Quiet suppresses everything. The final "aes: <error>" line in
	// root.go is outside this package, so an agent still sees why a run
	// failed.
	Quiet bool
}

// Printer writes progress. A nil Writer produces a no-op Printer, so a
// caller with nowhere to write never has to nil-check.
type Printer struct {
	w        io.Writer
	verbose  bool
	tty      bool
	quiet    bool
	useColor bool

	// spinning tracks whether a Spin line is currently the visible line, so
	// Success or Fail knows to overwrite it rather than append below it.
	spinning bool
}

var (
	ansiReset  = "\033[0m"
	ansiBlue   = "\033[0;34m"
	ansiGreen  = "\033[0;32m"
	ansiYellow = "\033[0;33m"
	ansiRed    = "\033[0;31m"
	ansiGray   = "\033[0;90m"
)

// New returns a Printer writing to w.
func New(w io.Writer, o Options) *Printer {
	p := &Printer{w: w, verbose: o.Verbose, tty: o.TTY, quiet: o.Quiet}
	p.useColor = o.TTY && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	return p
}

// Section prints a header line, used to open a phase of work. The trailing
// marker closes the line so a test asserting on the full header text matches
// with or without colour.
func (p *Printer) Section(format string, args ...any) {
	p.clearInPlace()
	p.writeRaw("\n%s %s %s\n", p.blue("═══"), p.blue(fmt.Sprintf(format, args...)), p.blue("═══"))
}

// Step records progress toward a total. On a TTY it overwrites the same
// line; otherwise it prints one line per step.
func (p *Printer) Step(i, n int, format string, args ...any) {
	p.spinning = false
	name := formatName(fmt.Sprintf(format, args...))

	if !p.tty {
		p.writeRaw("[%d/%d] %s\n", i, n, name)
		return
	}

	percent := 0
	if n > 0 {
		percent = i * 100 / n
	}
	if percent > 100 {
		percent = 100
	}
	p.writeRaw("\r\033[K[%s] %d/%d (%d%%) %s", bar(i, n), i, n, percent, name)
}

// Detail prints an indented explanation line, but only under --verbose.
func (p *Printer) Detail(format string, args ...any) {
	if !p.verbose {
		return
	}
	p.clearInPlace()
	p.writeRaw("    %s %s\n", p.gray("→"), fmt.Sprintf(format, args...))
}

// Spin announces a single operation of unknown length. On a TTY it is the
// line that Step just wrote; without one it is a plain line.
func (p *Printer) Spin(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if !p.tty {
		p.writeRaw("%s %s\n", "⏳", msg)
		return
	}
	p.spinning = true
	p.writeRaw("\r\033[K%s %s", "⏳", msg)
}

// Success prints a completed work item, overwriting an in-place line first.
func (p *Printer) Success(format string, args ...any) {
	p.clearInPlace()
	p.writeRaw("%s %s\n", p.green("✓"), fmt.Sprintf(format, args...))
}

// Warn prints something that went wrong without failing the item.
func (p *Printer) Warn(format string, args ...any) {
	p.clearInPlace()
	p.writeRaw("%s %s\n", p.yellow("⚠"), fmt.Sprintf(format, args...))
}

// Fail prints a failed work item.
func (p *Printer) Fail(format string, args ...any) {
	p.clearInPlace()
	p.writeRaw("%s %s\n", p.red("✖"), fmt.Sprintf(format, args...))
}

// Info prints a plain line, used for the log path and the resume hint.
func (p *Printer) Info(format string, args ...any) {
	p.clearInPlace()
	p.writeRaw("%s\n", fmt.Sprintf(format, args...))
}

// Box draws a boxed block, used for the command a user must run when a
// privileged step is needed. The width tracks the content so a short command
// does not get a 60-column frame.
func (p *Printer) Box(title, body string) {
	p.clearInPlace()
	if p.tty && p.useColor {
		p.writeRaw("%s %s\n", p.blue("╭─"+title+" ─"), "")
		p.writeRaw("%s %s\n", p.blue("│"), body)
		p.writeRaw("%s\n", p.blue("╰"))
		return
	}
	p.writeRaw("--- %s ---\n", title)
	p.writeRaw("  %s\n", body)
	p.writeRaw("---\n")
}

// Spinning reports whether an in-place spinner line is outstanding.
func (p *Printer) Spinning() bool { return p.spinning }

func (p *Printer) clearInPlace() {
	if !p.tty {
		return
	}
	p.writeRaw("\r\033[K")
	p.spinning = false
}

func (p *Printer) writeRaw(format string, args ...any) {
	if p == nil || p.w == nil || p.quiet {
		return
	}
	fmt.Fprintf(p.w, format, args...)
}

func (p *Printer) blue(s string) string   { return p.paint(s, ansiBlue) }
func (p *Printer) green(s string) string  { return p.paint(s, ansiGreen) }
func (p *Printer) yellow(s string) string { return p.paint(s, ansiYellow) }
func (p *Printer) red(s string) string    { return p.paint(s, ansiRed) }
func (p *Printer) gray(s string) string   { return p.paint(s, ansiGray) }

func (p *Printer) paint(s, code string) string {
	if !p.useColor || s == "" {
		return s
	}
	return code + s + ansiReset
}

// bar renders the filled/empty cell bar for i of n.
func bar(i, n int) string {
	if n <= 0 {
		return strings.Repeat("░", barWidth)
	}
	filled := i * barWidth / n
	if filled > barWidth {
		filled = barWidth
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
}

// formatName truncates so a long name cannot shift the whole line. The
// reference installer truncates at 35 characters for the same reason.
func formatName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= nameMax {
		return s
	}
	return s[:nameMax-3] + "..."
}
