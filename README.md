# aes — Agents Environment Setup

<div align="center">

![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-blue.svg)
![Go](https://img.shields.io/badge/Go-1.26-00ADD8.svg)
![Status](https://img.shields.io/badge/status-pre--1.0%20%7C%207%20tools%20proven%20on%20linux%2Farm64-orange.svg)

</div>

**One command takes a machine from nothing to a complete AI coding environment — and exits 0 only when it actually is.**

`aes setup` resolves a profile into a dependency-ordered plan, verifies what's already there, installs only what's missing, verifies again, and refuses to call it done until the tools are real.

```bash
curl -fsSL https://raw.githubusercontent.com/quangdang46/agents_environment_setup/main/install.sh | sh
aes setup
```

> **Status: pre-1.0, and honest about it.** No release is published yet, so the
> install line above does not work today — build from source. 62 tool
> definitions exist; **24 are marked `tested: true`**, each carrying a
> `tested_on` list so the flag says where it was proven rather than implying
> everywhere. `aes setup` with no flags installs and verifies the default
> profile end to end and exits 0; `aes setup --profile full` does the same for
> the whole catalog, measured on a clean `ubuntu:24.04` container. See
> [Status and limitations](#status-and-limitations).

---

## For agents

`aes` is built to be driven by another program. The stdout/stderr split and
the exit codes are a contract, not an implementation detail.

**Never run bare `aes` in an agent context** — it opens an interactive TUI.
Always pass a subcommand. (With no terminal it falls back to printing help, so
it degrades rather than hanging, but you still get nothing machine-readable.)

```bash
# 1. What would this do? Nothing is modified.
aes setup --dry-run

# 2. See what's installed and what the real state of each tool is.
aes list --json

# 3. Check without changing anything; exits 6 if anything is not OK.
aes verify --json

# 4. Where does the environment disagree with reality?
aes doctor --json

# 5. Do it. Never prompts, never blocks; escalates via `sudo -n` only.
aes setup --non-interactive --json
```

**Output contract**

| Stream | Carries |
|---|---|
| `stdout` | Exactly **one** JSON document under `--json`. Never prose. |
| `stderr` | Human-readable diagnostics, warnings, and printed commands |
| exit `0` | Every resolved action verified |
| exit `2` | Invalid usage or a bad flag |
| exit `3` | Catalog or manifest invalid, or a corrupt state file |
| exit `4` | Unsupported platform |
| exit `5` | **A privileged step needs a human** — run the printed command, then re-run |
| exit `6` | Verification failed after install; run `aes doctor` |

Code `5` exists so you can tell *"you must do something"* from *"it broke"*.
Neither blocking on a prompt nor reporting a failure is a correct response to
`5`.

**Two flags that are not synonyms:**

| | `--yes` | `--non-interactive` |
|---|---|---|
| Suppresses privilege confirmation | yes | yes, via `sudo -n` |
| Requires a TTY | **yes** — exit 2 without one | no |
| Blocks waiting for input | never | never |

`--yes` piped without a terminal is a **usage error**, not a silent downgrade —
that combination is exactly how a CI job ends up waiting on a prompt nobody
can answer. Use `--non-interactive` in automation.

---

## The problem

Setting up a new machine for AI-assisted coding is a scatter of one-off steps:
install ripgrep, git, jq, a terminal multiplexer, a fuzzy finder, two or three
coding agents, wire up `PATH` so the binaries are actually reachable, then
discover a week later that the agent's binary landed in `~/.local/bin` and
nothing can find it. The setup scripts that try to automate this are
shell-soup: they don't know what they installed, they re-run everything, and
they `sudo` without asking.

## The solution

AES treats installation as a **declared, verifiable** process:

- Tools are **declarations** — a `tool.yaml` saying how to install per
  platform, what binary proves it's present, and what version is too old.
  There is no arbitrary shell anywhere in the schema, so a manifest cannot
  smuggle in a command.
- Installation is **resolved, not scripted** — dependencies become a DAG, the
  plan is topologically sorted and deduplicated, and identical input always
  produces byte-identical output.
- Success is **measured, not assumed** — a tool counts as installed only after
  a post-install verification passes. State is a cache; the binary is the
  truth.
- Privilege is **never silent** — when root is genuinely required, AES prints
  the exact command and asks, or uses `sudo -n` in automation and reports
  exit `5` if that isn't available.

## Why not just a shell script?

| | Shell script | Package manager | **AES** |
|---|---|---|---|
| Resumes after interruption | rarely | n/a | **yes — verifies before installing, so tool 27 doesn't redo 1–26** |
| Knows what it installed | no | yes | **yes, and checks reality against that record** |
| Detects drift | no | no | **`aes doctor` compares state against the actual binaries** |
| Arbitrary shell in a config | yes | no | **no — the strategy set is closed** |
| Escalates privilege | silently | prompts | **prints the command, then asks** |
| Deterministic output | no | no | **sorted, deduplicated, byte-identical** |
| Adds tool dirs to `PATH` | manually | no | **`aes env --write`, per install strategy** |

## Design principles

| Principle | What it means here |
|---|---|
| A manifest is data, never a program | No `run:` field. A typo fails validation instead of executing. |
| Privilege is a property of the strategy | No `needs_sudo` field. Only `package` + `apt` escalates. |
| Verify is ground truth; state is a cache | Never decide from `state.json` alone. |
| Corruption is an error, never emptiness | A truncated file must not read as "nothing installed". |
| Refuse loudly rather than fail quietly | An unknown strategy errors listing the valid set. |
| Two frontends, one engine | The TUI and the CLI call the same `selection.Resolve`; identical `Action`s, by construction, not by agreement. |

---

## Commands

```bash
aes setup       # the North Star: install and verify a complete environment
aes list        # catalog tools with status from verification, not from state
aes verify      # check without modifying; exits 6 if anything is not OK
aes doctor      # report environment health and drift (reports; never fixes)
aes env         # print the environment file, or --write it
aes uninstall   # remove a tool through its own strategy
aes forget      # stop tracking a tool; the binary stays put
```

`uninstall` and `forget` are different commands on purpose:

| | `aes uninstall jq` | `aes forget jq` |
|---|---|---|
| Removes the binary | yes, via the tool's strategy | **no** |
| Drops the state entry | yes, but **only after** verifying the binary is gone | yes |
| Right for | a tool AES installed | a tool you already had |

If a strategy claims it removed something and the binary is still there, the
state entry is **kept** and the run exits 6 — dropping it would create the
drift `doctor` exists to find.

### `--dry-run` and `doctor` are the safe pair

```bash
# What would happen? Resolves and prints; touches nothing.
$ aes setup --dry-run --only ripgrep
dry run — nothing was installed and nothing was changed
TOOL     STATUS   DETAIL
ripgrep  present

# Does reality match the record? Below: state records ripgrep as installed
# while its binary is gone. Exits 6.
$ aes doctor
error   ripgrep     state records it as installed but the binary does not resolve; run: aes setup --force
warning             PATH does not contain ~/.aes/bin; run: aes env --write
```

`doctor` never fixes anything, and there is no `--fix`. A diagnostic that
changes your machine without showing you is not a diagnostic.

## Profiles

A profile is a named selection. `require_tested: true` — set on every shipped
profile — refuses to resolve to a tool that hasn't been verified end to end.

```yaml
name: minimal
description: The minimum a working machine needs
include: [git, ripgrep, jq]
require_tested: true
```

Shipped: `minimal` · `developer` · `ai` · `full` · `default` (alias for `ai`).
`--only <tool>` bypasses the profile entirely, so an explicit selection is
never blocked by a broken one.

## Adding a tool

Drop a manifest at `tools/<category>/<name>/tool.yaml`. The directory is for
humans — `category:` in the YAML is the source of truth.

```yaml
name: ripgrep
description: Fast recursive grep
category: search
provides: [rg]
verify:
  command: rg            # the BINARY, resolved on PATH — no arguments
  version:
    command: rg --version   # the command whose output carries the version
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
```

Four things that are easy to get wrong and are validated for you:

- **Ecosystem coordinates must pin a version** — `go_package: x@v1.2.3`.
  An unpinned install resolves to different bytes tomorrow.
- **Arch keys use Go naming** — `amd64`/`arm64`. `x86_64` is a hard error, not
  an alias.
- **`asset` and `sha256` must cover the same arches** — a missing checksum on
  exactly one architecture is the failure mode that ships unverified binaries.
- **`verify.command` is a bare binary, not a command line.** It is resolved with
  `exec.LookPath`, so `rg --version` there never matches anything; the arguments
  belong in `verify.version.command`.

## The 15 invariants

These are the properties the test suite exists to defend.

| # | Invariant |
|---|---|
| I1 | Every tool goes catalog → resolver → actions → installer. No `switch tool.Name`. |
| I2 | Privilege is a property of the strategy, never a manifest field. |
| I3 | No arbitrary shell. Validation rejects fields outside the schema. |
| I4 | State is written **after** verify passes. Install failure leaves it untouched. |
| I5 | State is a cache, not truth. `doctor` detects drift. |
| I6 | `--dry-run` does not mutate. |
| I7 | Never touches `~/.agents/`. |
| I8 | Same input → same `Action`s, byte for byte. |
| I9 | A corrupt manifest is an error, never "not installed". |
| I10 | Never auto-sudo. No `sudo` process without prior output. |
| I11 | A dependency cycle is an error, never a hang. |
| I12 | Unsupported platform is a skip, not an error. |
| I13 | TUI ≡ CLI: same input → same `Action`s. |
| I14 | `sudo` never runs silently — only after printing and confirming. |
| I15 | The default profile contains only `tested: true` tools. |

---

## Status and limitations

This is an active, pre-1.0 project. Stated plainly:

- **No published release.** `install.sh` and the checksum verification are
  complete and tested against a local fixture, but there is no GitHub release
  to download from yet. Build from source for now.
- **24 of 62 tools are `tested: true`, and the rest are not — deliberately.**
  Verification requires both layers: a real install, then `aes verify`
  confirming the result on that same machine. Both run in a fresh
  `ubuntu:24.04` container (`internal/sandbox`, opt-in behind
  `AES_LAYER2=1`), which is the only place a tool can start absent. Every
  version floor, asset name and checksum in the catalog was read from upstream
  during a full-catalog run on a clean container, not assumed; a dozen were
  wrong and are now right.

  The default profile selects **18** tools and 11 of them are unproven, so
  `require_tested` refuses the whole thing. That is I15 working, not a bug —
  the profile would be making a claim nothing has verified. Use `--only`, or
  `--only` plus `--profile` once more tools are proven.

  The proven scope is honest and narrow: `tested_on: [linux/arm64]`, and the
  resolver skips a tool on a host it was not proven on. Two of the remaining
  tools are known-broken and tracked rather than quietly excluded — see
  below.

- **The TUI is new and lightly exercised.** It exists and its selection
  logic is well tested, but it has never been run on a machine other than
  the one that wrote it, and raw mode goes through `stty`. Expect rough edges
  on terminals it has not seen.
- **Ecosystem installs cannot be uninstalled.** `aes uninstall` removes
  github-release and package-manager tools for real, but `go`/`npm`/`cargo`/
  `uv` have no supported per-package removal, so AES reports *not removed*
  with the manual route rather than pretending. `aes forget` always works.
- **Two catalog entries in the default profile cannot install**, both found by
  running the container rather than by reading the manifests. `aadc` publishes
  `.tar.xz` assets and the extractor is pure-Go gzip, so it needs a capability
  the installer does not have. `gemini` installs and then dies — its npm
  package declares `engines: node >=20` and Ubuntu 24.04 ships 18.19.1 (codex,
  claude and opencode all run fine on 18; it is gemini alone). Both are open
  decisions rather than exclusions.
- **The `tested: true` scope is one platform.** The flag is a bool plus a
  `tested_on` list; nothing has been proven on darwin or linux/amd64 yet.
- **CI runs on pull requests.** `ci.yml` does fmt+vet, a test matrix across
  macOS arm64 and Ubuntu amd64, and a build job that unpacks a fresh artifact
  and runs `aes list --json` against a throwaway HOME — the check that catches a
  binary that cannot find its own catalog.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `profile "default" requires tested tools, but these are not tested: …` | I15; the profile selects 18 tools and 11 are unproven | Use `--only <tool>` to bypass the profile |
| `catalog root: stat tools: no such file or directory` | Binary built before the catalog was embedded | Rebuild with the current tree |
| `profile "x": no tool named "y"` | Profile references a tool not in the catalog | Fix the profile; a dangling reference is a hard error by design |
| Exit `5` | Something needs root | Run the command AES printed, then re-run |
| Exit `6` after `aes setup` | A tool installed but doesn't verify | `aes doctor` — often a `PATH` problem |
| Tool installed but "not runnable" | Its strategy's bin dir isn't on `PATH` | `aes env --write` then source the file |
| `state file is corrupt` | Truncated or hand-edited | Restore from backup; AES will not guess |

## FAQ

**Does it need Go, brew, or apt installed first?**
No. The binary is fully self-contained and cross-compiled with `CGO_ENABLED=0`.
The catalog and profiles are embedded in it, so an installed binary needs
nothing beside itself.

**Will it `sudo` without asking?**
No. The `sudo` gate is structural: a command containing `sudo` is refused
without executing. The only privileged path requires that the command was
printed and confirmed, or `sudo -n` in automation.

**Why aren't more tools marked `tested: true`?**
Because the flag means a real install was verified end to end, in a fresh
container, and 24 tools have been through that. The rest install — they run
under `--profile full`, where a floor that only holds on the platform it was
measured on still refuses to over-claim. Marking a tool on detection evidence
alone would make the tool's central claim — a *verified* environment — a lie,
which is the one thing the flag exists to prevent.

The flag also carries `tested_on`, so "verified" is a claim about a named
platform rather than about the world. `aes` on darwin will skip a tool proven
only on linux/arm64 rather than assume the other platform behaves the same.

Worth being precise about why, because an earlier version of this file got it
wrong. The two layers do NOT have contradictory preconditions in general: a
tool that is neither installed nor proven passes both in sequence, since Layer 2
installs it and Layer 1 then detects it. The real constraint was narrower — the
Layer-1 fixture list is a fixed set of eight tools that are all pre-installed
on the machine that ran the tests, so every one of them is refused by Layer 2's
"must start absent" rule.

The container is worth having for a better reason than impossibility: a
container-sourced flag means "this works on a clean machine", which is the claim
the North Star actually makes.

**Can a malicious `tool.yaml` run an arbitrary command?**
No. The strategy set is closed to six values — `github-release`, `package`,
`go`, `npm`, `cargo`, `uv` — unknown fields are rejected at parse time, and
there is no free-form shell field. A manifest can only name a strategy and
its arguments.

**Does it touch `~/.zshrc`?**
Not unless you explicitly pass `--link-shell`, which backs the file up first
and manages only its own delimited block.

**How do I see what it would do?**
`aes setup --dry-run` resolves and prints the full plan, changes nothing, and
exits 0.

## License

MIT — see [LICENSE](LICENSE).

Chosen deliberately over a rider. AES exists to be *used* with an AI coding
assistant: the normal workflow is to paste `aes doctor` output into one and ask
what it means. A clause restricting "making the software available" to named
companies would read as forbidding exactly that, which is the product working
as intended. Plain MIT also keeps the widest adoption and no ambiguity about who
may read the source.

## Development

```bash
make check              # gofmt check + vet + race — what CI runs
make test               # go test ./...
make bootstrap-test     # install.sh against a local fixture, no network
make dist               # cross-compile the release matrix + checksums.txt
```

`AGENTS.md` is the working guide for agents: layering rules, the traps that
have cost time, and the mutation-testing discipline the suite is built on.

See [`docs/COMPREHENSIVE_FOR_AES.md`](docs/COMPREHENSIVE_FOR_AES.md) for the
full architecture and engineering specification.
