// Package installer turns a resolved action into an installed tool.
//
// Resolution is by strategy, never by tool name (invariant I1). The registry
// maps a strategy to an Installer, so the catalog can grow from ten tools to
// three hundred without a single new line of core logic — and so a strategy
// typo surfaces as an error naming the valid set instead of silently
// downloading code from the wrong place.
//
// The privilege boundary is structural rather than conventional. Package
// managers that need root do not get to say so themselves: exec.Run refuses
// any command containing sudo and returns a PrivilegeError without executing
// it. Deciding whether to escalate belongs to the caller, which prints the
// command and asks first (I10, I14). That is why this package depends on
// manifest, platform and exec, and not on the resolver: an action is a
// snapshot describing what should happen on one specific host, and the
// installer consumes it without re-resolving anything.
package installer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
)

var (
	// ErrChecksum is returned when a downloaded asset does not match its
	// recorded digest. The binary is never extracted when this is returned.
	ErrChecksum = errors.New("sha256 mismatch")

	// ErrUnknownStrategy is returned for a strategy with no registered
	// Installer. The message names the valid set so a typo is fixable.
	ErrUnknownStrategy = errors.New("unknown install strategy")

	// ErrMissingArch is returned when a manifest has no asset or sha256 key
	// for the host's architecture. Falling back to another arch is forbidden:
	// it installs a binary the machine cannot run.
	ErrMissingArch = errors.New("no asset for this architecture")

	// ErrUnsafeArchive is returned when a release archive contains an entry
	// that could write outside the destination directory.
	ErrUnsafeArchive = errors.New("unsafe archive entry")

	// ErrBinaryMissing is returned when a verified archive does not contain
	// the binary the manifest says the tool provides.
	ErrBinaryMissing = errors.New("archive does not contain the expected binary")
)

// Action is a resolved snapshot: what should happen, on one specific host.
//
// It is a plain value with no slices or maps, so identical input serialises
// identically. Host is a pointer shared across every action in one
// resolution. The installer does not re-derive the host — if reality differs
// from the snapshot, that is drift, and drift is doctor's job, not the
// installer's.
type Action struct {
	// Tool is the manifest tool name.
	Tool string
	// Target is the install recipe chosen for this host, already resolved
	// out of the manifest's per-GOOS map by the caller.
	Target manifest.Target
	// Host is the machine the action was resolved for. Required: the arch
	// selects the asset, and a nil host is a programming error, not a
	// recoverable condition.
	Host *platform.Host
	// Reason records why the tool is present ("default", "dependency:git",
	// "only:claude"). Diagnostic only, never parsed.
	Reason string
}

// Installer installs one tool according to one strategy.
type Installer interface {
	// Install performs the install. It returns an error rather than
	// reporting failure through a result, and it must leave the
	// destination untouched on every error path.
	Install(ctx context.Context, a Action) error
}

// Registry maps a strategy name to the Installer that implements it.
//
// It is safe for concurrent use: the CLI registers once at startup, but a
// future tool-level command may resolve strategies from several goroutines.
type Registry struct {
	mu         sync.RWMutex
	installers map[string]Installer
}

// NewRegistry returns a registry holding every strategy this build
// implements.
//
// The package installer is registered with its defaults: no configured
// stdin/out, and interactive mode. A caller that needs a different privilege
// mode replaces it via Register — the point of registering a default is that
// resolution by strategy works from the first call, not that the CLI is
// forbidden from configuring it.
func NewRegistry() *Registry {
	r := &Registry{installers: make(map[string]Installer)}
	r.Register(manifest.StrategyGithubRelease, NewGithubRelease())
	r.Register(manifest.StrategyPackage, &PackageInstaller{})
	r.Register(manifest.StrategyGo, NewGoInstaller())
	r.Register(manifest.StrategyNPM, NewNPMInstaller())
	r.Register(manifest.StrategyCargo, NewCargoInstaller())
	r.Register(manifest.StrategyUV, NewUVInstaller())
	return r
}

// Register adds or replaces the Installer for a strategy.
func (r *Registry) Register(strategy string, inst Installer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.installers[strategy] = inst
}

// Get returns the Installer for a strategy.
//
// An unknown strategy is an error naming the valid set. There is deliberately
// no default: silently falling back to github-release would mean a typo in a
// manifest changes where code is downloaded from, which is a supply-chain
// bug rather than a typo.
func (r *Registry) Get(strategy string) (Installer, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if inst, ok := r.installers[strategy]; ok {
		return inst, nil
	}
	return nil, fmt.Errorf("%w %q (valid: %v)", ErrUnknownStrategy, strategy, r.Strategies())
}

// Strategies returns the registered strategy names, sorted, for error
// messages and help text.
func (r *Registry) Strategies() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.installers))
	for name := range r.installers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Install resolves the action's strategy and runs the matching installer.
func (r *Registry) Install(ctx context.Context, a Action) error {
	inst, err := r.Get(a.Target.Strategy)
	if err != nil {
		return err
	}
	return inst.Install(ctx, a)
}

// DestinationFor reports where a strategy's binaries land, so envgen can add
// that directory to PATH.
//
// The second return is false for strategies that contribute nothing: brew
// and apt already put their binaries on PATH, so adding anything for them
// would be noise. An empty directory with true means the strategy has a
// destination it could not resolve — npm without a reachable npm, say — and
// envgen should skip it rather than guess.
func (r *Registry) DestinationFor(ctx context.Context, strategy string) (string, bool) {
	inst, err := r.Get(strategy)
	if err != nil {
		return "", false
	}
	return destinationFor(ctx, inst)
}

// RequiresPrivilege reports whether a strategy needs root.
//
// It is derived from the strategy, never read from YAML (invariant I2). The
// derivation lives here so the resolver, the installer and the CLI cannot
// drift apart on which strategies escalate.
func RequiresPrivilege(target manifest.Target) bool {
	return target.Strategy == manifest.StrategyPackage && target.Manager == manifest.ManagerApt
}

// binaryName is the filename a tool's binary is expected to have inside its
// release archive: the tool name, unless the strategy was configured with an
// override. A tool whose binary is named differently from the tool (ripgrep
// ships `rg`) declares that on the strategy rather than in the action, so
// Action stays free of slices and keeps serialising deterministically.
func (a Action) binaryName(override string) string {
	if override != "" {
		return override
	}
	return a.Tool
}
