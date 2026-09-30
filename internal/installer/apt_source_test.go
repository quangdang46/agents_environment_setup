package installer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quangdang46/agents_environment_setup/internal/exec"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// A declared apt source is a privileged step, and failing at it must say so.
//
// The bug this pins: `installApt` ran `prepareAptSource` BEFORE the privilege
// check, and every failure inside it was printed and discarded. On a non-root
// machine /usr/share/keyrings is unwritable, so the keyring was never written,
// the repository was never added, and apt then served whatever the distro index
// had — or, for a package only that repository carries, nothing at all.
// Measured 2026-09-30 on vault: the tool reported "installed but verification
// says stale" against a 2.1.1 floor, which sends the user to look at a version
// number when the real fact is that nothing was installed and a privileged step
// was needed.
//
// The assertion is on the ERROR TYPE, not the message. A plain error here still
// exits 1 with a "stale"-flavoured outcome somewhere above; only
// PrivilegeError carries the command out and the exit-5 contract the rest of
// the product already speaks.
func TestAptSourceFailureOnANonRootMachineIsAPrivilegeError(t *testing.T) {
	// Not parallel: it chmods a directory, which is process-wide.
	//
	// Read-only, so MkdirAll succeeds and the create inside it fails — the
	// shape of a non-root machine, where /usr/share/keyrings already exists
	// and cannot be written to. A missing directory would fail earlier and for
	// a different reason.
	keyrings := t.TempDir()
	if err := os.Chmod(keyrings, 0o555); err != nil {
		t.Skipf("cannot make a read-only directory here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(keyrings, 0o755) })

	rec := &recorder{}
	unpriv, priv := rec.installers()

	p := &PackageInstaller{
		RunUnprivileged: unpriv,
		RunPrivileged:   priv,
		IsRoot:          func() bool { return false },
		NonInteractive:  true,
		KeyringsDir:     keyrings,
		Out:             io.Discard,
	}

	a := pkgAction(manifest.ManagerApt, "vault")
	a.Target.AptSource = &manifest.AptSource{
		URL:           "https://apt.releases.hashicorp.com noble main",
		KeyringURL:    "https://apt.releases.hashicorp.com/gpg",
		KeyringSHA256: strings.Repeat("ab", 32),
	}

	err := p.Install(context.Background(), a)

	var pe *exec.PrivilegeError
	if !errors.As(err, &pe) {
		t.Fatalf("Install error = %v (%T), want a *exec.PrivilegeError; a plain error "+
			"here lets the run continue to a misleading 'installed but stale'", err, err)
	}
	if !pe.NonInteractive {
		t.Error("the PrivilegeError does not carry NonInteractive, so the CLI would " +
			"prompt under --non-interactive — the hang that flag exists to prevent")
	}
	if len(rec.privileged) != 0 {
		t.Errorf("the install went ahead anyway: %v", rec.privileged)
	}
	// The command must be one the user can run. The first version of this fix
	// reported "sudo create a temp keyring in /usr/share/keyrings" — a sentence
	// in the position where a command belongs, which is a worse message than
	// none: the user who runs it gets a usage error and learns nothing.
	if !strings.Contains(pe.Command, "apt-get install") {
		t.Errorf("PrivilegeError.Command = %q, want it to name the install command "+
			"the user can actually run", pe.Command)
	}
}

// The complement: a tool that declares no apt source must not be sent down the
// keyring path at all.
//
// Note what this does NOT assert: that an apt install needs no privilege. On a
// non-root machine it very much does — `sudo -n apt-get install -y jq` is
// correct and expected. The claim is only that the apt-SOURCE machinery, which
// writes to a root-owned directory, stays out of the way of a tool that never
// asked for a repository.
func TestNoAptSourceMeansNoKeyringWork(t *testing.T) {
	t.Parallel()

	keyrings := filepath.Join(t.TempDir(), "never-created")
	rec := &recorder{}
	unpriv, priv := rec.installers()
	p := &PackageInstaller{
		RunUnprivileged: unpriv,
		RunPrivileged:   priv,
		IsRoot:          func() bool { return false },
		NonInteractive:  true,
		KeyringsDir:     keyrings,
		Out:             io.Discard,
	}

	if err := p.Install(context.Background(), pkgAction(manifest.ManagerApt, "jq")); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(keyrings); !os.IsNotExist(err) {
		t.Errorf("the keyring directory was created for a tool that declares no apt_source (%s)", keyrings)
	}
}

// A manifest whose keyring digest is malformed must be refused outright rather
// than fetched: the whole safety property of `keyring_url` is that the bytes
// are pinned by a value a reviewer can check, and a digest that is not a
// digest pins nothing.
func TestMalformedKeyringDigestIsRefusedBeforeAnyFetch(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	unpriv, priv := rec.installers()
	p := &PackageInstaller{
		RunUnprivileged: unpriv,
		RunPrivileged:   priv,
		IsRoot:          func() bool { return true },
		Out:             io.Discard,
	}

	a := pkgAction(manifest.ManagerApt, "vault")
	a.Target.AptSource = &manifest.AptSource{
		URL:           "https://example.invalid noble main",
		KeyringURL:    "https://example.invalid/gpg",
		KeyringSHA256: "not-a-digest",
	}

	err := p.Install(context.Background(), a)
	if err == nil {
		t.Fatal("a malformed keyring digest was accepted")
	}
	if !strings.Contains(err.Error(), "64 hex characters") {
		t.Errorf("error = %v, want it to name the malformed digest", err)
	}
	if len(rec.unprivileged) != 0 || len(rec.privileged) != 0 {
		t.Errorf("a command ran despite the refusal: %v %v", rec.unprivileged, rec.privileged)
	}
}
