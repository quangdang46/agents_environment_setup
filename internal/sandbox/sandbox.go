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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
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
	// AllowUnproven passes --allow-unproven to the setup steps.
	//
	// It is false by default, and the container proof is the only caller that
	// sets it, because of a chicken-and-egg the resolver otherwise imposes: a
	// tool proven on linux/arm64 is SKIPPED on linux/amd64 (I15), so the very
	// run meant to produce amd64 evidence refuses to install the tool whose
	// evidence is missing. A tool could therefore only ever be proven on the
	// platform it was already proven on.
	//
	// This does not weaken the sequence. Every step still has to pass — the
	// binary must be present, verify must report OK, state must be written,
	// the re-run must change nothing. The flag only stops the resolver from
	// declining to try; it does not make a failed install look like a pass.
	AllowUnproven bool
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
	return runIn(ctx, Host{}, tool, cfg)
}

// RunIn is Run against a named environment.
//
// It exists because the steps a Layer 2 proof is made of cannot all be run on
// a developer machine: Layer 1 needs a tool present and Layer 2 needs it
// absent, so on one host a package-strategy tool is provable exactly once and
// an apt tool not at all. Passing the environment in rather than duplicating
// the steps is the point — a second copy of this function would prove that the
// copy works, not that aes does.
func RunIn(ctx context.Context, env Environment, tool *manifest.Tool, cfg Config) (Result, error) {
	return runIn(ctx, env, tool, cfg)
}

func runIn(ctx context.Context, environment Environment, tool *manifest.Tool, cfg Config) (Result, error) {
	if cfg.Binary == "" {
		return Result{}, fmt.Errorf("sandbox: Binary is required")
	}
	if !cfg.DryRun {
		if err := requireAbsent(ctx, environment, tool); err != nil {
			return Result{}, err
		}
	}
	home, err := environment.MkdirTemp(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("sandbox: create home: %w", err)
	}

	res := Result{Tool: tool.Name, Home: home}
	// The binary under test may be sitting next to the tool it is installing
	// (this repository's own dist/). A sandbox that can see the developer's
	// PATH state is not testing what a fresh machine would do, so the tool is
	// hidden from PATH for the duration and only the sandbox's own bin
	// directory is visible.
	env := environment.Env(home)

	if !cfg.Keep {
		defer environment.RemoveAll(ctx, home)
	}

	// 1. install
	out, code, err := run(ctx, environment, cfg, env, home, setupArgs(cfg, tool.Name)...)
	res.record("install", err == nil && code == 0, detail(out, code, err))
	if err != nil || code != 0 {
		return res, nil
	}

	binName := primaryBinary(tool)
	// Everything aes is responsible for lives under AES_HOME. Tool caches
	// land beside it, under the sandbox root, and are not aes's to account for.
	aesHome := aesHomeOf(home)

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

	// Verify against the sandbox's own view, and take the reported path from
	// it. A package strategy installs wherever the package manager puts its
	// binaries — /opt/homebrew/bin, /usr/bin, npm's global prefix — so
	// assuming $AES_HOME/bin reports "not installed" for a tool that
	// installed correctly.
	verifyRes := verifyIn(ctx, environment, cfg, env, home, tool.Name)
	binPath := verifyRes.Path
	if binPath == "" {
		binPath = filepath.Join(aesHome, "bin", binName)
	}
	binDigest, digestErr := environment.FileDigest(ctx, binPath)
	present := digestErr == nil && exists(binDigest)
	res.record("binary present", present, digestDetail(binPath, binDigest, digestErr))
	res.record("verify ok", verifyRes.Status == verifier.StatusOK,
		fmt.Sprintf("status=%s version=%q path=%s", verifyRes.Status, verifyRes.Version, binPath))

	// 3. state.json on disk, re-read from the file rather than trusted from
	// the process that wrote it
	res.record("state entry", stateHasIn(ctx, environment, aesHome, tool.Name), "state.json")

	// 4. idempotence: nothing about the sandbox may change
	//
	// The per-file diff is a host-only diagnostic. Copying a tree means
	// reaching into the environment's filesystem, and for a container that
	// means shipping the whole tree out over docker exec to explain a
	// failure. The digests are the assertion; the diff is only a courtesy to
	// whoever reads the failure.
	beforeHome, err := environment.TreeDigest(ctx, aesHome)
	if err != nil {
		return res, fmt.Errorf("sandbox: fingerprint before re-run: %w", err)
	}
	beforeBin, _ := environment.FileDigest(ctx, binPath)

	out, code, err = run(ctx, environment, cfg, env, home, setupArgs(cfg, tool.Name)...)

	afterHome, err := environment.TreeDigest(ctx, aesHome)
	if err != nil {
		return res, fmt.Errorf("sandbox: fingerprint after re-run: %w", err)
	}
	afterBin, _ := environment.FileDigest(ctx, binPath)
	idempotent := err == nil && code == 0 && beforeHome == afterHome && beforeBin == afterBin
	res.record("idempotent", idempotent,
		idempotentDetail(beforeHome, afterHome, beforeBin, afterBin, code, err))

	// 5. drift: remove the binary, doctor must notice
	if _, _, rmErr := environment.Sh(ctx, "rm -f "+shellQuote(binPath), env); rmErr != nil {
		res.record("drift detected", false, fmt.Sprintf("could not remove %s: %v", binPath, rmErr))
	} else {
		out, code, err := run(ctx, environment, cfg, env, home, "doctor")
		// Exit code deliberately ignored, same reasoning as verifyIn: doctor
		// exits non-zero precisely when it HAS found something, so requiring
		// err == nil here would make the check incapable of ever passing. What
		// is asserted is the report, and the tool named in it.
		drift := strings.Contains(strings.ToLower(out), "drift") &&
			strings.Contains(out, tool.Name)
		res.record("drift detected", drift, detail(out, code, err))
	}

	// 6. --force reinstalls
	out, code, err = run(ctx, environment, cfg, env, home, append(setupArgs(cfg, tool.Name), "--force")...)
	finalDigest, digestErr := environment.FileDigest(ctx, binPath)
	res.record("force reinstall",
		err == nil && code == 0 && digestErr == nil && exists(finalDigest),
		detail(out, code, err))

	res.Passed = allOK(res.Steps)
	return res, nil
}

// exists reports whether a digest describes a file that is there. Both the host
// and the container spell absence differently — "" and "absent" — so the check
// is written once here rather than in each caller.
func exists(digest string) bool { return digest != "" && digest != "absent" }

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
	bin := filepath.Join(aesHomeOf(home), "bin")
	path := bin
	if host := os.Getenv("PATH"); host != "" {
		path = bin + string(os.PathListSeparator) + host
	}
	env := []string{
		"PATH=" + path,
		// HOME is the sandbox root and AES_HOME is the conventional child of
		// it, which is what a real machine looks like. Collapsing the two
		// puts every cache a tool keeps in $HOME inside the directory this
		// package digests, and npm alone writes a timestamped debug log per
		// invocation — so "re-running changes nothing" was false for reasons
		// that had nothing to do with aes.
		"HOME=" + home,
		"AES_HOME=" + aesHomeOf(home),
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
// The question is asked of the environment the install will happen in, not of
// the host. On the host that is a question about the developer's machine; in a
// container it is a question about a machine where the tool has never been
// installed. Getting this wrong is the specific failure the container exists
// to prevent — a "fresh install" that was fresh only because PATH was
// arranged to hide something that was really already there.
func requireAbsent(ctx context.Context, env Environment, tool *manifest.Tool) error {
	bin := primaryBinary(tool)
	if bin == "" {
		return nil
	}
	present, err := env.HasBinary(ctx, bin)
	if err != nil {
		return fmt.Errorf("sandbox: checking whether %s is present: %w", bin, err)
	}
	if present {
		return fmt.Errorf("%w: %s", ErrToolPresent, bin)
	}
	return nil
}

// run invokes the binary under test with an isolated home.
//
// Every step is a separate process on purpose: it is the only way to observe
// that something reached disk rather than merely being set in memory.
// setupArgs is the argv prefix every `aes setup` step in the sequence shares.
// It exists so the tool selection and the opt-in are decided in one place: the
// same three steps must ask for the same thing, and a step that forgot the flag
// would be a step quietly running a different run than the one being proved.
func setupArgs(cfg Config, tool string) []string {
	args := []string{"setup", "--only", tool}
	if cfg.AllowUnproven {
		args = append(args, "--allow-unproven")
	}
	return args
}

func run(ctx context.Context, env Environment, cfg Config, environ []string, home string, args ...string) (string, int, error) {
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

	// Run from the sandbox so a checkout in the working directory cannot
	// supply the catalog instead of the one under test.
	res, err := env.Run(ctx, append([]string{cfg.Binary}, full...), environ, home)
	if err != nil {
		return res.Combined(), res.ExitCode, err
	}
	return res.Combined(), res.ExitCode, nil
}

// verifyIn checks the tool the way a user would: by running `aes verify`
// with the sandbox's PATH, in a subprocess.
//
// It shells out rather than calling internal/verifier directly. Calling the
// library would require setting PATH on this process, because the verifier
// resolves binaries through exec.LookPath — and process environment is global
// state. That works today only because no test here is parallel; the first
// t.Parallel() would make it flaky in a way that looks like a broken installer.
//
// Subprocess also makes the assertion match the contract. The bead asks that
// "Verify returns StatusOK", and in a real run that means what `aes verify`
// reports, not what the library returns in isolation.
func verifyIn(ctx context.Context, env Environment, cfg Config, environ []string, home, tool string) verifier.Result {
	res, _ := env.Run(ctx,
		[]string{cfg.Binary, "verify", "--json", "--non-interactive"}, environ, home)
	// Only stdout is parsed. The streams are kept apart by the environment,
	// and combining them is the documented trap in this repo: one warning
	// line on stderr turns a valid JSON document into something that will not
	// parse, and the parse failure reads as "the tool is missing" rather than
	// as "we merged the streams".
	//
	// The exit code is deliberately ignored. `aes verify` exits non-zero when
	// ANY tool is missing, and in a sandbox most of the catalog is missing by
	// design — so treating a non-zero exit as "could not determine" reported
	// every tool as missing even though the JSON on stdout had the answer.
	//
	// The contract is "what does verify say about THIS tool", not "did the
	// whole run succeed". A tool that was just installed is present whatever
	// the other fifty say.
	out := []byte(res.Stdout)
	if len(out) == 0 {
		return verifier.Result{Tool: tool, Status: verifier.StatusMissing,
			Path: "no output from aes verify"}
	}

	var payload struct {
		Tools []struct {
			Name    string `json:"name"`
			Status  string `json:"status"`
			Version string `json:"version"`
			Path    string `json:"path"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return verifier.Result{Tool: tool, Status: verifier.StatusMissing,
			Path: "unparseable: " + firstLine(string(out))}
	}
	for _, entry := range payload.Tools {
		if entry.Name != tool {
			continue
		}
		return verifier.Result{
			Tool:    tool,
			Status:  verifier.Status(entry.Status),
			Version: entry.Version,
			Path:    entry.Path,
		}
	}
	return verifier.Result{Tool: tool, Status: verifier.StatusMissing}
}

// stateHas reports whether state.json on the host names the tool.
func stateHas(home, tool string) bool {
	return stateHasIn(context.Background(), Host{}, home, tool)
}

// stateHasIn reads state.json from the environment the install happened in. It
// parses the file rather than asking the process that wrote it, so a writer
// that populates state and forgets to save cannot pass this.
func stateHasIn(ctx context.Context, env Environment, home, tool string) bool {
	data, err := env.ReadFile(ctx, filepath.Join(home, "state.json"))
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
//
// It names which of the two digests moved rather than which files moved. A
// per-file diff needs a copy of the tree, and a container's tree cannot be
// copied cheaply — the digests are the assertion, and a reader who needs the
// file list can re-run with the sandbox kept.
func idempotentDetail(beforeHome, afterHome, beforeBin, afterBin string, code int, err error) string {
	switch {
	case err != nil || code != 0:
		return "re-run did not succeed: " + detail("", code, err)
	case beforeHome != afterHome:
		return fmt.Sprintf("sandbox home changed on re-run (%s -> %s)", short(beforeHome), short(afterHome))
	case beforeBin != afterBin:
		return fmt.Sprintf("binary was rewritten on re-run (before=%s after=%s)", beforeBin, afterBin)
	default:
		return "nothing changed"
	}
}

// digestDetail explains a presence check. The digest is reported rather than
// just "exists", because a digest of "absent" and a digest of a zero-byte file
// are both evidence and they are not the same evidence.
func digestDetail(path, digest string, err error) string {
	if err != nil {
		return fmt.Sprintf("%s: %v", path, err)
	}
	if !exists(digest) {
		return path + " is absent"
	}
	return fmt.Sprintf("%s exists (%s)", path, digest)
}

// short trims a digest for a message.
func short(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

// firstLine is the first non-empty line of s, for diagnostics.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
