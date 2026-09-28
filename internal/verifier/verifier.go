// Package verifier decides whether a tool is present and new enough.
//
// Verify is the ground truth. It reads the machine — PATH and the tool's own
// version output — and nothing else. It never reads the state file: state is a
// cache written from these results, so a verifier that consulted it would be
// checking its own homework and could never detect drift.
//
// A normal outcome is not an error. A tool that is not installed returns
// StatusMissing, and a tool that is too old returns StatusStale. Verify returns
// no error at all; the only thing it can report is what it found.
//
// # Platform independence
//
// Verify takes a *manifest.Tool and nothing else, and that is deliberate. The
// per-GOOS variance in a manifest lives in Tool.Install — the same tool may
// install from a GitHub release on Linux and from brew on macOS — while
// Tool.Verify and Tool.Provides are declared once for every platform. A binary
// is on PATH or it is not, no matter how it got there, so verification needs no
// host and no strategy lookup.
package verifier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	aesexec "github.com/quangdang46/agents_environment_setup/internal/exec"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// Status is the outcome of verifying one tool.
type Status string

const (
	// StatusOK means present, and at or above any declared minimum. It asserts
	// nothing beyond that: a tool with no declared minimum verifies as OK on
	// presence alone, because presence was the only claim being made.
	StatusOK Status = "ok"
	// StatusMissing means a declared binary does not resolve on PATH.
	StatusMissing Status = "missing"
	// StatusStale means present but below the declared minimum.
	StatusStale Status = "stale"
	// StatusUnknown means present, with a minimum declared, but the version
	// could not be read — the version command failed, or printed no digits.
	//
	// This is deliberately not StatusOK and not StatusStale. Calling it OK
	// claims the tool meets a minimum nobody checked, and calling it Stale
	// claims it is too old, which would make aes reinstall a working tool on
	// every run. Neither is knowable from what is on the machine.
	//
	// aes setup treats this as a warning so the North Star still runs to
	// completion; aes doctor reports it as an anomaly.
	StatusUnknown Status = "unknown"
)

// Result is what Verify found. It is a value, not a pointer, so callers can
// put it in a table without ceremony.
type Result struct {
	Tool    string
	Status  Status
	Version string // raw, when detected; empty when none could be
	Path    string // resolved path of the primary binary
	// ProbeFailed reports that the version command RAN and exited non-zero —
	// the binary exists and does not work.
	//
	// It is separate from Status because StatusUnknown covers both this and
	// "the tool ran but printed something unreadable", and the two want
	// opposite treatment: an unreadable version is a warning, a broken binary
	// is something to roll back. A timeout is neither, and never sets this —
	// a slow binary is not a broken one, and rolling a working install back
	// over a slow probe is how a repair becomes damage.
	ProbeFailed bool
}

// versionRe finds the first dotted-integer run in a line, plus any trailing
// pre-release letters.
//
// The letters are captured so the reported version is what the tool actually
// printed. tmux ships "tmux 3.6b"; matching digits alone yields "3.6", which
// compares correctly but quietly loses the "b" from anything the user sees in
// `aes list` or a doctor report.
//
// Capturing the suffix is what makes leadingInt below necessary rather than
// decorative: a component of "6b" must compare as 6, because Atoi("6b") fails
// and a failed parse that read as 0 would turn 3.6b into 3.0 — below any 3.x
// minimum, reported stale on every run and reinstalled forever. The regex and
// leadingInt are one change, not two.
//
// The match is deliberately lenient about surrounding text — every tool formats
// its version differently and one parser has to cover all of them — and strict
// about not inventing: with no digits there is no version, and the caller gets
// an empty string rather than a guess.
var versionRe = regexp.MustCompile(`\d+(?:\.\d+)*(?:[a-z]+)?`)

// gitHashRe matches a git commit hash as it appears in a version string:
// `commit=<40 hex>`, or a short form `commit=<7+ hex>`. It is used to reject
// the token before versionRe sees it, because versionRe would otherwise take
// the hash's leading digits as a version.
//
// The `commit=` prefix is required. A bare 40-hex string is not necessarily a
// hash — it could be a build identifier that happens to be all hex — and the
// cost of rejecting a real version is far higher than the cost of a hash
// slipping through, which is only a wrong version on a tool that also prints
// its real one.
var gitHashRe = regexp.MustCompile(`(?i)\bcommit=[0-9a-f]{7,40}\b`)

// namedVersionRe matches a version that a tool has explicitly labelled, such
// as `version=0.65.1` or `version: 0.65.1`.
//
// It is preferred over versionRe when both are present, because a labelled
// version is the tool's own claim about itself, while the first dotted run
// on a line is often a date. `lazygit --version` prints
// `commit=<hash>, build date=2026-09-13T05:42:30Z, ..., version=0.65.1, ...`:
// after the hash is removed, versionRe takes "2026" from the build date, which
// is a year, not a version, and the tool is reported as version 2026.
var namedVersionRe = regexp.MustCompile(`(?i)\bversion\s*[=:]\s*(\d+(?:\.\d+)*(?:[a-z]+)?)`)

// ansiRe matches the escape sequences a tool may embed in its version output.
//
// This is not cosmetic. `btop --version` prints:
//
//	btop version: \x1b[1m1.4.7\x1b[0m
//
// and the first digit run in that line is the "1" of "[1m", with "m" trailing
// it as a pre-release suffix. The parser therefore reported "1m", which
// compares as 1 — so a perfectly healthy btop 1.4.7 was reported stale
// against a minimum of 1.2, and aes setup would reinstall it on every run.
//
// Stripping escapes first is strictly safer than tightening the regex, because
// every other real format already works and the escape is the one thing
// standing between the line and its digits.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// dottedRe requires at least two components. It is only used past line 1,
// where a bare number is far more likely to be a year than a version.
var dottedRe = regexp.MustCompile(`\d+\.\d+(?:\.\d+)*(?:[a-z]+)?`)

// ExtractVersion pulls a version out of a tool's version output.
//
// Line 1 is the source, because later lines are usually copyright years or
// build metadata. A leading "v" or "go" is not part of the match, so
// "go version go1.24.0 darwin/arm64" yields "1.24.0" without special-casing.
//
// When line 1 contains no digits at all, later lines are consulted under a
// stricter rule. That rescues banner-style tools — `eza --version` prints its
// description first and "v0.23.5 [+git]" second — and it can only ever add a
// version where there was none. It cannot turn a correct extraction into a
// wrong one, because a line 1 with any version in it still wins outright.
//
// The fallback requires at least two numeric components, so a bare copyright
// year on line 2 is rejected while a real version is not.
//
// # A commit hash is not a version
//
// `lazygit --version` prints one line:
//
//	commit=17cb09fa7b08bc96d9f0e81b91f4720fc1a36700, build date=..., version=0.65.1, ...
//
// Line 1 has digits, so the old rule took the first one: `17cb`, a git short
// hash. It compared as a version and passed — by luck. A hash beginning with
// a zero (`0abc...`) compares *below* any floor, so the same tool on the same
// machine would be reported stale and reinstalled on every run. That is the
// failure mode this repo has been bitten by before: a check that passes for
// the wrong reason.
//
// So a token that is a hex hash is rejected before the first match is taken.
// The rule is deliberately narrow — it must look like a git hash, not merely
// contain digits — because a real version can be short and a real tool can
// print one. `versionRe` is then applied to the remainder, which is where
// `version=0.65.1` lives.
func ExtractVersion(output string) string {
	lines := strings.Split(ansiRe.ReplaceAllString(output, ""), "\n")
	// Line 1 is the source, and the lenient rule applies to it: a bare "3" is a
	// legitimate version and we do not want to lose it.
	//
	// A `commit=<hash>` token is removed first. Without this, lazygit's single
	// line yields "17cb" — a git short hash — and the tool is reported as
	// version 17cb. It passes a floor of 0.40 by luck; a hash starting with a
	// zero fails the same floor and the tool is reinstalled on every run.
	first := strings.TrimSpace(lines[0])
	// A labelled `version=` wins over any other number on the line, and a
	// `commit=` hash is discarded rather than parsed. Both rules are needed:
	// dropping the hash alone still leaves `build date=2026-...` to be read as
	// the version.
	if m := namedVersionRe.FindStringSubmatch(first); m != nil {
		return m[1]
	}
	if hash := gitHashRe.FindString(first); hash != "" {
		if v := versionRe.FindString(strings.Replace(first, hash, " ", 1)); v != "" {
			return v
		}
		// The hash was the only digit-bearing token on line 1, so line 1 has
		// nothing left to offer and the fallback below applies.
	} else if v := versionRe.FindString(first); v != "" {
		return v
	}
	// Only when line 1 has no digits at all do we look further. This is what
	// rescues banner-style tools — `eza --version` prints its description
	// first and the version second — and it is deliberately strict, because
	// the reason line 1 is preferred is that later lines carry copyright
	// years. Requiring at least one dot rejects a bare "2001" while accepting
	// "0.23.5".
	for _, line := range lines[1:] {
		if m := dottedRe.FindString(strings.TrimSpace(line)); m != "" {
			return m
		}
	}
	return ""
}

// CompareVersions orders two dotted-integer versions, returning -1, 0 or 1.
//
// Missing trailing components are zero, so "14.1" equals "14.1.0". That is
// deliberately not semver: semver would call 14.1 less than 14.1.0, and the
// question being asked here is only "is this at least the minimum".
func CompareVersions(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := range max(len(as), len(bs)) {
		av, bv := componentAt(as, i), componentAt(bs, i)
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

// componentAt reads the i-th dotted component, treating a missing one as zero
// and a malformed one as zero rather than failing the whole comparison.
func componentAt(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	return leadingInt(parts[i])
}

// leadingInt reads the digits at the start of s and ignores any trailing
// letters, so "6b" in "3.6b" compares as 6. Reading it as 0 instead would make
// every pre-release look older than the release it precedes.
func leadingInt(s string) int {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0
	}
	return n
}

// LookPathFor finds a binary on PATH or in the AES bin directory.
//
// AES owns ~/.aes/bin — that is where every strategy that writes its own
// binary puts it, and it is what `aes env --write` adds to PATH (internal/envgen
// writes the same path into the generated env.sh). A tool that aes installed
// minutes ago is therefore on the machine and NOT on PATH, because a shell has
// to source env.sh first, and no shell has in a fresh process.
//
// Searching PATH alone made the difference between "aes installed this and it
// works" and "aes did not install this" indistinguishable from the verifier's
// side, which reported a working install as missing. That is not a test
// convenience: it is the difference between a user who activated the
// environment and one who did not, and a tool's status should not depend on
// which of those is true.
func LookPathFor(bin string) (string, error) {
	if p, err := exec.LookPath(bin); err == nil {
		return p, nil
	}
	if aesBin := AESBinDir(); aesBin != "" {
		cand := filepath.Join(aesBin, bin)
		if st, err := os.Stat(cand); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return cand, nil
		}
	}
	return "", fmt.Errorf("verifier: %q is on neither PATH nor %s", bin, AESBinDir())
}

// AESBinDir is the directory AES installs its own binaries into, or "" when it
// cannot be determined (no home, or HOME is somewhere unreadable).
func AESBinDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".aes", "bin")
}

// lookPath is the internal spelling used by Verify, kept separate so the
// exported helper is the one callers outside this package reach for.
func lookPath(bin string) (string, error) { return LookPathFor(bin) }

// probeEnv is the environment a version probe runs in: the current one, with
// the AES bin directory prepended to PATH.
//
// Prepending rather than replacing: the probe may need the rest of the machine
// (a dynamic linker, a config directory, a runtime), and a stripped environment
// would turn a working tool into a failing one for reasons that have nothing
// to do with the tool.
func probeEnv() []string {
	dir := AESBinDir()
	if dir == "" {
		return nil
	}
	path := os.Getenv("PATH")
	if path == "" {
		return []string{"PATH=" + dir}
	}
	return []string{"PATH=" + dir + string(os.PathListSeparator) + path}
}

// Verify reports what is true of t on this machine right now.
func Verify(t *manifest.Tool) Result {
	return VerifyContext(context.Background(), t)
}

// VerifyContext is Verify with cancellation, so a run over a whole profile
// stops when the user interrupts it. Verify is the form the spec names; this is
// the one callers should reach for.
func VerifyContext(ctx context.Context, t *manifest.Tool) Result {
	res := Result{Tool: t.Name, Status: StatusMissing}

	// verify.command is the primary binary; provides lists the rest. Every one
	// of them must resolve, not just the first — a tool can ship a binary that
	// resolved while a companion it needs did not.
	required := binaries(t)
	if len(required) == 0 {
		// Manifest validation rejects a tool that checks nothing, so this is
		// unreachable from a validated manifest. Fail closed rather than
		// reporting a tool as OK when nothing was actually checked.
		return res
	}

	for _, bin := range required {
		path, err := lookPath(bin)
		if err != nil {
			return res
		}
		if res.Path == "" {
			res.Path = path
		}
	}
	res.Status = StatusOK

	if t.Verify == nil || t.Verify.Version == nil || t.Verify.Version.Command == "" {
		return res
	}

	// The version command can fail, hang, or print something unparseable. A
	// declared minimum that could not be checked is reported as unknown, never
	// resolved into a verdict the machine does not support.
	//
	// The probe runs with the AES bin directory on PATH. A tool resolved out of
	// ~/.aes/bin — which is where every strategy that writes its own binary puts
	// it — was found by absolute path above, but the probe below is a shell
	// command that names the tool, and `sh` resolves names against PATH. Without
	// this the tool was present and found, and then its own version command
	// could not be run, so it reported as unknown. That is the same
	// missing-PATH assumption as lookPath, one layer later.
	run, err := aesexec.Run(ctx, t.Verify.Version.Command, aesexec.Options{
		Timeout: aesexec.VerifyTimeout,
		Env:     probeEnv(),
	})
	min := t.Verify.Version.Min
	if err != nil {
		// ErrFailed is the binary running and failing; ErrTimeout is the probe
		// running out of budget. Only the first says anything about the tool.
		res.ProbeFailed = errors.Is(err, aesexec.ErrFailed)
		if min != "" {
			res.Status = StatusUnknown
		}
		return res
	}
	res.Version = ExtractVersion(run.Stdout)
	switch {
	case min == "":
		// Presence was the only claim, and it held.
		return res
	case res.Version == "":
		res.Status = StatusUnknown
	case CompareVersions(res.Version, min) < 0:
		res.Status = StatusStale
	}
	return res
}

// binaries is everything that must resolve for the tool to count as present:
// the primary verify.command first, then each provides entry, deduplicated.
func binaries(t *manifest.Tool) []string {
	var out []string
	seen := map[string]bool{}
	add := func(b string) {
		if b == "" || seen[b] {
			return
		}
		seen[b] = true
		out = append(out, b)
	}
	if t.Verify != nil {
		add(t.Verify.Command)
	}
	for _, p := range t.Provides {
		add(p)
	}
	return out
}
