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
	"github.com/quangdang46/agents_environment_setup/internal/platform"
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

	// host is the machine this profile is being resolved for. It is not
	// declared in YAML: the file describes an intent, and whether a tool was
	// proven on THIS machine is a fact about the run. Nil falls back to
	// reporting only the never-tested case, which is the safe direction -
	// it under-claims rather than over-claims.
	host *platform.Host `yaml:"-"`
	// allowUnproven waives the platform half of the gate, not the evidence
	// half. A tool with no install ever proven still blocks.
	allowUnproven bool
	// warnings collects what the run accepted rather than refused, so the
	// caller can print it. A warning that has to be returned as an error is
	// not a warning.
	warnings []string
}

// Warnings returns what resolution accepted, to be printed before the run.
func (p *Profile) Warnings() []string { return p.warnings }

// WithAllowUnproven returns the profile set to accept tools proven only on
// another platform. It does not weaken the flag's purpose: AES's claim is
// that the environment is VERIFIED, and this records that the user has
// accepted a tool that is verified somewhere other than here.
func (p *Profile) WithAllowUnproven(v bool) *Profile {
	p.allowUnproven = v
	return p
}

// WithHost returns the profile bound to a host, so require_tested can say
// whether a tool is unverified outright or merely unverified here.
func (p *Profile) WithHost(h *platform.Host) *Profile {
	p.host = h
	return p
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

	if err := p.checkTested(tools, p.host); err != nil {
		return nil, err
	}
	return tools, nil
}

// checkTested enforces require_tested by naming every offender.
//
// It reports all of them, not the first: a user fixing a profile wants the
// whole list in one pass, not a game of whack-a-mole.
// host, when non-nil, lets the two reasons a tool is excluded be told apart:
// it was never verified, or it was verified somewhere else. Reporting both as
// "not tested" is false for the second, and a user on macOS reading it would
// conclude seven tools had no evidence at all, when they had a container run's
// worth on linux/arm64.
func (p *Profile) checkTested(tools []*manifest.Tool, host *platform.Host) error {
	if !p.RequireTested {
		return nil
	}
	// A profile can select the same tool by name and by tag, so the selection
	// is deduplicated before it is reported. Saying "claude, claude" reads as
	// two problems when there is one.
	var untested, unprovenHere []string
	seen := make(map[string]bool, len(tools))
	tools = dedupe(tools, seen)
	for _, t := range tools {
		switch {
		case !t.Tested:
			// Never proven anywhere. The opt-in does not cover this: there is
			// no evidence at all, and saying otherwise would make the flag
			// mean "ignore the requirement".
			untested = append(untested, t.Name)
		case host != nil && !host.ProvenOn(t):
			// Collected either way. When the caller opted in this is not a
			// refusal but a warning, and the names have to be known before
			// deciding which it is.
			//
			// Appended once. This was appended twice, and the duplicate was
			// invisible in every message because uniq sorts before joining —
			// but len(unprovenHere) is also the count in the warning at the
			// bottom of this function, so a user was told "installing 2
			// tool(s)" about one tool. A value that is wrong only where it is
			// counted rather than printed is the harder kind to notice.
			unprovenHere = append(unprovenHere, t.Name)
		}
	}
	var parts []string
	if len(untested) > 0 {
		sort.Strings(untested)
		parts = append(parts, fmt.Sprintf("no install has been proven for: %s",
			strings.Join(uniq(untested), ", ")))
	}
	if len(unprovenHere) > 0 {
		sort.Strings(unprovenHere)
		parts = append(parts, fmt.Sprintf(
			"proven only on %s, not on this host (%s/%s): %s",
			strings.Join(provenOn(tools, unprovenHere), ", "),
			host.Key(), host.Arch, strings.Join(uniq(unprovenHere), ", ")))
	}
	if len(untested) > 0 {
		// Evidence-free tools block even under the opt-in: there is nothing to
		// accept on their behalf. Reported together with the unproven-here set
		// rather than instead of it, so one run tells the user everything.
		parts := []string{fmt.Sprintf("no install has been proven anywhere for: %s",
			strings.Join(uniq(untested), ", "))}
		if !p.allowUnproven && len(unprovenHere) > 0 {
			sort.Strings(unprovenHere)
			parts = append(parts, fmt.Sprintf(
				"proven only on %s, not on this host (%s/%s): %s",
				strings.Join(provenOn(tools, unprovenHere), ", "),
				host.Key(), host.Arch, strings.Join(uniq(unprovenHere), ", ")))
		}
		if p.allowUnproven {
			parts = append(parts, "(--allow-unproven does not cover these: "+
				"there is no evidence to accept)")
		}
		sort.Strings(untested)
		return fmt.Errorf("profile %q requires tested tools — %s",
			p.Name, strings.Join(parts, "; "))
	}
	if p.allowUnproven && len(unprovenHere) > 0 {
		// Accepted, not ignored: the run proceeds and the caller is told
		// exactly which tools are unverified here. Returning an error here
		// would make the opt-in identical to refusing.
		sort.Strings(unprovenHere)
		p.warnings = append(p.warnings, fmt.Sprintf(
			"installing %d tool(s) proven only on %s, NOT on this host (%s/%s): %s — "+
				"their install path is unverified here and aes cannot confirm the result",
			len(unprovenHere), strings.Join(provenOn(tools, unprovenHere), ", "),
			host.Key(), host.Arch, strings.Join(uniq(unprovenHere), ", ")))
		return nil
	}
	if len(parts) == 0 {
		return nil
	}
	return fmt.Errorf("profile %q requires tested tools — %s",
		p.Name, strings.Join(parts, "; "))
}

// dedupe drops repeat selections while preserving order.
// uniq sorts and removes adjacent repeats. Applied where the names are
// REPORTED rather than where they are selected, because the caller may reach
// this with a list assembled by routes that overlap; saying "claude, claude"
// reads as two problems when there is one.
func uniq(names []string) []string {
	sort.Strings(names)
	out := names[:0]
	for i, n := range names {
		if i == 0 || n != names[i-1] {
			out = append(out, n)
		}
	}
	return out
}

func dedupe(tools []*manifest.Tool, seen map[string]bool) []*manifest.Tool {
	out := make([]*manifest.Tool, 0, len(tools))
	for _, t := range tools {
		if seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		out = append(out, t)
	}
	return out
}

// provenOn reports the platforms the named tools were verified on, so the
// message says where the evidence IS rather than only what is missing here.
func provenOn(tools []*manifest.Tool, names []string) []string {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range tools {
		if !want[t.Name] {
			continue
		}
		for _, where := range t.TestedOn {
			if !seen[where] {
				seen[where] = true
				out = append(out, where)
			}
		}
	}
	sort.Strings(out)
	return out
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
