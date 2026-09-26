# AES — agents_environment_setup

> **Status:** Architecture settled, engineering spec locked. Ready to code.
> **Date:** 2026-09-26 · **Research baseline:** ACFS `7dd2bb3`

---

## North Star

> **`aes setup` — one command, taking a machine from "nothing" → "complete, verified AI coding environment". Exit 0 only when done.**

```
ONE COMMAND → ZERO MANUAL INSTALL → ALL TOOLS → ALL DEPS
           → CONFIGURE ENV → VERIFY EVERYTHING → READY
```

**Core identity:** AES knows *"what a complete AI coding machine needs, how to install it on
this OS, how to verify it, and what to do on a re-run."* The user types `aes setup`.

### Two frontends, one core

| | Agent / CI | Human |
|---|---|---|
| Runs | `aes setup --yes --non-interactive --json` | `aes` (TUI) |
| Same core | ✓ | ✓ |

The TUI holds no business logic — the same principle ACFS settled in
`MANIFEST_SCHEMA_VNEXT.md:112`: *"No UI surface should maintain a separate dependency graph."*

### Tier-0 bootstrap

```
curl → GitHub Release → AES binary → ~/.aes/bin/ → aes setup
```

Go and brew are **not** prerequisites for having AES. When provisioning tools, `github-release`
is the primary strategy: independent of any package manager, explicit checksum, no sudo.
`brew`/`apt` are fallbacks.

---

## Settled decisions

| # | Decision | Outcome |
|---|---|---|
| 1 | Positioning | **Agent Developer Environment Manager** |
| 2 | Catalog | 40–50 definitions, **10–15 integration-tested** |
| 3 | `env` | Minimal in MVP |
| 4 | `needs_sudo` | **Removed from `tool.yaml`** → property of the strategy |
| 5 | Shell | No modification by default; `--link-shell` is opt-in, with backup |
| 6 | Directory | **`~/.aes/`** — never touches `~/.agents/` |
| 7 | Platforms | macOS (arm64/x64) + Ubuntu (amd64/arm64) |
| 8 | Testing | unit · dry-run · **real sandbox** |
| 9 | Arbitrary shell | **No** — install goes only through a closed strategy set |
| 10 | `phase` | **Dropped** — use `tags` + `dependencies` |
| 11 | `aes plan` | **Out of scope** — resolution still runs internally |
| 12 | ACFS in CLI | **None** — AES does not know ACFS exists |
| 13 | Binary name | `aes` |

---

## Architecture

```
              aes setup  /  aes (TUI)
                       │
              ┌────────▼─────────┐
              │  Config / Profile│
              └────────┬─────────┘
              ┌────────▼─────────┐
              │  Catalog         │  tool.yaml
              └────────┬─────────┘
              ┌────────▼─────────┐
              │  Resolver        │  deps DAG · platform · cycle detect
              └────────┬─────────┘
              ┌────────▼─────────┐
              │  Actions         │  resolved execution actions
              └────────┬─────────┘
    ┌──────────┬───────┼───────┬──────────┐
    ▼          ▼       ▼       ▼          ▼
  Github    Brew     Apt     Go      Npm/Cargo
  Release
    └──────────┴───────┼───────┴──────────┘
              ┌────────▼─────────┐
              │  Verify          │  ← ground truth
              └────────┬─────────┘
              ┌────────▼─────────┐
              │  State (cache)   │  ← NOT the ground truth
              └────────┬─────────┘
              ┌────────▼─────────┐
              │  ~/.aes/         │
              └──────────────────┘
```

Single execution path — nothing bypasses `tool.yaml`.

### Terminology

There is no `plan` anywhere in the codebase. The object between resolver and installer is named
**`Actions`**. Forbidden names: `InstallPlan` · `PlanCommand` · `PlanRenderer` · `PlanJSON` —
the feature was cut; do not let it regrow under another name.

`--dry-run` **stays**: it is a safety mechanism when running the pipeline, not a product surface.

---

## Invariants

| # | Invariant | Proved by |
|---|---|---|
| I1 | Every tool goes catalog→resolver→actions→installer | No `switch tool.Name`; all via strategy registry |
| I2 | Privilege is a property of the strategy | `tool.yaml` has no such field |
| I3 | No arbitrary shell | Validation rejects fields outside the schema |
| I4 | State written **after** verify passes | Install failure leaves state untouched |
| I5 | State is a cache, not truth | `doctor` detects drift |
| I6 | `--dry-run` does not mutate | No file changes after a dry run |
| I7 | Never touches `~/.agents/` | `~/.agents` unchanged after any command |
| I8 | Same input → same Actions | Run twice, compare bytes |
| I9 | Corrupt manifest ≠ "not installed" | Bad YAML errors out |
| I10 | Never auto-sudo | No `sudo` process spawned without prior output |
| I11 | Cycle → error, never hang | A→B→A reports a cycle |
| I12 | Unsupported platform → skip | A `darwin`-only tool on Linux is skipped |
| I13 | TUI ≡ CLI | Same input → same Actions |
| I14 | `sudo` never runs silently | Only after printing the command and confirming |
| I15 | Default profile contains **only `tested: true`** | Profile referencing an untested tool fails validation |

---

## Privilege model

The North Star says *zero manual install*; the security model says *never auto-sudo*. These
genuinely collide on Ubuntu.

**Decision: prompt explicitly — never run silently.**

The risk in `sudo` is not `sudo` itself; it is *who chose the command*. Silent `sudo` plus a
malicious `tool.yaml` means a bad actor gets your password. `sudo` that **prints the command
and asks** means you see exactly what is about to run before typing a password.

So the rule is: **never run silently — but never refuse outright.**

```
github-release         → no sudo → runs freely
brew / go / npm        → no sudo → runs freely
package + apt          → sudo
      ├─ interactive:    print command, confirm, then run
      └─ non-interactive: try `sudo -n` (CI NOPASSWD or cached credential)
                          → success: run
                          → failure: print command, stop, exit code 5
```

Three enforceable terms, each testable:

1. **`sudo` never runs unasked.** `exec.Command("sudo", …)` executes only after the command is
   printed and, when interactive, confirmed.
2. **`--non-interactive` uses `sudo -n`.** Never blocks on a password prompt. CI with `NOPASSWD`
   runs to completion; without it, AES reports rather than hangs.
3. **A required manual step gets exit code 5 and does not mark state done.** Agent-parseable,
   and it never pretends to have succeeded.

This preserves both halves: `aes setup` remains **one command that runs to completion** on a
machine with normal `sudo`, and it never escalates privilege on its own.

**Reducing how often sudo is needed:** the catalog prefers `github-release` for every tool with
an official release. `apt` is a fallback for system libraries and tools without releases.

---

## Repository layout

```
aes/
├── cmd/aes/main.go
├── internal/
│   ├── manifest/     # schema + validation
│   ├── catalog/      # load tool.yaml, index by name/category/tag
│   ├── resolver/     # deps DAG, platform filter, actions
│   ├── platform/     # OS / arch / package-manager detection
│   ├── installer/    # registry + strategies
│   ├── verifier/
│   ├── state/
│   ├── envgen/
│   └── cli/
├── profiles/         # minimal, developer, ai, full
├── tools/
├── install.sh        # tier-0 bootstrap
├── docs/
└── Makefile
```

---

## Stages

> Ordering note: **bootstrap runs first.** Without a working `bare machine → AES` path, every
> later stage is code on paper.

### Stage 0 — Bootstrap

**Files:**
- `install.sh` — tier 0: detect OS/arch → download binary from GitHub Release → verify sha256 →
  place at `~/.aes/bin/aes`
- `scripts/build.sh` — cross-compile `darwin/{arm64,amd64}` + `linux/{amd64,arm64}`
- `.github/workflows/release.yml` — build matrix, attach checksums

**Contract:**
- Does exactly five things: detect, download, verify, place, report. **Installs no tools** —
  that is `aes setup`'s job.
- The binary is fully self-contained: no Go, brew, or apt needed on the target machine
- Checksum is mandatory, from `checksums.txt` in the release

**Tests:**
- Asset exists + checksum matches → binary runs
- Checksum **mismatch** → abort, binary NOT placed
- No asset for this OS/arch → clear error, no junk download

**Done:** clean Ubuntu → `curl … | sh` → `aes --version` runs.

### Stage 1 — Manifest + Platform

**Files:**
- `internal/manifest/manifest.go` — `Tool`, `Install`, `Strategy`, `Verify`, `Validate()`
- `internal/manifest/manifest_test.go`
- `internal/platform/platform.go` — `Host{OS, Arch, Manager}`, `Current()`, `Supports(tool)`
- `internal/platform/detect_test.go`

**Contract:**
- `Validate()` rejects: missing name/description/install, unknown fields, the removed `run`
  field, `github-release` without sha256, self-referential deps
- `Platform.Supports(tool)` — a `darwin`-only tool returns false on Linux

**Tests:**
- Missing required field → error naming that exact field
- Unknown field (typo) → error, never silently ignored
- `github-release` without `sha256` → error
- `darwin`-only tool, `Supports` on Linux → false (table test)
- Tool with no `install` for the current platform → `Supports` false
- `runtime.GOOS`/`GOARCH` mapping correct — table test, never asserting the current host

**Done:** `go test ./internal/manifest/ ./internal/platform/` green.

### Stage 2 — Catalog + Resolver

**Files:**
- `internal/catalog/catalog.go` — load every `tool.yaml`, index by name/category/tag
- `internal/catalog/catalog_test.go`
- `internal/resolver/resolver.go` — `Resolve(c, req, host) ([]Action, error)`
- `internal/resolver/dag.go` — cycle detection + topological sort
- `internal/resolver/dag_test.go`

**Contract:**
- The resolver always runs: profile → only/exclude → dependency closure → platform filter → actions
- **Dependencies beat selection**: `--only claude` still pulls its deps. Excluding a required
  dep errors, naming who needs it
- Actions are **deterministic**: sorted by name; identical input yields byte-identical output

**Tests:**
- A→B→A (cycle) → cycle error, **never hangs**
- Diamond A→B, A→C, B→D, C→D → D appears once
- `--only claude` where claude needs git → actions include git
- `--exclude node` while claude needs node → error "node required by claude"
- Same input twice → identical actions (byte compare)
- Empty profile → every `default: true` tool

**Done:** resolver returns correct order, cycles error instead of hanging, output deterministic.

### Stage 3 — Verifier

**Files:**
- `internal/verifier/verifier.go` — `Verify(t) Result`
- `internal/verifier/verifier_test.go`

**Contract:**
- `command` must resolve on PATH → OK
- `version.min` → parse semver; below minimum yields `StatusStale`, not an error
- Verify is the **ground truth** and never reads state

**Tests:**
- Command present / absent on PATH → OK / Missing
- Version below / at minimum → Stale / OK
- A `provides` binary missing even though `command` resolves → detected

**Done:** verify distinguishes OK / Missing / Stale.

### Stage 4 — Installer (github-release + package)

**Files:**
- `internal/installer/installer.go` — `Installer` interface + registry
- `internal/installer/github_release.go` — fetch asset by OS/arch, verify sha256, extract to `~/.aes/bin`
- `internal/installer/package.go` — brew / apt, apt under the privilege model
- `internal/installer/installer_test.go`

**Contract:**
- Registry maps `strategy → Installer`. No `switch tool.Name`.
- `github-release`: download → verify sha256 → **on failure stop, never extract a corrupt file**
  → `chmod +x`
- `package`+apt: privilege model — print, confirm (interactive) or `sudo -n`
  (non-interactive). **`sudo` never runs without printing first.**
- `package`+brew: runs directly, no privilege needed
- Every command has a timeout. Output captured, capped at 1 MB.

**Tests:**
- Wrong sha256 → abort, **no file created** in `~/.aes/bin`
- `package`+apt **interactive** without confirmation input → `sudo` does not run (I14)
- `package`+apt **non-interactive**, `sudo -n` fails → prints command, exit code 5, no hang
- `package`+brew → runs, no prompt
- Timeout → error mentioning "timed out", never hangs
- Unknown strategy → clear error

**Done:** a wrong sha256 never produces a binary; `sudo` never runs unprinted.

### Stage 5 — State + Environment

**Files:**
- `internal/state/state.go` — `~/.aes/state.json`, atomic write
- `internal/state/state_test.go`
- `internal/envgen/envgen.go` — `~/.aes/env.sh`, atomic
- `internal/envgen/envgen_test.go`

**Contract:**
- State written **after verify passes**. Failure leaves it untouched.
- Atomic: temp file + rename. A crash mid-write never leaves corrupt JSON.
- `env.sh` is deterministic (no timestamp in the body). Refuses to overwrite a file it did not generate.

**Tests:**
- Corrupt state → error, **not** treated as "nothing installed" (I9)
- Atomic save: no `.tmp` left behind after success
- Newer version on disk → error naming the version, does not discard data
- `env.sh` generated twice from the same input → byte-identical
- `env.sh` exists without an AES header → refuse to write

**Done:** state and env are atomic, deterministic, and corruption is never read as emptiness.

### Stage 6 — CLI + Setup

**Files:**
- `internal/cli/root.go` — command tree
- `internal/cli/setup.go` — `aes setup` orchestration
- `internal/cli/list.go` · `verify.go` · `doctor.go` · `env.go`
- `cmd/aes/main.go`

**Contract:**
- `setup` runs: detect → load profile → resolve → verify current state → install what is
  missing → verify again → state → env → summary
- `--dry-run`: prints actions, **mutates nothing**
- `--yes` / `--non-interactive`: no prompts, fully deterministic
- `--json`: machine-parseable output
- `doctor`: detects drift (state says installed, binary is gone)

**Tests — the 10 MVP assertions:**
1. `list` shows status derived from verify
2. `setup --dry-run` prints actions, changes no file
3. Install runs the resolved strategy
4. Verify passes → state is written
5. Re-run is idempotent
6. Corrupt state → error
7. Corrupt manifest → error, nothing executes
8. Unsupported platform → skipped
9. Strategy needing privilege → prints first, `sudo` only after confirmation
10. 10–15 tools: real install + real verify

**Done:** all 10 assertions green on a real machine.

### Stage 7 — Real catalog

**Files:** `tools/<category>/<name>/tool.yaml` × 40–50

- 10–15 tools **verified**: real install, real verify, version compared → `tested: true`
- 25–35 tools: valid definitions, `tested: false` — **expansion pool, excluded from the
  default profile**
- Every tool carries `tested: true|false`; `doctor` warns about untested ones

**Categories:** shell · search · terminal · git · runtime · ai · agent · infra · utility

**The default profile contains only `tested: true`** (I15). Otherwise the North Star —
"complete, **verified** environment" — is a lie, and 25 tools that were never actually run are
exactly where it breaks on a user's first machine.

**Done:** `aes list` shows the catalog correctly; ≥10 tools verified; `aes setup` installs the
whole default profile on a clean machine without error.

### Stage 8 — Sandbox testing (later)

- `aes test` — Ubuntu 24.04 container: real apt, real binaries, real verification
- macOS: host-safe mode plus an optional VM
- `--keep-sandbox` for debugging

### Stage 9 — TUI (later)

`aes` opens the TUI. Menu: `Setup` · `Tools` · `Profiles` · `Doctor` · `Environment`

**Tools:** search and filter, tick to select several, Enter → the same resolver, the same
installer, the same verify. Selecting `rust` pulls in `cargo` automatically. This is
`aes setup --only rust` with a different selection UI.

**Three hard constraints on the TUI:**

| Forbidden | Why |
|---|---|
| Accepting free-form commands like `brew install xxx` | Breaks I1/I3; the YAML is data, do not reopen a shell |
| Any fast path that bypasses the resolver | TUI and CLI must produce **the same** Actions, or I13 breaks |
| Holding install business logic | Every decision belongs to the core |

The TUI only does this: **collect selection → call the core → display results.** Exactly like
`ssh` does not configure its own port forwarding underneath you.

> Agent: `aes setup` · Human: open `aes`, pick what you actually need. **Two frontends, one engine.**

### Stage 10 — Convenience distribution (later)

Bootstrap already shipped in Stage 0. What remains is an alternative, not the main path:
- Homebrew tap (`brew install aes`)
- `go install github.com/…/cmd/aes@latest`

Both are *convenience methods*. The canonical path remains `curl … | sh` → `aes setup`.

---

## CLI surface

**Core (2):**
```bash
aes setup      # --yes --non-interactive --dry-run --json --profile --only --exclude --force --verbose
aes            # TUI
```

### Flag semantics

`--yes` and `--non-interactive` are **not** synonyms, and neither implies the other:

| | `--yes` | `--non-interactive` |
|---|---|---|
| Suppresses the privilege confirmation | **yes** | yes (via `sudo -n` instead) |
| Suppresses TTY detection / prompts | no | **yes** |
| Asserts a TTY exists | **yes** (errors if stdin is not a TTY) | no |
| Blocks waiting for input | never | never |
| Implies the other | no | no |

- `--yes` means "I am at a terminal, don't ask me." It **requires** a TTY. Piping
  `aes setup --yes` without one is a usage error (exit 2), not a silent downgrade — that
  combination is exactly how a CI job ends up waiting on a prompt that never gets answered.
- `--non-interactive` means "there is nobody to ask." Every confirmation is resolved
  automatically: `sudo -n` first; if that fails, the action is skipped, the command is printed,
  and the run finishes with exit code 5.
- Passing both is legal and means the second's semantics, since `--non-interactive` is the
  stronger statement. But it is pointless — `--non-interactive` alone is correct.
- `--dry-run` is orthogonal: it resolves and prints, then exits 0 without touching anything,
  regardless of the other flags.

**`--force` behavior.** `--force` is *not* "delete everything and reinstall". It permits
corrective action on a tool whose recorded state disagrees with reality:

| Situation | Without `--force` | With `--force` |
|---|---|---|
| `StatusMissing` (state absent, binary absent) | install | install |
| `StatusStale` (version below `min`) | install | install |
| **Drifted** (state says installed, verify says missing) | report, exit 6 | reinstall |
| **Checksum mismatch** on a prior install | report, exit 6 | reinstall after the fix |

`--force` never widens the resolved set. It changes what happens to an already-resolved action
whose reality conflicts with its state. A tool excluded by `--exclude` stays excluded.

### Tool-level commands

**Maintenance:** `install` · `uninstall` · `update` · `list` · `verify` · `doctor` · `env` ·
`test` · `search` · `profile`

`uninstall` semantics are split, and the naming is deliberate — a command called `remove` that
only drops a state entry is a lie about what it did:

- `aes uninstall <tool>` — invokes the strategy's removal (`brew uninstall`, delete the binary
  from `~/.aes/bin`, `go install`-installed tools are reported not-removed with a reason)
- `aes forget <tool>` — drops the state entry only; the binary stays

MVP ships neither; `uninstall` arrives with Stage 10.

---

## Out of scope

Graphical TUIs outside the terminal · dotfile management · Docker/container management ·
secrets and tokens · IDEs and GUI apps · orchestration (beads, swarm, am) — those are
separate projects. **Do not let AES become ACFS 2.0.**

---

## Risks

| Risk | Mitigation |
|---|---|
| 40–50 unverified YAML files are unverified promises | `tested: true/false`; `doctor` warns |
| Catalog bloats, resolver gets tangled | Category is a field, not a directory |
| Ubuntu breaks and nobody notices for months | Stage 8 sandbox container; CI matrix |
| GitHub Release URL breaks when an upstream renames an asset | Per-OS/arch assets + pinned version |
| Resolved actions look right but the real environment differs | `--dry-run` + `doctor` drift detection + sandbox |
| AES accidentally becomes ACFS 2.0 | Invariant I1: nothing bypasses the manifest |

---

# Engineering Specification

No new features here — this is the contract layer underneath the architecture above, locking
the decisions a coding agent would otherwise have to invent.

## Go core types

```go
// internal/platform/platform.go
type Host struct {
    OS      string // "darwin" | "linux"
    Arch    string // "arm64" | "amd64"
    Manager string // "brew" | "apt" | ""
    Prefix  string // homebrew prefix, "" if absent
}
func Current() (*Host, error)
func (h *Host) Key() string            // maps to the install: key — "darwin" | "linux"
func (h *Host) Supports(t *manifest.Tool) bool
func (h *Host) Has(bin string) bool
```

### Arch vocabulary

Three different vocabularies collide here. They must never be confused:

| Layer | Vocabulary | Source |
|---|---|---|
| `install:` map key | `darwin` · `linux` | `runtime.GOOS` |
| `Host.Arch` | `arm64` · `amd64` | `runtime.GOARCH` (Go naming) |
| `uname -m` | `arm64` · `x86_64` | system `uname` |

**AES uses Go naming (`amd64`) everywhere.** The `uname -m` form appears only when shelling
out. `Host.Arch` is `runtime.GOARCH` verbatim, so no translation layer exists inside AES:

```go
// x86_64 → amd64, aarch64 → arm64
func unameArchToGo(m string) string
```

Consequences that MUST hold:

- `tool.yaml` `asset:` and `sha256:` keys are **Go arch** (`arm64`/`amd64`), never `x86_64`
- Release assets are named with Go arch: `aes-1.0.0-linux-amd64.tar.gz`
- `install.sh` translates `uname -m` → Go arch **once**, before downloading
- A `tool.yaml` written with `x86_64` as an asset key fails validation with an error naming
  the offending key and the valid set — silent fallback is forbidden

```go
// internal/manifest/manifest.go
type Tool struct {
    Name         string            `yaml:"name"`
    Description  string            `yaml:"description"`
    Category     string            `yaml:"category"`
    Default      bool              `yaml:"default"`
    Tested       bool              `yaml:"tested"`
    Tags         []string          `yaml:"tags"`
    Provides     []string          `yaml:"provides"`
    Dependencies []string          `yaml:"dependencies"`
    Verify       *Verify           `yaml:"verify"`
    Install      map[string]Target `yaml:"install"` // keyed by GOOS
}

type Target struct {
    Strategy   string            `yaml:"strategy"`   // github-release|package|go|npm|cargo
    Manager    string            `yaml:"manager"`    // brew|apt — only for strategy=package
    Package    string            `yaml:"package"`
    Repository string            `yaml:"repository"` // only for github-release
    Asset      map[string]string `yaml:"asset"`      // Go arch → asset filename
    SHA256     map[string]string `yaml:"sha256"`     // Go arch → checksum, REQUIRED for github-release
    GoPackage  string            `yaml:"go_package"`
    NPMPackage string            `yaml:"npm_package"`
    CargoName  string            `yaml:"cargo_name"`
}

type Verify struct {
    Command string        `yaml:"command"`
    Version *VersionCheck `yaml:"version"`
}
type VersionCheck struct {
    Command string `yaml:"command"`
    Min     string `yaml:"min"`
}
```

**`verify: null` is valid** — it means "presence is checked, version is not". A tool with
`verify: null` and a non-empty `provides` verifies as: the command resolves on PATH → OK;
otherwise Missing. `verify: null` with an empty `provides` is a validation error (nothing would
be checked). `verify.version` without `verify.command` is a validation error.

Validation uses `yaml.Decoder.KnownFields(true)` so a typo fails loudly instead of being
silently dropped.

### Per-strategy validation rules

Each strategy has a required-field set. Violations fail validation with the field named, and
fields belonging to *other* strategies are rejected as unknown:

| Strategy | Required | Rejected if present |
|---|---|---|
| `github-release` | `repository`, `asset`, `sha256` | `manager`, `package`, `go_package`, `npm_package`, `cargo_name` |
| `package` + `brew` | `manager: brew`, `package` | `repository`, `asset`, `sha256`, `*_package` |
| `package` + `apt` | `manager: apt`, `package` | same as brew |
| `package` | `manager`, `package` | `manager` other than brew/apt |
| `go` | `go_package` | `manager`, `package`, `repository`, `asset`, `sha256` |
| `npm` | `npm_package` | same as `go` |
| `cargo` | `cargo_name` | same as `go` |

Two cross-cutting rules:

- `asset` keys and `sha256` keys must cover exactly the same arch set, and every key must be a
  valid Go arch. A missing `sha256[amd64]` on a tool that has `asset[amd64]` is an error.
- Unknown `strategy` value → validation error listing the valid set. Never default to
  `github-release`; a typo must not silently change where code is downloaded from.

```go
// internal/resolver/resolver.go
type Operation string
const (
    OpInstall Operation = "install"
    OpUpgrade Operation = "upgrade"
)

type Action struct {
    Tool              string
    Strategy          string
    Operation         Operation
    Host              *platform.Host
    RequiresPrivilege bool
    Reason            string // "default" | "dependency:git" | "only:claude"
}

type Request struct {
    Profile string
    Only    []string
    Exclude []string
}

func Resolve(c *catalog.Catalog, req Request, h *platform.Host) ([]Action, error)
```

**`Action` is a resolved snapshot, not an execution context.** It is a pure value describing
what *should* happen on one specific host. Two consequences, both deliberate:

- `Host` is a **pointer**, shared across all actions in one resolution. Embedding a value would
  copy the struct per action and make the serialized form noisy for no gain.
- The installer **does not** re-resolve the host. It consumes the `Host` already on the action.
  If execution-time state differs from resolution-time state, that is drift — and drift is
  `doctor`'s job, not the installer's. This keeps `Resolve` pure and therefore testable without
  touching the filesystem.

`Action` carries no `[]string` slices, so it serializes to JSON deterministically. `Reason` is
a plain string, not an enum, because it is diagnostic text for `doctor` — never parsed.

**Action rules:**

- **Identity** is `Tool` + `Strategy` + `Operation`. A diamond dependency yields one action.
- **Ordering** is topological (deps first), ties broken by tool name — so identical input
  yields byte-identical output.
- **Deduplication** happens before ordering, keyed on identity.
- **Reason** records why a tool is present, so `doctor` can explain it.
- `RequiresPrivilege` is derived from `(Strategy, Manager)` — never read from YAML.

| Strategy | Manager | RequiresPrivilege |
|---|---|---|
| `github-release` | — | false |
| `package` | `brew` | false |
| `package` | `apt` | **true** |
| `go` / `npm` / `cargo` | — | false |

```go
// internal/verifier/verifier.go
type Status string
const (
    StatusOK      Status = "ok"
    StatusMissing Status = "missing"
    StatusStale   Status = "stale"
)

type Result struct {
    Tool    string
    Status  Status
    Version string // raw, when detected
    Path    string // resolved binary path
}

func Verify(t *manifest.Tool) Result
```

`Verify` returns no error for a normal outcome — a missing tool is `StatusMissing`. An error
means "could not determine", which is a different thing.

### Version extraction

Every tool prints its version differently. One parser handles all of them, and it is
deliberately lenient about format while being strict about *not inventing* a version:

| Real output | Extracted |
|---|---|
| `ripgrep 14.1.1` | `14.1.1` |
| `fzf 0.54.0 (brew)` | `0.54.0` |
| `git version 2.44.0` | `2.44.0` |
| `go version go1.24.0 darwin/arm64` | `1.24.0` |
| `jq-1.7.1` | `1.7.1` |
| `claude 1.0.283 (Claude Code)` | `1.0.283` |
| `tmux 3.4` | `3.4` |
| `zoxide 0.9.4` | `0.9.4` |

Algorithm:

1. Take the first line of output, trimmed
2. Match the first `\d+(\.\d+)*` run in the line, with an optional leading `v` stripped
3. Compare as dotted integers, not semver: missing components are zero
   (`14.1` == `14.1.0`), so `min: "14.0"` matches `14.1.1`

**No digits → no version.** Return `Version: ""` and `Status: StatusOK` when only presence was
being checked. Never guess, never fall back to a fabricated number.

**Unparseable `min`** is a validation error, not a runtime surprise: `min` must be `\d+(\.\d+)*`
with an optional `v` prefix. A malformed `min` fails catalog load.

Comparison direction: `StatusStale` when extracted `< min`, `StatusOK` when `>= min`. A tool
**newer** than the minimum is fine — AES never downgrades.

```go
// internal/state/state.go
const Version = 1

type File struct {
    Version   int                  `json:"version"`
    Installed map[string]Installed `json:"installed"`
}
type Installed struct {
    Strategy    string `json:"strategy"`
    Privileged  bool   `json:"requires_privilege"`
    Version     string `json:"version,omitempty"`
    InstalledAt string `json:"installed_at"`
}
```

## Errors and exit codes

```go
var (
    ErrCycle          = errors.New("dependency cycle")
    ErrNeedsPrivilege = errors.New("requires elevated privileges")
    ErrChecksum       = errors.New("sha256 mismatch")
    ErrUnsupportedOS  = errors.New("unsupported platform")
    ErrNotGenerated   = errors.New("file not generated by aes")
)

type PrivilegeError struct {
    Command        string
    NonInteractive bool
}
func (e *PrivilegeError) Error() string { return e.Command }
func (e *PrivilegeError) Unwrap() error { return ErrNeedsPrivilege }
```

| Exit code | Meaning | Agent should |
|---|---|---|
| 0 | All resolved actions verified | continue |
| 1 | Generic failure | inspect stderr |
| 2 | Invalid usage / bad flags | fix invocation |
| 3 | Catalog or manifest invalid | fix the definition |
| 4 | Unsupported platform | do not retry |
| 5 | **Needs a manual privileged step** | run the printed command, then re-run |
| 6 | Verification failed after install | inspect the tool |

Code 5 exists so an agent can distinguish "you must do something" from "it broke".

## State transitions

```
                 ┌──────────┐
   absent  ──────│          │
                 │ Resolve  │
                 └────┬─────┘
                      │  unsupported platform
                      ▼
                 ┌──────────┐
                 │ Skipped  │  no state written
                 └──────────┘
                      │ install
                      ▼
                 ┌──────────┐
                 │Verifying │
                 └────┬─────┘
          fail      │      │  pass
        ┌───────────┘      └───────────┐
        ▼                              ▼
   ┌─────────┐                   ┌──────────┐
   │ Failed  │                   │ Installed│  ← state written HERE
   │(no write)│                   └──────────┘
   └─────────┘                          │ binary removed
                                        ▼
                                  ┌──────────┐
                                  │ Drifted  │  state stale, verify says missing
                                  └──────────┘
```

Three rules:

1. **State is written only on verify pass.** Failure never writes.
2. **State is a cache.** `Verify` is ground truth; `doctor` compares the two and reports drift.
3. **A corrupt state file is an error, never an empty state.** Treating corruption as "nothing
   installed" causes a reinstall storm.

## Profile format

```yaml
# profiles/default.yaml
name: default
description: Verified AI coding environment
include:
  - ripgrep      # explicit tools
tags:            # or whole categories
  - search
  - git
require_tested: true   # I15 — only tested: true tools may appear
```

`require_tested: true` is enforced by the profile loader: any resolved tool with
`tested: false` fails validation with an error naming that tool.

## `env.sh` contract

```bash
# Generated by aes — DO NOT EDIT BY HAND
# Regenerate with: aes env --write

export AES_HOME="$HOME/.aes"
case ":$PATH:" in
  *":$AES_HOME/bin:"*) ;;
  *) export PATH="$AES_HOME/bin:$PATH" ;;
esac
```

- Idempotent — sourcing twice is harmless
- No timestamp in the body, so identical input yields byte-identical output
- The header is the ownership marker: `envgen` refuses to write a file lacking it
- **Never** writes `~/.zshrc`. `--link-shell` is a separate opt-in that appends a delimited
  block after taking a backup:

```bash
# >>> aes env >>>
[ -f "$HOME/.aes/env.sh" ] && source "$HOME/.aes/env.sh"
# <<< aes env <<<
```

## GitHub Release contract

Asset naming is fixed so `install.sh` resolves without a manifest lookup:

```
aes-<version>-<goos>-<goarch>.tar.gz
checksums.txt                       # sha256sum format, covers every asset
```

`install.sh`: detect OS/arch → fetch asset → fetch `checksums.txt` → verify → extract to
`~/.aes/bin/aes` → run `aes --version`. Any mismatch aborts before extraction.

## TUI interaction contract

| Key | Action |
|---|---|
| `↑` / `↓` | navigate |
| `space` | toggle selection |
| `/` | search/filter |
| `enter` | install selection |
| `esc` | back / clear search |
| `q` | quit |

The Tools screen passes a `Request` to `Resolve` — byte-identical to
`aes setup --only <selection>`. No TUI-side resolver, no free-form command entry.

## Integration test fixtures

Two distinct layers. Layer 1 runs on every `go test`; Layer 2 only in the sandbox, because it
mutates the machine.

### Layer 1 — Verify fixtures (no mutation, runs everywhere)

Tools already present on the dev machine. Install is skipped; **verify is the assertion**:

| Tool | Binary | Asserted |
|---|---|---|
| ripgrep | `rg` | `StatusOK` + version `14.x` extracted |
| git | `git` | `StatusOK` + version `2.x` extracted |
| fzf | `fzf` | `StatusOK` + version parse |
| jq | `jq` | `StatusOK` + version parse |
| tmux | `tmux` | `StatusOK` + version parse |
| zoxide | `zoxide` | `StatusOK` + version parse |
| gh | `gh` | `StatusOK` + version parse |
| claude | `claude` | `StatusOK` + version parse |

A tool marked `tested: true` that fails this fixture must be flipped to `tested: false`.
The test runs against a `GOOS`-derived expectation, never the host's actual identity, so it
does not become a false test on a different developer machine.

### Layer 2 — Install fixtures (mutates, sandbox only)

Layer 1 proves *detection*. It does **not** prove the install path — a manifest can verify a
tool perfectly and still install the wrong thing, or nothing. Only a real install proves that.

Run in the Stage 8 sandbox, per tool:

1. Start from a machine where the tool is **absent**
2. `aes setup --only <tool> --yes` → must exit 0
3. The binary must exist and `Verify` must now return `StatusOK`
4. `state.json` must contain an entry
5. Re-run `aes setup --only <tool> --yes` → idempotent, no reinstall
6. Delete the binary, run `aes doctor` → drift detected
7. Re-run `aes setup --only <tool> --force` → reinstalls

Steps 5–7 are the ones that catch real bugs: a non-idempotent installer, a state writer that
records something the installer never did, and a `doctor` that cannot see drift.

A tool may only be marked `tested: true` once **both** layers pass. Layer 1 alone is not
sufficient, and the catalog's `tested` flag means both.

---

## Definition of Done

- [ ] 15 invariants covered by tests
- [ ] 10 MVP assertions green
- [ ] 40–50 tool definitions, ≥10 verified for real
- [ ] `aes setup` bootstraps end to end in one command, exits 0 only after verification
- [ ] `~/.aes/` is the only boundary; `~/.agents/` is never touched
- [ ] TUI ≡ CLI (identical Actions)
- [ ] Sandbox proves it works on real Ubuntu
