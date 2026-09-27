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

	// Name is the filename this tool is INSTALLED as, which is not always the
	// tool name and is not the same as the filename inside the archive.
	//
	// Three genuinely different names are in play for a tool like yq:
	//
	//   tool name     ripgrep          - how the catalog refers to it
	//   archive member yq_darwin_arm64 - what the upstream tarball contains
	//   installed name yq               - what the user types
	//
	// They differ per tool, and conflating any two of them produces an
	// install that lands a file nobody can run. Name is the installed one,
	// taken from the manifest's verify.command so the verifier and the
	// installer are guaranteed to be talking about the same file. Empty falls
	// back to Tool.
	Name string
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
	// Force marks a corrective action: the caller saw state disagree with
	// reality and asked for a repair rather than a first install.
	//
	// It reaches the installer because a package manager's first install and
	// its repair are different commands. dpkg records "installed" from its own
	// database rather than the filesystem, so after a binary is deleted
	// out from under it, `apt-get install -y` reports the package as already
	// the newest and restores nothing. Only --reinstall re-extracts the files.
	// Without this field a forced run succeeds while repairing nothing.
	Force bool
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
// NewRegistry builds the default registry, with every installer that places
// binaries itself told where they go.
//
// binDir is required rather than optional because an installer with no
// destination does not fail loudly at construction — it fails later, at
// os.MkdirAll(""), with the directory missing from the message. Every
// github-release install died that way: Dest was never assigned anywhere in
// production, so the primary install strategy did not work at all.
func NewRegistry(binDir string) *Registry {
	return NewRegistryWithPrivilege(binDir, false)
}

// NewRegistryWithPrivilege builds the default registry and tells the package
// installer which privilege mode the caller is in.
//
// This exists because the interactive default was previously the only way to
// build the registry, so PackageInstaller.NonInteractive could never be true
// in production. The field and the code that reads it both existed, and nothing
// ever set it — a flag parsed at the CLI, threaded to nowhere. `aes setup
// --non-interactive` on an apt-backed tool then printed "Run it now? [y/N]"
// and blocked on a read, which is the exact hang the flag exists to prevent.
// Measured: a stdin that is open and silent hangs until killed; a closed stdin
// returns EOF, which looks like it works and is why this survived review.
func NewRegistryWithPrivilege(binDir string, nonInteractive bool) *Registry {
	r := &Registry{installers: make(map[string]Installer)}
	r.Register(manifest.StrategyGithubRelease, NewGithubRelease())
	if inst, err := r.Get(manifest.StrategyGithubRelease); err == nil {
		if g, ok := inst.(*GithubRelease); ok {
			g.Dest = binDir
		}
	}
	r.Register(manifest.StrategyPackage, &PackageInstaller{NonInteractive: nonInteractive})
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

// binaryName is the filename a tool's binary is expected to have inside its
// release archive: the tool name, unless the strategy was configured with an
// override. A tool whose binary is named differently from the tool (ripgrep
// ships `rg`) declares that on the strategy rather than in the action, so
// Action stays free of slices and keeps serialising deterministically.
// binaryName is the filename to look for inside the archive.
//
// The manifest wins, then the tool name. There is deliberately no third
// source: this once had an installer-level override as well, and two
// declarations of the same fact is how the "which one won" question becomes
// unanswerable. The manifest is the only place that describes the upstream
// artefact; the tool name is a local label, so it is the last resort precisely
// because it is the one most likely to be wrong.
//
// The manifest's entry is per-arch, because the filename inside an archive
// sometimes is. yq ships yq_linux_amd64 and yq_darwin_arm64, so a single
// string could not describe it and the lookup is by Host.Arch. Falling back to
// the tool name when no entry matches keeps every manifest that does not need
// the map working unchanged.
// installName is the name the binary is placed under, and what Verify will
// look for. It is Name when the caller supplied one, so the installer and the
// verifier cannot disagree about which file they mean.
//
// The fallback is binaryName(), not Tool. For most tools the archive member
// and the tool name are the same, but where they differ - ripgrep ships rg,
// bottom ships btm - the archive member is what the user types, so falling
// back to Tool would place a file nobody can run. The CLI always sets Name
// from the manifest's verify.command; the fallback is only for callers that
// construct an Action directly.
func (a Action) installName() string {
	if a.Name != "" {
		return a.Name
	}
	return a.binaryName()
}

func (a Action) binaryName() string {
	if len(a.Target.Binary) > 0 && a.Host != nil {
		if name := a.Target.Binary[a.Host.Arch]; name != "" {
			return name
		}
	}
	return a.Tool
}
