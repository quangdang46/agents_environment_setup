package verifier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// The contract: the verifier answers about the machine its CALLER names, not
// the machine the process happens to run on.
//
// This was not a theoretical concern. `App.Home` is injected and
// os.UserHomeDir() is not, so the verifier re-deriving AES_HOME answered about
// the developer's real ~/.aes while the command was working on a temp one. The
// observable failure was in `aes uninstall yq`: a test whose AES_HOME held no
// yq, with a removal that succeeded, reported "the binary is still present
// (/home/<user>/.aes/bin/yq)" and exited 6 — because a yq that the developer
// had genuinely installed, months ago, was standing in for the one under test.
//
// A test for "AES_HOME is passed" would pass the moment anyone re-adds the
// fallback, and would have passed before the fix too. The assertion has to be
// about discrimination: two homes, one binary, two different answers.
func TestVerifyInAnswersAboutTheNamedHomeNotTheUsers(t *testing.T) {
	t.Parallel()

	// A name no real machine has, so a stray binary on the developer's PATH
	// cannot make this pass. The path assertion below is the second guard: a
	// name that somehow did exist elsewhere would resolve outside withBin and
	// fail there rather than quietly satisfy the first check.
	const bin = "aes-target-contract-probe"

	withBin := t.TempDir()
	withoutBin := t.TempDir()
	if err := os.MkdirAll(filepath.Join(withBin, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(withoutBin, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// A real executable that prints a version, because a file that is present
	// but not runnable reports StatusUnknown, and Unknown would be a third
	// answer that hides which of the two paths was taken.
	script := "#!/bin/sh\necho probe 1.0.0\n"
	if err := os.WriteFile(filepath.Join(withBin, "bin", bin), []byte(script), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tool := &manifest.Tool{
		Name: "probe",
		Verify: &manifest.Verify{
			Command: bin,
			Version: &manifest.VersionCheck{Command: bin + " --version", Min: manifest.Floor{Default: "1.0"}},
		},
	}

	// PATH is deliberately left alone: probeEnv prepends the AES bin directory
	// to it, and that is the mechanism under test. Narrowing it here would make
	// the probe unrunnable for a reason that has nothing to do with which home
	// the verifier looks in.
	if got := VerifyIn(Target{AESHome: withBin}, tool); got.Status != StatusOK {
		t.Fatalf("the home holding the binary reported %s (path %q, version %q), want ok",
			got.Status, got.Path, got.Version)
	} else if !strings.HasPrefix(got.Path, withBin) {
		t.Errorf("path = %q, want it inside the AES_HOME that was passed (%q); "+
			"the verifier resolved the binary somewhere else", got.Path, withBin)
	}

	// The load-bearing half. One binary, two homes, two answers: a verifier that
	// ignored Target.AESHome and fell back to the user's home would report ok
	// here whenever the developer happens to have the tool installed, and
	// missing here only by accident of never having installed it.
	if got := VerifyIn(Target{AESHome: withoutBin}, tool); got.Status != StatusMissing {
		t.Errorf("the home WITHOUT the binary reported %s (path %q), want missing; "+
			"the verifier is not looking at the AES_HOME it was given",
			got.Status, got.Path)
	}
}

// Target{} means "the caller's own home", which is the right default for a
// library entry point and the reason the two forms cannot be one function: a
// caller that forgets to pass a home gets a machine-dependent answer, and only
// the caller knows whether that is acceptable.
func TestTargetWithoutAHomeFallsBackToTheUsers(t *testing.T) {
	t.Parallel()

	want := DefaultAESHome()
	if want == "" {
		t.Skip("no home directory on this machine")
	}
	if got := (Target{}).home(); got != want {
		t.Errorf("Target{}.home() = %q, want the user's own AES_HOME %q", got, want)
	}
	if got := (Target{AESHome: "/tmp/explicit"}).home(); got != "/tmp/explicit" {
		t.Errorf("an explicit home was overridden: got %q", got)
	}
	if got := (Target{AESHome: "/tmp/explicit"}).binDir(); got != filepath.Join("/tmp/explicit", "bin") {
		t.Errorf("binDir() = %q, want <explicit home>/bin", got)
	}
}
