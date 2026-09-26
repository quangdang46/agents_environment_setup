package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// writeTool lays down a minimal valid tool.yaml. tested controls the field
// invariant I15 turns on, and category/tag drive the selector tests.
func writeTool(t *testing.T, root, name, category string, tested bool, tags ...string) {
	t.Helper()
	dir := filepath.Join(root, category, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	tagLine := ""
	if len(tags) > 0 {
		quoted := make([]string, len(tags))
		for i, tag := range tags {
			quoted[i] = tag
		}
		tagLine = "tags: [" + strings.Join(quoted, ", ") + "]\n"
	}
	content := "name: " + name + "\n" +
		"description: fixture tool " + name + "\n" +
		"category: " + category + "\n" +
		"provides: [" + name + "]\n" +
		"tested: " + boolStr(tested) + "\n" +
		tagLine +
		"install:\n  linux:\n    strategy: package\n    manager: apt\n    package: " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "tool.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write tool.yaml: %v", err)
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// fixtureCatalog builds a catalog with tested and untested tools across two
// categories, so the require_tested gate and the selector paths can both be
// exercised.
func fixtureCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	root := t.TempDir()
	writeTool(t, root, "git", "git", true, "vcs")
	writeTool(t, root, "ripgrep", "search", true, "search", "text")
	writeTool(t, root, "jq", "utility", true)
	writeTool(t, root, "tmux", "terminal", true, "shell")
	// Never actually run: this is the tool that must be refused.
	writeTool(t, root, "risky", "utility", false)

	c, err := catalog.Load(root)
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	return c
}

func names(tools []*manifest.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name)
	}
	return out
}

func TestResolveIncludeByName(t *testing.T) {
	t.Parallel()

	c := fixtureCatalog(t)
	p := &Profile{Name: "p", Include: []string{"git", "jq"}}

	got, err := p.Resolve(c)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := []string{"git", "jq"}; !equalStrings(names(got), want) {
		t.Errorf("resolved %v, want %v", names(got), want)
	}
}

func TestResolveSelectorMatchesCategoryAndTag(t *testing.T) {
	t.Parallel()

	c := fixtureCatalog(t)

	t.Run("by category", func(t *testing.T) {
		t.Parallel()
		p := &Profile{Name: "p", Tags: []string{"terminal"}}
		got, err := p.Resolve(c)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if want := []string{"tmux"}; !equalStrings(names(got), want) {
			t.Errorf("resolved %v, want %v", names(got), want)
		}
	})

	t.Run("by tag", func(t *testing.T) {
		t.Parallel()
		// "vcs" is a tag, not a category.
		p := &Profile{Name: "p", Tags: []string{"vcs"}}
		got, err := p.Resolve(c)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if want := []string{"git"}; !equalStrings(names(got), want) {
			t.Errorf("resolved %v, want %v", names(got), want)
		}
	})
}

// TestUnknownReferenceIsAnError is the rule the package exists for: a typo
// that resolves to nothing looks exactly like a successful setup.
func TestUnknownReferenceIsAnError(t *testing.T) {
	t.Parallel()

	c := fixtureCatalog(t)

	t.Run("unknown include name", func(t *testing.T) {
		t.Parallel()
		p := &Profile{Name: "p", Include: []string{"gitt"}}
		_, err := p.Resolve(c)
		if err == nil {
			t.Fatal("expected an error for an unknown tool name")
		}
		if !strings.Contains(err.Error(), "gitt") {
			t.Errorf("error %q does not name the missing tool", err)
		}
	})

	t.Run("selector matching nothing", func(t *testing.T) {
		t.Parallel()
		p := &Profile{Name: "p", Tags: []string{"nonexistent"}}
		_, err := p.Resolve(c)
		if err == nil {
			t.Fatal("expected an error for a selector matching nothing")
		}
		if !strings.Contains(err.Error(), "nonexistent") {
			t.Errorf("error %q does not name the missing selector", err)
		}
	})
}

// TestRequireTestedNamesTheTool is invariant I15. The error must name the
// offending tool: "profile is invalid" would not tell a user what to remove.
func TestRequireTestedNamesTheTool(t *testing.T) {
	t.Parallel()

	c := fixtureCatalog(t)

	t.Run("untested tool is refused by name", func(t *testing.T) {
		t.Parallel()
		p := &Profile{Name: "strict", Include: []string{"git", "risky"}, RequireTested: true}
		_, err := p.Resolve(c)
		if err == nil {
			t.Fatal("expected an error for an untested tool")
		}
		if !strings.Contains(err.Error(), "risky") {
			t.Errorf("error %q does not name the untested tool", err)
		}
	})

	t.Run("all-tested profile resolves", func(t *testing.T) {
		t.Parallel()
		p := &Profile{Name: "strict", Include: []string{"git", "jq"}, RequireTested: true}
		got, err := p.Resolve(c)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("resolved %v, want 2 tools", names(got))
		}
	})

	t.Run("untested tools are fine without the gate", func(t *testing.T) {
		t.Parallel()
		p := &Profile{Name: "loose", Include: []string{"git", "risky"}}
		got, err := p.Resolve(c)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if want := []string{"git", "risky"}; !equalStrings(names(got), want) {
			t.Errorf("resolved %v, want %v", names(got), want)
		}
	})

	t.Run("every offender is reported, not just the first", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeTool(t, root, "bad-one", "utility", false)
		writeTool(t, root, "bad-two", "utility", false)
		c, err := catalog.Load(root)
		if err != nil {
			t.Fatalf("catalog.Load: %v", err)
		}
		p := &Profile{Name: "strict", Include: []string{"bad-one", "bad-two"}, RequireTested: true}
		_, err = p.Resolve(c)
		if err == nil {
			t.Fatal("expected an error")
		}
		// A user fixing a profile wants the whole list in one pass.
		for _, want := range []string{"bad-one", "bad-two"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}
	})
}

func TestResolveIsDeduplicatedAndSorted(t *testing.T) {
	t.Parallel()

	c := fixtureCatalog(t)
	// git is reachable by name and again through the "git" category, and
	// ripgrep carries a "search" tag that also matches its category.
	p := &Profile{Name: "p", Include: []string{"git"}, Tags: []string{"git", "search"}}

	got, err := p.Resolve(c)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := []string{"git", "ripgrep"}; !equalStrings(names(got), want) {
		t.Errorf("resolved %v, want %v (each tool exactly once, sorted)", names(got), want)
	}
}

func TestEmptyProfileResolvesToCatalogDefaults(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeToolWithDefault(t, root, "chosen", "search", true, true)
	writeToolWithDefault(t, root, "not-chosen", "search", true, false)
	c, err := catalog.Load(root)
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}

	p := &Profile{Name: "empty"}
	got, err := p.Resolve(c)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := []string{"chosen"}; !equalStrings(names(got), want) {
		t.Errorf("resolved %v, want %v", names(got), want)
	}
}

func writeToolWithDefault(t *testing.T, root, name, category string, tested, def bool) {
	t.Helper()
	dir := filepath.Join(root, category, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "name: " + name + "\ndescription: fixture\ncategory: " + category +
		"\ndefault: " + boolStr(def) + "\ntested: " + boolStr(tested) +
		"\nprovides: [" + name + "]\ninstall:\n  linux:\n    strategy: package\n    manager: apt\n    package: " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "tool.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestParseRejects(t *testing.T) {
	t.Parallel()

	t.Run("missing name", func(t *testing.T) {
		t.Parallel()
		if _, err := Parse([]byte("description: no name\n")); err == nil {
			t.Fatal("expected an error for a profile with no name")
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		t.Parallel()
		// A typo must not silently select nothing.
		_, err := Parse([]byte("name: p\nrequre_tested: true\n"))
		if err == nil {
			t.Fatal("expected an error for a typo'd field")
		}
		if !strings.Contains(err.Error(), "requre_tested") {
			t.Errorf("error %q does not name the offending field", err)
		}
	})
}

func TestSetGet(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{"minimal", "developer", "ai", "full", "default"} {
		body := "name: " + name + "\ndescription: fixture profile\ninclude: [git]\nrequire_tested: true\n"
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	set, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if set.Len() != 5 {
		t.Errorf("loaded %d profiles, want 5", set.Len())
	}
	if _, err := set.Get("ai"); err != nil {
		t.Errorf("Get(ai): %v", err)
	}
}

// TestUnknownProfileNamesTheValidSet makes a typo fixable without listing
// the directory.
func TestUnknownProfileNamesTheValidSet(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ai.yaml"), []byte("name: ai\ndescription: x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	set, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}

	_, err = set.Get("a1")
	if err == nil {
		t.Fatal("expected an error for an unknown profile")
	}
	for _, want := range []string{"a1", "ai"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestMissingProfileDirIsAnError: a missing directory is not an empty set.
func TestMissingProfileDirIsAnError(t *testing.T) {
	t.Parallel()

	if _, err := LoadDir(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected an error for a missing profiles directory")
	}
}

// TestShippedProfilesLoad guards the files in profiles/ against a syntax or
// schema error. They reference tools that bead 0ls has not created yet, so
// only loading is asserted here, not resolution.
func TestShippedProfilesLoad(t *testing.T) {
	t.Parallel()

	set, err := LoadDir(filepath.Join("..", "..", "profiles"))
	if err != nil {
		t.Fatalf("LoadDir(profiles): %v", err)
	}
	for _, want := range []string{"minimal", "developer", "ai", "full", "default"} {
		p, err := set.Get(want)
		if err != nil {
			t.Errorf("Get(%q): %v", want, err)
			continue
		}
		if !p.RequireTested {
			t.Errorf("profile %q does not set require_tested; the default profile must only ship verified tools", want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
