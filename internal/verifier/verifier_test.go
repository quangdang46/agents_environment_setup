package verifier

import (
	"context"
	"testing"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

const missingBin = "aes-definitely-not-a-real-binary"

// Every tool prints its version differently and one parser has to cover all of
// them. These are the real outputs, verbatim from the spec.
func TestExtractVersion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		want   string
	}{
		{"ripgrep", "ripgrep 14.1.1\n", "14.1.1"},
		{"fzf with build suffix", "fzf 0.54.0 (brew)\n", "0.54.0"},
		{"git", "git version 2.44.0\n", "2.44.0"},
		{"go with go prefix and platform", "go version go1.24.0 darwin/arm64\n", "1.24.0"},
		{"jq hyphenated, no space", "jq-1.7.1\n", "1.7.1"},
		{"claude with vendor suffix", "claude 1.0.283 (Claude Code)\n", "1.0.283"},
		{"tmux two components", "tmux 3.4\n", "3.4"},
		// Captured verbatim from a real machine. The pre-release suffix is the
		// reason this case exists: matching digits alone yields "3", which is
		// below any 3.x minimum and would reinstall tmux on every run.
		{"tmux pre-release suffix", "tmux 3.6b\n", "3.6b"},
		{"tmux alpha suffix", "tmux 3.5a\n", "3.5a"},
		{"jq apple suffix", "jq-1.7.1-apple\n", "1.7.1"},
		{"gh with a date in parens", "gh version 2.93.0 (2026-05-27)\n", "2.93.0"},
		{"fzf homebrew build suffix", "0.73.1 (Homebrew)\n", "0.73.1"},

		// Captured verbatim from `lazygit --version` on 2026-09-28. A commit
		// hash is not a version: the old rule took `17cb` from the hash and
		// compared it as one. It passed a 0.40 floor by luck; a hash starting
		// with a zero fails the same floor, so the same tool would be
		// reported stale and reinstalled on every run.
		{"lazygit with commit hash", "commit=17cb09fa7b08bc96d9f0e81b91f4720fc1a36700, build date=2026-09-13T05:42:30Z, build source=binaryRelease, version=0.65.1, os=linux, arch=amd64, git version=2.43.0\n", "0.65.1"},
		// The same shape with a zero-leading hash: without the fix this is
		// reported as version "0abc", which is below any floor and reinstalls
		// the tool on every run.
		{"lazygit with zero-leading commit hash", "commit=0abc09fa7b08bc96d9f0e81b91f4720fc1a36700, build date=2026-09-13T05:42:30Z, version=0.65.1\n", "0.65.1"},
		// A named `version=` wins even when an earlier token has a version-like
		// number. Here `build date=2026-...` must lose to `version=0.65.1`,
		// because a year is not a version.
		{"a build date is not the version", "tool build date=2026-01-01 version=1.2.3\n", "1.2.3"},

		// A tool that prints ONLY a commit hash. Without the hash-rejection
		// branch, versionRe takes "17cb" — a git short hash — and the tool is
		// reported as version 17cb. A zero-leading hash ("0abc") is worse: it
		// compares below any floor, so the tool is reported stale and
		// reinstalled on every run. The correct answer is no version at all.
		{"a commit hash alone is not a version", "commit=17cb09fa7b08bc96d9f0e81b91f4720fc1a36700\n", ""},
		{"a zero-leading commit hash alone is not a version", "commit=0abc09fa7b08bc96d9f0e81b91f4720fc1a36700\n", ""},
		// The hash is removed and the remainder is searched, so what follows
		// it is what gets read. "2026" is the year, not the hash — which is
		// the documented leniency applied: a bare number on line 1 is a version
		// (the same rule that keeps `tmux 3` reading as 3).
		{"a hash followed by a date reads the date", "commit=17cb09fa7b08bc96d9f0e81b91f4720fc1a36700, built 2026-01-01\n", "2026"},

		// Captured verbatim from binaries on a developer machine while writing
		// the agent/ catalog. ntm has no --version at all - it prints this from
		// `ntm version` - so a manifest written by pattern-matching its peers
		// would have used a command that errors.
		// btop colours its version, and the first digit run in an escape
		// sequence is not a version. Without stripping escapes the parser
		// returns "1m" from the [1m in "\x1b[1m1.4.7\x1b[0m", which compares
		// as 1 — so a healthy 1.4.7 was reported stale against min 1.2 and
		// aes setup reinstalled it on every run. A false actionable verdict
		// is worse than a lossy one, which is what the tmux case was.
		{"btop with ANSI colour", "btop version: \x1b[1m1.4.7\x1b[0m\n", "1.4.7"},
		{"ansi with no version after it", "\x1b[1mtool\x1b[0m\n", ""},
		{"ntm, from the version subcommand", "ntm version 1.18.2\n", "1.18.2"},
		{"agent-mail", "am 0.3.36\n", "0.3.36"},
		{"beads viewer, v-prefixed", "bv v0.16.4\n", "0.16.4"},
		{"cass", "cass 0.6.11\n", "0.6.11"},
		{"zoxide", "zoxide 0.9.4\n", "0.9.4"},
		{"leading v is not part of the match", "v1.2.3\n", "1.2.3"},
		{"no trailing newline", "rg 13.0.0", "13.0.0"},
		{"surrounding whitespace", "   jq 1.6   \n", "1.6"},

		// No digits means no version. Never a guess, never a fabricated 0.
		{"no digits at all", "unknown\n", ""},

		// Line 1 wins whenever it has digits, so the existing lenient rule is
		// never traded away for the fallback.
		{"line 1 wins over line 2", "tool 1.2.3\nv9.9.9 other\n", "1.2.3"},
		{"a bare single number on line 1 is still a version", "tmux 3\n", "3"},

		// Banner-style tools: the description is line 1 and the version is
		// line 2. This is eza's real output shape.
		{"eza banner, version on line 2", "eza eza - A modern, maintained replacement for ls\nv0.23.5 [+git]\nhttps://github.com/eza-community/eza\n", "0.23.5"},

		// The counter-case the fallback has to survive. A copyright year is a
		// bare number with no dot, so requiring two components rejects it. This
		// is the reason the fallback is strict where line 1 is not.
		{"copyright year on line 2 is not a version", "some tool\nCopyright (c) 2001-2024 Python Software Foundation\n", ""},
		{"a URL on line 2 is not a version", "tool\nhttps://example.com/x\n", ""},
		{"empty output", "", ""},
		{"whitespace only", "   \n", ""},
		{"words before any number", "coming soon\n", ""},

		// Line 1 is the source. Later lines are only consulted when it has no
		// digits at all, which is the eza/banner case rather than the general
		// one, so a version or a year on line 2 never displaces a real line 1.
		{"second line ignored when line 1 has a version", "tool 1.2.3\nbuilt 2019 with go1.4\n", "1.2.3"},
		{"copyright year ignored when line 1 has a version", "ripgrep 14.1.1\nCopyright 2011-2024\n", "14.1.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractVersion(tc.output); got != tc.want {
				t.Errorf("ExtractVersion(%q) = %q, want %q", tc.output, got, tc.want)
			}
		})
	}
}

// Dotted-integer comparison, not semver. The whole point is that "14.1" and
// "14.1.0" are the same version for the question being asked.
func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"14.1", "14.1.0", 0},
		{"14.1.0", "14.1", 0},
		{"14.1.1", "14.0", 1},
		{"14.0.9", "14.1", -1},
		{"3.4", "3.4", 0},
		{"1.0.283", "1.0.283", 0},
		{"1.0.284", "1.0.283", 1},
		{"1.0.282", "1.0.283", -1},
		{"2.0.0", "10.0.0", -1}, // numeric, not lexical
		{"10.0.0", "9.0.0", 1},  // numeric, not lexical
		{"1.0", "1.0.1", -1},
		{"1.0.1", "1.0", 1},
		{"14.1.1", "14.1.1", 0},
		// A pre-release suffix must compare as its numeric part, not as zero.
		{"3.6b", "3.4", 1},
		{"3.6b", "3.6", 0},
		{"3.5a", "3.6", -1},
		{"3.6b", "3.7", -1},
	} {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// The suffix capture and leadingInt are one change. Capturing "3.6b" without
// tolerating it in the comparison would make componentAt parse "6b" as 0,
// turning 3.6b into 3.0 — below any 3.x minimum, reported stale forever and
// reinstalled on every run. This guards that coupling in both directions.
func TestPreReleaseSuffixComparesAsItsNumericPart(t *testing.T) {
	// A pre-release must not read as older than the release it precedes.
	for _, tc := range []struct {
		got  string
		min  string
		want int
	}{
		{"3.6b", "3.4", 1},
		{"3.6b", "3.6", 0},
		{"3.6b", "3.7", -1},
		{"3.5a", "3.6", -1},
		{"1.0.283", "1.0.0", 1},
	} {
		if c := CompareVersions(tc.got, tc.min); c != tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.got, tc.min, c, tc.want)
		}
	}

	// And leadingInt must read the digits, not fail to the zero.
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"6b", 6}, {"5a", 5}, {"0", 0}, {"", 0}, {"283", 283}, {"b", 0}, {"12rc", 12},
	} {
		if got := leadingInt(tc.in); got != tc.want {
			t.Errorf("leadingInt(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestVerifyCommandOnPathIsOK(t *testing.T) {
	tool := &manifest.Tool{
		Name: "sh",
		Verify: &manifest.Verify{
			Command: "sh",
		},
	}
	got := Verify(tool)
	if got.Status != StatusOK {
		t.Errorf("Status = %q, want %q", got.Status, StatusOK)
	}
	if got.Path == "" {
		t.Error("Path was not resolved for a tool found on PATH")
	}
	if got.Tool != "sh" {
		t.Errorf("Tool = %q, want %q", got.Tool, "sh")
	}
}

func TestVerifyCommandAbsentIsMissing(t *testing.T) {
	tool := &manifest.Tool{
		Name:   "ghost",
		Verify: &manifest.Verify{Command: missingBin},
	}
	got := Verify(tool)
	if got.Status != StatusMissing {
		t.Errorf("Status = %q, want %q", got.Status, StatusMissing)
	}
	// A missing tool is an outcome, not an error: Verify has no error to return.
	if got.Path != "" {
		t.Errorf("Path = %q, want empty for a missing tool", got.Path)
	}
}

// A tool may check presence purely through provides, with verify: null.
func TestVerifyNilWithProvides(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		got := Verify(&manifest.Tool{Name: "sh", Provides: []string{"sh"}})
		if got.Status != StatusOK {
			t.Errorf("Status = %q, want %q", got.Status, StatusOK)
		}
	})
	t.Run("absent", func(t *testing.T) {
		got := Verify(&manifest.Tool{Name: "ghost", Provides: []string{missingBin}})
		if got.Status != StatusMissing {
			t.Errorf("Status = %q, want %q", got.Status, StatusMissing)
		}
	})
}

// The companion-binary case: the primary resolves, a provides entry does not.
// Checking only the primary would report this tool as fine.
func TestVerifyMissingProvidesEntryIsDetected(t *testing.T) {
	tool := &manifest.Tool{
		Name:     "partial",
		Verify:   &manifest.Verify{Command: "sh"},
		Provides: []string{"sh", missingBin},
	}
	got := Verify(tool)
	if got.Status != StatusMissing {
		t.Errorf("Status = %q, want %q — a provides entry does not resolve", got.Status, StatusMissing)
	}
}

// A version command whose output is older than the minimum is stale, not an
// error, and not "missing".
func TestVerifyVersionComparison(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		min     string
		want    Status
	}{
		{"below minimum", "echo 1.0.0", "2.0.0", StatusStale},
		{"at minimum", "echo 2.0.0", "2.0.0", StatusOK},
		{"above minimum", "echo 3.0.0", "2.0.0", StatusOK},
		{"newer is never stale", "echo 14.1.1", "14.0", StatusOK},
		{"dotted equality is not inequality", "echo 14.1", "14.1.0", StatusOK},
		{"just under", "echo 14.0.9", "14.1", StatusStale},
		{"no min means presence only", "echo 1.0.0", "", StatusOK},
		// A declared minimum that could not be read is unknown. It is not OK
		// (that claims the minimum was met) and not Stale (that claims it was
		// missed, and would make aes reinstall a working tool forever).
		{"unparseable version with a min is unknown", "echo unknown", "2.0.0", StatusUnknown},
		{"failing version command with a min is unknown", "exit 1", "2.0.0", StatusUnknown},
		{"unparseable version with no min is still OK", "echo unknown", "", StatusOK},
		{"failing version command with no min is still OK", "exit 1", "", StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := &manifest.Tool{
				Name:   "probe",
				Verify: &manifest.Verify{Command: "sh", Version: &manifest.VersionCheck{Command: tc.command, Min: tc.min}},
			}
			got := Verify(tool)
			if got.Status != tc.want {
				t.Errorf("Status = %q, want %q", got.Status, tc.want)
			}
		})
	}
}

// StatusUnknown covers two situations that call for opposite handling, and the
// status alone cannot say which happened.
//
// A version command that RAN and printed something unparseable means the tool
// works and we could not read its number. A version command that ran and
// exited non-zero means the tool is broken. Both are Unknown, and setup treats
// Unknown as a warning for both — which is right, and is why the rollback path
// needs a second signal rather than a different verdict.
//
// The third case is a timeout, and it belongs with neither: a binary that takes
// too long may be perfectly good, so a rollback on it would revert a working
// install. "Unavailable verification is not a failure" is not a nicety here;
// it is the difference between repairing a machine and breaking it.
func TestVerifyDistinguishesBrokenFromUnreadable(t *testing.T) {
	for _, tc := range []struct {
		name       string
		command    string
		wantStatus Status
		wantProbe  bool
	}{
		{"exits non-zero: the tool ran and failed", "exit 1", StatusUnknown, true},
		{"prints nothing and fails: still broken", "false", StatusUnknown, true},
		{"runs and prints garbage: unreadable, not broken", "echo not-a-version", StatusUnknown, false},
		{"runs and prints a version: healthy", "echo 2.0.0", StatusOK, false},
		{"below minimum: stale, not broken", "echo 1.0.0", StatusStale, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := &manifest.Tool{
				Name:   "probe",
				Verify: &manifest.Verify{Command: "sh", Version: &manifest.VersionCheck{Command: tc.command, Min: "2.0.0"}},
			}
			got := Verify(tool)
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.ProbeFailed != tc.wantProbe {
				t.Errorf("ProbeFailed = %v, want %v (the rollback path reads this, not Status)",
					got.ProbeFailed, tc.wantProbe)
			}
		})
	}
}

// A timeout must not read as broken. The command is slow rather than wrong,
// and rolling a working install back because a probe was slow is how a repair
// turns into damage.
func TestVerifyTimeoutIsNotProbeFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("sleeps for longer than the probe budget")
	}
	tool := &manifest.Tool{
		Name:   "probe",
		Verify: &manifest.Verify{Command: "sh", Version: &manifest.VersionCheck{Command: "sleep 30", Min: "2.0.0"}},
	}
	got := Verify(tool)
	if got.ProbeFailed {
		t.Error("a timed-out probe was reported as a broken tool; a slow binary is not a broken one")
	}
	if got.Status != StatusUnknown {
		t.Errorf("Status = %q, want %q — a timeout is an unknown, not a verdict", got.Status, StatusUnknown)
	}
}

func TestVerifyReportsDetectedVersion(t *testing.T) {
	tool := &manifest.Tool{
		Name: "probe",
		Verify: &manifest.Verify{
			Command: "sh",
			Version: &manifest.VersionCheck{Command: "echo jq-1.7.1", Min: "1.0.0"},
		},
	}
	got := Verify(tool)
	if got.Version != "1.7.1" {
		t.Errorf("Version = %q, want %q", got.Version, "1.7.1")
	}
	if got.Status != StatusOK {
		t.Errorf("Status = %q, want %q", got.Status, StatusOK)
	}
}

// When nothing can be checked, fail closed. Reporting OK for a tool with no
// binaries would be a silently lying verifier.
func TestVerifyWithNothingToCheck(t *testing.T) {
	got := Verify(&manifest.Tool{Name: "empty"})
	if got.Status != StatusMissing {
		t.Errorf("Status = %q, want %q", got.Status, StatusMissing)
	}
}

func TestVerifyIsRepeatable(t *testing.T) {
	// Same input must give the same result; a verifier that drifted between
	// calls would make doctor report phantom drift.
	tool := &manifest.Tool{
		Name:   "probe",
		Verify: &manifest.Verify{Command: "sh", Version: &manifest.VersionCheck{Command: "echo 1.2.3", Min: "1.0.0"}},
	}
	first := Verify(tool)
	second := Verify(tool)
	if first != second {
		t.Errorf("Verify is not deterministic: %+v then %+v", first, second)
	}
}

func TestVerifyContextRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tool := &manifest.Tool{
		Name:   "probe",
		Verify: &manifest.Verify{Command: "sh", Version: &manifest.VersionCheck{Command: "echo 1.2.3", Min: "1.0.0"}},
	}
	// Presence still resolves; the cancelled version probe must not hang or
	// panic, and must report the minimum as unchecked rather than met.
	got := VerifyContext(ctx, tool)
	if got.Version != "" {
		t.Errorf("Version = %q, want empty for a cancelled probe", got.Version)
	}
	if got.Status != StatusUnknown {
		t.Errorf("Status = %q, want %q for a cancelled probe with a declared min", got.Status, StatusUnknown)
	}
}

// Every status must be reachable, and they must mean different things. A
// collapsed pair here is exactly the bug StatusUnknown exists to prevent.
func TestStatusIsExhaustiveAndDistinct(t *testing.T) {
	cases := map[Status]struct {
		command string
		min     string
	}{
		StatusOK:      {"echo 5.0.0", "1.0.0"},
		StatusStale:   {"echo 1.0.0", "2.0.0"},
		StatusUnknown: {"exit 1", "2.0.0"},
	}
	seen := map[Status]bool{}
	for want, tc := range cases {
		tool := &manifest.Tool{
			Name:   "probe",
			Verify: &manifest.Verify{Command: "sh", Version: &manifest.VersionCheck{Command: tc.command, Min: tc.min}},
		}
		got := Verify(tool)
		if got.Status != want {
			t.Errorf("command %q min %q: Status = %q, want %q", tc.command, tc.min, got.Status, want)
		}
		seen[got.Status] = true
	}
	if len(seen) != 3 {
		t.Errorf("expected 3 distinct statuses, got %d", len(seen))
	}
}

func TestBinariesDeduplicates(t *testing.T) {
	tool := &manifest.Tool{
		Name:     "x",
		Verify:   &manifest.Verify{Command: "sh"},
		Provides: []string{"sh", "sh", "cat"},
	}
	got := binaries(tool)
	want := []string{"sh", "cat"}
	if len(got) != len(want) {
		t.Fatalf("binaries = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("binaries[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
