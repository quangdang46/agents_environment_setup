package verifier

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// Layer 1 proves detection against the tools actually on this machine. Install
// is skipped; verify is the assertion.
//
// The expectations here are floors, not pins. Each `min` sits below what a
// reasonably current machine has, so the assertion — "StatusOK, with a version
// read and above this floor" — holds on any host where the tool is installed
// and on whatever version it has next. Pinning an exact version would turn this
// into a test that fails the day a tool ships a release, which is a test about
// the internet, not about the verifier.
//
// Nothing here reads runtime.GOOS and compares it to runtime.GOOS. A test that
// does that passes on the machine that wrote it and fails on everyone else's,
// or worse passes everywhere while checking nothing.
type layer1Fixture struct {
	name string
	bin  string
	// versionCmd prints the tool's version, and min is the floor asserted
	// against it.
	versionCmd string
	min        string
}

var layer1Fixtures = []layer1Fixture{
	{"ripgrep", "rg", "rg --version", "14.0"},
	{"git", "git", "git --version", "2.30"},
	{"fzf", "fzf", "fzf --version", "0.50"},
	{"jq", "jq", "jq --version", "1.6"},
	{"tmux", "tmux", "tmux -V", "3.4"},
	{"zoxide", "zoxide", "zoxide --version", "0.9.0"},
	{"gh", "gh", "gh --version", "2.0"},
	{"claude", "claude", "claude --version", "1.0"},
	// Added with the linux/amd64 Layer 2 evidence (2026-09-27, bead gj9). Each
	// was measured, not assumed: the version command below is the one that
	// actually printed a version on this host, and the binary name is the one
	// the archive really ships — `bottom` installs as `btm`, which is the kind
	// of thing that looks like a working install until you go looking for it.
	{"bat", "bat", "bat --version", "0.26"},
	{"btop", "btop", "btop --version", "1.4"},
	{"dust", "dust", "dust --version", "1.2"},
	{"eza", "eza", "eza --version", "0.23"},
	{"codex", "codex", "codex --version", "0.130"},
	{"opencode", "opencode", "opencode --version", "1.10"},
	// Installs as `btm`; the archive ships that name and the manifest's
	// verify.command follows it. Looking for a file called `bottom` finds
	// nothing, which is the shape of a catalog entry that installs and then
	// fails to verify for a reason that has nothing to do with the installer.
	{"bottom", "btm", "btm --version", "0.14"},
	{"zellij", "zellij", "zellij --version", "0.45"},
	// The probe is `ntm version`, not `ntm --version` — see the manifest. The
	// dashed spelling is rejected by every ntm older than 1.35.
	{"ntm", "ntm", "ntm version", "1.0"},
	// node is the runtime every npm-strategy tool depends on, so its presence
	// is a precondition for three of the tools above. The floor is the lowest
	// number either supported platform can supply: darwin has 26, ubuntu
	// noble has 18.19.1 and nothing else, and a floor above what a platform
	// can offer is the node-18 failure that started bead 8tv.
	{"node", "node", "node --version", "18.0"},
	// npm is its own tool rather than something node provides — on Ubuntu
	// 24.04 `apt install nodejs` does not bring it — so it is verified on the
	// same terms. The floor is the lower of the two platforms' offerings.
	{"npm", "npm", "npm --version", "9.0"},
	// Cloud CLIs. These have no binary on most developer machines, so the
	// fixture skips here and runs for real wherever npm put them — which is
	// exactly what Layer 1 is for: it asserts a tool that IS on the machine
	// verifies, and says nothing about one that is not. Their Layer 2 evidence
	// is a fresh container, recorded in each manifest.
	{"vercel", "vercel", "vercel --version", "30.0"},
	{"supabase", "supabase", "supabase --version", "1.0"},
	{"wrangler", "wrangler", "wrangler --version", "3.0"},

	// The rest of the catalog, added 2026-09-30 to take `tested: true` across
	// every tool. Every version command below was RUN against the binary on
	// this host and the output pasted into the comment, because the wrong
	// spelling is the ordinary failure here and it is silent: `slb --version`
	// prints "Error: unknown flag" and exits 0, `fsfs --version` prints
	// `"ok": false` and exits 0, and `k9s --version` errors. A probe that
	// exits 0 while printing nothing reports the tool as UNKNOWN — which looks
	// like a broken tool rather than a broken probe, and sends the next
	// person to the manifest for a fault that is not there.
	//
	// The binary column is the name in the archive, not the tool name, because
	// the two differ in five cases: cm ships `cass-memory`, opentofu ships
	// `tofu`, jeffreysprompts ships `jfp`, ultimate-bug-scanner ships `ubs`,
	// brenner-bot ships `brenner`, and srps ships `sysmoni`.
	{"agent-settings-backup", "asb", "asb --version", "0.3.2"},
	{"automated-plan-reviser", "apr", "apr --version", "1.3.1"},
	{"brenner-bot", "brenner", "brenner --version", "0.4.1"},
	{"casr", "casr", "casr --version", "0.3.0"},
	{"cm", "cass-memory", "cass-memory --version", "0.2.0"},
	{"delta", "delta", "delta --version", "0.15"},
	{"ee", "ee", "ee --version", "0.15.0"},
	{"fmd", "fmd", "fmd --version", "0.4.0"},
	// `fsfs version`, not `fsfs --version`: the dashed spelling exits 0 having
	// printed a JSON object with "ok": false. Measured on 1.10.0, which
	// prints `fsfs 1.10.0 (frankensearch 1.10.0)`.
	{"fsfs", "fsfs", "fsfs version", "1.10.0"},
	{"go", "go", "go version", "1.22"},
	{"jeffreysprompts", "jfp", "jfp --version", "1.0.3"},
	{"k9s", "k9s", "k9s version", "0.32"},
	{"meta-skill", "ms", "ms --version", "0.2.2"},
	{"omp", "omp", "omp --version", "18.0"},
	{"opentofu", "tofu", "tofu version", "1.6"},
	{"postgres18", "psql", "psql --version", "18"},
	{"rano", "rano", "rano --version", "0.2.0"},
	{"rch", "rch", "rch --version", "1.0"},
	{"ru", "ru", "ru --version", "1.4.0"},
	{"s2p", "s2p", "s2p --version", "0.3.4"},
	{"sbh", "sbh", "sbh --version", "0.6.16"},
	// `slb version`, not `slb --version`: the dashed spelling prints
	// "Error: unknown flag: --version" and exits 0. Measured on 0.5.2.
	{"slb", "slb", "slb version", "0.5.0"},
	{"starship", "starship", "starship --version", "1.0"},
	{"tailscale", "tailscale", "tailscale version", "1.0"},
	{"ultimate-bug-scanner", "ubs", "ubs --version", "5.4.9"},
	{"vault", "vault", "vault --version", "2.1.1"},
	{"xf", "xf", "xf --version", "0.4.0"},
	{"yq", "yq", "yq --version", "4.0"},
	{"aadc", "aadc", "aadc --version", "0.1"},
	{"age", "age", "age --version", "1.1"},
	{"bun", "bun", "bun --version", "1.3.0"},
	{"csctf", "csctf", "csctf --version", "0.4"},
	{"giil", "giil", "giil --version", "3.2.1"},
	// gnupg is here because vault, postgres18 and tailscale cannot install
	// without it — their apt_source keys are armored and the conversion
	// shells out to gpg.
	{"gnupg", "gpg", "gpg --version", "2.2"},
}

// presentOnThisMachine reports whether every binary a tool declares resolves
// here. LookPath, not a shell: `command -v tmux` in zsh reports an alias for
// a plugin wrapper, and the assertion would pass against something that is not
// the binary.
func presentOnThisMachine(tool *manifest.Tool) bool {
	for _, bin := range binaries(tool) {
		if _, err := LookPathFor(bin); err != nil {
			return false
		}
	}
	return true
}

// toolFor builds the manifest a real tool.yaml would carry for this fixture.
func (f layer1Fixture) tool() *manifest.Tool {
	return &manifest.Tool{
		Name: f.name,
		Verify: &manifest.Verify{
			Command: f.bin,
			Version: &manifest.VersionCheck{Command: f.versionCmd, Min: manifest.Floor{Default: f.min}},
		},
	}
}

func TestLayer1VerifyFixtures(t *testing.T) {
	if testing.Short() {
		t.Skip("layer 1 fixtures exercise the real machine; skipped under -short")
	}
	for _, f := range layer1Fixtures {
		t.Run(f.name, func(t *testing.T) {
			// LookPath, not a shell: `command -v` in zsh reports function and
			// alias definitions, and this machine aliases tmux to a plugin
			// wrapper. A shell check would pass a tool that has no binary.
			if _, err := exec.LookPath(f.bin); err != nil {
				t.Skipf("%s is not installed on this machine", f.bin)
			}

			got := Verify(f.tool())

			if got.Status != StatusOK {
				t.Errorf("Status = %q, want %q (version %q, stderr from the probe may explain it)",
					got.Status, StatusOK, got.Version)
			}
			if got.Version == "" {
				t.Error("Version is empty; a version command was declared, so one was expected")
			}
			if got.Path == "" {
				t.Error("Path was not resolved for a tool found on PATH")
			}
			if got.Tool != f.name {
				t.Errorf("Tool = %q, want %q", got.Tool, f.name)
			}
		})
	}
}

// Every fixture's version string must actually parse. This is the check that
// caught tmux 3.6b being read as "3", which made a tmux with a 3.x minimum
// report stale on every single run.
// versionArgs splits "<binary> <args...>" into just the arguments, so the
// command can be run against an absolute path. Every fixture's versionCmd is a
// bare command with flags and none is a shell pipeline, so splitting on
// whitespace is the whole job — which is worth saying, because the day one
// needs a pipeline this stops being true and the split quietly mangles it.
func versionArgs(cmd string) []string {
	fields := strings.Fields(cmd)
	if len(fields) < 2 {
		return nil
	}
	return fields[1:]
}

func TestLayer1FixturesParseRealVersionOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("layer 1 fixtures exercise the real machine; skipped under -short")
	}
	for _, f := range layer1Fixtures {
		t.Run(f.name, func(t *testing.T) {
			path, err := LookPathFor(f.bin)
			if err != nil {
				t.Skipf("%s is not installed on this machine", f.bin)
			}
			// Run the resolved PATH, not the bare name through a shell.
			//
			// Resolving by name is how this test found three stale binaries
			// on 2026-09-30: aes had installed asb 0.3.2, apr 1.3.1 and
			// brenner 0.4.1 into ~/.aes/bin, and older copies in
			// ~/.local/bin came first on PATH. The fixture then asserted its
			// floor against the OLD binary and failed on a machine where the
			// tool was correctly installed and verifying. The test was
			// measuring the developer's PATH, not the tool.
			run, err := exec.Command(path, versionArgs(f.versionCmd)...).Output()
			if err != nil {
				t.Skipf("%s %q failed: %v", f.bin, f.versionCmd, err)
			}
			got := ExtractVersion(string(run))
			if got == "" {
				t.Errorf("%q printed %q, from which no version could be extracted",
					f.versionCmd, strings.TrimSpace(string(run)))
			}
			if c := CompareVersions(got, f.min); c < 0 {
				t.Errorf("%s reports %q, below the asserted floor %q", f.name, got, f.min)
			}
		})
	}
}

// The catalog's `tested` flag and this fixture are one coupled claim: a tool
// marked tested:true that fails here is lying. This check is what enforces it
// once the catalog exists (bead 0ls); until then it has nothing to inspect.
func TestTestedToolsPassLayer1(t *testing.T) {
	if testing.Short() {
		t.Skip("layer 1 fixtures exercise the real machine; skipped under -short")
	}
	root := filepath.Join("..", "..", "tools")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skip("no tools/ directory yet; the catalog is bead 0ls")
	}
	c, err := catalog.Load(root)
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	for _, tool := range c.Tested() {
		t.Run(tool.Name, func(t *testing.T) {
			// A tool the machine does not have says nothing about whether the
			// tool works, and `tested: true` records where it WAS proven, not
			// that every machine carries it. A tool AES installs on demand —
			// supabase, wrangler — is absent on a machine that never asked for
			// it, and failing here would mean the flag could never be set for
			// anything the developer had not already installed by hand.
			//
			// So the same rule as TestLayer1VerifyFixtures applies: absent is
			// skipped, and a tool that IS here must genuinely verify.
			if !presentOnThisMachine(tool) {
				t.Skipf("%s is not installed on this machine", tool.Name)
			}
			got := Verify(tool)
			// Unknown is the honest failure here: a tested:true tool whose
			// version cannot be read has not had its minimum verified, which is
			// the whole claim the flag makes.
			if got.Status != StatusOK {
				t.Errorf("%s is marked tested:true but verifies as %q (version %q) — flip it to tested:false",
					tool.Name, got.Status, got.Version)
			}
		})
	}
}
