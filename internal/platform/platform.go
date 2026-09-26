// Package platform answers three questions about the machine AES is running
// on: what OS and architecture is it, which package manager is available, and
// can this tool be installed here at all.
//
// Three different architecture vocabularies meet in this package, and mixing
// them up is the single easiest way to ship a binary that does not run:
//
//	install: map key   darwin · linux      runtime.GOOS
//	Host.Arch          arm64 · amd64       runtime.GOARCH (Go naming)
//	uname -m           arm64 · x86_64      the system, only when shelling out
//
// AES speaks Go naming everywhere, so Host.Arch is runtime.GOARCH verbatim
// and no translation layer exists inside the tool. unameArchToGo exists for
// the one place that reads uname; install.sh mirrors the same mapping in
// shell, which is why the table below is the authority for both.
package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// Supported operating systems, matching runtime.GOOS and the keys of a
// manifest's install: map.
const (
	OSDarwin = "darwin"
	OSLinux  = "linux"
)

// Go architecture names, matching runtime.GOARCH. These are the only
// spellings valid as asset: or sha256: keys in a tool.yaml.
const (
	ArchAMD64 = "amd64"
	ArchARM64 = "arm64"
)

// Manager values are the same vocabulary manifest.Target.Manager uses, so a
// host's detected manager can be compared with a target's declared manager
// without a translation table. Re-exported rather than restated so the two
// vocabularies cannot drift apart.
const (
	ManagerBrew = manifest.ManagerBrew
	ManagerApt  = manifest.ManagerApt
)

// ErrUnsupportedOS is returned when the running OS is outside the supported
// set. Callers map it to exit code 4: do not retry, this machine will not be
// supported by this release.
var ErrUnsupportedOS = errors.New("unsupported platform")

// Host describes the machine AES is running on.
//
// Every field is a string, which is what makes Host comparable and
// JSON-serializable. resolver.Action embeds *Host, so anything else here
// (a slice, a map) would make a resolved action non-deterministic on the
// wire for no benefit.
type Host struct {
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Manager string `json:"manager,omitempty"`
	Prefix  string `json:"prefix,omitempty"`
}

// brewPrefixes are the directories Homebrew installs into, most specific
// first: /opt/homebrew on Apple Silicon, /usr/local on Intel.
var brewPrefixes = []string{"/opt/homebrew", "/usr/local"}

// Current detects the running host.
//
// Manager detection is a presence check and nothing more. Running `apt-get`
// to see whether it works would be a side effect of merely asking, and
// detection has to stay safe to call from a --dry-run.
func Current() (*Host, error) {
	if runtime.GOOS != OSDarwin && runtime.GOOS != OSLinux {
		return nil, fmt.Errorf("%w: %s (aes supports %s and %s)",
			ErrUnsupportedOS, runtime.GOOS, OSDarwin, OSLinux)
	}

	h := &Host{OS: runtime.GOOS, Arch: runtime.GOARCH}
	if h.OS == OSDarwin {
		if prefix, ok := findHomebrewPrefix(string(os.PathSeparator)); ok {
			h.Manager, h.Prefix = ManagerBrew, prefix
		}
	} else if _, err := exec.LookPath("apt-get"); err == nil {
		h.Manager = ManagerApt
	}
	return h, nil
}

// Key returns the manifest install: map key for this host, which is simply
// the GOOS. The indirection is worth keeping: consumers should ask for the
// install key rather than reach for OS directly, so a future key scheme
// changes in one place.
func (h *Host) Key() string { return h.OS }

// Supports reports whether the tool declares an install recipe for this
// host. A darwin-only tool is skipped on Linux rather than being an error
// (invariant I12) — the default profile is shared across platforms, so
// "not here" is the normal case, not a failure.
func (h *Host) Supports(t *manifest.Tool) bool {
	_, ok := h.Target(t)
	return ok
}

// Target returns the install recipe for this host and whether one exists.
// This is the lookup the installer and resolver both need: a single tool can
// declare a github-release recipe for linux and a package/brew recipe for
// darwin, and the caller must not guess which applies.
func (h *Host) Target(t *manifest.Tool) (manifest.Target, bool) {
	if t == nil {
		return manifest.Target{}, false
	}
	target, ok := t.Install[h.Key()]
	return target, ok
}

// Has reports whether a binary resolves on PATH.
//
// An empty name is false rather than an error: callers pass user-supplied
// names, and an empty string must not be treated as "the current directory"
// or a bare PATH element.
func (h *Host) Has(bin string) bool {
	if bin == "" {
		return false
	}
	_, err := exec.LookPath(bin)
	return err == nil
}

// Path returns the resolved location of a binary on PATH, or "" when it is
// not present. The verifier records this so a tool that resolved from an
// unexpected location is visible after the fact.
func (h *Host) Path(bin string) string {
	if bin == "" {
		return ""
	}
	p, err := exec.LookPath(bin)
	if err != nil {
		return ""
	}
	return p
}

// findHomebrewPrefix locates the Homebrew installation beneath root, falling
// back to brew on PATH for installations that live somewhere else entirely.
func findHomebrewPrefix(root string) (string, bool) {
	if prefix, ok := homebrewPrefixIn(root); ok {
		return prefix, true
	}
	// brew somewhere unusual still counts: report the prefix it implies,
	// which is two levels up from the binary (/custom/bin/brew -> /custom).
	p, err := exec.LookPath("brew")
	if err != nil {
		return "", false
	}
	return filepath.Dir(filepath.Dir(p)), true
}

// homebrewPrefixIn scans the known prefixes beneath root, most specific
// first, and reports the first one holding a usable brew binary.
//
// It is split from findHomebrewPrefix so the ordering can be tested against a
// real filesystem without the result depending on what brew the machine
// running the tests happens to have. The production call passes the
// filesystem separator, making this "look under /".
func homebrewPrefixIn(root string) (string, bool) {
	for _, dir := range brewPrefixes {
		prefix := filepath.Join(root, dir)
		if isExecutable(filepath.Join(prefix, "bin", "brew")) {
			return prefix, true
		}
	}
	return "", false
}

// isExecutable reports whether path is a regular file with an execute bit
// set. A directory that happens to be named brew is not a working install.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}

// unameArchToGo translates uname's architecture vocabulary into Go's. It is
// the single translation point in AES, and install.sh mirrors this exact
// table in shell. An unrecognised value returns "" rather than a guess: a
// wrong architecture downloads the wrong binary, and failing loudly is the
// only safe response.
func unameArchToGo(m string) string {
	switch m {
	case "x86_64", "amd64":
		return ArchAMD64
	case "aarch64", "arm64":
		return ArchARM64
	default:
		return ""
	}
}
