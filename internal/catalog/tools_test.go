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
	// The Layer 1 fixture binaries, which is the set proven to verify.
	layer1 := map[string]bool{
		"ripgrep": true, "git": true, "fzf": true, "jq": true,
		"tmux": true, "zoxide": true, "gh": true, "claude": true,
	}
	c := loadTools(t)
	var names []string
	for _, tool := range c.Tested() {
		if !layer1[tool.Name] {
			names = append(names, tool.Name)
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		t.Errorf("tools marked tested:true but not covered by the Layer 1 fixtures: %s",
			strings.Join(names, ", "))
	}
}
