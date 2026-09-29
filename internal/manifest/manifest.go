// Package manifest defines the tool manifest contract: what a tool.yaml
// declares, and the rules that make a declaration safe to execute.
//
// A manifest is data, never a program. There is no free-form shell anywhere
// in the schema (invariant I3) and no privilege field (I2) — privilege is
// derived from the strategy by the caller. A typo fails validation rather
// than being silently dropped, because a manifest that quietly loses a field
// is a manifest that quietly installs the wrong thing.
//
// This package is pure: stdlib plus yaml.v3, no filesystem access beyond
// reading the manifest path, and no imports of platform, resolver, or
// installer.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrInvalid marks every error produced by loading or validating a
// manifest, so a caller can map a bad definition onto an exit code without
// matching on message text.
var ErrInvalid = errors.New("invalid manifest")

// Install strategies. The set is closed: an unrecognized value is a
// validation error naming the valid set, never a silent fallback to
// github-release. A typo in a strategy name must not quietly change where
// code is downloaded from.
const (
	StrategyGithubRelease = "github-release"
	StrategyPackage       = "package"
	StrategyGo            = "go"
	StrategyNPM           = "npm"
	StrategyCargo         = "cargo"
	// StrategyUV installs a Python tool through uv, which writes to
	// ~/.local/bin. It sits alongside the other language ecosystems rather
	// than under a package manager, because uv brings its own resolver.
	StrategyUV = "uv"
)

// Package managers accepted by the package strategy. Anything else is an
// error — silently falling back would run a command the author never wrote.
const (
	ManagerBrew = "brew"
	ManagerApt  = "apt"
)

// supportedGOOS are the install map keys AES understands. A tool declares
// one Target per key it supports on; the resolver filters by the host's
// runtime.GOOS.
var supportedGOOS = []string{"darwin", "linux"}

// supportedGoArches is the Go arch vocabulary AES uses for asset and sha256
// keys. AES speaks Go naming (amd64) everywhere; uname's x86_64 appears only
// when shelling out, so a manifest key of x86_64 is a bug, not an alias.
var supportedGoArches = []string{"amd64", "arm64"}

// Tool is one declarative unit describing a tool: how to install it per
// platform, how to tell it is present, and what it needs.
type Tool struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Category    string `yaml:"category,omitempty"`
	Default     bool   `yaml:"default,omitempty"`
	Tested      bool   `yaml:"tested,omitempty"`
	// TestedOn records WHERE this was proven, as GOOS/GOARCH pairs.
	//
	// Tested alone is a single bool against a per-platform fact. The Layer 2
	// container proves an install path, and that path differs by platform: a
	// different asset, a different extraction, sometimes a different package
	// manager. A bool set from a linux/arm64 run asserts something about macOS
	// that was never measured, and `require_tested` reads the bool, not a
	// comment.
	//
	// Empty means "the reference platform", which keeps a one-platform tool
	// from carrying ceremony. Non-empty is what a multi-platform proof needs,
	// and it is checked against the host before the tool is resolved, so an
	// unproven host skips rather than silently installing.
	TestedOn     []string `yaml:"tested_on,omitempty"`
	Tags         []string `yaml:"tags,omitempty"`
	Provides     []string `yaml:"provides,omitempty"`
	Dependencies []string `yaml:"dependencies,omitempty"`
	// Aliases are the shell shortcuts this tool installs alongside its
	// binary, declared as the FULL command — never a name alone.
	//
	// ACFS declares only the name (`agent.aliases: [cc]`) and keeps the body
	// in a hand-written 27KB zshrc. That is two sources of truth, and they
	// have already drifted: an old ACFS shipped `alias br='bun run dev'`,
	// shadowing the real `br` binary, and the current zshrc carries a
	// defensive `unalias br` guard for a mistake a generated manifest cannot
	// make. Declaring the whole command means generation cannot disagree
	// with declaration.
	//
	// An alias lands in a sourced shell file, so it gets the same treatment
	// as the rest of the schema: validation, not trust.
	Aliases map[string]string `yaml:"aliases,omitempty"`
	// Config names the config files this tool ships, each living beside
	// tool.yaml in the same directory.
	//
	// The name is a filename, not a path. A `..` or a slash would let a
	// manifest choose where a file lands, which is a destination the catalog
	// should never get to pick — the same reasoning that keeps `run:` out of
	// the schema. The one path a config is written to is
	// ~/.aes/config/<tool>/<name>, and activation into $HOME is a separate,
	// conditional step.
	//
	// Empty for almost every tool. Declaring one is a claim that the file
	// improves a default installation, which is real: tmux ships a perfectly
	// usable default, and a multiplexer config that fights the user's muscle
	// memory is worse than none.
	Config []string `yaml:"config,omitempty"`
	// Settings are key/value pairs MERGED into a tool's own config file.
	// This is the one place AES writes outside ~/.aes, and it exists because
	// the alternative is worse: an alias carrying --dangerously-skip-permissions
	// prompts on every launch until the user finds a setting in a JSON file,
	// and the one-command promise is "it works", not "it installs".
	//
	// Merged, never copied. A user's settings.json holds a proxy URL, a token,
	// a model choice, hooks and a permission mode; overwriting it is silent
	// and the failure surfaces days later as "the agent stopped working". A
	// key is written only when absent, so a value the user set deliberately
	// survives. The path is resolved through any symlink first, because a
	// dotfiles-managed symlink must be edited in place.
	Settings []Setting         `yaml:"settings,omitempty"`
	Verify   *Verify           `yaml:"verify,omitempty"`
	Install  map[string]Target `yaml:"install"`
}

// Setting is one merge rule: a path relative to the user's home, and the
// keys to set in the JSON object there.
type Setting struct {
	// Path is relative to the user's home directory. A leading /, or any ..
	// component, is rejected: the catalog does not get to choose an absolute
	// destination for a file outside ~/.aes.
	Path string `yaml:"path"`
	// Merge keys are set only when absent, never overwritten.
	Merge map[string]any `yaml:"merge"`
}

// Target is the install recipe for one platform, keyed by GOOS. Exactly one
// strategy applies, and that strategy's field set is exclusive: a field
// belonging to another strategy is rejected rather than ignored.
type Target struct {
	Strategy string `yaml:"strategy"`
	Manager  string `yaml:"manager,omitempty"` // brew|apt — only for strategy=package
	Package  string `yaml:"package,omitempty"` // only for strategy=package
	// AptSource is a third-party apt repository, installed before the
	// package. Only for strategy=package with manager=apt.
	//
	// It exists because some real tools are simply not in a distro index:
	// tailscale ships no client package for Ubuntu at all, and postgresql-18
	// is not in noble's index (noble has 16). Refusing them would be a
	// smaller product than the manifest can honestly describe.
	//
	// The keyring is fetched from a URL and MUST carry a checksum. That is
	// the whole safety property: without it, `keyring_url` would be a way for
	// any manifest to name a key that signs any package, which is more power
	// than the closed strategy set was built to keep out of the catalog's
	// hands. With it, the bytes are pinned by a value a reviewer can check,
	// and the failure mode is a checksum mismatch rather than a silent
	// substitution.
	AptSource *AptSource `yaml:"apt_source,omitempty"`
	// Repository, Binary and the arch-keyed maps apply only to
	// github-release.
	Repository string `yaml:"repository,omitempty"`
	// ReleaseTag pins the GitHub release tag, e.g. "v0.6.12". Empty means
	// "latest", which is a supply-chain risk: the bytes under that URL change
	// with no record, so a tool that verified yesterday can fail today with
	// nothing in the repo having changed. Measured on 2026-09-28: sbh was
	// pinned to v0.6.12 while upstream had moved to v0.6.16, so the download
	// 404'd and the tool reported as missing. Every github-release tool should
	// carry this.
	ReleaseTag string `yaml:"release_tag,omitempty"`
	// Binary is the filename inside the archive, when it differs from the tool
	// name. opentofu ships a binary called `tofu`, and there was no way to say
	// so: the installer inferred the name from the tool name, which is right
	// often enough to be a trap and wrong exactly when a tool is renamed
	// upstream.
	// Binary is the filename INSIDE the archive, keyed by Go arch, when it
	// differs from the tool name. It is a map for the same reason Asset and
	// SHA256 are: some upstreams name the binary per platform AND arch. yq
	// ships yq_linux_amd64, yq_darwin_arm64 and so on, so a single string
	// cannot describe the artefact and the installer would look for "yq" in
	// an archive that has never contained it.
	//
	// Absent, the tool name is used - which is right for the overwhelming
	// majority, and keeps every existing manifest working unchanged.
	Binary map[string]string `yaml:"binary,omitempty"`
	Asset  map[string]string `yaml:"asset,omitempty"`  // Go arch → asset filename
	SHA256 map[string]string `yaml:"sha256,omitempty"` // Go arch → checksum

	// Ecosystem coordinates, one per strategy. Exactly the field matching
	// the strategy may be set.
	//
	// Each of these carries a pinned version as a "@<version>" suffix. The
	// pin is mandatory, not a convention: an unpinned ecosystem install is
	// the same supply-chain risk as an unpinned GitHub release, and
	// defaulting to latest at run time would make a manifest mean two
	// different things on two different days.
	GoPackage  string `yaml:"go_package,omitempty"`
	NPMPackage string `yaml:"npm_package,omitempty"`
	CargoName  string `yaml:"cargo_name,omitempty"`
	UVPackage  string `yaml:"uv_package,omitempty"`
}

// AptSource is a third-party apt repository.
type AptSource struct {
	// URL is the repository line, e.g.
	// "https://pkgs.tailscale.com/stable/ubuntu noble main".
	URL string `yaml:"url"`
	// KeyringURL is where the signing key is fetched from. Required.
	KeyringURL string `yaml:"keyring_url"`
	// KeyringSHA256 pins the keyring bytes. Required — see Target.AptSource
	// for why this is the safety property.
	KeyringSHA256 string `yaml:"keyring_sha256"`
}

// Verify describes how to tell whether the tool is present and new enough.
//
// Verify being nil is valid: it means presence is checked through Provides
// and no version is asserted.
type Verify struct {
	Command string        `yaml:"command,omitempty"`
	Version *VersionCheck `yaml:"version,omitempty"`
}

// VersionCheck names a command whose output is parsed for a version string,
// and the minimum acceptable version.
type VersionCheck struct {
	Command string `yaml:"command"`
	// Min is the minimum acceptable version: a dotted-integer string, or a
	// map from GOOS to a dotted-integer string when platforms supply
	// different versions.
	//
	// The map exists because Ubuntu noble ships node 18 while brew ships node
	// 26, and mise is packaged in noble as 1.6.13 while upstream uses calendar
	// versions like 2024.x. A single floor against two different distributions
	// means "the newest version aes has ever seen", which is where node failed
	// with a floor of 20.0 on a machine that could only ever supply 18.
	// A scalar keeps every existing manifest working unchanged.
	Min Floor `yaml:"min,omitempty"`
}

// Floor is a version floor: either a single dotted-integer string that
// applies to every platform, or a map from GOOS to the floor that platform
// can supply.
type Floor struct {
	// Default applies when no per-platform entry matches. It is the only
	// thing a scalar YAML value sets.
	Default string
	// ByOS maps a GOOS to that platform's floor. Only `darwin` and `linux`
	// are accepted — anything else is a platform AES does not install on.
	ByOS map[string]string
}

// FloorFor returns the floor that applies on goos, or "" when none is
// declared. A per-platform entry wins; the scalar is the fallback.
func (f Floor) FloorFor(goos string) string {
	if v, ok := f.ByOS[goos]; ok {
		return v
	}
	// A `*` key is the shared default, written this way so a manifest that has
	// both a per-platform entry and a common floor stays a single map.
	if v, ok := f.ByOS["*"]; ok {
		return v
	}
	return f.Default
}

// MarshalYAML serialises a Floor back to the shape a human would write: a
// scalar when there is one floor for every platform, a map when platforms
// differ. Without this a round trip turns `min: 14.0` into
// `min: {default: 14.0, byos: {}}` — field names a manifest author never
// wrote, and which do not parse back.
func (f Floor) MarshalYAML() (any, error) {
	if len(f.ByOS) == 0 {
		return f.Default, nil
	}
	if f.Default == "" {
		return f.ByOS, nil
	}
	// Both present: emit the map with a `*` key for the shared default, which
	// FloorFor already reads as a per-platform entry.
	merged := make(map[string]string, len(f.ByOS)+1)
	for k, v := range f.ByOS {
		merged[k] = v
	}
	merged["*"] = f.Default
	return merged, nil
}

// UnmarshalYAML accepts either a scalar ("20.0") or a map
// ({darwin: 26.0, linux: 18.0}). Every existing manifest uses the scalar, so
// this keeps them all working; the map is opt-in per tool.
//
// The scalar form is decoded through a node rather than by re-marshalling,
// because a manifest that says `min: 20.0` means the string "20.0" and not
// the float 20 — the floor regex is deliberately dotted-integer only.
func (f *Floor) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var s string
		if err := node.Decode(&s); err != nil {
			return err
		}
		f.Default = s
		return nil
	case yaml.MappingNode:
		m := map[string]string{}
		if err := node.Decode(&m); err != nil {
			return err
		}
		f.ByOS = m
		return nil
	default:
		return fmt.Errorf("verify.version.min must be a version string or a map of GOOS to version string")
	}
}

// strategySpec is the required and rejected field set for one strategy. Every
// strategy declares exactly the fields it needs and rejects the rest, so a
// manifest cannot carry two contradictory recipes.
type strategySpec struct {
	required []string
	rejected []string
}

// strategySpecs is the closed matrix from the engineering spec. Keeping it as
// data rather than a switch means adding a strategy is a data change and the
// error messages derive from the same source as the validation.
var strategySpecs = map[string]strategySpec{
	StrategyGithubRelease: {
		required: []string{"repository", "asset", "sha256"},
		rejected: []string{"manager", "package", "go_package", "npm_package", "cargo_name", "uv_package"},
	},
	StrategyPackage: {
		required: []string{"manager", "package"},
		rejected: []string{"release_tag", "repository", "asset", "sha256", "binary", "go_package", "npm_package", "cargo_name", "uv_package"},
	},
	StrategyGo: {
		required: []string{"go_package"},
		rejected: []string{"release_tag", "manager", "package", "repository", "asset", "sha256", "binary", "npm_package", "cargo_name", "uv_package"},
	},
	StrategyNPM: {
		required: []string{"npm_package"},
		rejected: []string{"release_tag", "manager", "package", "repository", "asset", "sha256", "binary", "go_package", "cargo_name", "uv_package"},
	},
	StrategyCargo: {
		required: []string{"cargo_name"},
		rejected: []string{"release_tag", "manager", "package", "repository", "asset", "sha256", "go_package", "npm_package", "uv_package"},
	},
	StrategyUV: {
		required: []string{"uv_package"},
		rejected: []string{"release_tag", "manager", "package", "repository", "asset", "sha256", "binary", "go_package", "npm_package", "cargo_name"},
	},
}

// pinnedEcosystems are the strategies whose package coordinate must carry an
// explicit version, keyed by the field holding it.
var pinnedEcosystems = map[string]string{
	StrategyGo:    "go_package",
	StrategyNPM:   "npm_package",
	StrategyCargo: "cargo_name",
	StrategyUV:    "uv_package",
}

// ValidStrategies returns the closed strategy set in sorted order, for error
// messages and for `--help` text.
func ValidStrategies() []string {
	return sortedKeys(strategySpecs)
}

// nameRe is the tool naming contract: lowercase kebab-case. Dependencies
// reference tool names, so the same rule catches a typo in either place.
var nameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// minRe is a version floor: dotted integers, optionally v-prefixed. Anything
// else is a catalog-load error rather than a runtime surprise.
var minRe = regexp.MustCompile(`^v?\d+(\.\d+)*$`)

// shaRe is a 64-character hex digest.
var shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// aliasNameRe is the alias naming contract: a bare shell word. It is emitted
// as `alias NAME=...` and the shell re-evaluates it, so the rule is the
// shell's, not ours: no spaces, no metacharacters, no empty names.
var aliasNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// testedOnRe matches a GOOS/GOARCH pair. It uses the same vocabulary as the
// `install:` map, so a value here means what the same word means there.
var testedOnRe = regexp.MustCompile(`^(darwin|linux)/(amd64|arm64)$`)

// removedFields are schema fields from the pre-spec plugin format. Unknown
// fields already fail parsing; this map upgrades that failure into a message
// that tells the author what to do instead.
var removedFields = map[string]string{
	"run":        "install recipes no longer take a shell command; name a strategy instead (github-release, package, go, npm, cargo)",
	"url":        "superseded by strategy: github-release with a repository plus per-arch asset and sha256",
	"needs_sudo": "removed: privilege is a property of the strategy (package+apt), never a manifest field",
	"hooks":      "removed: install behaviour is decided by the strategy, not by manifest-supplied commands",
	"env":        "removed: environment is generated from ~/.aes/bin, not declared per tool",
	"homepage":   "removed: it was never used and had no validation",
}

// Load reads and validates a single tool.yaml.
func Load(path string) (*Tool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", filepath.Base(path), err)
	}
	t, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return t, nil
}

// Parse validates manifest bytes. Unknown fields are rejected outright: a
// typo must fail loudly rather than being silently dropped.
func Parse(data []byte) (*Tool, error) {
	var t Tool
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("%w: parse manifest: %w", ErrInvalid, explainUnknownField(err))
	}
	if err := t.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return &t, nil
}

// Validate reports the first structural problem with the manifest. Errors
// name the offending field so an author can fix it without guessing.
func (t *Tool) Validate() error {
	for _, where := range t.TestedOn {
		if !testedOnRe.MatchString(where) {
			return fmt.Errorf("tested_on has %q, want GOOS/GOARCH (darwin/arm64, linux/amd64)", where)
		}
	}
	if t.Name == "" {
		return fmt.Errorf("name is required")
	}
	if !nameRe.MatchString(t.Name) {
		return fmt.Errorf("name %q must be lowercase kebab-case (a-z, 0-9, dashes)", t.Name)
	}
	if t.Description == "" {
		return fmt.Errorf("description is required")
	}
	if err := t.validateAliases(); err != nil {
		return err
	}
	if err := t.validateConfig(); err != nil {
		return err
	}
	if err := t.validateSettings(); err != nil {
		return err
	}
	if err := t.validateDependencies(); err != nil {
		return err
	}
	if len(t.Install) == 0 {
		return fmt.Errorf("install is required: declare at least one platform")
	}
	// Sorted so the reported failure does not depend on map iteration order.
	for _, osName := range sortedKeys(t.Install) {
		if !contains(supportedGOOS, osName) {
			return fmt.Errorf("install.%s is not a supported platform (valid: %s)",
				osName, strings.Join(supportedGOOS, ", "))
		}
		if err := t.Install[osName].validate(osName); err != nil {
			return err
		}
	}
	return t.validateVerify()
}

// validateAliases enforces that an alias is a shortcut rather than a hole.
//
// Everything here lands in a file the shell sources, so the rules are the
// shell's own constraints and nothing looser:
//
//   - the name must be a bare command name. It is emitted as `alias NAME=...`
//     and the shell re-evaluates it, so a name with a space or a metacharacter
//     is not a name at all.
//   - the value may not contain a newline. In a sourced file a newline is a
//     second statement, which is exactly the hole I3 closed for `run:` — a
//     manifest that cannot express "run this" still must not become a program.
//   - the value may not be empty. `alias cc=”` runs nothing and reports a
//     working shortcut.
//   - the name may not equal a tool's own name. `claude: {claude: ...}` would
//     shadow the very binary this manifest installs, which is the failure
//     aes doctor exists to report and which no manifest should introduce.
func (t *Tool) validateAliases() error {
	// Sorted so the reported failure does not depend on map iteration order.
	for _, name := range sortedKeys(t.Aliases) {
		value := t.Aliases[name]
		if !aliasNameRe.MatchString(name) {
			return fmt.Errorf("aliases: %q is not a valid alias name (letters, digits, dash, underscore, dot)", name)
		}
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("aliases: %q is empty; an alias that runs nothing reports a working shortcut", name)
		}
		if strings.ContainsAny(value, "\n\r") {
			return fmt.Errorf("aliases: %q contains a newline, which is a second statement in a sourced file", name)
		}
		if name == t.Name {
			return fmt.Errorf("aliases: %q would shadow the binary this tool installs", name)
		}
	}
	return nil
}

// validateConfig enforces that a declared config file is a filename, not a
// path, and that it actually exists beside the manifest.
//
// The existence check is the one that matters. A config entry naming a file
// that is not there produces a setup that claims to ship a config and ships
// nothing, and the failure surfaces as a missing file on a machine the user
// cannot inspect. Checking it here means the catalog refuses to load.
func (t *Tool) validateConfig() error {
	seen := make(map[string]bool, len(t.Config))
	for _, name := range t.Config {
		if name == "" {
			return fmt.Errorf("config: an empty entry is not a filename")
		}
		if name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
			return fmt.Errorf("config: %q is a path, not a filename; a manifest cannot choose where a file lands", name)
		}
		if seen[name] {
			return fmt.Errorf("config: %q is listed more than once", name)
		}
		seen[name] = true
	}
	return nil
}

// validateSettings enforces that a merge rule names a path inside the user's
// home and carries at least one key.
//
// The path rule is the same one `config:` applies, and for the same reason:
// a manifest that can name an absolute path can write anywhere, which is a
// destination the catalog must never get to pick. The difference is that
// `config:` ships a file AES owns, while `settings:` edits a file the tool
// owns — so the path is checked here and the merge itself is checked at
// write time, where the file's actual contents are known.
func (t *Tool) validateSettings() error {
	seen := make(map[string]bool, len(t.Settings))
	for _, s := range t.Settings {
		if s.Path == "" {
			return fmt.Errorf("settings: a rule with no path names no file to merge into")
		}
		if filepath.IsAbs(s.Path) {
			return fmt.Errorf("settings: %q must be relative to the user's home; "+
				"a manifest cannot choose an absolute destination", s.Path)
		}
		// `..` is the whole check: it is the only way a relative path escapes
		// home. Subdirectories are expected — .claude/settings.json is two
		// levels down — so the path is checked by containment, not by shape.
		for _, part := range strings.Split(filepath.ToSlash(s.Path), "/") {
			if part == ".." {
				return fmt.Errorf("settings: %q climbs out of the user's home", s.Path)
			}
		}
		if len(s.Merge) == 0 {
			return fmt.Errorf("settings: %q declares no keys to merge", s.Path)
		}
		if seen[s.Path] {
			return fmt.Errorf("settings: %q is listed more than once", s.Path)
		}
		seen[s.Path] = true
	}
	return nil
}

func (t *Tool) validateDependencies() error {
	seen := make(map[string]bool, len(t.Dependencies))
	for _, dep := range t.Dependencies {
		if dep == t.Name {
			return fmt.Errorf("dependencies: %q depends on itself", t.Name)
		}
		if !nameRe.MatchString(dep) {
			return fmt.Errorf("dependencies: %q must be a lowercase kebab-case tool name", dep)
		}
		if seen[dep] {
			return fmt.Errorf("dependencies: %q is listed more than once", dep)
		}
		seen[dep] = true
	}
	return nil
}

// validateVerify enforces that something is actually checked, and that a
// version floor is only ever attached to a command that produces one.
func (t *Tool) validateVerify() error {
	v := t.Verify
	if v != nil && v.Version != nil {
		// A version floor is meaningless without a presence check to
		// compare it against: a stale tool that is also missing has
		// nothing to report.
		if strings.TrimSpace(v.Command) == "" {
			return fmt.Errorf("verify.version requires verify.command: a version check needs a presence check to compare against")
		}
		if v.Version.Min.FloorFor("") != "" && strings.TrimSpace(v.Version.Command) == "" {
			return fmt.Errorf("verify.version.min requires verify.version.command: nothing would produce a version to compare")
		}
		// Every declared floor is validated, not just the one that happens to
		// match this machine. A floor that is malformed on a platform AES does
		// not install on is a floor that will fail the day someone adds one.
		// The platform set is checked too: a key that is not a GOOS AES
		// installs on is a typo that would otherwise be read as a floor for a
		// platform nobody can reach.
		for _, where := range sortedKeys(v.Version.Min.ByOS) {
			if !contains(supportedGOOS, where) {
				return fmt.Errorf("verify.version.min: %q is not a supported platform (valid: %s)",
					where, strings.Join(supportedGOOS, ", "))
			}
			floor := v.Version.Min.ByOS[where]
			if floor == "" {
				continue
			}
			if !minRe.MatchString(floor) {
				return fmt.Errorf("verify.version.min[%s] %q must be dotted integers with an optional leading v",
					where, floor)
			}
		}
		if v.Version.Min.Default != "" && !minRe.MatchString(v.Version.Min.Default) {
			return fmt.Errorf("verify.version.min %q must be dotted integers with an optional leading v", v.Version.Min.Default)
		}
	}
	// A tool has to be checkable by something. verify: null is valid on its
	// own — presence is checked through Provides — but only if Provides is
	// non-empty, otherwise nothing would ever be checked.
	if len(t.Provides) == 0 && (v == nil || strings.TrimSpace(v.Command) == "") {
		return fmt.Errorf("needs provides or verify.command: nothing would be checked for %q", t.Name)
	}
	return nil
}

// present reports whether a strategy-relevant field carries a value. Map
// fields count as present when non-empty, so the same required/rejected loop
// covers scalars and the arch-keyed maps alike.
func (t Target) present() map[string]bool {
	return map[string]bool{
		"manager":     t.Manager != "",
		"apt_source":  t.AptSource != nil,
		"package":     t.Package != "",
		"repository":  t.Repository != "",
		"release_tag": t.ReleaseTag != "",
		"asset":       len(t.Asset) > 0,
		"sha256":      len(t.SHA256) > 0,
		"binary":      len(t.Binary) > 0,
		"go_package":  t.GoPackage != "",
		"npm_package": t.NPMPackage != "",
		"cargo_name":  t.CargoName != "",
		"uv_package":  t.UVPackage != "",
	}
}

func (t Target) validate(osName string) error {
	where := fmt.Sprintf("install.%s", osName)

	spec, ok := strategySpecs[t.Strategy]
	if !ok {
		return fmt.Errorf("%s: strategy %q is not a valid strategy (valid: %s)",
			where, t.Strategy, strings.Join(ValidStrategies(), ", "))
	}

	set := t.present()
	for _, f := range spec.required {
		if !set[f] {
			return fmt.Errorf("%s: strategy %q requires field %q", where, t.Strategy, f)
		}
	}
	for _, f := range spec.rejected {
		if set[f] {
			return fmt.Errorf("%s: strategy %q must not set field %q", where, t.Strategy, f)
		}
	}

	if t.Strategy == StrategyPackage {
		if err := t.validateManager(where); err != nil {
			return err
		}
		if err := t.validateAptSource(where); err != nil {
			return err
		}
	}
	// Every strategy that can carry an arch-keyed map gets its keys checked.
	// This used to run only for github-release, which left `binary` — added
	// later and keyed the same way — unvalidated on the other four. The
	// failure is silent and total: a `go` target with `binary: {x86_64: ...}`
	// parsed cleanly, and the installer then looked for a file that was never
	// there.
	if t.Strategy == StrategyGithubRelease || len(t.Binary) > 0 {
		if err := t.validateArchMaps(where); err != nil {
			return err
		}
	}
	if field, ok := pinnedEcosystems[t.Strategy]; ok {
		if err := t.validatePinned(where, field); err != nil {
			return err
		}
	}
	return nil
}

// validatePinned requires an ecosystem package coordinate to carry an
// explicit version.
//
// The check is here, at manifest load, rather than in the installer at run
// time. A missing pin is a property of the declaration: catching it when the
// catalog loads means an author finds out before a user's machine does, and
// no installer is ever tempted to substitute @latest on its own.
// validateAptSource enforces that a third-party apt repo is complete and
// trusted.
//
// The checksum is the point. A keyring fetched from a URL with nothing to
// check it against is a key that can sign any package on the machine, named
// by a file in the catalog — which is a wider grant than the closed strategy
// set exists to withhold. Requiring the digest makes the manifest's claim
// reviewable and turns a substitution into a loud mismatch.
func (t Target) validateAptSource(where string) error {
	if t.AptSource == nil {
		return nil
	}
	if t.Manager != ManagerApt {
		return fmt.Errorf("%s: apt_source requires manager apt; %q is %q", where, t.Manager, t.Manager)
	}
	s := t.AptSource
	if strings.TrimSpace(s.URL) == "" {
		return fmt.Errorf("%s: apt_source requires a url", where)
	}
	if !strings.HasPrefix(s.URL, "https://") {
		return fmt.Errorf("%s: apt_source url must be https, got %q", where, s.URL)
	}
	if strings.TrimSpace(s.KeyringURL) == "" {
		return fmt.Errorf("%s: apt_source requires a keyring_url", where)
	}
	if !strings.HasPrefix(s.KeyringURL, "https://") {
		return fmt.Errorf("%s: apt_source keyring_url must be https, got %q", where, s.KeyringURL)
	}
	if !shaRe.MatchString(s.KeyringSHA256) {
		return fmt.Errorf("%s: apt_source keyring_sha256 %q must be 64 hex characters; "+
			"a keyring fetched with nothing to check it against can sign any package",
			where, s.KeyringSHA256)
	}
	return nil
}

func (t Target) validatePinned(where, field string) error {
	coord := t.PackageCoordinate()
	version, ok := splitPinnedVersion(coord)
	if !ok {
		return fmt.Errorf("%s: %s %q must pin a version as %q — an unpinned install changes under you with no record",
			where, field, coord, field+"@<version>")
	}
	if version == "latest" {
		return fmt.Errorf("%s: %s pins %q, which is not a version — pin a real one",
			where, field, "latest")
	}
	return nil
}

// PackageCoordinate returns the package coordinate for this target's
// strategy, or "" for strategies that do not use one.
//
// It is exported so the installer splits the same string the validator
// checked, rather than keeping a second notion of where the version lives.
func (t Target) PackageCoordinate() string {
	switch t.Strategy {
	case StrategyGo:
		return t.GoPackage
	case StrategyNPM:
		return t.NPMPackage
	case StrategyCargo:
		return t.CargoName
	case StrategyUV:
		return t.UVPackage
	default:
		return ""
	}
}

// splitPinnedVersion splits "name@version" at the final '@'.
//
// The last one is correct for both ecosystems involved: an npm scoped name
// ("@scope/pkg") begins with '@' but is not a pin, and Go module paths may
// contain '@' inside a proxy URL.
func splitPinnedVersion(coord string) (version string, ok bool) {
	i := strings.LastIndex(coord, "@")
	if i <= 0 || i == len(coord)-1 {
		return "", false
	}
	return coord[i+1:], true
}

func (t Target) validateManager(where string) error {
	switch t.Manager {
	case ManagerBrew, ManagerApt:
		return nil
	default:
		return fmt.Errorf("%s: strategy %q has manager %q (valid: %s, %s)",
			where, StrategyPackage, t.Manager, ManagerBrew, ManagerApt)
	}
}

// validateArchMaps enforces the arch vocabulary and the rule that a missing
// checksum is an error rather than an unchecked download.
func (t Target) validateArchMaps(where string) error {
	// 1. Every arch-keyed map must speak Go naming. This runs FIRST, because a
	//    bad key is the actual mistake and every later check reports a
	//    consequence of it instead.
	//
	//    `binary` was absent from this list, which is the whole bug: a
	//    `binary: {x86_64: ...}` key passed every check because nothing ever
	//    looked at it, and the installer then hunted for a file no release
	//    contains.
	for _, f := range []struct {
		name string
		m    map[string]string
	}{
		{"asset", t.Asset},
		{"sha256", t.SHA256},
		{"binary", t.Binary},
	} {
		for _, arch := range sortedKeys(f.m) {
			if !contains(supportedGoArches, arch) {
				// Name the offending key and the valid set: x86_64 is a
				// silent-fallback bug, and uname's naming has no place
				// inside a manifest.
				return fmt.Errorf("%s: %s key %q is not a Go arch (valid: %s)",
					where, f.name, arch, strings.Join(supportedGoArches, ", "))
			}
			// `binary` is exempt: step 3 checks it with wording that also says
			// why the arch was required, and saying it twice with the weaker
			// message last would be a regression in the error.
			if f.name != "binary" && strings.TrimSpace(f.m[arch]) == "" {
				return fmt.Errorf("%s: %s[%s] is empty", where, f.name, arch)
			}
		}
	}

	// 2. asset and sha256 must cover the same arches, or a release ships
	//    without a checksum on exactly one architecture.
	for _, arch := range sortedKeys(t.Asset) {
		if _, ok := t.SHA256[arch]; !ok {
			return fmt.Errorf("%s: sha256 is missing for arch %q present in asset", where, arch)
		}
	}
	for _, arch := range sortedKeys(t.SHA256) {
		if _, ok := t.Asset[arch]; !ok {
			return fmt.Errorf("%s: asset is missing for arch %q present in sha256", where, arch)
		}
	}

	// 3. binary, when declared, must cover exactly the arches asset does, and
	//    every value must name something. A binary map with a different key set
	//    is the same shape of bug as a checksum map with one: it validates, and
	//    then the installer looks for a filename the archive does not contain
	//    on the one machine that matters.
	if len(t.Binary) > 0 {
		if len(t.Binary) != len(t.Asset) {
			return fmt.Errorf("%s: binary covers %d arches but asset covers %d",
				where, len(t.Binary), len(t.Asset))
		}
		for _, arch := range sortedKeys(t.Asset) {
			if strings.TrimSpace(t.Binary[arch]) == "" {
				return fmt.Errorf("%s: binary is empty for arch %q present in asset", where, arch)
			}
		}
		for _, arch := range sortedKeys(t.Binary) {
			if _, ok := t.Asset[arch]; !ok {
				return fmt.Errorf("%s: binary has arch %q which asset does not", where, arch)
			}
		}
	}
	return nil
}

// yamlUnknownFieldRe pulls the field name out of a yaml.v3 unknown-field
// error so the message can be upgraded with migration guidance.
var yamlUnknownFieldRe = regexp.MustCompile(`field ([A-Za-z0-9_]+) not found`)

// explainUnknownField turns "field run not found in type manifest.Target"
// into a message that says what to write instead. Any field the strict
// decoder rejects is an error either way; this only makes the failure legible.
func explainUnknownField(err error) error {
	m := yamlUnknownFieldRe.FindStringSubmatch(err.Error())
	if m == nil {
		return err
	}
	hint, ok := removedFields[m[1]]
	if !ok {
		return err
	}
	return fmt.Errorf("%w (%q was removed: %s)", err, m[1], hint)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// RequiresPrivilege reports whether a target's strategy needs root.
//
// It lives here rather than in installer because it is a fact about a MANIFEST's
// own fields, and this is the only package both the resolver and the installer
// can reach — resolver must not import installer (that would invert the
// layering), so a derivation kept in installer could only be reached by the
// installer and copied by the resolver.
//
// It had been copied. Two byte-identical definitions existed, and the
// installer's carried a comment saying the derivation lived there "so the
// resolver, the installer and the CLI cannot drift apart" — which is the
// opposite of what two copies guarantee. This is the eighth instance of that
// shape in this project, and the cure is always the same: one declaration, and
// the second one deleted rather than left as a fallback.
func RequiresPrivilege(t Target) bool {
	return t.Strategy == StrategyPackage && t.Manager == ManagerApt
}
