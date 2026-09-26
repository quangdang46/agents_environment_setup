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
		{"zoxide", "zoxide 0.9.4\n", "0.9.4"},
		{"leading v is not part of the match", "v1.2.3\n", "1.2.3"},
		{"no trailing newline", "rg 13.0.0", "13.0.0"},
		{"surrounding whitespace", "   jq 1.6   \n", "1.6"},

		// No digits means no version. Never a guess, never a fabricated 0.
		{"no digits at all", "unknown\n", ""},
		{"empty output", "", ""},
		{"whitespace only", "   \n", ""},
		{"words before any number", "coming soon\n", ""},

		// Only the first line is read; later ones carry years and build ids.
		{"second line ignored", "tool 1.2.3\nbuilt 2019 with go1.4\n", "1.2.3"},
		{"version on the second line only", "loading...\nready 2.0.0\n", ""},
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
