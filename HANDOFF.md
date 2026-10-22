# AES — handoff

Written at the end of a working session, for picking this up somewhere else and
finishing it. Everything below is measured, not remembered; where a number is a
claim rather than a measurement, it says so.

- **Repo:** `github.com/quangdang46/agents_environment_setup`
- **Branch:** `main` · **History:** 175 commits · **Tree:** clean at the commit
  that carries this file
- **Go:** 1.26.4+ · **Module:** `github.com/quangdang46/agents_environment_setup`

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
| Test suite | green, **415 test functions**, 16 packages, `-race` clean |
| Catalog | **68 tools** — github-release 41, package/apt 20, npm 7 |
| `tested: true` | **63**, each carrying a `tested_on` list |
| `default: true` | **63** — the same 63 (see §3.2) |
| Not verified | 5: `pi` `br` `bvr` `discord-cli` `hashline` |
| Beads open | `9x0` (P3, publish — in progress; see §5.3) |
| Published | **v0.1.0** at `70e4cb8`; this commit and the six before it are unreleased |

```bash
go build ./... && go vet ./... && go test ./...   # the whole story for "is it healthy"
```

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

## 3. What this session changed

### 3.1 The profiles feature is gone

`internal/profile/`, `profiles/*.yaml`, the `--profile` flag, the `Profile`
field on four types, the TUI's "Profiles" screen, and the profile
materialisation in `cmd/aes/main.go` and `embed.go` are all deleted
(`8bfc8cf`).

The premise for the removal was that "`aes setup` installs everything anyway".
It did not: the old default profile selected **10 of 68** tools — `bat`
`bottom` `btop` `claude` `codex` `dust` `eza` `opencode` `tmux` `zellij` — by
matching the tags `terminal, ai`. The profiles were the only thing deciding
what a bare `aes setup` installs, and the manifest field built for the job —
`default: true` — was never set on a single tool in the catalog. The feature
was a selection mechanism sitting on top of an unused one.

The `full` profile was worse than useless: it selected by **category**, so it
picked up every untested tool, and `require_tested: false` could not save it
because a tool with no evidence anywhere has nothing to waive. Measured
2026-09-28, `aes setup --profile full` and `--profile full --allow-unproven`
both exited 1, listing the same 29 untested tools. The name of the profile
promised the one thing it could not deliver.

### 3.2 The catalog's default set is the selection now

Every tool that is `tested: true` is also `default: true` (63 of 68). So,
captured from a real build:

```
$ aes setup --dry-run
═══ aes setup v0.1.0-6-g12c60ce — 63 tools ═══
dry run — nothing will be installed

63 tools selected
     CATEGORY    TOOL                  INSTALL                                                        WHY
1.   [utility]   aadc                  -> Dicklesworthstone/aadc@v0.1.1                               (default)
2.   [utility]   age                   -> FiloSottile/age@v1.3.2                                      (default)
3.   [search]    ast-grep              -> ast-grep/ast-grep@0.45.3                                    (default)
...
22.  [git]       git-lfs               -> package(apt) git-lfs                                        (default)
23.  [utility]   gnupg                 -> package(apt) gnupg                                          (default)
24.  [runtime]   go                    -> package(apt) golang-go                                      (default)
```

The five unverified tools stay in the catalog and install on request with
`--only <tool>`. They are not in the default set because a tool that has never
been installed anywhere is exactly the one that must not be handed to a user as
part of a *verified* environment (invariant I15).

`internal/catalog/tools_test.go` asserts that every `default: true` tool is
also `tested: true`, so the default set can never grow a tool nobody has
proven.

**Naming no tool and selecting nothing are different intents**, and the
difference is carried in the type rather than recomputed by each caller:
`selection.Selection.Defaults` says that an empty `Only` means the catalog's
default set, and the CLI sets it when `--only` was not given. The TUI leaves
it false, because nothing ticked means nothing.

### 3.3 Two product bugs the verification found

Both are in the **repair** path (`aes setup --force`), and both were found by
running the product, not by reading it.

**`--exclude` did not remove a tool from the resolved set** (`8bfc8cf`). It
only reported an exclusion that broke a dependency; a tool the user excluded
and then watched get installed was the flag lying. An excluded tool is never a
root now, and naming one with `--only` while excluding it is an error (exit 2)
rather than a silent choice.

**A forced apt repair reinstalled the wrong package** (`12c60ce`).
`apt-get install --reinstall` re-extracts the packages it is given and nothing
else, and the package a manifest names is often not the package that ships the
file the verifier looks at:

| tool | manifest names | the file is owned by |
|---|---|---|
| `rust` | `cargo` | `rustc` |
| `gnupg` | `gnupg` | `gpg` |
| `postgres18` | `postgresql-18` | `postgresql-client-18` |

A **first** install works for all three, because the named package pulls the
owner in as a dependency. A **forced repair** does not: the reinstall restores
the named package's own files, the binary the user lost lives in another
package, and nothing puts it back. The repair then reported "installed but
verification says missing" — on a machine where the tool was still broken. The
owner is not a second source of truth to maintain: dpkg already knows, and
`dpkg -S` answers it.

### 3.4 The Layer 2 harness

**One container per tool now** (`8746aaa`). The sweep used to share a single
container, and a shared container is a machine that remembers: `docker`'s
install drags `git` in as a dependency, so by the time the sweep reached `git`
the tool was already present and the run could prove nothing. The same
contamination reached the apt state, and later package installs failed for
reasons that had nothing to do with the tool under test. Freshness is the
property the whole proof rests on.

**Failures now carry the command's own output** (`33bf1a1`, `7da4c98`). Both
the product's installer and the harness reported a failed `apt` as "exit code
100" and nothing else, while the reason — dpkg's own line — was captured and
thrown away. It cost a whole sweep to find out: sixteen tools reported "prepare
container: … exit status 100", and the reader could not tell a mirror refusing
connections from a package list that would not resolve. Thirteen of the sixteen
passed when re-run in a clean window.

**A tool the base image already ships is a SKIP, not a failure** (`7da4c98`).
`bash`, `node`, `npm` and `python` cannot be absent in a stock `ubuntu:24.04`,
so the absence precondition fires for all four. That was reported as a FAILURE,
which says the tool is broken — and it is not: they are verified tools on real
machines, and a failure here would invite demoting them from `tested: true`
because of what an image happens to contain.

**The vault refusal was diagnosed wrongly** (`8746aaa`). The message blamed a
seccomp profile. It is not seccomp, and it is not the profile: measured, plain
`docker run` and `--cap-add=SETGID` and `--cap-add=SETFCAP` all fail the same
way and only `--cap-add=ALL` runs. The real mechanism is a Linux **file
capability**. The `.deb` records `cap_setgid` on the binary; dpkg's
`linux-capabilities` trigger applies it — but only once `setcap` is on the
machine, and it is not in a bare `ubuntu:24.04` image. It *is* once something
has pulled in `libcap2-bin`, and `docker.io` does. So the same `.deb` installs
two ways:

```
vault installed on its own      -> no capability applied -> the binary runs
vault installed after docker.io -> capability applied -> execve is EPERM
```

and once the capability is applied the container's bounding set cannot grant
it, so the kernel refuses the exec. `setcap -r /usr/bin/vault` removes it and
the binary runs. The message now names the mechanism and the fix; the vault
manifest records the whole measurement, including that a real Linux host grants
the capability and runs the same binary.

---

## 4. What the Docker verification actually proved

Everything below was run against the real binary in a fresh `ubuntu:24.04`
container, with absence measured *inside* the container rather than inferred
from the host.

### 4.1 The aggregate run

One container, `aes setup` with no flags (all 63 default tools), then
`aes verify` over the whole 68-tool catalog:

| status | count | which |
|---|---|---|
| `ok` | **62** | installed and verified with a parsed version |
| `missing` | 5 | `br` `bvr` `discord-cli` `hashline` `pi` — the unverified ones, correctly excluded |
| `unknown` | 1 | `vault` — the file-capability refusal above |

### 4.2 The full per-tool sweep

63 tools, a fresh container each, the full seven-step sequence (install →
verify → state → idempotent → drift detected → force reinstall → present
after):

| verdict | count | which |
|---|---|---|
| **PASS** | **58** | every default tool except the four below |
| **SKIP** | 4 | `bash` `node` `npm` `python` — the base image ships them, so absence cannot hold |
| **FAIL** | 1 | `git-lfs` — see §5.1 |

Three of the four failures at the last step (`gnupg`, `rust`, `postgres18`)
were the wrong-package bug of §3.3 and pass now. The fifth, `go`, was an
environment failure (a contended apt mirror during the sweep) and passes when
re-run alone.

---

## 5. Remaining work, in the order I would take it

### 5.1 `git-lfs` — the last red Layer 2 test

`git-lfs` installs, verifies, writes state, is idempotent, and its drift is
detected. It fails at the **last** step: `--force` cannot restore the binary.

The cause is not aes. `apt-get install -y --reinstall git-lfs` makes dpkg run
the package's **pre-removal script** first, and that script is
`git lfs uninstall --skip-repo --system` — it needs the very binary the
reinstall is restoring, so it fails, dpkg gives up, and the binary is never
put back:

```
dpkg: warning: old git-lfs package pre-removal script subprocess returned error exit status 1
dpkg: error processing archive .../git-lfs_3.4.1-1ubuntu0.4_amd64.deb (--unpack):
 new git-lfs package pre-removal script subprocess returned error exit status 1
E: Sub-process /usr/bin/dpkg returned an error code (1)
```

Three experiments, measured in a real `ubuntu:24.04` container (2026-10-22):

| # | the drift | the repair | result |
|---|---|---|---|
| A | binary deleted, then `dpkg --remove` | `apt-get install --reinstall` | **worse than the original failure** — `E: Internal Error, No file name for git-lfs:amd64`; apt can no longer resolve the package at all |
| B | `apt-get remove --purge` (binary intact) | `apt-get install --reinstall` | **works** — "Setting up git-lfs", binary restored, 11.7 MB freed and re-installed |
| C | binary deleted, then `dpkg-deb --extract` puts the file back | `apt-get install --reinstall` | **works** — "Setting up git-lfs", and `git-lfs/3.4.1` runs |

Experiment A kills the obvious idea: removing the package *after* deleting the
binary is not a cleaner drift, it is a poisoned one. The prerm runs with the
binary already gone, fails, and leaves dpkg in a half-configured (`iF`) state
that apt refuses to repair.

What is left is two separate decisions, and they are not the same decision:

1. **The harness's drift step for package tools should be
   `apt-get remove --purge <pkg>`** (experiment B). That is the *realistic*
   drift for a package tool — "I uninstalled it by hand and aes still thinks it
   is installed" — and the product already repairs it. The harness currently
   deletes the binary instead, which is a rarer and much nastier drift, and it
   is the one that finds this bug. **This change alone makes git-lfs pass and
   is the cheap one.**
2. **The product's repair path cannot fix a file deleted behind dpkg's back**
   (experiment C is the only fix that works: restore the file from the package
   archive first, then reinstall). It is a compound privileged command, or a
   second privileged operation and a second prompt. I stopped short of it:
   for one tool out of 63, in a rare drift scenario, it is a lot of new
   machinery in the path that installs software on people's machines.

The third option — accept it and document it in `tools/git/git-lfs/tool.yaml`,
the way vault's container limitation is recorded — is still legitimate, and it
becomes the right one if you take (1) and decide (2) is not worth the
privileged-path complexity.

Whatever you choose, record it in the manifest or the spec — not only in a
commit.

### 5.2 The five unverified tools

`pi` `br` `bvr` `discord-cli` `hashline` are in the catalog with
`tested: false`, so they are not in the default set. They need the Layer 2
proof before they can be marked `tested: true` and join it. That is ordinary
work, and it is the same work the other 63 have had.

### 5.3 `9x0` — publish

Held deliberately. The DoD wants a boot that works end to end, and it does not
yet on a stock macOS: the default set is linux-proven, and the resolver skips
a tool it cannot vouch for on the host asking, so `aes setup` on darwin exits 1
with "the default selection resolved to no tools on this platform". The
decision behind that dead end is §5.4.

### 5.4 The product decision nobody has made

**Is a `tested: true` flag allowed to be set from evidence the host asking
cannot obtain?**

The argument for "no, require a darwin proof first" is that a flag gated on
unobtainable evidence is a flag that never gets set — which is where this
project started. The argument for "yes" is that macOS users have a dead end
today: `aes setup` exits 1 and the only way forward is `--only` per tool.

Current state is the honest middle: linux evidence is linux evidence, macOS
says plainly it has none, and `--allow-unproven` is the explicit opt-in. It is
not mine to settle, and it should not be settled by whoever writes code first.

---

## 6. Bugs found and fixed this session

These are the ones that were not caught by a failing test, and each is a shape
worth recognising.

| Commit | What was wrong |
|---|---|
| `8bfc8cf` | **`--exclude` did not exclude.** It only reported an exclusion that broke a dependency. A tool the user excluded and then watched get installed was the flag lying — and with the default set now 63 tools, every one of them was reachable that way. |
| `8bfc8cf` | The catalog's `default: true` flag — designed for exactly this — was set on **no tool at all**, because the profiles were doing the job. The selection mechanism existed and was unused. |
| `12c60ce` | **A forced apt repair reinstalled the wrong package.** The manifest names `cargo`; the file is owned by `rustc`. The repair restored nothing and reported success on a machine where the tool was still broken. |
| `8746aaa` | The Layer 2 sweep **shared one container**, so `docker`'s install brought `git` in and the `git` proof was impossible. Sixteen tools failed for reasons that had nothing to do with the tool under test. |
| `8746aaa` | **vault's refusal was diagnosed as a seccomp profile.** It is a file capability. The message sent the reader to the wrong system entirely. |
| `33bf1a1` | **A failed apt install threw away dpkg's own explanation.** "exit code 100" with the diagnosis — the whole point of the capture — discarded. |
| `7da4c98` | The harness did the same thing, one layer up: sixteen tools reported "exit status 100" and the reason was in the output it threw away. |
| `7da4c98` | **A tool the base image ships was reported as a failure.** bash, node, npm and python cannot be absent in a stock `ubuntu:24.04`, and a failure there says the tool is broken. |

---

## 7. Running the tests from a Windows box

The project does not build on Windows: `internal/exec/process_starttime_other.go`
has a `!linux` build tag and calls `syscall.Kill`, which does not exist there.
`go build ./...` fails for reasons unrelated to any change. Cross-compiling
(`GOOS=linux go build ./...`) works, but the test binaries cannot be run
cross-compiled.

Everything therefore runs in a Linux Go container:

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W)":/src -w /src golang:1.26 \
  go test -short ./...
```

`MSYS_NO_PATHCONV=1` matters: without it Git Bash rewrites the `-v` path and
the daemon gets a relative path.

For the **Layer 2** proof, three more things bite, in this order:

1. The `golang:1.26` image has no `docker` CLI. Copy one out of `docker:cli`
   and mount it in: `docker create --name x docker:cli && docker cp
   x:/usr/local/bin/docker ./bin/docker`.
2. The test bind-mounts the aes binary into the container, and the **outer**
   daemon resolves that path — so it must exist on the outer host, not inside
   the Go container. Set `TMPDIR` to a path the Docker Desktop daemon can
   resolve (`/c/tmp` maps to `C:\tmp`), so `t.TempDir()` lands somewhere the
   daemon sees. Otherwise Docker silently creates a **directory** at the source
   path and every subtest fails with `exec: "/usr/local/bin/aes": is a
   directory: permission denied`.
3. Mount `/var/run/docker.sock` and set `DOCKER_HOST=unix:///var/run/docker.sock`
   so the in-container CLI reaches the outer daemon.

**Do not run other containers while a Layer 2 sweep is running.** They share
the apt mirrors, and the sweep's container preparation starts failing with
`exit status 100` — which reads as sixteen broken tools and is really a
contended mirror. This cost a full sweep before it was noticed; re-run the
affected tools in a clean window before believing any of them.

---

## 8. Three traps you will hit within the hour

From `AGENTS.md`, the ones most likely to bite in a *new* environment:

1. **This shell is zsh, which does not word-split an unquoted variable.**
   `FLAGS="-e A=1"; docker exec $FLAGS $C cmd` passes **one** argument, and
   the command runs with the wrong environment and **no error**. Cost an hour
   chasing a `envgen` bug that did not exist. (On Git Bash the same trap waits
   in the other direction: `docker cp` and `docker run -v` paths get mangled
   unless `MSYS_NO_PATHCONV=1` is set.)
2. **A bind-mounted file is bound by inode.** `go build -o ./aes` after the
   container started leaves it running the old binary, so a fix verifies as
   still broken. Recreate the container.
3. **A file named `*_linux_test.go` carries an implicit GOOS build
   constraint** and will not run on macOS at all — reporting "no tests to run",
   forever.

---

## 9. First hour in a new environment

```bash
git pull && go build ./... && go vet ./... && go test ./...   # expect green
docker version                                                  # needed for the gated tests
AES_CATALOG_CHECK=1 go test -v -run TestAptCoordinates ./internal/sandbox/
AES_LAYER2=1 go test -v -run TestLayer2Container ./internal/sandbox/       # ~90 min for the full sweep
go run ./cmd/aes setup --dry-run                                 # read the refusal carefully
```

Then read §5.1 and §5.4, and make those decisions rather than inheriting them.
