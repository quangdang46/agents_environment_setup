package envgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// stubDestinations replaces the strategy-to-directory mapping so tests never
// depend on what the machine running them happens to have installed.
func stubDestinations(t *testing.T, m map[string]string) {
	t.Helper()
	prev := destinationFor
	destinationFor = func(strategy, home string) (string, bool) {
		dir, ok := m[strategy]
		return dir, ok && dir != ""
	}
	t.Cleanup(func() { destinationFor = prev })
}

// defaultStub installs a predictable destination map and returns the home
// it was built around. Tests must use the returned home, not a fresh
// TempDir, or they assert against a different tree than the one generated.
func defaultStub(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	stubDestinations(t, map[string]string{
		manifest.StrategyGithubRelease: filepath.Join(home, ".aes", "bin"),
		manifest.StrategyGo:            filepath.Join(home, "go", "bin"),
		manifest.StrategyCargo:         filepath.Join(home, ".cargo", "bin"),
		manifest.StrategyUV:            filepath.Join(home, ".local", "bin"),
		manifest.StrategyNPM:           filepath.Join(home, "npm", "bin"),
		// brew and apt deliberately absent: they contribute nothing.
	})
	return home
}

func TestHeaderIsTheOwnershipMarker(t *testing.T) {
	stubDestinations(t, nil)
	home := t.TempDir()

	out, err := Generate(nil, home)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	text := string(out)

	if !strings.Contains(text, "DO NOT EDIT") {
		t.Errorf("header does not say DO NOT EDIT:\n%s", text)
	}
	if !strings.Contains(text, "aes env --write") {
		t.Errorf("header does not name the regeneration command:\n%s", text)
	}
	if !strings.HasPrefix(text, Header) {
		t.Error("output does not begin with the header")
	}
}

// TestGenerateIsDeterministic is the property that makes a generated file
// diffable: two runs with the tools in different orders must produce the
// same bytes.
func TestGenerateIsDeterministic(t *testing.T) {
	home := defaultStub(t)

	forward := []Tool{
		{Name: "a-rg", Strategy: manifest.StrategyGo},
		{Name: "b-cargo", Strategy: manifest.StrategyCargo},
		{Name: "c-uv", Strategy: manifest.StrategyUV},
	}
	reversed := []Tool{forward[2], forward[1], forward[0]}

	a, err := Generate(forward, home)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	b, err := Generate(reversed, home)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if string(a) != string(b) {
		t.Errorf("output differs by input order:\n--- forward ---\n%s\n--- reversed ---\n%s", a, b)
	}
}

// TestNoTimestamp guards the rule that makes a regenerated file show only
// real changes.
//
// The check looks for a date or time *shape*, not a bare digit run. An
// earlier version tested for the substring "202", which meant to catch a
// year — and then failed at random, because a macOS temp directory name can
// contain those three digits. That is the substring-assertion trap: a test
// that fires on correct code trains you to ignore it.
func TestNoTimestamp(t *testing.T) {
	home := defaultStub(t)
	out, err := Generate([]Tool{{Name: "x", Strategy: manifest.StrategyGo}}, home)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	text := string(out)

	// Shapes a timestamp would take, none of which may appear.
	for _, pattern := range []string{
		`\d{4}-\d{2}-\d{2}`,  // 2026-09-27
		`\d{2}:\d{2}:\d{2}`,  // 12:34:56
		`\d{4}-\d{2}-\d{2}T`, // RFC3339
		`Generated on`,
		`UTC|GMT`,
	} {
		if re := regexp.MustCompile(pattern); re.MatchString(text) {
			t.Errorf("output matches %q, which suggests a timestamp:\n%s", pattern, text)
		}
	}

	// The point of the property: two runs over the same input are identical,
	// and that is checkable directly rather than by pattern-guessing.
	again, err := Generate([]Tool{{Name: "x", Strategy: manifest.StrategyGo}}, home)
	if err != nil {
		t.Fatalf("Generate again: %v", err)
	}
	if text != string(again) {
		t.Error("two runs over the same input differ, so something time-dependent got in")
	}
}

func TestStrategyContributions(t *testing.T) {
	home := defaultStub(t)

	out, err := Generate([]Tool{
		{Name: "rg", Strategy: manifest.StrategyGo},
		{Name: "fd", Strategy: manifest.StrategyCargo},
		{Name: "ruff", Strategy: manifest.StrategyUV},
	}, home)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	text := string(out)

	for _, want := range []string{"go/bin", ".cargo/bin", ".local/bin"} {
		if !strings.Contains(text, want) {
			t.Errorf("output is missing the %s contribution:\n%s", want, text)
		}
	}
}

// TestPackageManagersContributeNothing pins the rule that keeps PATH clean:
// brew and apt already place their binaries on PATH.
func TestPackageManagersContributeNothing(t *testing.T) {
	home := defaultStub(t)

	brewOnly, err := Generate([]Tool{{Name: "jq", Strategy: manifest.StrategyPackage}}, home)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.Contains(string(brewOnly), "case \":$PATH:\"") {
		t.Errorf("a package-manager tool added a PATH entry:\n%s", brewOnly)
	}

	withGo, err := Generate([]Tool{
		{Name: "jq", Strategy: manifest.StrategyPackage},
		{Name: "rg", Strategy: manifest.StrategyGo},
	}, home)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Exactly one guarded block: the go contribution and nothing from brew.
	if got := strings.Count(string(withGo), "case \":$PATH:\""); got != 1 {
		t.Errorf("PATH blocks = %d, want 1:\n%s", got, withGo)
	}
}

func TestDuplicateStrategyContributesOnce(t *testing.T) {
	out, err := Generate([]Tool{
		{Name: "a", Strategy: manifest.StrategyGo},
		{Name: "b", Strategy: manifest.StrategyGo},
		{Name: "c", Strategy: manifest.StrategyGo},
	}, defaultStub(t))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := strings.Count(string(out), "case \":$PATH:\""); got != 1 {
		t.Errorf("PATH blocks = %d, want 1 for three tools sharing a strategy:\n%s", got, out)
	}
}

func TestVariableDedupeIsDeterministic(t *testing.T) {
	stubDestinations(t, nil)
	home := t.TempDir()

	// Two tools set FOO. The winner must be decided by sorted tool name, not
	// by which tool happened to be passed first.
	first := []Tool{
		{Name: "zebra", Strategy: manifest.StrategyPackage, Vars: map[string]string{"FOO": "from-zebra"}},
		{Name: "alpha", Strategy: manifest.StrategyPackage, Vars: map[string]string{"FOO": "from-alpha"}},
	}
	second := []Tool{first[1], first[0]}

	a, err := Generate(first, home)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	b, err := Generate(second, home)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if string(a) != string(b) {
		t.Errorf("deduped output depends on input order:\n%s\n---\n%s", a, b)
	}
	if got := strings.Count(string(a), "export FOO="); got != 1 {
		t.Errorf("FOO exported %d times, want exactly 1:\n%s", got, a)
	}
	// Sorted-name order means alpha wins the tie.
	if !strings.Contains(string(a), "from-alpha") {
		t.Errorf("tie was not won by sorted name:\n%s", a)
	}
}

// TestShellQuotingRoundTripsThroughARealShell is stronger than a string
// comparison: it proves the generated file, when sourced by sh, produces the
// value that went in.
func TestShellQuotingRoundTripsThroughARealShell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh available")
	}
	stubDestinations(t, nil)

	nasty := "it's $HOME \"quoted\" `cmd` \\back\nnewline"
	home := t.TempDir()
	path := filepath.Join(home, "env.sh")

	if err := Write(path, []Tool{{
		Name:     "nasty",
		Strategy: manifest.StrategyPackage,
		Vars:     map[string]string{"AES_TEST_VALUE": nasty},
	}}, home); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Sourcing the file and echoing the variable must reproduce the input
	// exactly, including the embedded newline.
	script := ". " + shellSingle(path) + "\nprintf '%s' \"$AES_TEST_VALUE\""
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("sourcing the generated file failed: %v", err)
	}
	if string(out) != nasty {
		t.Errorf("value did not survive the round trip\n got: %q\nwant: %q", out, nasty)
	}
}

func TestNewlineInValueDoesNotBreakTheFile(t *testing.T) {
	stubDestinations(t, nil)
	out, err := Generate([]Tool{{
		Name:     "n",
		Strategy: manifest.StrategyPackage,
		Vars:     map[string]string{"MULTI": "one\ntwo\nPATH=/evil"},
	}}, t.TempDir())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// A line scan cannot tell a quoted continuation from a live assignment:
	// "PATH=/evil" on its own line is harmless inside single quotes and
	// catastrophic outside them. Sourcing the file decides it.
	if _, err := exec.LookPath("sh"); err == nil {
		path := filepath.Join(t.TempDir(), "env.sh")
		if err := Write(path, []Tool{{
			Name:     "n",
			Strategy: manifest.StrategyPackage,
			Vars:     map[string]string{"MULTI": "one\ntwo\nPATH=/evil"},
		}}, t.TempDir()); err != nil {
			t.Fatalf("Write: %v", err)
		}
		script := ". " + shellSingle(path) + "\nprintf '%s' \"$MULTI\""
		got, err := exec.Command("sh", "-c", script).Output()
		if err != nil {
			t.Fatalf("sourcing failed: %v", err)
		}
		if string(got) != "one\ntwo\nPATH=/evil" {
			t.Errorf("value did not survive sourcing\n got: %q\nwant: %q", got, "one\ntwo\nPATH=/evil")
		}
	}
	_ = out
}

// TestWriteRefusesToClobber proves the refusal: the user's file must be
// byte-identical afterwards, not merely un-changed in spirit.
func TestWriteRefusesToClobber(t *testing.T) {
	stubDestinations(t, nil)
	home := t.TempDir()
	path := filepath.Join(home, "env.sh")

	const handWritten = "# my own env, hands off\nexport EDITOR=vim\n"
	if err := os.WriteFile(path, []byte(handWritten), FileMode); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := Write(path, []Tool{{Name: "rg", Strategy: manifest.StrategyGo}}, home)
	if err == nil {
		t.Fatal("expected a refusal for a file with no aes header")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("error %q does not explain the refusal", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(after) != handWritten {
		t.Errorf("the file was modified despite the refusal:\n got: %q\nwant: %q", after, handWritten)
	}
}

func TestWriteIsAtomic(t *testing.T) {
	stubHome := defaultStub(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "env.sh")

	if err := Write(path, []Tool{{Name: "rg", Strategy: manifest.StrategyGo}}, stubHome); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	// A second write updates a file we own.
	if err := Write(path, []Tool{{Name: "fd", Strategy: manifest.StrategyCargo}}, stubHome); err != nil {
		t.Fatalf("second Write: %v", err)
	}

	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	goDir := filepath.Join(stubHome, "go", "bin")
	cargoDir := filepath.Join(stubHome, ".cargo", "bin")
	if !strings.Contains(string(updated), cargoDir) {
		t.Errorf("update did not take effect:\n%s", updated)
	}
	// Compare whole paths: "go/bin" is a substring of "cargo/bin".
	if strings.Contains(string(updated), goDir) {
		t.Errorf("stale content survived the update:\n%s", updated)
	}

	// No temp file may survive a successful write.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file %s was left behind", e.Name())
		}
	}
}

func TestWriteFileMode(t *testing.T) {
	stubHome := defaultStub(t)
	path := filepath.Join(t.TempDir(), "env.sh")

	if err := Write(path, []Tool{{Name: "rg", Strategy: manifest.StrategyGo}}, stubHome); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != FileMode {
		t.Errorf("mode = %o, want %o", got, FileMode)
	}
}

// TestSourcingTwiceIsANoOp exercises the PATH guard through a real shell.
// Without the guard, sourcing twice would prepend the directory twice.
func TestSourcingTwiceIsANoOp(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh available")
	}
	stubHome := defaultStub(t)
	path := filepath.Join(t.TempDir(), "env.sh")

	if err := Write(path, []Tool{{Name: "rg", Strategy: manifest.StrategyGo}}, stubHome); err != nil {
		t.Fatalf("Write: %v", err)
	}

	script := ". " + shellSingle(path) + "\n. " + shellSingle(path) + "\nprintf '%s' \"$PATH\""
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("sourcing failed: %v", err)
	}
	got := string(out)

	dir := filepath.Join(stubHome, "go", "bin")
	if n := strings.Count(got, dir); n != 1 {
		t.Errorf("%s appears %d times on PATH after sourcing twice, want 1:\n%s", dir, n, got)
	}
}

func TestGenerateRequiresHome(t *testing.T) {
	stubDestinations(t, nil)
	if _, err := Generate(nil, ""); err == nil {
		t.Fatal("expected an error for an empty home")
	}
}

// shellSingle quotes a path for use inside a sh -c script.
func shellSingle(p string) string {
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}
