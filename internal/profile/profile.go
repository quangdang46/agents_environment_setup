// Package profile loads profile files and expands them against a catalog.
//
// A profile is a named selection of tools — the thing a user reaches for when
// they do not want the whole catalog. The load-bearing rule here is
// require_tested (invariant I15).
//
// The North Star promises a "complete, **verified** environment". A default
// profile containing tools that were never actually run makes that a lie, and
// those 25 never-executed tools are exactly where it breaks on a user's first
// machine. So a profile that declares require_tested resolves to nothing at
// all if even one selected tool is untested, and the error names that tool.
// A setup command that quietly installs something unverified is worse than
// one that refuses and explains.
//
// The other load-bearing rule is that a reference resolving to nothing is an
// error, never an empty selection. A typo'd profile that installs nothing
// looks exactly like a successful setup.
package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// Profile is one named selection of tools.
//
// Tags entries are matched against both tool tags and tool categories: the
// spec's own example lists "search" and "git", which are categories, under a
// field called tags. Matching both is the only reading that makes the field
// name and the example agree.
type Profile struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description,omitempty"`
	Include     []string `yaml:"include,omitempty"`
	Tags        []string `yaml:"tags,omitempty"`

	// RequireTested gates resolution on every selected tool being
	// tested: true. It is what keeps the default profile honest.
	RequireTested bool `yaml:"require_tested,omitempty"`
}

// Load reads and validates one profile file.
func Load(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("profile: read %s: %w", filepath.Base(path), err)
	}
	p, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return p, nil
}

// Parse validates profile bytes. Unknown fields are rejected so a typo fails
// loudly rather than silently selecting nothing.
func Parse(data []byte) (*Profile, error) {
	var p Profile
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("profile: parse: %w", err)
	}
	if p.Name == "" {
		return nil, fmt.Errorf("profile: name is required")
	}
	return &p, nil
}

// Resolve expands the profile against a catalog into a deduplicated,
// name-sorted tool set.
//
// A profile selecting nothing is not an error when it genuinely selects
// nothing, but every individual reference must resolve: an unknown name or a
// tag matching nothing is reported by name, because a silently-empty
// selection is the failure mode this package exists to prevent.
func (p *Profile) Resolve(c *catalog.Catalog) ([]*manifest.Tool, error) {
	if c == nil {
		return nil, fmt.Errorf("profile %q: no catalog", p.Name)
	}

	seen := make(map[string]*manifest.Tool)
	add := func(t *manifest.Tool) {
		// First writer wins. Selection order is already deterministic, and
		// a tool reached by two routes must appear once.
		if _, dup := seen[t.Name]; !dup {
			seen[t.Name] = t
		}
	}

	// A profile with no explicit selection means "the catalog's defaults",
	// which is what an empty profile.yaml resolves to.
	if len(p.Include) == 0 && len(p.Tags) == 0 {
		for _, t := range c.Default() {
			add(t)
		}
	}

	for _, name := range p.Include {
		t, ok := c.ByName(name)
		if !ok {
			return nil, fmt.Errorf("profile %q: no tool named %q", p.Name, name)
		}
		add(t)
	}

	for _, selector := range p.Tags {
		matched := append([]*manifest.Tool{}, c.ByCategory(selector)...)
		matched = append(matched, c.ByTag(selector)...)
		if len(matched) == 0 {
			// A selector matching nothing is reported, not ignored: the
			// user asked for a group of tools and is getting none.
			return nil, fmt.Errorf("profile %q: no tool matches tag or category %q", p.Name, selector)
		}
		for _, t := range matched {
			add(t)
		}
	}

	tools := make([]*manifest.Tool, 0, len(seen))
	for _, t := range seen {
		tools = append(tools, t)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	if err := p.checkTested(tools); err != nil {
		return nil, err
	}
	return tools, nil
}

// checkTested enforces require_tested by naming every offender.
//
// It reports all of them, not the first: a user fixing a profile wants the
// whole list in one pass, not a game of whack-a-mole.
func (p *Profile) checkTested(tools []*manifest.Tool) error {
	if !p.RequireTested {
		return nil
	}
	var untested []string
	for _, t := range tools {
		if !t.Tested {
			untested = append(untested, t.Name)
		}
	}
	if len(untested) == 0 {
		return nil
	}
	sort.Strings(untested)
	return fmt.Errorf("profile %q requires tested tools, but these are not tested: %s",
		p.Name, strings.Join(untested, ", "))
}

// Set is a directory of profiles, indexed by name.
type Set struct {
	byName map[string]*Profile
}

// LoadDir reads every *.yaml in dir.
//
// A missing directory is an error rather than an empty set: a `aes setup
// --profile default` against a directory that is not there should say so, not
// resolve to nothing.
func LoadDir(dir string) (*Set, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("profile: read %s: %w", dir, err)
	}

	set := &Set{byName: make(map[string]*Profile)}
	// Sorted so a malformed profile is reported deterministically rather
	// than in whatever order the filesystem hands back.
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		p, err := Load(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		set.byName[p.Name] = p
	}
	return set, nil
}

// Get returns a profile by name. An unknown name is an error listing the
// valid set, so a typo is fixable without listing the directory.
func (s *Set) Get(name string) (*Profile, error) {
	if p, ok := s.byName[name]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("profile: no profile named %q (available: %s)", name, strings.Join(s.Names(), ", "))
}

// Names returns the available profile names, sorted.
func (s *Set) Names() []string {
	names := make([]string, 0, len(s.byName))
	for name := range s.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Len reports how many profiles are loaded.
func (s *Set) Len() int { return len(s.byName) }
