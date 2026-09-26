// Package cli is the aes command tree.
//
// # Exit codes are a contract
//
// The codes below are how an agent parses this tool, so they live in one
// function and no command invents its own. Code 5 exists so "you must do
// something" is distinguishable from "it broke" — an agent that cannot tell
// those apart either hangs waiting for a prompt or gives up on a fixable
// machine.
//
//	0  every resolved action verified
//	1  generic failure
//	2  invalid usage or a bad flag
//	3  the catalog or a manifest is invalid
//	4  unsupported platform
//	5  a manual privileged step is required
//	6  verification failed after an install
//
// # --yes and --non-interactive are not synonyms
//
// --yes means "I am at a terminal, do not ask me", and it *requires* one:
// piping `aes setup --yes` without a TTY is a usage error, not a silent
// downgrade. That combination is exactly how a CI job ends up waiting on a
// prompt nobody can see.
//
// --non-interactive means "there is nobody to ask". Every confirmation
// resolves automatically — sudo -n, else skip, print, and exit 5 — and it
// never reads stdin. Passing both is legal and means --non-interactive,
// because that is the stronger statement; it is just pointless.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/exec"
	"github.com/quangdang46/agents_environment_setup/internal/installer"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
	"github.com/quangdang46/agents_environment_setup/internal/resolver"
	"github.com/quangdang46/agents_environment_setup/internal/state"
)

// Exit codes. These are the tool's public contract; see the package comment.
const (
	ExitOK                  = 0
	ExitFailure             = 1
	ExitUsage               = 2
	ExitInvalidCatalog      = 3
	ExitUnsupportedPlatform = 4
	ExitNeedsPrivilege      = 5
	ExitVerifyFailed        = 6
)

// UsageError is a bad invocation: an unknown flag, a missing argument, or a
// flag combination that cannot work. It maps to exit 2.
type UsageError struct {
	msg  string
	help string
}

func (e *UsageError) Error() string { return e.msg }

func usagef(format string, args ...any) *UsageError {
	return &UsageError{msg: fmt.Sprintf(format, args...)}
}

// ExitCode maps an error onto the documented exit code.
//
// This is the single place the mapping lives. Commands return errors; they
// do not decide codes, because a second mapping is a second thing to forget.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	// A command may choose among the documented codes for a case the error
	// taxonomy does not name — `aes verify` failing as a CI gate is exit 6,
	// not exit 1. It may not invent a value: an undocumented code breaks the
	// promise that these numbers mean something.
	var coded exitCoder
	if errors.As(err, &coded) {
		if code := coded.ExitCode(); isDocumented(code) {
			return code
		}
	}

	switch {
	// The privilege error is checked first and specifically: it unwraps to
	// ErrNeedsPrivilege, and code 5 is the whole reason that sentinel
	// exists. Checking it after the generic cases would collapse it into 1.
	case errors.Is(err, exec.ErrNeedsPrivilege):
		return ExitNeedsPrivilege

	// A bad definition is not a usage error and not a generic failure: the
	// invocation was fine, the input was not.
	case errors.Is(err, manifest.ErrInvalid),
		errors.Is(err, catalog.ErrDuplicateTool),
		// A corrupt state file is exit 3, not a generic failure: it is an
		// invalid input, and treating it as "retry" invites the reinstall
		// storm that invariant I9 exists to prevent.
		errors.Is(err, state.ErrCorrupt),
		errors.Is(err, state.ErrVersionTooNew),
		// A dependency cycle, an unknown tool, or an excluded dependency is
		// a catalog defect. Reporting it as a generic failure would tell
		// the user to retry something that can never work.
		errors.Is(err, resolver.ErrCycle),
		errors.Is(err, resolver.ErrUnknownTool),
		errors.Is(err, resolver.ErrExcludedDependency):
		return ExitInvalidCatalog

	case errors.Is(err, platform.ErrUnsupportedOS):
		return ExitUnsupportedPlatform

	// A strategy the build does not implement is a catalog defect too.
	case errors.Is(err, installer.ErrUnknownStrategy):
		return ExitInvalidCatalog

	default:
		return ExitFailure
	}
}

// permute moves flags ahead of positional arguments so `aes uninstall jq
// --confirm` works as well as `aes uninstall --confirm jq`.
//
// Go's flag package stops parsing at the first non-flag argument, so without
// this every command taking a tool name would treat a trailing flag as a
// second tool name — and "uninstall takes exactly one tool name" for a
// perfectly ordinary invocation is a bad first experience.
//
// A bare "--" ends flag parsing, so anything after it stays positional.
func permute(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			// Everything after the terminator is positional, verbatim.
			return append(append(flags, positional...), args[i:]...)
		case len(a) > 1 && a[0] == '-':
			flags = append(flags, a)
			// A flag written as "--name value" consumes the value; without
			// this, "jq" would be pulled out of position and the flag left
			// with the next flag as its argument.
			if !strings.Contains(a, "=") && i+1 < len(args) && !isFlagLike(args[i+1]) {
				i++
				flags = append(flags, args[i])
			}
		default:
			positional = append(positional, a)
		}
	}
	return append(flags, positional...)
}

// isFlagLike reports whether s looks like another flag rather than a value.
func isFlagLike(s string) bool {
	return len(s) > 1 && s[0] == '-'
}

// isDocumented reports whether code is one the contract defines.
func isDocumented(code int) bool {
	switch code {
	case ExitOK, ExitFailure, ExitUsage, ExitInvalidCatalog,
		ExitUnsupportedPlatform, ExitNeedsPrivilege, ExitVerifyFailed:
		return true
	}
	return false
}

// Usage reports the documented exit code for a usage error.
func (e *UsageError) ExitCode() int { return ExitUsage }

// exitCoder lets a command choose among the documented codes for a case the
// error taxonomy does not name. It can only return a contract code — an
// arbitrary value would break the promise that the codes mean something.
type exitCoder interface{ ExitCode() int }

// Command is one subcommand.
type Command struct {
	// Name is the word typed on the command line.
	Name string
	// Summary is the one-line description in help output.
	Summary string
	// Register adds command-specific flags and returns the struct they
	// write into, or nil when the command has none. It exists so a
	// command-specific flag like --link-shell does not have to be spelled
	// on every other command, where it would be meaningless.
	Register func(fs *flag.FlagSet) any
	// Run executes the command. args are the positional arguments left
	// after flag parsing; extra is whatever Register returned.
	Run func(ctx context.Context, app *App, flags *Flags, extra any, args []string) error
}

// Flags are the flags shared across commands. The spec requires them to mean
// the same thing everywhere, so they are parsed once by one struct.
type Flags struct {
	// Yes asserts a TTY and suppresses privilege confirmation.
	Yes bool
	// NonInteractive suppresses prompting entirely and resolves privilege
	// via sudo -n.
	NonInteractive bool
	// DryRun resolves and prints, then exits 0 without touching anything.
	DryRun bool
	// JSON selects machine-parseable output with no human prose.
	JSON bool
	// Force permits corrective action on a tool whose state disagrees with
	// reality. It never widens the resolved set.
	Force bool
	// Verbose raises log detail.
	Verbose bool

	// Profile names the profile to resolve.
	Profile string
	// Only and Exclude narrow the resolved set.
	Only    stringList
	Exclude stringList
}

// stringList is a repeatable flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	if v == "" {
		return errors.New("value is empty")
	}
	*s = append(*s, v)
	return nil
}

func init() { commands = append(commands, lifecycleCommands...) }

// App holds everything a command touches. Injecting it rather than reaching
// for os.Stdout and os.Getenv is what makes the exit-code contract testable.
type App struct {
	// In, Out, Err are the command's streams.
	In  io.Reader
	Out io.Writer
	Err io.Writer

	// IsTTY reports whether In is a terminal. Nil means auto-detect.
	IsTTY func() bool

	// RunTUI opens the human frontend. Nil means the TUI is not wired in,
	// and bare `aes` falls back to printing help.
	RunTUI func()

	// Home is AES_HOME, conventionally ~/.aes.
	Home string
	// CatalogRoot is where tool.yaml files live.
	CatalogRoot string
	// ProfileDir is where profile files live.
	ProfileDir string

	// Registry resolves a strategy to an installer.
	Registry *installer.Registry
}

// Version is the build version, set from main.
var Version = "dev"

// commands is the MVP command tree. The TUI is not here: bare `aes` prints
// help until it is.
// commands is the full command tree. Lifecycle commands are appended from
// their own file so the tree stays readable and the split between the two
// meanings lives next to the code that implements it.
var commands = []*Command{
	{Name: "setup", Summary: "install a complete, verified AI coding environment", Run: runSetup},
	{Name: "list", Summary: "list catalog tools and their verified status", Run: runList},
	{Name: "verify", Summary: "check tools without changing anything", Run: runVerify},
	{Name: "doctor", Summary: "report environment health and drift", Run: runDoctor},
	{
		Name:     "env",
		Summary:  "print or write the environment file",
		Register: registerEnvFlags,
		Run:      runEnv,
	},
}

// Run dispatches args and returns the process exit code.
func (a *App) Run(args []string) int {
	if len(args) == 0 {
		// Bare `aes` is the TUI. Without a terminal — a pipe, a CI job —
		// it prints help rather than emitting control codes into a pipe.
		if a.stdinIsTTY() && a.RunTUI != nil {
			a.RunTUI()
			return ExitOK
		}
		a.writeHelp(a.Out)
		return ExitOK
	}

	switch args[0] {
	case "-v", "--version", "version":
		fmt.Fprintf(a.Out, "aes %s\n", Version)
		return ExitOK
	case "-h", "--help", "help":
		a.writeHelp(a.Out)
		return ExitOK
	}

	var cmd *Command
	for _, c := range commands {
		if c.Name == args[0] {
			cmd = c
			break
		}
	}
	if cmd == nil {
		fmt.Fprintf(a.Err, "aes: unknown command %q\n\n", args[0])
		a.writeHelp(a.Err)
		return ExitUsage
	}

	flags, extra, rest, err := parseFlags(cmd, args[1:], a.Err)
	if err != nil {
		return ExitCode(err)
	}
	if err := a.validateFlags(flags); err != nil {
		fmt.Fprintf(a.Err, "aes: %v\n", err)
		return ExitCode(err)
	}

	counter := &countingWriter{w: a.Out}
	a.Out = counter
	err = cmd.Run(context.Background(), a, flags, extra, rest)
	a.Out = counter.w

	if err != nil {
		// stdout must hold exactly one JSON document. A command that
		// already wrote its own envelope carries the error inside it, so
		// appending a second document here would make the stream
		// unparseable — which defeats the point of --json.
		if flags.JSON && counter.n == 0 {
			writeJSONError(a.Out, err, ExitCode(err))
		}
		fmt.Fprintf(a.Err, "aes: %v\n", err)
		return ExitCode(err)
	}
	return ExitOK
}

// countingWriter tracks how much a command wrote, so Run can tell an
// unwritten stream from one already holding a complete document.
type countingWriter struct {
	w io.Writer
	n int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += n
	return n, err
}

// parseFlags parses the shared flag set. Every command takes the same flags
// so their meanings cannot drift apart.
func parseFlags(cmd *Command, args []string, errOut io.Writer) (*Flags, any, []string, error) {
	fs := flag.NewFlagSet("aes "+cmd.Name, flag.ContinueOnError)
	fs.SetOutput(errOut)

	// Usage text goes to stderr so --json keeps stdout clean even when a
	// flag is bad.
	fs.Usage = func() {}

	f := &Flags{}
	fs.BoolVar(&f.Yes, "yes", false, "suppress privilege confirmation; requires a TTY")
	fs.BoolVar(&f.NonInteractive, "non-interactive", false, "never prompt; resolve privilege with sudo -n")
	fs.BoolVar(&f.DryRun, "dry-run", false, "resolve and print, change nothing")
	fs.BoolVar(&f.JSON, "json", false, "machine-parseable output")
	fs.BoolVar(&f.Force, "force", false, "repair tools whose state disagrees with reality")
	fs.BoolVar(&f.Verbose, "verbose", false, "more detail")
	fs.StringVar(&f.Profile, "profile", "", "profile to resolve")
	fs.Var(&f.Only, "only", "install only this tool (repeatable)")
	fs.Var(&f.Exclude, "exclude", "skip this tool (repeatable)")

	var extra any
	if cmd.Register != nil {
		extra = cmd.Register(fs)
	}

	if err := fs.Parse(permute(args)); err != nil {
		// flag already wrote the reason; map it onto the usage contract.
		return nil, nil, nil, &UsageError{msg: err.Error()}
	}
	return f, extra, fs.Args(), nil
}

// validateFlags enforces the flag contract before a command runs, so every
// command inherits it rather than re-implementing it.
func (a *App) validateFlags(f *Flags) error {
	// --yes asserts a TTY. Without one, a silent downgrade is the failure
	// mode that hangs CI, so this is a usage error rather than a fallback.
	if f.Yes && !f.NonInteractive && !a.tty() {
		return usagef("--yes requires a terminal; without a TTY use --non-interactive " +
			"(piping aes setup --yes is how a CI job waits on a prompt nobody sees)")
	}
	return nil
}

// stdinIsTTY reports whether stdin is a terminal, for deciding whether bare
// `aes` can open the interactive UI at all.
func (a *App) stdinIsTTY() bool { return a.tty() }

func (a *App) tty() bool {
	if a.IsTTY != nil {
		return a.IsTTY()
	}
	return IsTerminal(os.Stdin)
}

// IsTerminal reports whether f is an interactive terminal.
//
// It checks for a character device rather than running a shell command:
// `command -v tmux` on a configured zsh returns an *alias* for a plugin
// wrapper, which is not evidence that a binary exists.
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

func (a *App) writeHelp(w io.Writer) {
	fmt.Fprintf(w, "aes %s — agents environment setup\n\n", Version)
	fmt.Fprintf(w, "USAGE\n  aes <command> [flags]\n\nCOMMANDS\n")
	for _, c := range commands {
		fmt.Fprintf(w, "  %-8s %s\n", c.Name, c.Summary)
	}
	fmt.Fprintf(w, "\n  aes --version   print the version\n")
	fmt.Fprintf(w, "\nFLAGS\n")
	fmt.Fprintf(w, "  --yes              suppress privilege confirmation; REQUIRES a TTY\n")
	fmt.Fprintf(w, "  --non-interactive  never prompt; resolve privilege with sudo -n\n")
	fmt.Fprintf(w, "  --dry-run          resolve and print, change nothing\n")
	fmt.Fprintf(w, "  --json             machine-parseable output on stdout\n")
	fmt.Fprintf(w, "  --force            repair state that disagrees with reality\n")
	fmt.Fprintf(w, "  --profile <name>   profile to resolve\n")
	fmt.Fprintf(w, "  --only <tool>      resolve only this tool (repeatable)\n")
	fmt.Fprintf(w, "  --exclude <tool>   skip this tool (repeatable)\n")
	fmt.Fprintf(w, "  --verbose          more detail\n")
	fmt.Fprintf(w, "\nEXIT CODES\n")
	fmt.Fprintf(w, "  0 ok   1 failure   2 usage   3 invalid catalog   4 unsupported platform\n")
	fmt.Fprintf(w, "  5 manual privileged step required   6 verification failed\n")
}
