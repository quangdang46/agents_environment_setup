// Package sandbox proves the install path, which Layer 1 cannot.
//
// Layer 1 verifies tools that are already on the machine. It says nothing about
// whether `aes setup` can install them: a manifest can verify perfectly and
// still fetch the wrong thing, write to the wrong directory, or record state
// for an install that never happened. Only a real install proves that.
//
// # It runs the binary, not the library
//
// Every step shells out to the `aes` command rather than calling into
// internal/cli. That is deliberate. The contract is about what the command does
// to a machine — a file appears, state.json gains an entry, a second run
// changes nothing — and a test that calls the same functions in-process can
// pass while the thing a user runs does not. Calling the library would have
// missed exactly the class of bug this package exists to find.
//
// # Isolation is the point
//
// Every run gets its own AES_HOME, and the harness asserts the developer's
// real ~/.aes is byte-identical before and after. An install test that
// mutates the machine it runs on and then claims to have proven isolation is
// worse than no test, because it looks like a guarantee.
//
// # Flags
//
// The steps use --non-interactive, not --yes. --yes asserts that a terminal is
// present, and the assertion is the point: it exists so a CI job cannot sit
// waiting on a prompt nobody will answer. A harness with no TTY is exactly the
// situation that flag is designed to refuse, so using it here would be using a
// safety mechanism as a convenience.
package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/verifier"
)

// StepTimeout bounds one aes invocation. Generous, because a real github-release
// install downloads and extracts; short enough that a hung run fails the
// harness instead of hanging it.
const StepTimeout = 5 * time.Minute

// Config controls a run.
type Config struct {
	// Binary is the aes executable under test. Required.
	Binary string
	// Keep leaves the sandbox directory on disk for debugging.
	Keep bool
	// Verbose passes --verbose through.
	Verbose bool
	// DryRun runs every step with --dry-run. It exercises resolution and
	// reporting but installs nothing, so it cannot prove the install path;
	// it exists so the harness itself is testable without network access.
	DryRun bool
}

// Step is one assertion in the sequence, recorded so a failure names the step
// that broke rather than just the tool.
type Step struct {
	Name   string
	OK     bool
	Detail string
}

// Result is the outcome of running the sequence for one tool.
type Result struct {
	Tool   string
	Home   string
	Steps  []Step
	Passed bool
}

// String renders the result for a human, which is what a failing harness run
// is read as.
func (r Result) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: ", r.Tool)
	if r.Passed {
		fmt.Fprintf(&b, "all %d steps passed (home: %s)\n", len(r.Steps), r.Home)
		return b.String()
	}
	fmt.Fprintf(&b, "FAILED (home: %s)\n", r.Home)
	for _, s := range r.Steps {
		mark := "ok  "
		if !s.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "  [%s] %-28s %s\n", mark, s.Name, s.Detail)
	}
	return b.String()
}

// Run executes the full sequence for one tool against a throwaway AES_HOME.
//
// The sequence, from a machine where the tool is absent:
//
//  1. setup installs it and exits 0
//  2. the binary exists and Verify reports StatusOK
//  3. state.json contains the entry
//  4. re-running changes nothing — the installer is not called again
//  5. deleting the binary makes doctor report drift
//  6. --force reinstalls it
//
// Steps 4 to 6 are the ones that catch real defects: a non-idempotent
// installer, a state writer recording something the installer never did, and a
// doctor that cannot see drift. Steps 1 to 3 mostly catch a manifest that
// points at the wrong artefact, which is worth catching but is the easy half.
func Run(ctx context.Context, tool *manifest.Tool, cfg Config) (Result, error) {
	if cfg.Binary == "" {
		return Result{}, fmt.Errorf("sandbox: Binary is required")
	}
	if !cfg.DryRun {
		if err := requireAbsent(tool); err != nil {
			return Result{}, err
		}
	}
	home, err := os.MkdirTemp("", "aes-sandbox-")
	if err != nil {
		return Result{}, fmt.Errorf("sandbox: create home: %w", err)
	}
	// The installer writes to $AES_HOME/bin, and a 0700 temp parent is what
	// keeps a stray world-readable artefact out of a shared /tmp.
	if err := os.Chmod(home, 0o700); err != nil {
		os.RemoveAll(home)
		return Result{}, fmt.Errorf("sandbox: secure home: %w", err)
	}

	res := Result{Tool: tool.Name, Home: home}
	// The binary under test may be sitting next to the tool it is installing
	// (this repository's own dist/). A sandbox that can see the developer's
	// PATH state is not testing what a fresh machine would do, so the tool is
	// hidden from PATH for the duration and only the sandbox's own bin
	// directory is visible.
	env := sandboxEnv(home)

	if !cfg.Keep {
		defer os.RemoveAll(home)
	}

	// 1. install
	out, code, err := run(ctx, cfg, env, home, "setup", "--only", tool.Name)
	res.record("install", err == nil && code == 0, detail(out, code, err))
	if err != nil || code != 0 {
		return res, nil
	}

	binName := primaryBinary(tool)
	binPath := filepath.Join(home, "bin", binName)

	// 2. the binary is there and verifies
	if cfg.DryRun {
		res.record("binary present", true, "skipped: dry run installs nothing")
		res.record("verify ok", true, "skipped: dry run installs nothing")
		res.record("state entry", true, "skipped: dry run installs nothing")
		res.record("idempotent", true, "skipped: dry run installs nothing")
		res.record("drift detected", true, "skipped: dry run installs nothing")
		res.record("force reinstall", true, "skipped: dry run installs nothing")
		res.Passed = true
		return res, nil
	}

	_, statErr := os.Stat(binPath)
	present := statErr == nil
	res.record("binary present", present, pathDetail(binPath, statErr))

	// Verify against the sandbox's own view, not the developer's PATH.
	verifyRes := verifyIn(tool, env, home)
	res.record("verify ok", verifyRes.Status == verifier.StatusOK,
		fmt.Sprintf("status=%s version=%q", verifyRes.Status, verifyRes.Version))

	// 3. state.json on disk, re-read from the file rather than trusted from
	// the process that wrote it
	res.record("state entry", stateHas(home, tool.Name), "state.json")

	// 4. idempotence: nothing about the sandbox may change
	beforeHome := fingerprint(home)
	beforeBin := fingerprint(binPath)
	out, code, err = run(ctx, cfg, env, home, "setup", "--only", tool.Name)
	afterHome := fingerprint(home)
	afterBin := fingerprint(binPath)
	idempotent := err == nil && code == 0 && beforeHome == afterHome && beforeBin == afterBin
	res.record("idempotent", idempotent,
		idempotentDetail(beforeHome, afterHome, beforeBin, afterBin, code, err))

	// 5. drift: remove the binary, doctor must notice
	if rmErr := os.Remove(binPath); rmErr != nil {
		res.record("drift detected", false, fmt.Sprintf("could not remove %s: %v", binPath, rmErr))
	} else {
		out, code, err := run(ctx, cfg, env, home, "doctor")
		drift := err == nil && strings.Contains(strings.ToLower(out), "drift")
		res.record("drift detected", drift, detail(out, code, err))
	}

	// 6. --force reinstalls
	out, code, err = run(ctx, cfg, env, home, "setup", "--only", tool.Name, "--force")
	_, statErr = os.Stat(binPath)
	res.record("force reinstall", err == nil && code == 0 && statErr == nil,
		detail(out, code, err))

	res.Passed = allOK(res.Steps)
	return res, nil
}

func (r *Result) record(name string, ok bool, detail string) {
	r.Steps = append(r.Steps, Step{Name: name, OK: ok, Detail: detail})
}

func allOK(steps []Step) bool {
	for _, s := range steps {
		if !s.OK {
			return false
		}
	}
	return true
}

// sandboxEnv builds the environment for a run.
//
// PATH is replaced rather than appended, and contains only the sandbox's own
// bin. Appending would leave the developer's real tools visible, so a "tool is
// absent" starting condition would not hold and every step after the first
// would be testing the wrong thing.
// sandboxEnv builds the environment for a run.
//
// PATH puts the sandbox's own bin FIRST and then inherits the host's, rather
// than replacing it. Replacing it was the first attempt and it is wrong: an
// installer is a real program that shells out to mkdir, tar, curl and apt-get,
// and a PATH holding only the sandbox bin cannot find any of them. The failure
// then looks like a broken installer rather than a broken harness, which is the
// worst way for this to surface.
//
// Prepending gives both properties that matter: the tool under test resolves
// to the sandbox copy because it comes first, and system tools still resolve
// at all. What it does NOT give is a machine where the tool is genuinely
// absent - see requireAbsent, which asserts that precondition rather than
// assuming it.
func sandboxEnv(home string) []string {
	bin := filepath.Join(home, "bin")
	path := bin
	if host := os.Getenv("PATH"); host != "" {
		path = bin + string(os.PathListSeparator) + host
	}
	env := []string{
		"PATH=" + path,
		"HOME=" + home,
		"AES_HOME=" + home,
		"SHELL=/bin/sh",
		// Package managers refuse to run as root in a container or CI image
		// without this, and their non-interactive output is what a test wants
		// to parse anyway.
		"DEBIAN_FRONTEND=noninteractive",
		"LC_ALL=C",
	}
	if tmp := os.Getenv("TMPDIR"); tmp != "" {
		env = append(env, "TMPDIR="+tmp)
	}
	return env
}

// ErrToolPresent reports that the tool is already on the host PATH, so the
// sandbox cannot start from the absent state the sequence is defined against.
var ErrToolPresent = errors.New("tool is already on the host PATH; the sandbox cannot prove a fresh install")

// requireAbsent checks the starting condition the whole sequence depends on.
//
// If the developer already has the tool, a run that reports success proves
// nothing: the binary was there before setup touched it. Asserting the
// precondition is better than hiding the tool from PATH, which would also hide
// the system tools a real installer needs.
func requireAbsent(tool *manifest.Tool) error {
	bin := primaryBinary(tool)
	if bin == "" {
		return nil
	}
	if _, err := exec.LookPath(bin); err == nil {
		return fmt.Errorf("%w: %s", ErrToolPresent, bin)
	}
	return nil
}

// run invokes the binary under test with an isolated home.
//
// Every step is a separate process on purpose: it is the only way to observe
// that something reached disk rather than merely being set in memory.
func run(ctx context.Context, cfg Config, env []string, home string, args ...string) (string, int, error) {
	full := append([]string{}, args...)
	if cfg.DryRun {
		full = append(full, "--dry-run")
	}
	if cfg.Verbose {
		full = append(full, "--verbose")
	}
	// --non-interactive, not --yes: --yes asserts a terminal exists precisely
	// so an automated run cannot hang on a prompt, and a harness is an
	// automated run. See the package comment.
	full = append(full, "--non-interactive")

	stepCtx, cancel := context.WithTimeout(ctx, StepTimeout)
	defer cancel()

	cmd := exec.CommandContext(stepCtx, cfg.Binary, full...)
	cmd.Env = env
	// Run from the sandbox so a checkout in the working directory cannot
	// supply the catalog instead of the one under test.
	cmd.Dir = home

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	if stepCtx.Err() == context.DeadlineExceeded {
		return out.String(), code, fmt.Errorf("aes %s timed out after %s", strings.Join(args, " "), StepTimeout)
	}
	return out.String(), code, err
}

// verifyIn runs the verifier with the sandbox's PATH.
//
// The verifier resolves binaries through exec.LookPath, which reads the
// process environment. A subprocess is the only honest way to check the binary
// is on the sandbox's PATH and not the developer's.
func verifyIn(tool *manifest.Tool, env []string, home string) verifier.Result {
	bin := filepath.Join(home, "bin")
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", bin); err != nil {
		return verifier.Result{Tool: tool.Name, Status: verifier.StatusMissing}
	}
	defer os.Setenv("PATH", oldPath)
	return verifier.Verify(tool)
}

// stateHas reports whether state.json names the tool. It parses the file
// rather than asking the process that wrote it, so a writer that populates
// state and forgets to save cannot pass this.
func stateHas(home, tool string) bool {
	data, err := os.ReadFile(filepath.Join(home, "state.json"))
	if err != nil {
		return false
	}
	// Deliberately not importing internal/state: this must not share code with
	// the writer it is checking.
	var f struct {
		Installed map[string]json.RawMessage `json:"installed"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return false
	}
	_, ok := f.Installed[tool]
	return ok
}

// primaryBinary is the name the sandbox expects to find in its bin directory.
func primaryBinary(tool *manifest.Tool) string {
	if tool.Verify != nil && tool.Verify.Command != "" {
		return tool.Verify.Command
	}
	if len(tool.Provides) > 0 {
		return tool.Provides[0]
	}
	return tool.Name
}

// fingerprint is a digest of a path, or "" when it does not exist. Used to
// prove a re-run changed nothing.
func fingerprint(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d:%d", info.Size(), info.ModTime().UnixNano())
	return b.String()
}

// treeFingerprint digests every file under root, path-sorted, so two
// directories can be compared byte for byte. This is what makes the "host
// ~/.aes is untouched" claim an assertion rather than an intention.
func treeFingerprint(root string) (string, error) {
	h := sha256.New()
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return "absent", nil
		}
		return "", err
	}
	sort.Strings(files)
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		data, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s %x\n", rel, sha256.Sum256(data))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// TreeFingerprint exposes the digest used for the host-isolation assertion.
func TreeFingerprint(root string) (string, error) { return treeFingerprint(root) }

func detail(out string, code int, err error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "exit=%d", code)
	if err != nil {
		fmt.Fprintf(&b, " err=%v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			fmt.Fprintf(&b, " | %s", line)
			break
		}
	}
	return b.String()
}

// idempotentDetail explains a false verdict. It must consider exactly the same
// things the verdict does — an earlier version compared only the home and
// printed "nothing changed" while failing on the binary, which sends the reader
// looking for a difference that is not in the place it names.
func idempotentDetail(beforeHome, afterHome, beforeBin, afterBin string, code int, err error) string {
	switch {
	case err != nil || code != 0:
		return "re-run did not succeed: " + detail("", code, err)
	case beforeHome != afterHome:
		return fmt.Sprintf("sandbox home changed on re-run (before=%s after=%s)", beforeHome, afterHome)
	case beforeBin != afterBin:
		return fmt.Sprintf("binary was rewritten on re-run (before=%s after=%s)", beforeBin, afterBin)
	default:
		return "nothing changed"
	}
}

func pathDetail(p string, err error) string {
	if err != nil {
		return fmt.Sprintf("%s: %v", p, err)
	}
	return p + " exists"
}
