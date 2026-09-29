package catalog_test

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/resolver"
)

// toolsRoot is the catalog the repo ships. These tests exist so a typo in a
// tool.yaml fails CI rather than someone's `aes setup`.
func toolsRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "tools")
}

// loadTools fails the test if any manifest is invalid, naming every offender
// rather than only the first. One broken file should not hide the other nine.
func loadTools(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load(toolsRoot(t))
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	return c
}

func TestEveryToolManifestIsValid(t *testing.T) {
	// catalog.Load is already all-or-nothing, so reaching here means every
	// file parsed and validated. The separate assertion is that the tree is
	// not simply empty, which would make the rest of this file vacuous.
	c := loadTools(t)
	if c.Len() == 0 {
		t.Fatal("tools/ contains no manifests; the catalog is empty")
	}
	t.Logf("catalog holds %d tools", c.Len())
}

// The bead's hard requirements, re-asserted against the shipped catalog rather
// than a fixture: these only mean something on the real files.
func TestCatalogIntegrity(t *testing.T) {
	c := loadTools(t)

	t.Run("every dependency exists", func(t *testing.T) {
		for _, tool := range c.All() {
			for _, dep := range tool.Dependencies {
				if _, ok := c.ByName(dep); !ok {
					t.Errorf("%s depends on %q, which is not in the catalog", tool.Name, dep)
				}
			}
		}
	})

	t.Run("no dependency cycles", func(t *testing.T) {
		// TopoSort is the same code the resolver uses, so a cycle here is a
		// cycle there. The whole catalog is walked at once rather than
		// per-tool, because a cycle can span any number of manifests.
		graph := make(map[string][]string, c.Len())
		for _, tool := range c.All() {
			graph[tool.Name] = tool.Dependencies
		}
		order, err := resolver.TopoSort(graph)
		if err != nil {
			t.Fatalf("catalog has a dependency cycle: %v", err)
		}
		if len(order) != c.Len() {
			t.Errorf("topological order covers %d tools, catalog has %d", len(order), c.Len())
		}
	})

	t.Run("every tool declares both platforms or says why", func(t *testing.T) {
		for _, tool := range c.All() {
			for _, goos := range []string{"darwin", "linux"} {
				if _, ok := tool.Install[goos]; !ok {
					t.Errorf("%s has no %s install target", tool.Name, goos)
				}
			}
		}
	})

	t.Run("every tool declares a verify", func(t *testing.T) {
		for _, tool := range c.All() {
			if tool.Verify == nil && len(tool.Provides) == 0 {
				t.Errorf("%s has no verify block and no provides", tool.Name)
			}
		}
	})

	t.Run("github-release targets carry a checksum for every asset", func(t *testing.T) {
		for _, tool := range c.All() {
			for goos, target := range tool.Install {
				if target.Strategy != manifest.StrategyGithubRelease {
					continue
				}
				for arch := range target.Asset {
					if target.SHA256[arch] == "" {
						t.Errorf("%s install.%s: asset %q has no sha256", tool.Name, goos, arch)
					}
				}
				for arch, sum := range target.SHA256 {
					if sum == "" {
						t.Errorf("%s install.%s: sha256 for %q is empty", tool.Name, goos, arch)
					}
					if len(sum) != 64 || strings.Trim(sum, "0123456789abcdef") != "" {
						t.Errorf("%s install.%s: sha256[%s] = %q is not 64 lowercase hex chars",
							tool.Name, goos, arch, sum)
					}
				}
			}
		}
	})

	t.Run("no tool asserts a privilege field", func(t *testing.T) {
		// Privilege is derived from (Strategy, Manager). A catalog that encoded
		// it would be asserting something the schema deliberately cannot say.
		for _, tool := range c.All() {
			for goos, target := range tool.Install {
				if target.Strategy == manifest.StrategyPackage && target.Manager != "" &&
					target.Manager != manifest.ManagerBrew && target.Manager != manifest.ManagerApt {
					t.Errorf("%s install.%s: manager %q is neither brew nor apt",
						tool.Name, goos, target.Manager)
				}
			}
		}
	})

	t.Run("categories are drawn from the documented set", func(t *testing.T) {
		allowed := map[string]bool{
			"shell": true, "search": true, "terminal": true, "git": true,
			"runtime": true, "ai": true, "agent": true, "infra": true, "utility": true,
		}
		for _, tool := range c.All() {
			if tool.Category == "" {
				t.Errorf("%s declares no category", tool.Name)
				continue
			}
			if !allowed[tool.Category] {
				t.Errorf("%s has category %q, outside the documented set", tool.Name, tool.Category)
			}
		}
	})
}

// The catalog's `tested` flag is the North Star's claim that the default
// profile is *verified*. A tool claiming tested:true must at minimum be one
// the Layer 1 fixtures actually exercise, or the flag is a promise about
// nothing.
func TestTestedToolsAreOnTheLayer1List(t *testing.T) {
	c := loadTools(t)
	var names []string
	for _, tool := range c.Tested() {
		if !layer1Tools[tool.Name] {
			names = append(names, tool.Name)
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		t.Errorf("tools marked tested:true but not covered by the Layer 1 fixtures: %s",
			strings.Join(names, ", "))
	}
}

// layer1Tools is the eight-tool set the spec's Layer 1 fixture table names:
// the tools already present on a developer machine, which verify rather than
// install. It lives here, in the catalog, rather than only in the verifier's
// fixture table, because the two lists can drift apart and nothing compares
// them.
var layer1Tools = map[string]bool{
	"ripgrep": true, "git": true, "fzf": true, "jq": true,
	"tmux": true, "zoxide": true, "gh": true, "claude": true,
	// Proven in both layers on linux/amd64 (2026-09-27, bead gj9). This list is
	// the mirror of the verifier's fixture table; the test below exists to keep
	// the two from drifting apart, so update them together.
	"bat": true, "btop": true, "dust": true,
	"eza": true, "codex": true, "opencode": true,
	"bottom": true, "zellij": true, "ntm": true,
	// node and npm are the runtimes every npm-strategy tool depends on, so
	// they are verified on the same terms as the tools that need them. The
	// cloud CLIs are here for the same reason: Layer 1 skips a tool the
	// machine does not have, and runs it wherever npm put it.
	"node": true, "npm": true,
	"vercel": true, "supabase": true, "wrangler": true,
	// beads, dcg and gemini ship prebuilt binaries rather than building from
	// source, so they are verified on the same terms as the other release
	// tools. gemini is the one AI tool that runs on node 18.
	"beads": true, "dcg": true, "gemini": true,
	// The binary names are the ones the archives actually ship: `pi` for
	// pi-agent-rust and `ft` for wezterm-automata (which renamed itself from
	// wezterm_automata/wa to frankenterm/ft at v0.12.0). Declaring a name
	// here that is not in the archive makes the fixture fail on a tool that
	// installs correctly.
	"antigravity": true, "pi-agent-rust": true, "wezterm-automata": true,
	// The rest of the catalog, promoted after the full-catalog run proved both
	// layers on each. Layer 1 skips a tool the machine does not have, so these
	// are verified wherever they happen to be installed; their Layer 2 evidence
	// is a fresh container, recorded in each manifest.
	"agent-mail": true, "ast-grep": true, "atuin": true, "bash": true,
	"beads-viewer": true, "caam": true, "direnv": true, "docker": true,
	"fd": true, "git-lfs": true, "httpie": true, "hyperfine": true,
	"lazydocker": true, "lazygit": true, "ncdu": true, "pi": true,
	"python": true, "rust": true, "toon": true, "tree": true, "uv": true,
	"zsh": true,
}

// The other direction, and the one that actually caught a bug: every tool the
// Layer 1 fixtures exercise must be installable from this catalog.
//
// An alias must not shadow a system binary. `alias cc=...` on a machine where
// /usr/bin/cc is the C compiler means typing `cc` to compile runs an agent
// instead — measured on 2026-09-28, when aes shipped exactly that and the
// user's fresh shell stopped being able to compile C. ACFS ships the same
// collision, and their zshrc carries a defensive `unalias br` guard for
// their own earlier `alias br='bun run dev'` shadowing the real binary.
//
// The check cannot look at the developer's PATH: a test that reads runtime
// PATH passes on the machine that wrote it and fails on everyone else's. The
// allowlist below is the POSIX-required commands plus the C toolchain, which
// is the set of names that exist on every Unix rather than the set this
// machine happens to have.
var systemBinaryNames = map[string]bool{
	// POSIX-required commands (IEEE 1003.1): an alias with one of these
	// names shadows something present on every conforming system.
	"cat": true, "cp": true, "mv": true, "rm": true, "ls": true,
	"echo": true, "sh": true, "test": true, "kill": true, "sleep": true,
	"sort": true, "grep": true, "awk": true, "sed": true, "tar": true,
	"vi": true, "ed": true, "find": true, "make": true, "man": true,
	// The C toolchain lives in /usr/bin on every Unix with a compiler, and
	// none of these is an interactive alias anyone wants: `cc` compiles C.
	"cc": true, "c89": true, "c99": true, "cpp": true, "ld": true,
	"gcc": true, "g++": true, "as": true, "ar": true, "nm": true,
}

func TestAliasMustNotShadowASystemBinary(t *testing.T) {
	c := loadTools(t)
	for _, tool := range c.All() {
		for name := range tool.Aliases {
			if systemBinaryNames[name] {
				t.Errorf("%s declares alias %q, which shadows a system binary; "+
					"a shortcut that hides /usr/bin/%s is the failure aes doctor "+
					"exists to report, not something a manifest should introduce",
					tool.Name, name, name)
			}
		}
	}
}

// A verify command must not name a directory any local user can write to.
//
// Four manifests carried `HOME="${HOME:-/tmp/aes-<tool>-home}" <tool> --version`
// as a workaround for a tool that aborts without a HOME, and it was a
// code-execution path, not an untidy probe. /tmp is 1777; `ru` runs
// `source "${TOON_SH_PATH:-$HOME/.local/lib/toon.sh}"` at load, before argument
// dispatch; so any local user could pre-create that file and have it execute on
// every `aes verify`, `aes list` and `aes setup` — while ru still printed its
// real version line, so nothing looked wrong. Reproduced 2026-09-30.
//
// The real fix is in the verifier, which now carries HOME into the probe
// environment so no manifest needs the workaround. This test is the second
// net: it cannot tell you why a probe works, but it will tell you the moment
// one starts steering itself from a world-writable path again.
func TestVerifyCommandMustNotNameAWorldWritableDir(t *testing.T) {
	c := loadTools(t)
	for _, tool := range c.All() {
		if tool.Verify == nil {
			continue
		}
		probes := []string{}
		if tool.Verify.Version != nil {
			probes = append(probes, tool.Verify.Version.Command)
		}
		for _, probe := range probes {
			for _, bad := range []string{"/tmp/", "/var/tmp/", "/dev/shm/"} {
				if strings.Contains(probe, bad) {
					t.Errorf("%s probes with %q, which names %s — a directory any "+
						"local user can write to. A tool that reads from $HOME at "+
						"load time will execute whatever they left there.",
						tool.Name, probe, strings.TrimSuffix(bad, "/"))
				}
			}
		}
	}
}

// The verifier builds its fixtures from the spec's table, and it passed
// happily while zoxide had no manifest at all. A fixture suite and the thing
// it fixtures are separate artifacts; only a comparison between them catches
// the drift, and the gap surfaced in production when a shipped profile
// referenced zoxide and `aes setup` refused to load.
//
// Any list a test enumerates needs a second test asserting the system under
// test still contains it.
func TestCatalogCoversTheLayer1FixtureSet(t *testing.T) {
	c := loadTools(t)
	var missing []string
	for name := range layer1Tools {
		if _, ok := c.ByName(name); !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the Layer 1 fixtures exercise %s, but the catalog has no manifest for them; "+
			"a fixture for a tool aes cannot install is a test of nothing",
			strings.Join(missing, ", "))
	}
}
