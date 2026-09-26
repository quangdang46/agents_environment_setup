// Package manifest defines the plugin contract: what a plugin.yaml declares
// and how the core reads it.
package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Plugin is one declarative unit describing a tool: how to install it per OS,
// how to tell it is present, and what environment it needs.
type Plugin struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description,omitempty"`
	Homepage    string            `yaml:"homepage,omitempty"`
	Provides    []string          `yaml:"provides,omitempty"`
	Tags        []string          `yaml:"tags,omitempty"`
	Install     map[string]Target `yaml:"install,omitempty"`
	Verify      *Verify           `yaml:"verify,omitempty"`
	Env         []EnvVar          `yaml:"env,omitempty"`
	Hooks       map[string]string `yaml:"hooks,omitempty"`

	// Path is the directory the manifest was loaded from. Not parsed from YAML.
	Path string `yaml:"-"`
}

// Target is the install recipe for one platform. `run` is a shell command
// string; `url`+`sha256` describe a direct binary download.
type Target struct {
	Run      string   `yaml:"run,omitempty"`
	URL      string   `yaml:"url,omitempty"`
	Bin      string   `yaml:"bin,omitempty"`
	SHA256   string   `yaml:"sha256,omitempty"`
	Arch     []string `yaml:"arch,omitempty"` // restrict to these GOARCH values
	NeedsSudo bool    `yaml:"needs_sudo,omitempty"`
}

// Verify describes how to tell whether the tool is present.
type Verify struct {
	Run string `yaml:"run,omitempty"`
	// PathBased short-circuits shell entirely when any of these binaries
	// resolve on PATH. Cheaper and avoids shell dependency for simple cases.
	PathBased []string `yaml:"path_based,omitempty"`
	// Version is a command whose first line is parsed as a version string.
	Version string `yaml:"version,omitempty"`
	// MinVersion, when set, makes Verify.Version meaningful.
	MinVersion string `yaml:"min_version,omitempty"`
}

// EnvVar is a single environment entry a plugin contributes.
type EnvVar struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Load reads and validates a single plugin.yaml.
func Load(path string) (*Plugin, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	return Parse(data, filepath.Dir(path))
}

// Parse validates manifest bytes. `dir` becomes Plugin.Path.
func Parse(data []byte, dir string) (*Plugin, error) {
	var p Plugin
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true) // reject typos instead of silently ignoring them
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	p.Path = dir
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate reports the first structural problem with the manifest. Errors name
// the offending field so a plugin author can fix it without guessing.
func (p *Plugin) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("name is required")
	}
	if !nameRe.MatchString(p.Name) {
		return fmt.Errorf("name %q must be lowercase kebab-case (a-z, 0-9, dashes)", p.Name)
	}
	if p.Description == "" {
		return fmt.Errorf("description is required")
	}
	if len(p.Install) == 0 {
		return fmt.Errorf("install is required: declare at least one platform")
	}
	for osName, t := range p.Install {
		if err := t.validate(osName); err != nil {
			return fmt.Errorf("install.%s: %w", osName, err)
		}
	}
	if len(p.Provides) == 0 && p.Verify == nil {
		return fmt.Errorf("needs provides or verify: nothing to check for")
	}
	if p.Verify != nil {
		if p.Verify.Run == "" && len(p.Verify.PathBased) == 0 && p.Verify.Version == "" {
			return fmt.Errorf("verify is present but declares no check")
		}
		if p.Verify.MinVersion != "" && p.Verify.Version == "" {
			return fmt.Errorf("verify.min_version requires verify.version")
		}
	}
	if p.Hooks != nil {
		for _, h := range []string{"pre_install", "post_install"} {
			if v, ok := p.Hooks[h]; ok && strings.TrimSpace(v) == "" {
				return fmt.Errorf("hooks.%s is empty", h)
			}
		}
	}
	return nil
}

func (t Target) validate(osName string) error {
	switch {
	case t.Run != "":
		if t.URL != "" || t.SHA256 != "" || t.Bin != "" {
			return fmt.Errorf("run and url/bin/sha256 are mutually exclusive")
		}
	case t.URL != "":
		if t.Bin == "" {
			return fmt.Errorf("url requires bin (the binary name to create)")
		}
		if t.SHA256 == "" {
			return fmt.Errorf("url requires sha256 (never download unverified binaries)")
		}
	default:
		return fmt.Errorf("needs run, or url+bin+sha256")
	}
	return nil
}

// WantsSudo reports whether any declared target needs root. The core never
// calls sudo itself — it prints the command and lets the user run it.
func (p *Plugin) WantsSudo() bool {
	for _, t := range p.Install {
		if t.NeedsSudo {
			return true
		}
	}
	return false
}

// HasBinaryDownload reports whether any target fetches a binary directly
// rather than delegating to a package manager.
func (p *Plugin) HasBinaryDownload() bool {
	for _, t := range p.Install {
		if t.URL != "" {
			return true
		}
	}
	return false
}
