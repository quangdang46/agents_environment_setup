# AES — handoff

Written at the end of a long working session, for picking this up somewhere
else and finishing it. Everything below is measured, not remembered; where a
number is a claim rather than a measurement, it says so.

- **Repo:** `github.com/quangdang46/agents_environment_setup`
- **Pushed at:** `becd242` on `main`, 98 commits, tree clean
- **Go:** 1.26.4 · **Module:** `github.com/quangdang46/agents_environment_setup`

---

## 1. What this tool is

`aes setup` takes a machine from nothing to a complete, verified AI coding
environment and **exits 0 only when it actually is**. One command, no shell
soup, privilege never silent, and success measured by re-verifying rather than
assumed from a package manager's exit code.

Read these two first — they are the contract, and everything else is downstream
of them:

- **`AGENTS.md`** — working guide. Read the traps section; every one of them
  cost real time here and several cost an hour.
- **`docs/COMPREHENSIVE_FOR_AES.md`** — the spec. It wins when code and spec
  disagree.

---

## 2. State right now

| | |
|---|---|
| Build + vet | green |
| Test suite | green, **620 test functions**, 15 packages |
| Catalog | **54 tools** — github-release 32, package/apt 16, npm 4, cargo 2 |
| `tested: true` | **7**, every one carrying `tested_on: [linux/arm64]` |
| Beads open | `gj9` (P1, mine) · `8tv` (P1, mine) · `9x0` (P3, theirs) · `rp7` (P3, filed deliberately) |
| Published | **no release yet** — `9x0` is held on purpose |

`go build ./... && go vet ./... && go test ./...` is the whole story for
"is it healthy".

### The gated tests, and why they are gated

Two tests install software or ask a package index, so they do not run by
default. **Both are the ones that matter.** Run them before believing anything
about the product.

```bash
# Layer 2: real installs in a fresh ubuntu:24.04 container
AES_LAYER2=1 go test -v -run TestLayer2Container ./internal/sandbox/

# the catalog's apt coordinates, asked of apt rather than guessed at
AES_CATALOG_CHECK=1 go test -v -run TestAptCoordinates ./internal/sandbox/

# keep the container and its sandbox for debugging
AES_KEEP_SANDBOX=1 AES_LAYER2=1 go test -v -run TestLayer2Container ./internal/sandbox/
```

Both **skip** rather than fail when there is no container runtime, and they skip
under `-short`. `go test -short ./...` therefore does *not* exercise either —
that is deliberate, and it means a green plain `go test` says nothing about
whether the product works.

---

## 3. The one thing that will surprise you

**`aes setup` with no flags exits 1 on a machine that is not linux/arm64.**

Not a bug. Every proven tool carries `tested_on: [linux/arm64]`, and the
resolver *skips* a tool it cannot vouch for on the host asking. On macOS the
default profile therefore selects nothing.

```
$ aes setup --dry-run
aes: profile "default" requires tested tools — no install has been proven anywhere
for: bat, bottom, btop, codex, dust, eza, gemini, ntm, opencode, zellij; proven
only on linux/arm64, not on this host (darwin/arm64): claude, tmux
exit status 1
```

Note the two halves of that sentence: ten tools have **no proof anywhere**, and
two have proof that is simply not about this machine. They are different
problems and `--allow-unproven` only waives the second.

`--allow-unproven` waives the **platform** half only. Tools with no proven
install anywhere still block, because there is nothing to accept on their
behalf.

**This is the open product question** (§5.1). It is not a thing to fix quietly.

---

## 4. What the container proved

A fresh `ubuntu:24.04`, the real binary, absence measured *inside* — not
inferred from the host, because on a macOS laptop `git` and `jq` are already
installed and a host-side check would call them present.

**11 of the 12 tools the default profile selects pass** the full seven-step
sequence. All three strategies. ~185s.

```
pass  bat bottom btop claude codex dust eza ntm opencode tmux zellij
FAIL  gemini
```

`gemini` fails for a real reason: its npm package declares
`engines: node >=20`, Ubuntu 24.04 ships 18.19.1, so it installs and then dies
with `SyntaxError: Invalid regular expression flags`. `codex`, `claude` and
`opencode` all run fine on Node 18 — it is gemini alone.

The list is **derived from `profiles/default.yaml`**, not hand-maintained. It
was, and it went stale the moment a tool left the catalog: the test still asked
for `aadc`, failed with "not in the catalog", and reported a catalog change as
a Layer 2 product failure. Do not reintroduce a hand-kept list.

---

## 5. Remaining work, in the order I would take it

### 5.1 The product decision nobody has made

`gj9` cannot close while this is open, and it is not mine to settle.

**Is a `tested: true` flag allowed to be set from evidence the host asking
cannot obtain?**

The argument for "no, require a darwin proof first" is that a flag gated on
unobtainable evidence is a flag that never gets set — which is where this
project started. The argument for "yes" is that macOS users have a dead end
today: `aes setup` exits 1 and the only way forward is `--only` per tool.

Current state is the honest middle: linux/arm64 evidence is linux/arm64
evidence, macOS says plainly it has none, and `--allow-unproven` is the
explicit opt-in. **Decide it deliberately and record it in the spec**, not in a
commit. Both agents agree it should not be settled by whoever writes code first.

### 5.2 `gj9` — close the loop

Open. Needs the decision above, then whatever falls out of it. The container
harness itself is done and green.

### 5.3 `8tv` — version floors are one number; platforms supply many

P1, mine. Half done.

**Landed:** the check that asks apt, inside the container
(`AES_CATALOG_CHECK=1`, `catalog_apt_test.go`). It compares every apt-backed
tool's declared floor against the version apt *would install*, and it found
`kubectl` naming a package with no installation candidate.

**Not done, and it is a schema question, not a patch.** `verify.version.min` is
a single string meaning "the newest version aes has ever seen" rather than
"what this platform can supply". A floor above what a platform offers reports
stale forever, and `node` at `min: 20.0` against noble's 18.19.1 made every
AI tool uninstallable. Options: per-platform floors, or `verify` distinguishing
"below my floor" from "below what this platform can supply" so an
unsatisfiable tool is *unavailable* rather than *stale*.

### 5.4 `rp7` — tools the strategy set cannot express

Filed deliberately as a **decision record, not an action item**. Do not treat
it as a to-do.

`kubectl` (binaries only on `dl.k8s.io`) and `aadc` (`.tar.xz` against a
gzip-only extractor) cannot be installed by any current strategy. A `url`
strategy with mandatory `sha256` was proposed and **rejected**: it is the first
place aes would fetch from an address the *catalog* chose rather than the
strategy set. A checksum protects integrity, not intent. Two entries out of 56
was not worth re-opening I3, and the ratio would not change the answer.

If it is ever built, the acceptance tests are in the bead, and those two tools
must not be deleted from it.

### 5.5 `9x0` — publish

Held deliberately. Do not publish. The DoD wants a boot that works end to end,
and it does not yet on a stock macOS.

---

## 6. Bugs found and fixed this session — read these before touching the code

These are the ones that were not caught by a failing test, and each is a shape
worth recognising.

| Commit | What was wrong |
|---|---|
| `8ee0529` | **`aes uninstall` removed nothing and exited 0.** Install wrote `installName()`, uninstall hunted `binaryName()`, and the CLI set `Name` on only one of the two paths. For `yq` (archive ships `yq_darwin_arm64`) it looked for a file that never existed, said "not removed", and returned success. **Indistinguishable from the deliberate not-removed path** — same output, different cause. |
| `1cb80b1` | **`binary:` was never validated.** `Target.present()` omitted it so four strategies' `rejected` entries could never fire; `validateArchMaps` only ran for github-release. `binary: {x86_64: …}` parsed on a `go` target — the exact hard-validation error AGENTS.md promises. |
| `184a46b` | Privilege was derived **twice**, byte-identical, while the comment on one copy claimed the derivation lived there "so the resolver… cannot drift apart". |
| `70f8bb2` | eu4: a broken ecosystem install destroyed a working binary. Needed a *second* signal, because `StatusUnknown` covers both "ran and printed garbage" and "ran and exited non-zero". |
| `c60df3c` | `env.sh` exported `AES_HOME=/opt/.aes` — a directory that never existed — whenever `AES_HOME` was not the default. |
| `becd242` | `aes doctor` now reports an alias shadowing a verified binary. ACFS shipped that bug (`alias br='bun run'` hiding the binary). |
| `c5bf81b` | A tool proven on linux/arm64 was reported as **"not tested"** on macOS. A false statement: the evidence existed. |

---

## 7. The discipline that produced most of those

**Prove the mutation applied before you believe the result.** A mutation that
did not compile, one that never ran, and a test that passes for the wrong
reason all look identical from the outside. This went wrong *seven times* in
one session across two agents, in these shapes:

- a `sed`/python anchor that did not match, so the file was unchanged and
  `go test` correctly reported a **cache hit** — read as a passing suite
- a `-run` filter that excluded the very test which would have caught it
- a test asserting `"binary"` was in the error, satisfied by a completely
  unrelated error that also mentioned `binary`

Also worth internalising: a **hand-written tool list in a test is a second
source of truth**, and it drifts silently. Derive it.

`AGENTS.md` now carries most of these. VioletOtter was writing up *"a check
that cannot fail is not a check"*.

---

## 8. Two working agreements with the other agent

**VioletOtter** (`agents-environment-setup-*`, the only other agent) shares
this working tree **and this git identity** — `git status` before staging,
always. MCP Agent Mail has been **down for both of us**; the peer
`SendMessage` channel is what actually works. Treat "no mail" as silence, not
as absence.

Division: they hold `tools/` (catalog) and `9x0`; I hold `internal/installer`,
`internal/sandbox`, `internal/verifier`, `internal/cli` and `eu4`/`gj9`/`8tv`.
Coordinate before editing anything shared — every collision here cost more than
a message would have.

---

## 9. Three traps you will hit within the hour

From `AGENTS.md`, the ones most likely to bite in a *new* environment:

1. **This shell is zsh, which does not word-split an unquoted variable.**
   `FLAGS="-e A=1"; docker exec $FLAGS $C cmd` passes **one** argument, and
   the command runs with the wrong environment and **no error**. Cost an hour
   chasing a `envgen` bug that did not exist.
2. **A bind-mounted file is bound by inode.** `go build -o ./aes` after the
   container started leaves it running the old binary, so a fix verifies as
   still broken. Recreate the container.
3. **A file named `*_linux_test.go` carries an implicit GOOS build
   constraint** and will not run on macOS at all — reporting "no tests to run",
   forever.

Plus: `cat` is aliased to `bat` and macOS `ls -l` emits `@` in the permissions
field, which shifts every `awk` column. Use `wc -c` and `shasum`.

---

## 10. First hour in a new environment

```bash
git pull && go build ./... && go vet ./... && go test ./...   # expect green
docker version                                                  # needed for the gated tests
AES_CATALOG_CHECK=1 go test -v -run TestAptCoordinates ./internal/sandbox/   # ~20s
AES_LAYER2=1 go test -v -run TestLayer2Container ./internal/sandbox/       # ~185s
go run ./cmd/aes setup --dry-run                                 # read the refusal carefully
```

Then read §3 and §5.1, and make the decision rather than inheriting it.
