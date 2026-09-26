// Package catalog loads the tool manifests and indexes them for the resolver.
//
// Loading is all-or-nothing. One invalid tool.yaml fails the whole load, naming
// the file, because the alternative is worse: a manifest that fails validation
// and is silently skipped produces a setup run that quietly omits a tool the
// user expected to be there, with no error anywhere to explain why. A loud
// failure at load time is the cheaper bug.
//
// The on-disk layout is tools/<category>/<name>/tool.yaml, but the directory is
// a convenience for humans. Category is a field in the YAML and is the only
// source of truth — never infer a tool's category from the path it was found at.
package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// manifestName is the file a tool definition must be called.
const manifestName = "tool.yaml"

// ErrDuplicateTool reports two manifests claiming the same tool name. It is a
// distinct error because "last one wins" would make the catalog depend on
// directory walk order, which is exactly the nondeterminism this package exists
// to prevent.
var ErrDuplicateTool = errors.New("duplicate tool name")

// Catalog is an immutable snapshot of the tool manifests under a root. It is
// safe for concurrent reads.
type Catalog struct {
	byName     map[string]*manifest.Tool
	byCategory map[string][]*manifest.Tool
	byTag      map[string][]*manifest.Tool
	// names is the sorted key set, so every iteration is deterministic and no
	// caller has to sort after us.
	names []string
}

// Load reads every tool.yaml under root.
//
// A root that exists but holds no manifests is an empty catalog, not an error —
// a first run legitimately has nothing to install. A root that does not exist
// at all is an error, because that is nearly always a wrong path, and quietly
// returning an empty catalog there is the exact silent-omission failure this
// package guards against.
func Load(root string) (*Catalog, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("catalog root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("catalog root %s is not a directory", root)
	}

	paths, err := manifestPaths(root)
	if err != nil {
		return nil, err
	}

	// Load into a slice first, then sort, then index. Building indexes by
	// appending during a directory walk would bake walk order into every
	// byCategory and byTag slice.
	tools := make([]*manifest.Tool, 0, len(paths))
	seen := make(map[string]string, len(paths)) // tool name -> file it came from
	for _, path := range paths {
		tool, err := manifest.Load(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if prev, dup := seen[tool.Name]; dup {
			return nil, fmt.Errorf("%w: %q declared in both %s and %s",
				ErrDuplicateTool, tool.Name, prev, path)
		}
		seen[tool.Name] = path
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	return newCatalog(tools), nil
}

// newCatalog indexes an already name-sorted tool slice.
func newCatalog(tools []*manifest.Tool) *Catalog {
	c := &Catalog{
		byName:     make(map[string]*manifest.Tool, len(tools)),
		byCategory: make(map[string][]*manifest.Tool),
		byTag:      make(map[string][]*manifest.Tool),
		names:      make([]string, 0, len(tools)),
	}
	for _, t := range tools {
		c.byName[t.Name] = t
		c.names = append(c.names, t.Name)
		if t.Category != "" {
			c.byCategory[t.Category] = append(c.byCategory[t.Category], t)
		}
		for _, tag := range t.Tags {
			c.byTag[tag] = append(c.byTag[tag], t)
		}
	}
	return c
}

// manifestPaths finds every tool.yaml under root, in lexical order.
func manifestPaths(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// A directory that cannot be read is a problem, not an empty category.
		if d.IsDir() {
			return nil
		}
		if d.Name() == manifestName {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk catalog: %w", err)
	}
	// WalkDir is already lexical; sorting makes the guarantee explicit rather
	// than inherited, because deterministic output is a contract here (I8).
	sort.Strings(paths)
	return paths, nil
}

// ByName looks up one tool.
func (c *Catalog) ByName(name string) (*manifest.Tool, bool) {
	t, ok := c.byName[name]
	return t, ok
}

// ByCategory returns every tool in a category, sorted by name.
func (c *Catalog) ByCategory(category string) []*manifest.Tool {
	return c.byCategory[category]
}

// ByTag returns every tool carrying a tag, sorted by name.
func (c *Catalog) ByTag(tag string) []*manifest.Tool {
	return c.byTag[tag]
}

// Categories lists every category present, sorted.
func (c *Catalog) Categories() []string {
	out := make([]string, 0, len(c.byCategory))
	for name := range c.byCategory {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Tags lists every tag present, sorted.
func (c *Catalog) Tags() []string {
	out := make([]string, 0, len(c.byTag))
	for name := range c.byTag {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Default returns every tool marked default: true, sorted by name. This is
// what an empty profile resolves to.
func (c *Catalog) Default() []*manifest.Tool {
	out := make([]*manifest.Tool, 0, len(c.names))
	for _, name := range c.names {
		if t := c.byName[name]; t.Default {
			out = append(out, t)
		}
	}
	return out
}

// Tested returns every tool marked tested: true, sorted by name. The default
// profile draws from here, so an untested tool cannot reach a user by accident
// (I15).
func (c *Catalog) Tested() []*manifest.Tool {
	out := make([]*manifest.Tool, 0, len(c.names))
	for _, name := range c.names {
		if t := c.byName[name]; t.Tested {
			out = append(out, t)
		}
	}
	return out
}

// All returns every tool, sorted by name.
func (c *Catalog) All() []*manifest.Tool {
	out := make([]*manifest.Tool, 0, len(c.names))
	for _, name := range c.names {
		out = append(out, c.byName[name])
	}
	return out
}

// Names returns every tool name, sorted.
func (c *Catalog) Names() []string {
	return append([]string(nil), c.names...)
}

// Len is the number of tools loaded.
func (c *Catalog) Len() int { return len(c.names) }

// String renders the catalog for diagnostics, one tool per line.
func (c *Catalog) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "catalog: %d tools\n", len(c.names))
	for _, name := range c.names {
		t := c.byName[name]
		fmt.Fprintf(&b, "  %-20s %-10s default=%-5t tested=%t\n",
			t.Name, t.Category, t.Default, t.Tested)
	}
	return b.String()
}
