package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An alias can shadow a binary aes installed and verified.
//
// `aes verify` resolves through exec.LookPath, which finds the binary and is
// immune to shell aliases — that is a deliberate property and AGENTS.md traps
// anyone who replaces it with `command -v`. But the consequence is that aes
// reports `ok` for a tool the user's own shell will not run: the binary is
// present, and the alias is what actually executes.
//
// ACFS shipped exactly that bug. An older version declared
// `alias br='bun run dev'`, which shadowed the `br` binary it had just
// installed, and the fix in their zshrc is a guard that unaliases `br` when a
// real binary exists. Having the guard means having had the bug.
//
// # Why this reads files rather than running the shell
//
// The honest way to ask "what will my shell do" is to run it. That is also the
// one thing this must not do: an rc file is arbitrary user code that can
// prompt, block on input, start a tmux server, open a browser or run a
// prompt-toolkit that waits forever. `aes doctor` has a timeout budget and no
// TTY, and turning it into something that executes a user's startup files would
// make a diagnostic able to hang the command that is meant to explain the hang.
//
// So it reads. That is weaker — an alias from oh-my-zsh, a plugin, or a
// sourced file is invisible to it — and the finding says so rather than
// implying it saw everything.

// aliasCases exercise the parser, which has to survive real rc files: they are
// written by people, commented out, re-sourced, and occasionally binary.
var aliasCases = []struct {
	name  string
	input string
	want  map[string]string // alias name -> target
}{
	{
		name:  "the ordinary case",
		input: "alias cc='claude --dangerously-skip-permissions'\n",
		want:  map[string]string{"cc": "claude --dangerously-skip-permissions"},
	},
	{
		// zsh permits spaces around the equals; bash does not. Reading the
		// permissive form is right because we are parsing for a human's
		// attention, not reproducing shell behaviour.
		name:  "spaces around the equals",
		input: "alias jq = /usr/local/bin/jq\n",
		want:  map[string]string{"jq": "/usr/local/bin/jq"},
	},
	{
		name:  "double quoted, with a space in the value",
		input: `alias cdg="cd /home/dev/go/src"` + "\n",
		want:  map[string]string{"cdg": "cd /home/dev/go/src"},
	},
	{
		// An alias inside a comment is not an alias, and reporting it would be
		// a false positive about something the user deliberately turned off.
		name:  "commented out",
		input: "# alias tmux='tmux -L plugin'\nalias eza='eza --icons'\n",
		want:  map[string]string{"eza": "eza --icons"},
	},
	{
		// A commented-out alias yields nothing for two independent reasons:
		// the line is not the keyword, and — the one that actually holds — the
		// keyword check rejects it. The parser carries no comment check of its
		// own, so this case is what holds that line in place.
		name:  "indented comment",
		input: "if true; then\n    # alias sneaky='something'\n    #alias other='x'\nfi\n",
		want:  map[string]string{},
	},
	{
		// `aliasfoo=1` is a variable assignment that happens to start with
		// the keyword. Reading it as an alias produces a finding about a
		// command nobody can type.
		name:  "a variable that starts with the keyword",
		input: "aliasfoo=1\naliasb=2\n",
		want:  map[string]string{},
	},
	{
		name:  "indented, inside a function",
		input: "setup() {\n  alias dev='go run ./cmd/api'\n}\n",
		want:  map[string]string{"dev": "go run ./cmd/api"},
	},
	{
		name:  "later declaration wins, as it does in a shell",
		input: "alias ripgrep=rg\nalias rg=/usr/local/bin/rg\n",
		want:  map[string]string{"ripgrep": "rg", "rg": "/usr/local/bin/rg"},
	},
	{
		name:  "not an alias at all",
		input: "source ~/.other/aliases.sh\nexport FOO=bar\n# alias x=y\n",
		want:  map[string]string{},
	},
	{
		name:  "empty file",
		input: "",
		want:  map[string]string{},
	},
}

func TestReadShellAliases(t *testing.T) {
	for _, tc := range aliasCases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rc")
			if err := os.WriteFile(path, []byte(tc.input), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			got := readShellAliases(path)
			if len(got) != len(tc.want) {
				t.Errorf("read %d aliases, want %d:\n  got  %v\n  want %v",
					len(got), len(tc.want), got, tc.want)
			}
			for name, wantTarget := range tc.want {
				decl, ok := got[name]
				if !ok {
					t.Errorf("alias %q not found in %v", name, got)
					continue
				}
				if decl.Target != wantTarget {
					t.Errorf("alias %q target = %q, want %q", name, decl.Target, wantTarget)
				}
			}
		})
	}
}

// A missing rc file is the normal case on a machine that has never customised
// its shell, and it must not be an error. Reporting "cannot read ~/.zshrc" to
// a user who has no ~/.zshrc would be noise dressed as a diagnosis.
func TestReadShellAliasesOnAMissingFileIsEmpty(t *testing.T) {
	got := readShellAliases(filepath.Join(t.TempDir(), "does-not-exist"))
	if len(got) != 0 {
		t.Errorf("a missing rc produced %v, want nothing", got)
	}
}

// A real rc file is not guaranteed to be text. Reading one that is not must
// not panic and must not invent aliases out of the bytes.
func TestReadShellAliasesSurvivesBinaryContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rc")
	if err := os.WriteFile(path, []byte("alias real=thing\n\x00\x01\xff\xfe garbage\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := readShellAliases(path)
	if got["real"].Target != "thing" {
		t.Errorf("the readable alias was lost: %v", got)
	}
	if _, ok := got["garbage"]; ok {
		t.Errorf("an alias was invented from binary bytes: %v", got)
	}
}

// The finding must name the file, because "there is an alias" is not
// actionable and "there is an alias in ~/.zshrc" is.
func TestShadowFindingNamesTheFileAndTheTarget(t *testing.T) {
	d := filepath.Join(t.TempDir(), ".zshrc")
	if err := os.WriteFile(d, []byte("alias claude='claude --dangerously-skip-permissions'\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	aliases := readShellAliases(d)
	decl, ok := aliases["claude"]
	if !ok {
		t.Fatalf("the alias was not parsed: %v", aliases)
	}
	msg := shadowMessage("claude", decl, "/usr/bin/claude")
	for _, want := range []string{"claude", ".zshrc", "--dangerously-skip-permissions", "/usr/bin/claude"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
}

// The wiring, not the parser: a tool that verifies must actually produce a
// finding when its name is aliased. The parser tests would all pass with
// addAliasFindings never called.
func TestDoctorReportsAnAliasShadowingAVerifiedTool(t *testing.T) {
	h := newHarness(t)

	// A tool whose binary really is on PATH, so Verify returns ok and the
	// shadow is the only thing wrong.
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "shadowy")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho shadowy 1.0.0\n"), 0o755); err != nil {
		t.Fatalf("seed binary: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(rc, []byte("alias shadowy='shadowy --dangerously-skip-everything'\n"), 0o644); err != nil {
		t.Fatalf("write rc: %v", err)
	}

	h.app.CatalogRoot = writeAliasCatalog(t, home, "shadowy", binDir)
	h.app.ProfileDir = filepath.Join(home, "profiles")

	// A shadow is a WARNING, and doctor still exits 0. Nothing about the
	// machine is broken: the binary is installed, it verifies, and the alias
	// is a choice the user made. Making aes exit non-zero over a shell
	// preference would be aes refusing to run on a healthy machine.
	//
	// The assertion is therefore that the finding EXISTS, not that the exit
	// code changes. Asserting the exit code would encode the opposite decision
	// and the test would then be defending the wrong thing.
	if code := h.app.Run([]string{"doctor", "--json"}); code != 0 {
		t.Errorf("doctor exited %d for a warning; a shadow is not a broken machine:\n%s",
			code, h.stdout.String())
	}
	if !strings.Contains(h.stdout.String(), `"kind": "shadowed"`) {
		t.Errorf("doctor did not report a shadowed tool:\n%s", h.stdout.String())
	}
	if !strings.Contains(h.stdout.String(), "dangerously-skip-everything") {
		t.Errorf("the finding does not say what the alias actually runs:\n%s", h.stdout.String())
	}
	// And it must name the file, so the user can go and change it.
	if !strings.Contains(h.stdout.String(), ".zshrc") {
		t.Errorf("the finding does not name the file to edit:\n%s", h.stdout.String())
	}
	// The remedy must be the alias. `aes forget` drops a state entry and
	// cannot touch a shell; suggesting it sends the user to the wrong tool.
	if strings.Contains(h.stdout.String(), "aes forget") {
		t.Errorf("the finding suggests `aes forget`, which cannot fix an alias:\n%s", h.stdout.String())
	}
}

// The negative control, and the reason the first test means anything: with no
// alias, the same tool is clean. A doctor that always says "shadowed" would
// pass the test above.
func TestDoctorIsQuietWithoutAnAlias(t *testing.T) {
	h := newHarness(t)
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "quiet")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho quiet 1.0.0\n"), 0o755); err != nil {
		t.Fatalf("seed binary: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("alias somethingelse=thing\n"), 0o644); err != nil {
		t.Fatalf("write rc: %v", err)
	}
	h.app.CatalogRoot = writeAliasCatalog(t, home, "quiet", binDir)
	h.app.ProfileDir = filepath.Join(home, "profiles")

	h.app.Run([]string{"doctor", "--json"})
	if strings.Contains(h.stdout.String(), "shadowed") {
		t.Errorf("doctor reported a shadow where there is no alias:\n%s", h.stdout.String())
	}
}

// An alias that changes nothing is not a shadow. `alias rg=rg` is a no-op and
// reporting it would train a reader to skim past doctor.
func TestAliasThatChangesNothingIsNotAShadow(t *testing.T) {
	decls := map[string]aliasDecl{
		"rg":    {Name: "rg", Target: "rg", File: "~/.zshrc"},
		"real":  {Name: "real", Target: "something-else", File: "~/.zshrc"},
		"empty": {Name: "empty", Target: "", File: "~/.zshrc"},
	}
	tracked := map[string]bool{"rg": true, "real": true, "empty": true}
	got := shadowedAliases(decls, tracked)
	if len(got) != 1 {
		t.Fatalf("shadowed = %v, want only the one that actually shadows", got)
	}
	if _, ok := got["real"]; !ok {
		t.Error("the genuine shadow was not reported")
	}
}

// writeAliasCatalog puts one tool in a catalog whose binary resolves.
func writeAliasCatalog(t *testing.T, home, name, binDir string) string {
	t.Helper()
	root := filepath.Join(home, "catalog")
	dir := filepath.Join(root, "utility", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "name: " + name + "\ndescription: fixture\ncategory: utility\n" +
		"provides: [" + name + "]\n" +
		"verify:\n  command: " + name + "\n  version:\n    command: " + name +
		" --version\n    min: \"1.0.0\"\n" +
		"install:\n  darwin:\n    strategy: go\n    go_package: example.com/" + name + "@v1.0.0\n" +
		"  linux:\n    strategy: go\n    go_package: example.com/" + name + "@v1.0.0\n"
	if err := os.WriteFile(filepath.Join(dir, "tool.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return root
}

// A tool that does not verify is not shadowed — it is MISSING, and that is a
// bigger problem. Reporting both for one tool buries the first, and the alias
// line is noise beside "the binary is gone".
//
// Without this the StatusOK check could be deleted and nothing would notice.
func TestDoctorDoesNotReportAShadowForAToolThatIsMissing(t *testing.T) {
	h := newHarness(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	// The alias exists; the binary does not. Nothing is on PATH for it.
	t.Setenv("PATH", t.TempDir())
	if err := os.WriteFile(filepath.Join(home, ".zshrc"),
		[]byte("alias absent='absent --flag'\n"), 0o644); err != nil {
		t.Fatalf("write rc: %v", err)
	}
	h.app.CatalogRoot = writeAliasCatalog(t, home, "absent", "")
	h.app.ProfileDir = filepath.Join(home, "profiles")

	h.app.Run([]string{"doctor", "--json"})
	if strings.Contains(h.stdout.String(), `"kind": "shadowed"`) {
		t.Errorf("reported a shadow for a tool whose binary is missing; the missing "+
			"binary is the finding that matters:\n%s", h.stdout.String())
	}
}
