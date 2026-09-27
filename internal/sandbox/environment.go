package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	osexec "os/exec"
)

// Environment is where a sandbox sequence runs.
//
// It exists because Layer 1 and Layer 2 cannot both happen on one machine. A
// Layer 1 fixture needs the tool PRESENT; a Layer 2 install needs it ABSENT, so
// the starting condition the whole sequence is defined against is
// unsatisfiable on a developer laptop. A fresh container is the only place both
// can be true: nothing is installed, and a real install can be observed from
// nothing.
//
// The sequence is identical in both environments. That is the point of the
// abstraction — a second implementation of the steps would prove the container
// harness works, not that aes does. Only the capabilities the steps actually
// call are abstracted, and each is here because a step calls it:
//
//	Run        — invoke the binary under test (every step)
//	Sh         — a short shell probe (rm, command -v)
//	ReadFile   — re-read state.json from the file, not from the writer
//	TreeDigest — prove a re-run changed nothing
//	FileDigest — prove the binary itself was not rewritten
//	HasBinary  — the absent precondition, measured where the install happens
//	MkdirTemp  — a fresh AES_HOME per run
//	Env        — the run's environment, so PATH is the environment's own
type Environment interface {
	// Run executes argv and returns stdout, stderr and the exit code, kept
	// apart. Combining them is a documented trap in this repo: one warning
	// line on stderr turns a JSON document into something that will not parse,
	// and the parse failure reads as "the tool is missing" rather than as "we
	// merged the streams".
	Run(ctx context.Context, argv []string, env []string, dir string) (ExecResult, error)

	// Sh runs a script through `sh -c`. It is for short filesystem and PATH
	// probes, never for the binary under test.
	Sh(ctx context.Context, script string, env []string) (string, int, error)

	// ReadFile returns a file's bytes.
	ReadFile(ctx context.Context, path string) ([]byte, error)

	// TreeDigest digests every file under root, path-sorted, so two trees can
	// be compared. An absent root digests as "absent" rather than erroring,
	// because absent-versus-empty is exactly the distinction this package cares
	// about.
	TreeDigest(ctx context.Context, root string) (string, error)

	// FileDigest identifies one file for a before/after comparison, by size
	// and mtime rather than content: an idempotent re-run may legitimately
	// rewrite a file byte for byte, and that is not drift.
	FileDigest(ctx context.Context, path string) (string, error)

	// HasBinary reports whether name resolves on this environment's PATH.
	HasBinary(ctx context.Context, name string) (bool, error)

	// MkdirTemp creates a fresh, private directory and returns its path.
	MkdirTemp(ctx context.Context) (string, error)

	// RemoveAll deletes a directory in this environment. It exists so cleanup
	// is not done with os.RemoveAll against a path that only means something
	// here: a container home is /tmp/aes-sandbox-XXXX inside the container, and
	// the same string on the host is either nothing or — far worse — some
	// unrelated directory that happens to share the name.
	RemoveAll(ctx context.Context, path string) error

	// Env builds the environment for a run rooted at home.
	//
	// It belongs on the interface rather than being derived from os.Environ,
	// because PATH is a correctness property here and not a convenience: the
	// sandbox prepends its own bin so the tool under test resolves to the
	// sandbox copy, and the base that follows is the environment's own. A
	// container inheriting the host's PATH would put macOS directories inside
	// a Linux process, and a host inheriting nothing would break every
	// installer that shells out to tar or apt-get.
	Env(home string) []string
}

// ExecResult is one command's outcome.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Combined joins the streams for diagnostics only. Nothing that parses output
// may use it.
func (r ExecResult) Combined() string { return r.Stdout + r.Stderr }

// Host is the local machine.
//
// It is the default, and it is the original behaviour: everything in this
// package worked before the container existed, and a difference that appears
// only in the container is a difference in the harness rather than a discovery
// about aes.
type Host struct{}

// Both environments satisfy the same contract. Without this, adding a method to
// the interface leaves one implementation quietly short of it and the sequence
// fails somewhere far from the cause.
var (
	_ Environment = Host{}
	_ Environment = (*Container)(nil)
)

// Run executes a command on the local machine.
func (Host) Run(ctx context.Context, argv []string, env []string, dir string) (ExecResult, error) {
	stepCtx, cancel := context.WithTimeout(ctx, StepTimeout)
	defer cancel()

	cmd := osexec.CommandContext(stepCtx, argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	res := ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if stepCtx.Err() == context.DeadlineExceeded {
		return res, fmt.Errorf("%s %v timed out after %s", argv[0], argv[1:], StepTimeout)
	}
	return res, err
}

// Sh runs a script on the local machine.
func (h Host) Sh(ctx context.Context, script string, env []string) (string, int, error) {
	res, err := h.Run(ctx, []string{"sh", "-c", script}, env, "")
	return res.Stdout, res.ExitCode, err
}

// ReadFile reads from the local filesystem.
func (Host) ReadFile(_ context.Context, path string) ([]byte, error) {
	return os.ReadFile(path)
}

// TreeDigest digests a local tree.
//
// It reuses the same digest the host-isolation assertion has always used, so a
// value written before this abstraction existed still compares equal to one
// written after it.
func (Host) TreeDigest(_ context.Context, root string) (string, error) {
	return treeFingerprint(root)
}

// FileDigest describes one local file, and reuses the existing helper so the
// meaning of "changed" has not shifted under the tests that depend on it.
func (Host) FileDigest(_ context.Context, path string) (string, error) {
	return fingerprint(path), nil
}

// HasBinary reports whether a name resolves on the local PATH.
func (Host) HasBinary(_ context.Context, name string) (bool, error) {
	if name == "" {
		return false, nil
	}
	_, err := osexec.LookPath(name)
	return err == nil, nil
}

// MkdirTemp creates a private local directory.
func (Host) MkdirTemp(_ context.Context) (string, error) {
	dir, err := os.MkdirTemp("", "aes-sandbox-")
	if err != nil {
		return "", err
	}
	// 0700 keeps a stray world-readable artefact out of a shared /tmp.
	if err := os.Chmod(dir, 0o700); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// RemoveAll deletes a local directory.
func (Host) RemoveAll(_ context.Context, path string) error { return os.RemoveAll(path) }

// Env builds the environment for a run on the local machine.
//
// It is the original sandboxEnv, unchanged, and that is deliberate: the host
// path is what every existing Layer 2 assertion was written against, so the
// container must not quietly become the definition of correct.
func (Host) Env(home string) []string { return sandboxEnv(home) }
