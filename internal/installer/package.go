package installer

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/quangdang46/agents_environment_setup/internal/exec"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// shaRe is a 64-character hex digest, lowercase.
var shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// PackageInstaller installs a tool through a system package manager.
//
// It carries the privilege model the spec locks in, and the model is the
// whole reason this file exists. The tension is real: the North Star says
// "one command, zero manual install", the security model says "never
// auto-sudo", and on Ubuntu those collide.
//
// The resolution is that the risk in sudo is not sudo — it is *who chose the
// command*. Silent sudo plus a hostile tool.yaml means a bad actor gets your
// password. Sudo that prints the command and asks means you see exactly what
// is about to run before you type anything.
//
// So: never run silently, but never refuse outright. Refusing outright would
// break the North Star on any machine where apt needs root.
//
//	brew                → no privilege, runs directly
//	package + apt       → sudo
//	  interactive:       print, confirm, run
//	  non-interactive:   sudo -n (never blocks); on failure print, stop,
//	                     return PrivilegeError — the CLI maps that to exit 5
//
// # Two runners, and why
//
// exec.Run refuses any command containing sudo, without executing it. That
// gate is correct and stays. But it means an *authorized* command cannot go
// through exec.Run, so this type has two runners:
//
//   - RunUnprivileged — no escalation, delegates to exec.Run. The gate applies.
//   - RunPrivileged   — the one path that may invoke sudo, reachable only
//     after the command has been printed and Authorize has agreed.
//
// Splitting them keeps the capability visible in the type rather than
// smuggled through a flag, and makes the tests assert on which one was
// reached. A test that only checked the error value would pass even if the
// command had already run.
type PackageInstaller struct {
	// RunUnprivileged runs a command needing no escalation. Defaults to
	// exec.Run, which refuses anything containing sudo.
	RunUnprivileged func(ctx context.Context, cmd string) (exec.Result, error)

	// RunPrivileged runs a command that has already been printed and
	// authorized. It is the only path that may invoke sudo. Defaults to a
	// runner that executes directly, because by the time it is reached the
	// gate has deliberately been passed.
	RunPrivileged func(ctx context.Context, cmd string) (exec.Result, error)

	// Authorize decides whether a printed command may proceed. It is called
	// only AFTER the command has been printed, never before. Nil means
	// confirm on stdin.
	Authorize func(ctx context.Context, command string) error

	// NonInteractive suppresses prompting. The command is attempted with
	// sudo -n so it can never block on a password.
	NonInteractive bool

	// In is read for the interactive confirmation. Defaults to os.Stdin.
	// EOF or a non-affirmative answer means no.
	In io.Reader

	// Out receives the printed command and the prompt. Defaults to os.Stderr.
	// Commands are always printed, even on success, so the user can see what
	// was run after the fact.
	Out io.Writer

	// IsRoot reports whether the process is already root, in which case apt
	// needs no sudo at all. This is the normal case inside a container and
	// it is why the sandbox tests do not need a password.
	IsRoot func() bool
}

// defaultPrivilegeTimeout bounds a privileged install. It matches the
// unprivileged default: a hung apt-get must not hang AES forever.
const defaultPrivilegeTimeout = exec.DefaultTimeout

func (p *PackageInstaller) out() io.Writer {
	if p.Out != nil {
		return p.Out
	}
	return os.Stderr
}

func (p *PackageInstaller) root() bool {
	if p.IsRoot != nil {
		return p.IsRoot()
	}
	return os.Geteuid() == 0
}

func (p *PackageInstaller) unprivileged() func(context.Context, string) (exec.Result, error) {
	if p.RunUnprivileged != nil {
		return p.RunUnprivileged
	}
	return func(ctx context.Context, cmd string) (exec.Result, error) {
		return exec.Run(ctx, cmd, exec.Options{Timeout: defaultPrivilegeTimeout})
	}
}

// privileged executes a command that has already been printed and authorized.
//
// The default is exec.RunAuthorized rather than a local copy of the mechanism.
// That function exists in exec precisely to be the one place a privileged
// command is spawned, and a second implementation here made that untrue: the
// spec claims one place constructs a privileged command, and while this copy
// existed there were two — with the one in exec unreachable, so its
// Authorization gate guarded nothing at all.
//
// An injected RunPrivileged short-circuits the gate, which is the point of
// injection: these tests are about the installer's DECISIONS, and exec has its
// own tests for what RunAuthorized does once given a decision.
func (p *PackageInstaller) privileged() func(context.Context, string, exec.Authorization) (exec.Result, error) {
	if p.RunPrivileged != nil {
		return func(ctx context.Context, cmd string, _ exec.Authorization) (exec.Result, error) {
			return p.RunPrivileged(ctx, cmd)
		}
	}
	return func(ctx context.Context, cmd string, auth exec.Authorization) (exec.Result, error) {
		return exec.RunAuthorized(ctx, cmd, exec.Options{Timeout: defaultPrivilegeTimeout}, auth)
	}
}

// Install dispatches on the target's manager. It never dispatches on the
// tool name (invariant I1).
func (p *PackageInstaller) Install(ctx context.Context, a Action) error {
	pkg := a.Target.Package
	if strings.TrimSpace(pkg) == "" {
		return fmt.Errorf("install %s: strategy %s requires a package", a.Tool, manifest.StrategyPackage)
	}

	switch a.Target.Manager {
	case manifest.ManagerBrew:
		// brew never needs root. No prompt, no sudo, no ceremony.
		//
		// `reinstall` rather than `install` when forcing, for the same reason
		// apt needs --reinstall below: brew also keeps its own record of what
		// it installed, and a binary deleted behind its back leaves that record
		// saying everything is fine.
		verb := "install"
		if a.Force {
			verb = "reinstall"
		}
		_, err := p.unprivileged()(ctx, "brew "+verb+" "+pkg)
		return err
	case manifest.ManagerApt:
		return p.installApt(ctx, a)
	default:
		// manifest validation already rejects this, but the installer is
		// reachable from anywhere and must not fall through to a default.
		return fmt.Errorf("install %s: unknown package manager %q (valid: %s, %s)",
			a.Tool, a.Target.Manager, manifest.ManagerBrew, manifest.ManagerApt)
	}
}

// installApt implements the privilege model for apt.
func (p *PackageInstaller) installApt(ctx context.Context, a Action) error {
	pkg := a.Target.Package

	// A declared apt source must exist BEFORE apt is asked for the package.
	//
	// postgresql-18 is absent from noble's index and only present if the PGDG
	// repository has been added. Skipping this step produced the exact bug the
	// comment above warns about, one layer up: `apt-get install -y
	// postgresql-18` said "Unable to locate package", and because that came out
	// of the privileged path it was reported as a privileged failure — an
	// escalation the user had no reason to expect for a package that simply
	// had not been declared.
	p.prepareAptSource(ctx, a)

	// --reinstall is what makes a forced run a repair rather than a no-op.
	//
	// dpkg's idea of what is installed comes from its own database, not from
	// the filesystem. Delete /usr/bin/jq without telling dpkg and the two
	// disagree: `apt-get install -y jq` prints "already the newest version",
	// exits 0, and restores nothing at all. So a --force that used the
	// first-install command would report success while leaving the tool
	// broken — a run that repairs nothing and says it did.
	//
	// It is omitted otherwise. --reinstall re-unpacks every file of the
	// package, so putting it on ordinary installs would rewrite files that
	// were already correct on every setup run.
	base := "apt-get install -y"
	if a.Force {
		base += " --reinstall"
	}
	base += " " + pkg

	// Already root: no escalation question arises. Containers and CI images
	// land here, and it is why `aes test` needs no password.
	//
	// The command carries no sudo, so it goes down the UNprivileged path.
	// Routing a command that needs no elevation through the privileged runner
	// would have it claim an Authorization it never earned, and the gate would
	// stop meaning anything the first time it was passed something true.
	if p.root() {
		_, err := p.unprivileged()(ctx, base)
		if err != nil {
			return fmt.Errorf("install %s: %w", a.Tool, err)
		}
		return nil
	}

	if p.NonInteractive {
		// sudo -n fails immediately rather than prompting. Success means the
		// install ran; failure means we stop and report. There is no retry:
		// a second attempt is how a tool ends up waiting on a prompt that
		// nobody is there to answer.
		// Confirmed is true because `sudo -n` is itself the authorization: it
		// cannot block on a password nobody is there to type, so there is
		// nothing to ask and nothing to wait for. It fails fast and reports.
		auth := exec.Authorization{
			Confirmed:      true,
			NonInteractive: true,
			Reason:         "apt install (non-interactive)",
		}
		return p.runApt(ctx, a.Tool, "sudo -n "+base, auth)
	}

	// Interactive. Print first — the user sees the exact command before
	// being asked, and before anything can run.
	p.printf("aes: this needs elevated privileges. The command is:\n\n    sudo %s\n\n", base)
	if err := p.authorize(base); err != nil {
		return err
	}
	// Reached only because the command was printed and then agreed to, which is
	// the whole content of Confirmed.
	auth := exec.Authorization{
		Confirmed: true,
		Reason:    "apt install (confirmed by the user)",
	}
	return p.runApt(ctx, a.Tool, "sudo "+base, auth)
}

// runApt executes an apt command, translating failure into a PrivilegeError
// when escalation was involved.
//
// On the failure path the printable command is emitted, because the user's
// next move is to run it themselves and the CLI will exit 5.
func (p *PackageInstaller) runApt(ctx context.Context, tool, cmd string, auth exec.Authorization) error {
	_, err := p.privileged()(ctx, cmd, auth)
	if err == nil {
		return nil
	}
	if auth.NonInteractive {
		// Strip the sudo prefix so the message shows the command, not the
		// escalation wrapper.
		plain := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(cmd, "sudo -n "), "sudo "))
		p.printf("aes: could not install %s automatically. Run this yourself:\n\n    sudo %s\n\n", tool, plain)
		return &exec.PrivilegeError{Command: "sudo " + plain, NonInteractive: true}
	}
	return fmt.Errorf("install %s: %w", tool, err)
}

// authorize asks the user to confirm a printed command.
//
// Anything other than an explicit yes is a no. An EOF — which is what a
// closed or non-interactive stdin looks like — is a no, so a command can
// never run merely because nobody answered (I14).
func (p *PackageInstaller) authorize(command string) error {
	if p.Authorize != nil {
		return p.Authorize(context.Background(), command)
	}
	in := p.In
	if in == nil {
		in = os.Stdin
	}
	p.printf("Run it now? [y/N] ")
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && answer == "" {
		// EOF with nothing read: no confirmation was given.
		p.printf("\nnot confirmed\n")
		return &exec.PrivilegeError{Command: "sudo " + command, NonInteractive: false}
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	default:
		return &exec.PrivilegeError{Command: "sudo " + command, NonInteractive: false}
	}
}

func (p *PackageInstaller) printf(format string, args ...any) {
	fmt.Fprintf(p.out(), format, args...)
}

// prepareAptSource installs a tool's declared apt source, if it declares one.
//
// The keyring and the sources list are written to the standard system
// locations, which requires root. That is fine: this only runs when a manifest
// has explicitly declared a third-party repository, and the caller is already
// on the privileged path by the time it matters. A manifest that declares no
// source is a no-op, so ordinary distro packages are untouched.
//
// The key is verified against the digest in the manifest before it is trusted.
// A keyring is what makes apt accept packages from a repository at all, so an
// unverified one is not a lesser risk than an unverified binary — it is the
// thing that decides which binaries are acceptable.
// dearmorKeyring converts an armored (ASCII) GPG keyring to the binary form
// apt's signed-by= expects. It returns the input unchanged if gpg is not
// available or the input is already binary, so a repository that publishes a
// binary keyring keeps working and a missing gpg degrades to the previous
// behaviour rather than failing the install.
func dearmorKeyring(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// A binary keyring starts with the OpenPGP packet tag 0x99 (secret key
	// packet, long form). An armored one starts with "-----BEGIN PGP".
	if len(data) > 0 && data[0] == 0x99 {
		return data, nil
	}
	if _, err := osexec.LookPath("gpg"); err != nil {
		return data, nil
	}
	// gpg does not write the dearmored keyring to stdout in a way a caller can
	// capture reliably, so it is written to a file and read back. Measured on
	// gpg 2.4.4: `--dearmor --output -` produced an empty stdout, and
	// `--to-stdout` (the more memorable spelling) is not an option at all — gpg
	// answers "invalid option". Both failures surface as a zero-length keyring,
	// which reads as a corrupt download rather than a wrong flag.
	dearmored := path + ".dearmored"
	if err := osexec.Command("gpg", "--batch", "--yes",
		"--dearmor", "--output", dearmored, path).Run(); err != nil {
		return nil, fmt.Errorf("gpg --dearmor: %w", err)
	}
	out, err := os.ReadFile(dearmored)
	os.Remove(dearmored)
	if err != nil {
		return nil, fmt.Errorf("read dearmored keyring: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("gpg --dearmor produced no output")
	}
	return out, nil
}

func (p *PackageInstaller) prepareAptSource(ctx context.Context, a Action) {
	src := a.Target.AptSource
	if src == nil {
		return
	}
	if strings.TrimSpace(src.KeyringURL) == "" {
		return
	}
	if !shaRe.MatchString(src.KeyringSHA256) {
		// A malformed digest is a manifest error, not a runtime one. Refuse
		// rather than fetch a key we cannot check.
		fmt.Fprintf(os.Stderr, "aes: %s declares apt_source.keyring_sha256 %q, which is not 64 hex characters\n",
			a.Tool, src.KeyringSHA256)
		return
	}

	dir := "/usr/share/keyrings"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "aes: create %s: %v\n", dir, err)
		return
	}
	dst := filepath.Join(dir, a.Tool+"-archive-keyring.gpg")

	// Fetch to a temp file beside the destination, verify, then rename. A
	// partial download must never become the keyring apt reads.
	tmp, err := os.CreateTemp(dir, ".keyring-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "aes: temp keyring: %v\n", err)
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := downloadFile(ctx, src.KeyringURL, tmp); err != nil {
		tmp.Close()
		fmt.Fprintf(os.Stderr, "aes: fetch apt keyring for %s: %v\n", a.Tool, err)
		return
	}
	tmp.Close()

	// The digest is checked against the bytes as DOWNLOADED, before any
	// conversion. The manifest pins what came off the wire; a keyring rewritten
	// into a different encoding on the way in would be a manifest claim about
	// bytes the user never saw.
	if err := verifyFile(tmpName, src.KeyringSHA256); err != nil {
		fmt.Fprintf(os.Stderr, "aes: apt keyring for %s: %v\n", a.Tool, err)
		return
	}
	// apt's signed-by= needs a BINARY keyring. A repository that publishes
	// the armored form (postgresql.org does: ACCC4CF8.asc) is written
	// verbatim to the file apt reads, and apt then reports NO_PUBKEY for a key
	// that is right there in the file — the failure looks like a missing key
	// rather than a format mismatch. gpg --dearmor converts; the key material
	// is the same, only the envelope changes.
	dearmored, err := dearmorKeyring(tmpName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aes: apt keyring for %s: %v\n", a.Tool, err)
		return
	}
	if err := os.WriteFile(tmpName, dearmored, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "aes: write dearmored keyring: %v\n", err)
		return
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "aes: chmod keyring: %v\n", err)
		return
	}
	if err := os.Rename(tmpName, dst); err != nil {
		fmt.Fprintf(os.Stderr, "aes: install keyring to %s: %v\n", dst, err)
		return
	}

	// The sources list is written last, so a failure above leaves apt
	// configured for repositories it cannot verify rather than the reverse.
	line := fmt.Sprintf("deb [signed-by=%s] %s\n", dst, src.URL)
	if err := os.WriteFile("/etc/apt/sources.list.d/"+a.Tool+".list",
		[]byte(line), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "aes: write apt sources for %s: %v\n", a.Tool, err)
		return
	}

	// The index refresh uses the same root/unprivileged choice installApt
	// makes, for the same reason: a command that needs no elevation must not
	// go down the privileged path, or the gate stops meaning anything the
	// first time something true is passed to it.
	update := func(ctx context.Context, cmd string) (exec.Result, error) {
		if p.root() {
			return p.unprivileged()(ctx, cmd)
		}
		return p.privileged()(ctx, cmd, exec.Authorization{
			Confirmed:      true,
			NonInteractive: p.NonInteractive,
			Reason:         "apt-get update after adding a declared source",
		})
	}
	if out, err := update(ctx, "apt-get update -qq"); err != nil {
		fmt.Fprintf(os.Stderr, "aes: apt-get update after adding source for %s: %v\n%s",
			a.Tool, err, out.Stdout)
	}
}

// downloadFile streams url to dest, refusing to write more than maxAssetBytes.
func downloadFile(ctx context.Context, url string, dest *os.File) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	_, err = io.Copy(dest, io.LimitReader(resp.Body, maxAssetBytes+1))
	return err
}

// verifyFile checks the sha256 of path against want, which must be lowercase hex.
func verifyFile(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("sha256 mismatch: expected %s, got %s", want, got)
	}
	return nil
}
