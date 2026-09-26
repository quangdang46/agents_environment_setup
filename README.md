# aes — Agents Environment Setup

<div align="center">

![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-blue.svg)
![Go](https://img.shields.io/badge/Go-1.26-00ADD8.svg)
![Status](https://img.shields.io/badge/status-pre--1.0%20%7C%20no%20tested%20tools%20yet-orange.svg)

</div>

**One command takes a machine from nothing to a complete AI coding environment — and exits 0 only when it actually is.**

`aes setup` resolves a profile into a dependency-ordered plan, verifies what's already there, installs only what's missing, verifies again, and refuses to call it done until the tools are real.

```bash
curl -fsSL https://raw.githubusercontent.com/quangdang46/agents_environment_setup/main/install.sh | sh
aes setup
```

> **Status: pre-1.0, and honest about it.** No release is published yet, so the
> install line above does not work today. 47 tool definitions exist; **0 are
> marked `tested: true` yet**, which by design means `aes setup` with no flags
> currently resolves to nothing and exits non-zero. See
> [Status and limitations](#status-and-limitations) — this is the intended
> behaviour, not a bug, and the reasoning is below.

---

## For agents

`aes` is built to be driven by another program. The stdout/stderr split and
the exit codes are a contract, not an implementation detail.

**Never run bare `aes` in an agent context** expecting a TUI — there isn't one
yet. Use the subcommands directly.

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
| Two frontends, one engine | The TUI will call the same resolver; identical `Action`s, by construction. |

---

## Commands

```bash
aes setup      # the North Star: install and verify a complete environment
aes list       # catalog tools with status from verification, not from state
aes verify     # check without modifying; exits 6 if anything is not OK
aes doctor     # report environment health and drift (reports; never fixes)
aes env        # print the environment file, or --write it
```

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
```

Three things that are easy to get wrong and are validated for you:

- **Ecosystem coordinates must pin a version** — `go_package: x@v1.2.3`.
  An unpinned install resolves to different bytes tomorrow.
- **Arch keys use Go naming** — `amd64`/`arm64`. `x86_64` is a hard error, not
  an alias.
- **`asset` and `sha256` must cover the same arches** — a missing checksum on
  exactly one architecture is the failure mode that ships unverified binaries.

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
- **Zero tools are `tested: true`.** Verification requires both Layer 1
  (fixtures on a real machine) and Layer 2 (a real install in a sandbox), and
  Layer 2 is not built. So `aes setup` with **no flags resolves to nothing and
  exits non-zero** — which is I15 working as designed. Use `--only` or a
  profile until Layer 2 lands.
- **No TUI.** `aes` with no arguments prints help. The design constraint is
  locked (no free-form commands, no separate resolver, no business logic in
  the UI) but it isn't built.
- **`aes uninstall` / `aes forget` are not built.** The split between them
  (remove the thing vs. forget the record) is specified; neither exists.
- **Layer 2 sandbox is not built**, which is why nothing can honestly be
  marked `tested: true`.
- **I7 has no automated guard.** Nothing writes to `~/.agents/`, but nothing
  would catch a regression either.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `profile "default" requires tested tools, but these are not tested: …` | I15; no tool is verified yet | Use `--only <tool>`, or a profile, until Layer 2 lands |
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

**Why is nothing marked `tested: true` yet?**
Because `tested: true` means "a real install was verified end to end", and the
sandbox that proves that (Layer 2) doesn't exist. Marking tools tested on
detection evidence alone would make the tool's central claim — a *verified*
environment — a lie, which is the one thing the flag exists to prevent.

**Can a malicious `tool.yaml` run an arbitrary command?**
No. The strategy set is closed to five values, unknown fields are rejected at
parse time, and there is no free-form shell field. A manifest can only name a
strategy and its arguments.

**Does it touch `~/.zshrc`?**
Not unless you explicitly pass `--link-shell`, which backs the file up first
and manages only its own delimited block.

**How do I see what it would do?**
`aes setup --dry-run` resolves and prints the full plan, changes nothing, and
exits 0.

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
