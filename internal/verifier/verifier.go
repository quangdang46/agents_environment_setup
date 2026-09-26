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
	"os/exec"
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

// ExtractVersion pulls a version out of a tool's version output.
//
// Only the first line is considered, because later lines are usually copyright
// years or build metadata. A leading "v" or "go" is not part of the match, so
// "go version go1.24.0 darwin/arm64" yields "1.24.0" without special-casing.
func ExtractVersion(output string) string {
	line, _, _ := strings.Cut(output, "\n")
	return versionRe.FindString(strings.TrimSpace(line))
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
		path, err := exec.LookPath(bin)
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
	run, err := aesexec.Run(ctx, t.Verify.Version.Command, aesexec.Options{
		Timeout: aesexec.VerifyTimeout,
	})
	min := t.Verify.Version.Min
	if err != nil {
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
