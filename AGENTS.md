# AGENTS.md — working in `agents_environment_setup`

AES is an **Agent Developer Environment Manager**. One command, `aes setup`, takes a machine
from nothing to a complete, verified AI coding environment, and exits 0 only when it is done.

This file is the *working guide*: where things are, how to run them, how the swarm coordinates,
and which traps cost someone an hour. The design contract — architecture, invariants, the Go
type shapes, exit codes — lives in [`docs/COMPREHENSIVE_FOR_AES.md`](docs/COMPREHENSIVE_FOR_AES.md).
**Read that first.** This file assumes you have.

---

## Orientation

The repo is at the **plan-locked, code-in-progress** stage. `docs/COMPREHENSIVE_FOR_AES.md` is the
authoritative spec and is treated as a contract, not a suggestion. When code and spec disagree,
the spec wins and the code is wrong.

```
cmd/aes/            main entrypoint (thin; all logic lives in internal/cli)
internal/
  manifest/         Tool/Target/Verify types + validation. Pure: stdlib + yaml.v3.
  catalog/          loads tools/**/tool.yaml, indexes by name/category/tag
  platform/         Host{OS,Arch,Manager,Prefix}, Supports/Target/Has
  resolver/         Resolve(c, req, h) -> []Action. deps DAG + cycle detection
  verifier/         the ground truth. presence + version. never reads state
  state/            ~/.aes/state.json, atomic. a CACHE, not truth
  exec/             runs external commands. the sudo gate lives here
  installer/        strategy registry: github-release, package, go/npm/cargo
  envgen/           deterministic ~/.aes/env.sh with an ownership marker
  cli/              command tree, flag contract, exit-code mapping
profiles/           minimal · developer · ai · full          [not yet — bead wi1]
tools/<category>/<name>/tool.yaml                            [not yet — bead 0ls]
```

Bracketed entries are planned, not present. `catalog` and `verifier` are useful before `tools/`
exists — they have fixture-driven tests — but the catalog itself lands with `0ls`.

**Layering is one-directional.** `manifest` imports nothing but stdlib + yaml. `state` and `exec`
import nothing internal. `verifier` may use `exec`; the `installer` may use everything. Never
import `installer` from `resolver`, and never let a UI package hold business logic.

---

## Build and test

```bash
go build ./...
go vet ./...
go test ./...              # everything
go test -race ./...        # before you close a bead
go test -short ./...       # skips the Layer 1 real-machine fixtures
go test -cover ./internal/<pkg>/
```

A bead is not done when it compiles. It is done when the tests assert the **contract**, and you
have checked that they would actually fail if the contract were broken.

### Layer 1 / Layer 2

- **Layer 1** (`internal/verifier/layer1_test.go`) — verifies tools already on the machine. No
  mutation. Runs everywhere, skips per tool when absent.
- **Layer 2** (bead `le2`) — real installs in a sandbox. Mutates the machine.

A tool may only be `tested: true` once **both** pass. Layer 1 alone is not sufficient.

---

## Traps

**The pre-spec `plugin.yaml` design is gone.** An early `internal/manifest` implemented `Plugin`,
`Target.Run`, `Target.NeedsSudo`, `Env` and `Hooks` — all of which the spec removed. That file was
rewritten from scratch. If you find prose describing a `run` field or a `needs_sudo` flag, it is
stale: **`run` is forbidden by I3** (a manifest is data, never a program) and **privilege is a
property of the strategy, never a field** (I2). Do not reintroduce either, and do not preserve
that old shape out of politeness — it will regrow the hole.

**Three architecture vocabularies collide. AES speaks Go naming everywhere.**

| Layer | Vocabulary | Source |
|---|---|---|
| `install:` map key | `darwin` · `linux` | `runtime.GOOS` |
| `Host.Arch`, `asset:`, `sha256:` keys | `arm64` · `amd64` | `runtime.GOARCH` |
| `uname -m` | `arm64` · `x86_64` | only when shelling out |

There is no translation layer inside AES. `x86_64` in an asset key is a **hard validation error**,
never an alias. The one place that reads `uname` translates once.

**Never assert the host's own identity in a test.** A test that reads `runtime.GOOS` and compares
it to `runtime.GOOS` passes on the machine that wrote it and fails on everyone else's. Construct
`platform.Host{...}` literally, or assert a property that holds regardless of host.

**Check presence with `exec.LookPath`, never a shell.** On a configured zsh, `command -v tmux`
returns an *alias* for a plugin wrapper — the assertion passes against something that is not a
binary.

**`sudo` is refused in `exec.Run`, structurally.** It scans the command for sudo and returns
`PrivilegeError` **without executing anything**. Do not add a bypass. The decision to escalate
lives in the installer: print the command, ask, and only then run. A tool needing a package
manager as root is `package`+`apt`; the privilege is derived, never declared.

**Corruption is an error, never an empty state.** A corrupt `state.json` or `tool.yaml` must fail
loudly. Reading corruption as "nothing installed" causes a reinstall storm — one truncated JSON
file makes AES reinstall everything on the machine.

**A failure that fires against correct code sends you to fix the wrong file.** Two that have
actually cost time here:

- A substring assertion: `"go/bin"` is a substring of `"cargo/bin"`, so a staleness check fires on
  a correct file.
- A newline inside a Go **raw** string is a literal backslash-n, not a line break — so an embedded
  `sh -c` script is a syntax error while the generated file round-trips perfectly.

Both produce convincing errors. When a test fails, check whether the code is wrong before you
conclude it is; re-reading the failing output by hand is cheap and catches this class immediately.

**Parsing is not resolving.** A profile naming a tool or a tag that does not exist parses
perfectly and then fails at run time, taking the whole binary down — `aes setup --dry-run` exits 1
on a profile it happily loaded. A test that only checks "the file parses" cannot catch this. Ship
one that walks every `include` and every selector in `profiles/*.yaml` and asserts each resolves
against the real catalog. It must check *references* rather than `Resolve()`, so it stays valid
while the catalog has no `tested:` tools yet — a profile naming something nonexistent never is
legitimate, a catalog with nothing tested is.

**There is no `plan`.** The value between the resolver and the installer is an `Action`. The
`aes plan` feature was cut; do not let it regrow under another name (`InstallPlan`, `PlanCommand`,
`PlanRenderer`, `PlanJSON`). `--dry-run` stays — it is a safety mechanism, not a product surface.

**`--yes` and `--non-interactive` are not synonyms** and neither implies the other. See the
flag-semantics table in the spec. `--yes` asserts a TTY exists and is a usage error (exit 2)
without one; `--non-interactive` never blocks on input and resolves privilege via `sudo -n`,
finishing with exit code 5 if a manual step is needed.

---

## Working a bead

```bash
bv --robot-plan                      # dependency-respecting plan; start at the head of a track
br list                              # what is open
br show <id>                         # the full contract
br update <id> --assignee <you> -s in_progress --notes-push "..."
br close <id> --reason "what landed, how it was verified"
```

Claim a bead before touching it, and **do not start a blocked one** — the block usually means
another agent is mid-flight in a package you would collide with. `internal/exec`, `internal/state`,
`internal/manifest`, `internal/platform`, `internal/catalog`, `internal/resolver` and
`internal/installer` have all been live-edited by two agents at once; a reservation or a message
is cheaper than a merge.

Write the tests from the bead's contract *before* the implementation. The contracts are written as
prose precisely so the tests are not a transcription of whatever the code happens to do.

### Prove the test can fail

A test that has never failed is not evidence. After the suite is green, break the load-bearing
guard on purpose and confirm the suite catches it:

```bash
cp internal/foo/foo.go /tmp/foo.bak
# ...remove the invariant...
go test ./internal/foo/     # must FAIL
cp /tmp/foo.bak internal/foo/foo.go
```

This is how several real defects were caught here: a corrupt state file read as empty, the sudo
gate deleted, `provides` entries not checked, dotted-integer comparison swapped for semver, the
deterministic tie-break removed. Each of those passed review and only failed under mutation.
A mutation that *doesn't* fail the suite is also a finding — it means the test cannot distinguish
correct behaviour from the mutation, and the test is too weak.

---

## Swarm coordination

Agents coordinate over **MCP Agent Mail** (project key: the absolute repo path).

- `register_agent` — omit `name` and let it be generated; names must be adjective+noun.
- `fetch_inbox` **at the start of every turn**, and acknowledge anything with `ack_required`.
- Message a peer **before** starting work in a package they might hold, and **after** landing a
  bead that unblocks them.
- Report a **correction as loudly as the original claim.** A confidently wrong bug report costs
  a collaborator more than a late one. Say what was wrong, show the evidence, and state the
  narrower true version.
- When two reasonable readings of the spec exist, **propose one and record it in the spec doc**.
  A decision that lives only in a commit message is a decision the next agent will relitigate.
