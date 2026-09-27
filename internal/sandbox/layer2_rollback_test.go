package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// eu4's last criterion, in the place the bead asked for it: the Layer 2
// harness, in a container, driving the real binary.
//
// The unit test in internal/cli covers the decision. This covers the claim
// underneath it — that on a machine with nothing installed, a real `aes setup`
// takes a working binary away from a user and then puts it back. That is a
// different question from "does the function call Restore", and only running
// the product answers it.
//
// # Why a fake toolchain
//
// The real `go install` cannot be made to produce a broken binary on demand,
// and an ecosystem tool that lands a broken build by accident is not something
// to go looking for. So `go` is replaced by a script that writes whatever
// FIXTURE_BODY says, and everything above it — catalog resolution, the
// dependency graph, retain-before-install, the post-install probe, the
// restore, the state write — is the real code path running the real binary.
//
// The substitution is the smallest possible one, and it is above the line that
// matters: `exec.Run` still spawns it, the verifier still probes it, and
// nothing in AES knows the difference.

func TestLayer2ContainerRestoresABinaryThatDoesNotRun(t *testing.T) {
	layer2Enabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	if err := ContainerAvailable(ctx); err != nil {
		t.Skipf("no usable container runtime: %v", err)
	}

	arch, err := ImageGoArch(ctx, DefaultImage)
	if err != nil {
		t.Fatalf("read image architecture: %v", err)
	}
	// buildAesFor returns a HOST path — that is what gets mounted. The binary is
	// EXECUTED at c.BinaryPath(), which is the mount point inside the container.
	// Passing the host path as argv[0] fails with a stat error that reads like a
	// missing binary rather than a path in the wrong namespace.
	bin := buildAesFor(t, "linux", arch)

	c, err := StartContainer(ctx, ContainerConfig{Binary: bin, Keep: os.Getenv("AES_KEEP_SANDBOX") == "1"})
	if err != nil {
		t.Fatalf("start container: %v", err)
	}
	t.Cleanup(func() {
		if !c.keep {
			_ = c.Close(context.Background())
		}
	})
	if err := c.Prepare(ctx, "ca-certificates"); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	// A fake `go` that writes whatever the environment asks for, into GOBIN —
	// which is the directory the go strategy's Destination reports, so the
	// harness and aes agree on where the binary lives without either of them
	// being told.
	const fakeGo = `#!/bin/sh
if [ -n "$FIXTURE_BROKEN" ]; then
  printf '#!/bin/sh\nexit 1\n' > "$GOBIN/fixture"
else
  printf '#!/bin/sh\necho fixture %s\n' "${FIXTURE_VERSION:-1.0.0}" > "$GOBIN/fixture"
fi
chmod 0755 "$GOBIN/fixture"
`
	if err := c.Sh2(ctx, "mkdir -p /usr/local/bin /opt/fixture-home/go/bin && cat > /usr/local/bin/go <<'EOS'\n"+fakeGo+"EOS\nchmod 0755 /usr/local/bin/go"); err != nil {
		t.Fatalf("install the fake toolchain: %v", err)
	}

	// The fixture checkout has to exist INSIDE the container: `docker exec`
	// takes a working directory in the container's namespace, and a host temp
	// path there is simply "no such file or directory".
	repo := "/opt/fixture-repo"
	writeFixtureToolIn(t, c, repo, "fixture")
	home := "/opt/fixture-home"

	env := c.Env(home)
	env = append(env, "GOBIN="+home+"/go/bin", "PATH="+home+"/go/bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin")

	// 1. A real install, from nothing, on a machine with nothing.
	res, err := c.Run(ctx, []string{c.BinaryPath(), "setup", "--only", "fixture", "--non-interactive"},
		env, repo)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("first install: exit=%d err=%v\n%s", res.ExitCode, err, res.Combined())
	}
	toolBin := filepath.Join(home, "go", "bin", "fixture")
	if _, err := c.FileDigest(ctx, toolBin); err != nil {
		t.Fatalf("the first install produced no binary: %v", err)
	}

	// 2. Age it, so the next run is an install candidate that will overwrite
	// a file already there. Deleting it instead would mean there is nothing to
	// retain, and the test would prove nothing.
	const aged = "#!/bin/sh\necho fixture 0.5.0\n"
	if err := c.writeFile(ctx, toolBin, aged); err != nil {
		t.Fatalf("age the binary: %v", err)
	}

	// 3. The reinstall produces something that runs and fails.
	brokenEnv := append(append([]string{}, env...), "FIXTURE_BROKEN=1")
	// The exit code here is EXPECTED to be non-zero: verification failed, and
	// that is the correct outcome. Treating a non-zero exit as "the harness
	// could not run it" is the trap this project has been bitten by four times
	// — the question the code answers is "did aes setup report a working
	// environment", and the answer here is deliberately no.
	res, err = c.Run(ctx, []string{c.BinaryPath(), "setup", "--only", "fixture", "--non-interactive"},
		brokenEnv, repo)
	if res.ExitCode == 0 {
		t.Fatalf("a run that installed a non-working binary reported success:\n%s", res.Combined())
	}
	if err != nil && res.Combined() == "" {
		t.Fatalf("the run produced no output at all: %v", err)
	}
	out := res.Combined()

	// 4. The user must not be left with a broken binary. This is the whole
	// assertion: not the exit code, which is non-zero either way, and not the
	// state file, but the file on disk.
	got, err := c.ReadFile(ctx, toolBin)
	if err != nil {
		t.Fatalf("eu4: the binary is gone entirely — a working install was destroyed. %v", err)
	}
	if string(got) == "#!/bin/sh\nexit 1\n" {
		t.Errorf("eu4: the broken binary is still in place; nothing was restored.\n%s", out)
	}
	if string(got) != aged {
		t.Errorf("eu4: the restored binary is %q, want the predecessor %q", got, aged)
	}
	if !strings.Contains(out, "put back") {
		t.Errorf("the run did not tell the user it put something back:\n%s", out)
	}

	// 5. And the state must not have been corrupted by any of it. The entry
	// from the first install still describes a binary that exists, which is
	// true again only because the restore happened.
	state, err := c.ReadFile(ctx, filepath.Join(home, ".aes", "state.json"))
	if err != nil {
		t.Fatalf("state.json: %v", err)
	}
	if !strings.Contains(string(state), "fixture") {
		t.Errorf("state.json no longer records the tool after a rollback:\n%s", state)
	}
	// A failed verification must not have written a SECOND entry claiming the
	// broken one was installed.
	if n := strings.Count(string(state), `"fixture"`); n != 1 {
		t.Errorf("state.json mentions the tool %d times, want 1:\n%s", n, state)
	}
}

// writeFixtureToolIn stands up a checkout inside the container for aes to load
// its catalog from.
//
// The binary finds its catalog by walking up for a go.mod, so a fixture needs a
// real module root — pointing it at a directory with no go.mod falls back to
// the EMBEDDED catalog and quietly tests the wrong manifest, which is the
// failure mode a fixture is most able to hide from itself.
func writeFixtureToolIn(t *testing.T, c *Container, repo, name string) {
	t.Helper()
	ctx := context.Background()
	if err := c.Sh2(ctx, "mkdir -p "+repo+"/tools/utility/"+name); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := c.writeFile(ctx, repo+"/go.mod", "module aesfixture\n\ngo 1.24\n"); err != nil {
		t.Fatalf("go.mod: %v", err)
	}
	dir := repo + "/tools/utility/" + name
	body := `name: ` + name + `
description: fixture whose installer can be told to produce a broken binary
category: utility
provides: [` + name + `]
verify:
  command: ` + name + `
  version:
    command: ` + name + ` --version
    min: "1.0.0"
install:
  darwin:
    strategy: go
    go_package: example.com/` + name + `@v1.0.0
  linux:
    strategy: go
    go_package: example.com/` + name + `@v1.0.0
`
	if err := c.writeFile(ctx, dir+"/tool.yaml", body); err != nil {
		t.Fatalf("tool.yaml: %v", err)
	}
}

var _ = manifest.StrategyGo
