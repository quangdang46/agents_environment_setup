package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// writeTool lays down a tool.yaml at <root>/<category>/<name>/tool.yaml. The
// body is the smallest thing manifest.Load accepts.
func writeTool(t *testing.T, root, category, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, category, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, manifestName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// validTool builds a manifest that passes validation, with optional overrides
// spliced in before the install block.
func validTool(name, description string, extra string) string {
	return "name: " + name + "\n" +
		"description: " + description + "\n" +
		extra +
		"install:\n" +
		"  darwin:\n" +
		"    strategy: package\n" +
		"    manager: brew\n" +
		"    package: " + name + "\n" +
		"verify:\n" +
		"  command: " + name + "\n"
}

func TestLoadIndexesEveryTool(t *testing.T) {
	root := t.TempDir()
	writeTool(t, root, "search", "ripgrep", validTool("ripgrep", "fast grep", "category: search\ndefault: true\ntested: true\ntags:\n  - search\n  - fast\n"))
	writeTool(t, root, "search", "fzf", validTool("fzf", "fuzzy finder", "category: search\ntested: true\ntags:\n  - search\n"))
	writeTool(t, root, "git", "git", validTool("git", "version control", "category: git\ndefault: true\n"))

	c, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Len() != 3 {
		t.Fatalf("Len = %d, want 3", c.Len())
	}
	for _, name := range []string{"ripgrep", "fzf", "git"} {
		if _, ok := c.ByName(name); !ok {
			t.Errorf("%s missing from byName", name)
		}
	}
}

func TestLoadEmptyDirIsNotAnError(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load on an empty dir: %v", err)
	}
	if c.Len() != 0 {
		t.Errorf("Len = %d, want 0", c.Len())
	}
	if len(c.All()) != 0 || len(c.Default()) != 0 || len(c.Names()) != 0 {
		t.Error("empty catalog returned non-empty indexes")
	}
}

// A root that is not there is a wrong path, not an empty catalog. Returning an
// empty one would silently drop every tool.
func TestLoadMissingRootIsAnError(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("Load on a missing root returned no error")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error %q does not name the path", err)
	}
}

// A broken manifest must fail the whole load and name the file. Skipping it
// would produce a setup run that quietly omits a tool.
func TestLoadInvalidToolIsAnErrorNamingTheFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"missing description", "name: broken\ninstall:\n  darwin:\n    strategy: package\n    manager: brew\n    package: x\nverify:\n  command: broken\n"},
		{"unknown field typo", validTool("typo", "has a typo", "catgory: search\n")},
		{"github-release without sha256", "name: gh\ndescription: d\ninstall:\n  darwin:\n    strategy: github-release\n    repository: o/r\n    asset:\n      amd64: a.tgz\nverify:\n  command: gh\n"},
		{"nothing to check", "name: empty\ndescription: d\ninstall:\n  darwin:\n    strategy: package\n    manager: brew\n    package: x\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTool(t, root, "search", "ripgrep", validTool("ripgrep", "fine", "category: search\n"))
			path := writeTool(t, root, "util", "broken", tc.body)

			c, err := Load(root)
			if err == nil {
				t.Fatal("Load with an invalid tool returned no error")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q does not name the file %s", err, path)
			}
			// All-or-nothing: no partial catalog.
			if c != nil {
				t.Error("Load returned a partial catalog alongside an error")
			}
		})
	}
}

func TestLoadDuplicateNameIsAnError(t *testing.T) {
	root := t.TempDir()
	first := writeTool(t, root, "search", "ripgrep-a", validTool("ripgrep", "first", "category: search\n"))
	second := writeTool(t, root, "util", "ripgrep-b", validTool("ripgrep", "second", "category: util\n"))

	_, err := Load(root)
	if err == nil {
		t.Fatal("Load with a duplicate tool name returned no error")
	}
	if !errors.Is(err, ErrDuplicateTool) {
		t.Errorf("error = %v, want ErrDuplicateTool", err)
	}
	// Both paths must appear, or the user cannot find the offending pair.
	if !strings.Contains(err.Error(), first) || !strings.Contains(err.Error(), second) {
		t.Errorf("error %q does not name both files (%s, %s)", err, first, second)
	}
}

func TestCategoryAndTagIndexes(t *testing.T) {
	root := t.TempDir()
	writeTool(t, root, "search", "ripgrep", validTool("ripgrep", "fast grep", "category: search\ntags:\n  - fast\n  - text\n"))
	writeTool(t, root, "search", "fzf", validTool("fzf", "fuzzy finder", "category: search\ntags:\n  - text\n"))
	writeTool(t, root, "git", "git", validTool("git", "vcs", "category: git\n"))

	c, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := names(c.ByCategory("search")); got != "fzf,ripgrep" {
		t.Errorf("ByCategory(search) = %s, want fzf,ripgrep", got)
	}
	if got := names(c.ByCategory("git")); got != "git" {
		t.Errorf("ByCategory(git) = %s, want git", got)
	}
	if got := names(c.ByCategory("nonexistent")); got != "" {
		t.Errorf("ByCategory(missing) = %s, want empty", got)
	}
	if got := names(c.ByTag("text")); got != "fzf,ripgrep" {
		t.Errorf("ByTag(text) = %s, want fzf,ripgrep", got)
	}
	if got := names(c.ByTag("fast")); got != "ripgrep" {
		t.Errorf("ByTag(fast) = %s, want ripgrep", got)
	}
	if got := strings.Join(c.Categories(), ","); got != "git,search" {
		t.Errorf("Categories = %s, want git,search", got)
	}
	if got := strings.Join(c.Tags(), ","); got != "fast,text" {
		t.Errorf("Tags = %s, want fast,text", got)
	}
}

func TestDefaultAndTestedSelections(t *testing.T) {
	root := t.TempDir()
	writeTool(t, root, "search", "ripgrep", validTool("ripgrep", "fast grep", "category: search\ndefault: true\ntested: true\n"))
	writeTool(t, root, "search", "ag", validTool("ag", "silver searcher", "category: search\ndefault: true\n"))
	writeTool(t, root, "util", "jq", validTool("jq", "json", "category: util\ntested: true\n"))

	c, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := names(c.Default()); got != "ag,ripgrep" {
		t.Errorf("Default = %s, want ag,ripgrep", got)
	}
	if got := names(c.Tested()); got != "jq,ripgrep" {
		t.Errorf("Tested = %s, want jq,ripgrep", got)
	}
}

// Identical input must yield identical ordering. A catalog whose iteration
// order varied would make the resolver's Actions nondeterministic (I8).
func TestLoadIsDeterministic(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"zoxide", "ripgrep", "claude", "jq", "fzf", "git", "tmux"} {
		writeTool(t, root, "util", n, validTool(n, "tool "+n, "category: util\ntags:\n  - shared\n"))
	}

	first, err := Load(root)
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	for range 5 {
		again, err := Load(root)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if strings.Join(again.Names(), ",") != strings.Join(first.Names(), ",") {
			t.Fatalf("Names differ between loads: %v vs %v", again.Names(), first.Names())
		}
		if strings.Join(again.Tags(), ",") != strings.Join(first.Tags(), ",") {
			t.Fatalf("Tags differ between loads: %v vs %v", again.Tags(), first.Tags())
		}
		if names(again.ByCategory("util")) != names(first.ByCategory("util")) {
			t.Fatalf("byCategory order differs: %v vs %v",
				names(again.ByCategory("util")), names(first.ByCategory("util")))
		}
	}

	want := "claude,fzf,git,jq,ripgrep,tmux,zoxide"
	if got := strings.Join(first.Names(), ","); got != want {
		t.Errorf("Names = %s, want %s", got, want)
	}
}

// Category is a field, not a directory. A tool filed under util/ but declaring
// category: search must be indexed as search.
func TestCategoryComesFromTheFieldNotThePath(t *testing.T) {
	root := t.TempDir()
	writeTool(t, root, "misfiled-dir", "oddball", validTool("oddball", "odd", "category: search\n"))

	c, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := names(c.ByCategory("search")); got != "oddball" {
		t.Errorf("ByCategory(search) = %s, want oddball", got)
	}
	if got := len(c.ByCategory("misfiled-dir")); got != 0 {
		t.Errorf("category was inferred from the directory: %d entries", got)
	}
}

func TestNamesReturnsACopy(t *testing.T) {
	root := t.TempDir()
	writeTool(t, root, "util", "jq", validTool("jq", "json", "category: util\n"))
	c, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	names := c.Names()
	names[0] = "mutated"
	if c.Names()[0] != "jq" {
		t.Error("mutating the returned slice changed the catalog")
	}
}

func TestStringListsEveryTool(t *testing.T) {
	root := t.TempDir()
	writeTool(t, root, "util", "jq", validTool("jq", "json", "category: util\ndefault: true\ntested: true\n"))
	c, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.String(); !strings.Contains(got, "jq") || !strings.Contains(got, "1 tools") {
		t.Errorf("String() = %q", got)
	}
}

// names renders a tool slice for comparison against an expected order.
func names(tools []*manifest.Tool) string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return strings.Join(out, ",")
}
