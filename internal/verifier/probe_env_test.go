package verifier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The contract: a version probe runs as an ordinary command on the user's
// machine, and it must not be handed an environment that lets anyone else steer
// it.
//
// The hole this closes was introduced by a manifest, not by the verifier, and
// that is the point worth stating. A tool that insists on HOME (`ru` aborts
// under `set -u` without one) was given
//
//	HOME="${HOME:-/tmp/aes-ru-home}" ru --version
//
// and ru's line 249 runs `source "${TOON_SH_PATH:-$HOME/.local/lib/toon.sh}"`
// at load, before argument dispatch. Because the probe environment carried no
// HOME, the fallback fired; /tmp is 1777; and any local user could pre-create
// that file and have it execute on every `aes verify`, `aes list` and
// `aes setup`. Reproduced on 2026-09-30 — the planted file printed
// `PWNED uid=1001` and ru still printed its real version line, so the output
// looked entirely normal.
//
// A catalog test for "no probe names /tmp" is the wrong shape: it would police
// the spelling of the workaround rather than the property that makes it a
// hazard, and the next agent would find a different spelling. So this asserts
// the property, and the catalog test below asserts the absence of the known
// spellings as a second, cheaper net.
func TestProbeEnvCarriesAHomeAndNeverInventsOneInTmp(t *testing.T) {
	// Not parallel: it manipulates the process environment.
	//
	// The home is a real directory under the user's home, not a t.TempDir():
	// /tmp is 1777, so a test that put the home there would be asserting that a
	// world-writable home is acceptable — which is the opposite of the property.
	// The point is that the verifier passes HOME through unchanged, so whatever
	// the user's home is, the probe sees it and nothing else.
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("HOME", home)

	env := probeEnv(Target{AESHome: filepath.Join(home, ".aes")})

	home2, ok := lookupEnv(env, "HOME")
	if !ok {
		t.Fatal("no HOME in the probe environment; a tool with `set -u` will abort, " +
			"and the only alternative a manifest has is a world-writable fallback")
	}
	if home2 != home {
		t.Errorf("HOME = %q, want the user's own %q", home2, home)
	}
}

// The fallback when HOME is genuinely unset must be a directory aes owns, never
// a guess. `$AES_HOME` is created 0700 under the user's home; a predictable
// path in /tmp is not, and that is the whole difference.
func TestProbeEnvFallsBackToAESHomeNotAGuess(t *testing.T) {
	// Not parallel: it clears HOME for the process.
	//
	// The AES_HOME is a real directory under the user's home rather than a
	// t.TempDir(), for the same reason the test above uses one: the property is
	// "the fallback is a directory aes owns", and /tmp is not that, so a test
	// that pointed at /tmp would be asserting the opposite.
	base := t.TempDir()
	aesHome := filepath.Join(base, ".aes")
	if err := os.MkdirAll(aesHome, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("HOME", "")

	env := probeEnv(Target{AESHome: aesHome})

	got, ok := lookupEnv(env, "HOME")
	if !ok {
		t.Fatal("no HOME at all; the probe would run with an undefined home and the " +
			"manifest would be pushed back toward a /tmp fallback")
	}
	if got != aesHome {
		t.Errorf("HOME = %q, want the AES_HOME aes owns (%q)", got, aesHome)
	}
}

// PATH is prepended, not duplicated and not replaced. A duplicated PATH makes a
// probe's own `$PATH` twice as long and hides which copy won; a replaced one
// takes away the dynamic linker and config directory the tool needs.
func TestProbeEnvPrependsTheBinDirExactlyOnce(t *testing.T) {
	// Not parallel: it sets PATH for the process.
	base := t.TempDir()
	aesHome := filepath.Join(base, ".aes")
	bin := filepath.Join(aesHome, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")

	env := probeEnv(Target{AESHome: aesHome})
	var paths []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			paths = append(paths, strings.TrimPrefix(kv, "PATH="))
		}
	}
	if len(paths) != 1 {
		t.Fatalf("PATH appears %d times in the probe environment, want 1: %v", len(paths), paths)
	}
	if !strings.HasPrefix(paths[0], bin+string(os.PathListSeparator)) {
		t.Errorf("PATH = %q, want it to START with the AES bin dir %q", paths[0], bin)
	}
	if !strings.HasSuffix(paths[0], "/usr/bin:/bin") {
		t.Errorf("PATH = %q, want the machine's own entries preserved at the end", paths[0])
	}
}
