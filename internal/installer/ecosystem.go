package installer

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/quangdang46/agents_environment_setup/internal/exec"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
)

// Ecosystem installs a tool through a language ecosystem's own installer.
//
// This is a thin adapter, not a reimplementation. AES decides *which* package
// and *whether it is already present*; the ecosystem tool decides how to
// fetch and link it. Rewriting `go install` inside AES would be a worse
// version of the thing, one that stops tracking upstream.
//
// Two properties are asserted rather than assumed, because both are the kind
// of thing that quietly rots:
//
//   - None of these strategies need privilege. They all write to
//     user-owned directories, so RequiresPrivilege is false for every one.
//   - Each requires its base toolchain to be present. If `go` is not on
//     PATH, this installer reports that and stops — it does not go hunting
//     for a toolchain. Declaring the toolchain as a dependency is the
//     resolver's job, and that is what the dependency DAG is for.
type Ecosystem struct {
	// name is the strategy name, used in error messages.
	name string
	// Toolchain is the binary that must already be on PATH.
	Toolchain string
	// Command renders the install command for a pinned package coordinate.
	Command func(coord string) string
	// binDir resolves where this ecosystem writes binaries. It is lazy
	// because npm's answer requires running npm.
	binDir func(context.Context) (string, error)

	// Host answers "is the toolchain present". Nil means the current host.
	Host *platform.Host
	// HasToolchain overrides the on-PATH check for the toolchain binary,
	// for tests that assert on command CONSTRUCTION: whether uv exists on
	// the CI runner is irrelevant to whether "uv tool install ruff@0.5.0"
	// is the right command. Nil means the real LookPath. The earlier shape
	// had no override, so a runner without uv failed a test that was never
	// about the runner.
	HasToolchain func(bin string) bool
	// Run executes the command. Nil means a timed exec.Run.
	Run func(ctx context.Context, cmd string) (exec.Result, error)
	// probe answers a short local query, such as npm's global prefix. Nil
	// means run it directly. It is separate from Run because it returns
	// stdout as a value rather than a full Result.
	probe func(ctx context.Context, cmd string) (string, error)

	// verbose makes Install log the constructed command before running it.
	// verboseLog receives the line; nil means discard. Both are set from the
	// CLI's --verbose via SetVerboseLog, never from the manifest, so they
	// cannot change what is installed (I8).
	verbose    bool
	verboseLog io.Writer

	once   sync.Once
	cached string
	err    error
}

// NewEcosystem builds an Ecosystem with the defaults filled in.
func NewEcosystem(name, toolchain string, command func(string) string, binDir func(context.Context) (string, error)) *Ecosystem {
	return &Ecosystem{name: name, Toolchain: toolchain, Command: command, binDir: binDir}
}

func (e *Ecosystem) host() *platform.Host {
	if e.Host != nil {
		return e.Host
	}
	h, err := platform.Current()
	if err != nil {
		// An unsupported OS still has a PATH; fall back to a bare host so
		// the error the user sees names the toolchain, not the platform.
		return &platform.Host{}
	}
	return h
}

func (e *Ecosystem) run() func(context.Context, string) (exec.Result, error) {
	if e.Run != nil {
		return e.Run
	}
	return func(ctx context.Context, cmd string) (exec.Result, error) {
		return exec.Run(ctx, cmd, exec.Options{Timeout: exec.DefaultTimeout})
	}
}

// Install runs the ecosystem's own installer for one tool.
func (e *Ecosystem) Install(ctx context.Context, a Action) error {
	coord, err := e.coordinate(a)
	if err != nil {
		return err
	}
	// Gate before running, not after. A toolchain that is absent is a
	// declaration problem — the catalog should list it as a dependency — and
	// running anyway would produce a confusing shell error instead.
	has := e.HasToolchain
	if has == nil {
		host := e.host()
		has = host.Has
	}
	if !has(e.Toolchain) {
		return fmt.Errorf("install %s: strategy %s requires %q on PATH, which is not there; declare %s as a dependency",
			a.Tool, e.name, e.Toolchain, e.Toolchain)
	}
	cmd := e.Command(coord)
	if e.verbose {
		fmt.Fprintf(e.log(), "    → %s\n", cmd)
	}
	res, err := e.run()(ctx, cmd)
	if err != nil {
		// The captured tail goes into the ERROR, not just the log. A bare
		// "command failed with exit code 1" tells a reader nothing they can
		// act on — npm and cargo bury the reason at the end, so "nothing to
		// see" and "cannot find node" arrive as the same one line.
		//
		// This was measured on a from-scratch container run: opencode's
		// install failed transiently and the log recorded only the exit
		// code, so the failure could not be diagnosed or re-checked from it.
		if tail := lastLines(res.Stdout+"\n"+res.Stderr, 20); tail != "" {
			return fmt.Errorf("%w\n%s%s", err, e.failureIndent(), tail)
		}
	}
	return err
}

func (e *Ecosystem) failureIndent() string { return "    " }

// SetVerboseLog enables command logging for --verbose. It satisfies the
// CLI's verboseLogger seam so verbosity is set post-resolution and cannot
// change the resolved actions (I8).
func (e *Ecosystem) SetVerboseLog(verbose bool, w io.Writer) {
	e.verbose = verbose
	e.verboseLog = w
}

func (e *Ecosystem) log() io.Writer {
	if e.verboseLog == nil {
		return io.Discard
	}
	return e.verboseLog
}

// lastLines returns the last n non-empty lines of s, for failure detail.
func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	kept := make([]string, 0, n)
	for i := len(lines) - 1; i >= 0 && len(kept) < n; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		kept = append([]string{lines[i]}, kept...)
	}
	return strings.Join(kept, "\n")
}

// coordinate returns the pinned package coordinate for the action.
//
// The split at the final '@' mirrors manifest's own rule, so a coordinate
// that loaded successfully always renders here.
func (e *Ecosystem) coordinate(a Action) (string, error) {
	raw := a.Target.PackageCoordinate()
	if raw == "" {
		return "", fmt.Errorf("install %s: strategy %s requires a package", a.Tool, e.name)
	}
	i := strings.LastIndex(raw, "@")
	if i <= 0 || i == len(raw)-1 {
		// manifest rejects this at load; reaching it means the target was
		// built by hand rather than parsed.
		return "", fmt.Errorf("install %s: %q must pin a version as name@version", a.Tool, raw)
	}
	return raw, nil
}

// Destination reports the directory this ecosystem writes binaries into, so
// envgen can put it on PATH.
//
// It is the whole reason this file returns a value rather than just running
// commands: a go-installed tool that nobody added to PATH installed
// successfully and is not runnable — a broken setup wearing a success message.
func (e *Ecosystem) Destination(ctx context.Context) string {
	e.once.Do(func() {
		if e.binDir == nil {
			return
		}
		e.cached, e.err = e.binDir(ctx)
	})
	return e.cached
}

// Destiner is implemented by installers that place binaries somewhere AES
// must add to PATH. Strategies that do not (brew and apt, whose binaries are
// already reachable through the package manager) simply do not implement it.
type Destiner interface {
	Destination(ctx context.Context) string
}

// destinationFor is the shared lookup behind Registry.DestinationFor.
func destinationFor(ctx context.Context, inst Installer) (string, bool) {
	d, ok := inst.(Destiner)
	if !ok {
		return "", false
	}
	return d.Destination(ctx), true
}
