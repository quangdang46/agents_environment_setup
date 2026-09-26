package plan

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
)

func fixture(t *testing.T) *catalog.Catalog {
	t.Helper()
	root := t.TempDir()
	for name, cat := range map[string]string{"a": "utility", "b": "utility", "c": "utility"} {
		dir := filepath.Join(root, cat, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		content := "name: " + name + "\ndescription: f\ncategory: " + cat + "\nprovides: [" + name + "]\n" +
			"install:\n  darwin:\n    strategy: go\n    go_package: example.com/" + name + "@v1.0.0\n" +
			"  linux:\n    strategy: go\n    go_package: example.com/" + name + "@v1.0.0\n"
		if err := os.WriteFile(filepath.Join(dir, "tool.yaml"), []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	c, err := catalog.Load(root)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	return c
}

func host() *platform.Host {
	return &platform.Host{OS: platform.OSDarwin, Arch: platform.ArchARM64}
}

// Selection order must not matter. This is the property that lets two
// frontends agree without comparing their outputs field by field.
func TestSelectionNamesAreSortedAndDeduped(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"c", "a", "b"}, []string{"a", "b", "c"}},
		{[]string{"a", "a", "b"}, []string{"a", "b"}},
		{nil, []string{}},
		{[]string{"b"}, []string{"b"}},
	}
	for _, tc := range cases {
		got := Selection{Only: tc.in}.Names()
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Selection{%v}.Names() = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestResolveIsOrderIndependent(t *testing.T) {
	t.Parallel()

	c := fixture(t)
	h := host()

	first, err := Resolve(c, h, Selection{Only: []string{"a", "b", "c"}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	second, err := Resolve(c, h, Selection{Only: []string{"c", "a", "b"}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(first) != len(second) {
		t.Fatalf("different lengths: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Tool != second[i].Tool {
			t.Errorf("position %d differs: %q vs %q", i, first[i].Tool, second[i].Tool)
		}
	}
}

func TestResolveEmptySelection(t *testing.T) {
	t.Parallel()

	got, err := Resolve(fixture(t), host(), Selection{})
	if err != nil {
		t.Errorf("empty selection errored: %v", err)
	}
	if got != nil {
		t.Errorf("empty selection produced %v", got)
	}
}

// A typo is an error, not an empty plan — the same reasoning as a dangling
// profile reference.
func TestResolveRejectsUnknownTool(t *testing.T) {
	t.Parallel()

	_, err := Resolve(fixture(t), host(), Selection{Only: []string{"nope"}})
	if err == nil {
		t.Fatal("expected an error for a tool not in the catalog")
	}
}

func TestResolveRejectsMissingInputs(t *testing.T) {
	t.Parallel()

	c := fixture(t)
	if _, err := Resolve(nil, host(), Selection{Only: []string{"a"}}); err == nil {
		t.Error("expected an error with no catalog")
	}
	if _, err := Resolve(c, nil, Selection{Only: []string{"a"}}); err == nil {
		t.Error("expected an error with no host")
	}
}

func TestStringRendersReadably(t *testing.T) {
	t.Parallel()

	if got := (Selection{}).String(); got != "(nothing selected)" {
		t.Errorf("empty Selection.String() = %q", got)
	}
	s := Selection{Only: []string{"c", "a"}}
	if got := s.String(); got != "a, c" {
		t.Errorf("Selection.String() = %q, want %q", got, "a, c")
	}
}
