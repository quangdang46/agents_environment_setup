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
	Name         string            `yaml:"name"`
	Description  string            `yaml:"description"`
	Category     string            `yaml:"category,omitempty"`
	Default      bool              `yaml:"default,omitempty"`
	Tested       bool              `yaml:"tested,omitempty"`
	Tags         []string          `yaml:"tags,omitempty"`
	Provides     []string          `yaml:"provides,omitempty"`
	Dependencies []string          `yaml:"dependencies,omitempty"`
	Verify       *Verify           `yaml:"verify,omitempty"`
	Install      map[string]Target `yaml:"install"`
}

// Target is the install recipe for one platform, keyed by GOOS. Exactly one
// strategy applies, and that strategy's field set is exclusive: a field
// belonging to another strategy is rejected rather than ignored.
type Target struct {
	Strategy string `yaml:"strategy"`
	Manager  string `yaml:"manager,omitempty"` // brew|apt — only for strategy=package
	Package  string `yaml:"package,omitempty"` // only for strategy=package
	// Repository and the arch-keyed maps apply only to github-release.
	Repository string            `yaml:"repository,omitempty"`
	Asset      map[string]string `yaml:"asset,omitempty"`  // Go arch → asset filename
	SHA256     map[string]string `yaml:"sha256,omitempty"` // Go arch → checksum

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
	Min     string `yaml:"min,omitempty"`
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
		rejected: []string{"repository", "asset", "sha256", "go_package", "npm_package", "cargo_name", "uv_package"},
	},
	StrategyGo: {
		required: []string{"go_package"},
		rejected: []string{"manager", "package", "repository", "asset", "sha256", "npm_package", "cargo_name", "uv_package"},
	},
	StrategyNPM: {
		required: []string{"npm_package"},
		rejected: []string{"manager", "package", "repository", "asset", "sha256", "go_package", "cargo_name", "uv_package"},
	},
	StrategyCargo: {
		required: []string{"cargo_name"},
		rejected: []string{"manager", "package", "repository", "asset", "sha256", "go_package", "npm_package", "uv_package"},
	},
	StrategyUV: {
		required: []string{"uv_package"},
		rejected: []string{"manager", "package", "repository", "asset", "sha256", "go_package", "npm_package", "cargo_name"},
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
	if t.Name == "" {
		return fmt.Errorf("name is required")
	}
	if !nameRe.MatchString(t.Name) {
		return fmt.Errorf("name %q must be lowercase kebab-case (a-z, 0-9, dashes)", t.Name)
	}
	if t.Description == "" {
		return fmt.Errorf("description is required")
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
		if v.Version.Min != "" && strings.TrimSpace(v.Version.Command) == "" {
			return fmt.Errorf("verify.version.min requires verify.version.command: nothing would produce a version to compare")
		}
		if v.Version.Min != "" && !minRe.MatchString(v.Version.Min) {
			return fmt.Errorf("verify.version.min %q must be dotted integers with an optional leading v", v.Version.Min)
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
		"package":     t.Package != "",
		"repository":  t.Repository != "",
		"asset":       len(t.Asset) > 0,
		"sha256":      len(t.SHA256) > 0,
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
	}
	if t.Strategy == StrategyGithubRelease {
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
	for _, f := range []struct {
		name string
		m    map[string]string
	}{
		{"asset", t.Asset},
		{"sha256", t.SHA256},
	} {
		for _, arch := range sortedKeys(f.m) {
			if !contains(supportedGoArches, arch) {
				// Name the offending key and the valid set: x86_64 is a
				// silent-fallback bug, and uname's naming has no place
				// inside a manifest.
				return fmt.Errorf("%s: %s key %q is not a Go arch (valid: %s)",
					where, f.name, arch, strings.Join(supportedGoArches, ", "))
			}
			if strings.TrimSpace(f.m[arch]) == "" {
				return fmt.Errorf("%s: %s[%s] is empty", where, f.name, arch)
			}
		}
	}

	// The two maps must cover the same arches, or a release silently ships
	// without a checksum on exactly one architecture.
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
