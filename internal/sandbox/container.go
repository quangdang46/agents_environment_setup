package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ContainerRunner executes a command inside the sandbox container and returns
// its combined output.
//
// It is a function rather than a docker dependency so this package stays
// testable without a daemon. The container harness supplies the real
// implementation; the tests supply a fake, which is the point — the property
// asserted here is about WHERE a check ran, not about docker.
type ContainerRunner func(ctx context.Context, argv ...string) (string, error)

// Presence is the result of asking one environment whether a binary resolves.
type Presence struct {
	// Found is whether the binary resolved.
	Found bool
	// Evidence is the output that decided it. It is kept because "the tool was
	// absent" is a claim the next person should be able to check, and a bare
	// boolean invites the reader to assume someone measured.
	Evidence string
	// Where names the environment the check ran in, so a result can never be
	// silently attributed to the wrong machine.
	Where string
}

// AbsentInside asks the CONTAINER whether a binary resolves, and reports the
// answer together with the evidence.
//
// This exists because "the tool is absent" is a claim about a specific machine,
// and a harness that only knows about the host cannot make it honestly. The
// failure mode is concrete: a developer machine that already has the tool
// satisfies every host-side absence check, the container inherits that state,
// and the sequence passes without a byte crossing the container boundary. The
// run looks perfect and proves nothing — the same shape as a green main with
// no CI.
//
// Measuring inside is the fix. `command -v` in the container can only find
// what the container has, and the output comes back so the claim is checkable
// after the fact rather than taken on trust.
func AbsentInside(ctx context.Context, run ContainerRunner, bin string) (Presence, error) {
	if run == nil {
		return Presence{Where: "container"}, fmt.Errorf("sandbox: no container runner")
	}
	if strings.TrimSpace(bin) == "" {
		return Presence{Where: "container"}, fmt.Errorf("sandbox: empty binary name")
	}

	out, err := run(ctx, "sh", "-c", "command -v "+shellQuote(bin))
	if err != nil {
		// A non-zero exit may mean "not found", which is the answer we came
		// for. Anything else — the container refusing to start, sh missing —
		// is a real error and must not read as an absent tool.
		if isNotFound(out) {
			return Presence{Found: false, Evidence: strings.TrimSpace(out), Where: "container"}, nil
		}
		return Presence{Where: "container"},
			fmt.Errorf("sandbox: probing %q in container: %w (%s)", bin, err, firstLine(out))
	}
	return Presence{Found: true, Evidence: strings.TrimSpace(out), Where: "container"}, nil
}

// isNotFound distinguishes `command -v` reporting absence from the command
// failing for any other reason.
//
// Without this, a container that cannot start produces output that reads as
// "not found", the error is swallowed, and the harness concludes the tool is
// absent — then installs into a container that was never there. A false
// absence is worse than a loud failure: it is the precondition for every
// subsequent step being meaningless.
func isNotFound(out string) bool {
	// `command -v` prints NOTHING and exits 1 on a plain miss — silence is the
	// miss. Any output at all on the error path means the shell said something
	// other than a bare path, which is a different condition: a dead daemon
	// prints "Cannot connect", a missing shell prints "not found". Laundering
	// either into "the tool is absent" would let the harness install into a
	// container that was never running.
	return strings.TrimSpace(out) == ""
}

// shellQuote wraps a token in single quotes for POSIX sh, escaping any embedded
// single quote. Binary names come from a manifest, and a manifest is data — but
// data still should not be concatenated into a shell unquoted.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ---------------------------------------------------------------------------
// The container itself.
//
// The AbsentInside primitive above is deliberately ignorant of docker: it takes
// a runner and answers a question about wherever that runner points. This half
// supplies a real one. The split is what makes the primitive testable without a
// daemon, and it is also why the runner below is built to fail the way
// AbsentInside expects — a miss is a non-zero exit with empty output, and a
// dead container is a non-zero exit WITH output, and the two must not
// collapse into each other.
// ---------------------------------------------------------------------------

// DefaultImage is the container the Layer 2 proof runs in.
//
// Ubuntu 24.04 specifically, not "the latest Linux": it is the one platform
// the spec promises, and the only place the apt privilege path is reachable at
// all. brew cannot run in a Linux container, so on a macOS host package+apt is
// unreachable outside one — which is the entire reason this file exists.
const DefaultImage = "ubuntu:24.04"

// Container is a fresh machine that nothing has been installed on.
//
// # Why a container and not a temp directory
//
// The host Layer 2 run is real but not sufficient. It starts from "the tool is
// not on the developer's PATH", which is a weaker claim than "the tool is not
// installed", and it cannot reach apt at all: installing one mutates the
// developer's machine, and several apt tools are already installed here, so
// requireAbsent correctly refuses them. That refusal is the harness being
// honest, and it means a package-strategy tool is provable exactly once.
//
// A fresh container makes the starting condition structural rather than
// incidental. `command -v jq` inside a new ubuntu:24.04 is empty because
// nothing has ever installed jq there — not because PATH was arranged to hide
// it.
type Container struct {
	// Runtime is the container CLI: docker or podman.
	Runtime string
	// ID is the running container's name, which docker accepts in place of an
	// id everywhere exec does.
	ID string
	// GoArch is the container's architecture in Go naming, read from the image
	// rather than assumed from the host.
	GoArch string
	// binary is the path to the aes under test inside the container.
	binary string
	// keep leaves the container running after Close, for debugging.
	keep bool
	// owned records that this process created the container, and so may remove
	// it. A container passed in by a caller is theirs to stop.
	owned bool
}

// ContainerConfig describes the container to start.
type ContainerConfig struct {
	// Image defaults to DefaultImage.
	Image string
	// Runtime defaults to docker, falling back to podman.
	Runtime string
	// Binary is the host path to the aes executable under test. It is
	// bind-mounted read-only, so the build is the one from this checkout.
	Binary string
	// Keep leaves the container running after Close.
	Keep bool
}

// StartContainer launches a fresh container with the binary mounted.
//
// It pulls the image if needed. That is a network operation on first use and a
// no-op afterwards, and it happens here rather than in the test so a harness
// failure is never mistaken for a product failure.
func StartContainer(ctx context.Context, cfg ContainerConfig) (*Container, error) {
	if cfg.Binary == "" {
		return nil, fmt.Errorf("sandbox: Binary is required to start a container")
	}
	abs, err := filepath.Abs(cfg.Binary)
	if err != nil {
		return nil, fmt.Errorf("sandbox: resolve binary: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("sandbox: binary %s: %w", abs, err)
	}

	image := cfg.Image
	if image == "" {
		image = DefaultImage
	}

	runtime, err := containerRuntime(ctx, cfg.Runtime)
	if err != nil {
		return nil, err
	}

	// The image's Architecture field is already Go naming, which is what makes
	// this a lookup rather than a translation. Note the neighbouring command:
	// `docker info` reports aarch64, the uname spelling, for the same machine.
	// Two of the three vocabularies AGENTS.md warns about meet in these two
	// commands, and picking the wrong one produces a binary that cannot run.
	arch, err := imageArch(ctx, runtime, image)
	if err != nil {
		return nil, err
	}

	c := &Container{Runtime: runtime, GoArch: arch, binary: "/usr/local/bin/aes", keep: cfg.Keep}
	c.ID = fmt.Sprintf("aes-layer2-%d-%s", os.Getpid(), randomSuffix())

	// sleep infinity keeps the container alive. ubuntu:24.04 has no init, so
	// there is no PID 1 to exit and take the container down with it.
	run := exec.CommandContext(ctx, runtime, "run", "-d",
		"--name", c.ID,
		"-v", abs+":"+c.binary+":ro",
		"--entrypoint", "/bin/sleep",
		image, "infinity")
	out, err := run.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("sandbox: start %s from %s: %w\n%s", c.ID, image, err, out)
	}
	c.owned = true
	return c, nil
}

// BinaryPath is where the binary under test lives inside the container. A
// Config built for a container run must name this, not the host path, because
// the host path means nothing to a process inside.
func (c *Container) BinaryPath() string { return c.binary }

// RemoveAll deletes a directory inside the container.
//
// It never touches the host filesystem. A container home is
// /tmp/aes-sandbox-XXXX in the container's namespace, and deleting that string
// on the host would be a guess about a path the host knows nothing about.
func (c *Container) RemoveAll(ctx context.Context, path string) error {
	_, _, err := c.Sh(ctx, "rm -rf "+shellQuote(path), nil)
	return err
}

// ImageGoArch reports the Go architecture of an image without starting it.
//
// A caller needs this BEFORE it can build: the binary under test must be
// cross-compiled for the container, and the container is not up yet. Assuming
// runtime.GOARCH instead would be right on both of this project's runners and
// wrong the first time someone runs these tests against a remote or emulated
// daemon, which is exactly the case where a wrong-architecture binary fails
// as a corrupt download.
func ImageGoArch(ctx context.Context, image string) (string, error) {
	runtime, err := containerRuntime(ctx, "")
	if err != nil {
		return "", err
	}
	if image == "" {
		image = DefaultImage
	}
	return imageArch(ctx, runtime, image)
}

// Prepare installs packages the container needs before a run can work.
//
// Two of them are not optional, and their absence is a failure mode that looks
// like a broken installer rather than a broken fixture:
//
//   - ca-certificates: a bare ubuntu:24.04 has no CA bundle, so EVERY https
//     request fails with "certificate signed by unknown authority". That takes
//     out github-release downloads, the npm registry and the go proxy alike,
//     and the error names the network rather than the missing package.
//   - git: the go toolchain shells out to git for VCS resolution.
//
// A real user's machine has both, so a fixture that omits them is testing a
// machine nobody has.
func (c *Container) Prepare(ctx context.Context, packages ...string) error {
	if len(packages) == 0 {
		packages = []string{"ca-certificates"}
	}
	if _, _, err := c.Sh(ctx, "apt-get update -qq", nil); err != nil {
		return fmt.Errorf("sandbox: apt-get update: %w", err)
	}
	script := "DEBIAN_FRONTEND=noninteractive apt-get install -y -qq " + strings.Join(packages, " ")
	if _, _, err := c.Sh(ctx, script, nil); err != nil {
		return fmt.Errorf("sandbox: install %v: %w", packages, err)
	}
	return nil
}

// Close stops and removes the container.
func (c *Container) Close(ctx context.Context) error {
	if !c.owned || c.keep {
		return nil
	}
	// A short timeout: `docker rm -f` on a wedged container can hang, and a
	// cleanup that hangs turns a passing test into a stuck one.
	rmCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(rmCtx, c.Runtime, "rm", "-f", c.ID).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sandbox: remove %s: %w\n%s", c.ID, err, out)
	}
	return nil
}

// ContainerAvailable reports whether a container runtime is usable.
//
// It is the gate for the whole file. A test that requires Docker should skip
// when there is none rather than fail: a developer on a laptop without Docker
// has not broken anything, and a test that cannot be skipped is a test that
// makes the suite unrunnable for them.
func ContainerAvailable(ctx context.Context) error {
	runtime, err := containerRuntime(ctx, "")
	if err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, runtime, "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s is installed but not usable: %w\n%s", runtime, err, out)
	}
	return nil
}

// runner adapts the container to the ContainerRunner AbsentInside takes.
//
// The error contract matters more than it looks. A plain miss must arrive as a
// non-nil error with EMPTY output, because that is what isNotFound tests for;
// anything else arriving as an error with output is read as a broken container
// and is correctly escalated rather than laundered into an absence.
func (c *Container) runner() ContainerRunner { return c.runnerWithEnv(nil) }

// runnerWithEnv is the same probe with an environment applied.
//
// It exists because "is the tool installed" has no single honest answer until
// you say where you are looking. A github-release tool lands in $AES_HOME/bin,
// which is on the PATH the run used and NOT on the container's default PATH —
// so probing with the default PATH reports a successful install as absent. The
// first version of the container test did exactly that and produced a failure
// message that was confidently wrong about a working install.
//
// The correct question after a run is therefore asked with the run's own
// environment, and the one before it is asked with the container's — before
// there is no sandbox to look in, and the strongest claim available is the
// container default.
func (c *Container) runnerWithEnv(env []string) ContainerRunner {
	return func(ctx context.Context, argv ...string) (string, error) {
		res, err := c.Run(ctx, argv, env, "")
		if err == nil && res.ExitCode != 0 {
			err = fmt.Errorf("exit status %d", res.ExitCode)
		}
		return res.Combined(), err
	}
}

// containerRuntime picks the CLI, preferring an explicit choice.
func containerRuntime(ctx context.Context, want string) (string, error) {
	candidates := []string{want, "docker", "podman"}
	for _, name := range candidates {
		if name == "" {
			continue
		}
		if _, err := exec.LookPath(name); err == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("sandbox: no container runtime found (looked for %v); "+
		"this test needs docker or podman", candidates)
}

// imageArch returns the image's architecture in Go naming.
func imageArch(ctx context.Context, runtime, image string) (string, error) {
	out, err := exec.CommandContext(ctx, runtime, "image", "inspect",
		image, "--format", "{{.Architecture}}").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("sandbox: inspect %s architecture: %w\n%s", image, err, out)
	}
	arch := strings.TrimSpace(string(out))
	switch arch {
	case "arm64", "amd64":
		return arch, nil
	default:
		// Guessing here downloads the wrong binary, which then fails in a way
		// that reads as a corrupt download.
		return "", fmt.Errorf("sandbox: image %s reports architecture %q, which is not a Go "+
			"architecture name; expected arm64 or amd64", image, arch)
	}
}

// Run executes a command inside the container.
func (c *Container) Run(ctx context.Context, argv []string, env []string, dir string) (ExecResult, error) {
	if len(argv) == 0 {
		return ExecResult{}, fmt.Errorf("sandbox: empty command")
	}
	args := []string{"exec"}
	for _, kv := range env {
		if !strings.Contains(kv, "=") {
			continue
		}
		// A value containing '=' is fine: docker splits on the first one.
		args = append(args, "-e", kv)
	}
	if dir != "" {
		args = append(args, "-w", dir)
	}
	args = append(args, c.ID)
	args = append(args, argv...)

	stepCtx, cancel := context.WithTimeout(ctx, StepTimeout)
	defer cancel()

	var stdout, stderr strings.Builder
	cmd := exec.CommandContext(stepCtx, c.Runtime, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()

	res := ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if stepCtx.Err() == context.DeadlineExceeded {
		return res, fmt.Errorf("in-container %s timed out after %s", strings.Join(argv, " "), StepTimeout)
	}
	return res, err
}

// Sh runs a script inside the container.
func (c *Container) Sh(ctx context.Context, script string, env []string) (string, int, error) {
	res, err := c.Run(ctx, []string{"sh", "-c", script}, env, "")
	return res.Stdout, res.ExitCode, err
}

// ReadFile returns a file's bytes from inside the container.
//
// It goes through cat rather than a bind mount because the file is written by
// the process under test; a host-side copy of the same path would be a second
// source of truth about state the container owns.
func (c *Container) ReadFile(ctx context.Context, path string) ([]byte, error) {
	probeCtx, cancel := context.WithTimeout(ctx, StepTimeout)
	defer cancel()
	return exec.CommandContext(probeCtx, c.Runtime, "exec", c.ID, "cat", path).Output()
}

// TreeDigest digests a tree inside the container.
//
// The shell version of treeFingerprint: every file under root, path-sorted,
// each content-hashed, and the whole listing hashed again. Sorting matters —
// without it two identical trees could hash differently because the container
// returned directory entries in a different order.
func (c *Container) TreeDigest(ctx context.Context, root string) (string, error) {
	script := fmt.Sprintf(`if [ ! -d %s ]; then echo absent; exit 0; fi
cd %s || exit 1
find . -type f | LC_ALL=C sort | while IFS= read -r f; do
  printf '%%s %%s\n' "$f" "$(sha256sum "$f" | cut -d" " -f1)"
done | sha256sum | cut -d" " -f1`, shellQuote(root), shellQuote(root))
	out, _, err := c.Sh(ctx, script, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// FileDigest describes one file inside the container.
func (c *Container) FileDigest(ctx context.Context, path string) (string, error) {
	script := fmt.Sprintf(`if [ -f %s ]; then stat -c '%%s:%%.9Y' %s; else echo absent; fi`,
		shellQuote(path), shellQuote(path))
	out, _, err := c.Sh(ctx, script, nil)
	return strings.TrimSpace(out), err
}

// HasBinary reports whether a name resolves on the container's PATH.
//
// It delegates to AbsentInside rather than repeating the probe, so the rule
// that decides "a miss is silent, a broken container talks" has exactly one
// implementation and is covered by the tests that were written for it.
func (c *Container) HasBinary(ctx context.Context, name string) (bool, error) {
	p, err := AbsentInside(ctx, c.runner(), name)
	return p.Found, err
}

// MkdirTemp creates a private directory inside the container.
func (c *Container) MkdirTemp(ctx context.Context) (string, error) {
	out, _, err := c.Sh(ctx, "mktemp -d /tmp/aes-sandbox-XXXXXX", nil)
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(out)
	if dir == "" {
		return "", fmt.Errorf("sandbox: mktemp -d produced no path")
	}
	return dir, nil
}

// Env builds the environment for a run inside the container.
//
// The base PATH is written out in full rather than inherited, because the
// container has no relationship to the host's PATH and inheriting it would put
// macOS paths inside a Linux process.
func (c *Container) Env(home string) []string {
	return []string{
		"PATH=" + aesHomeOf(home) + "/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		// See sandboxEnv: HOME is the sandbox root, AES_HOME its child, so a
		// tool's own caches stay out of the directory this package digests.
		"HOME=" + home,
		"AES_HOME=" + aesHomeOf(home),
		"SHELL=/bin/sh",
		"DEBIAN_FRONTEND=noninteractive",
		"LC_ALL=C",
	}
}

// randomSuffix keeps two containers from colliding when a run is repeated
// within a process lifetime, or when two test binaries run concurrently.
func randomSuffix() string {
	return strconv.FormatInt(time.Now().UnixNano()%1e8, 36)
}
