# AES — handoff

Written at the end of a working session, for picking this up somewhere else and
finishing it. Everything below is measured, not remembered; where a number is a
claim rather than a measurement, it says so.

- **Repo:** `github.com/quangdang46/agents_environment_setup`
- **Branch:** `main` · **History:** 182 commits · **Tree:** clean at the commit
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
| Test suite | green, **421 test functions**, 16 packages, `-race` clean |
| Catalog | **68 tools** — github-release 41, package/apt 20, npm 7 |
| `tested: true` | **67**, each carrying a `tested_on` list |
| `default: true` | **67** — the same 67 (see §3.3) |
| Not verified | **1**: `pi` (see §4.2) |
| Beads open | `9x0` (P3, publish — in progress; see §5.2) |
| Published | **v0.1.0** at `70e4cb8`; this session's commits are unreleased |

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

**The sweep can be narrowed.** `AES_LAYER2_TOOL=git-lfs,bvr` sweeps those tools
instead of the default selection. A sweep takes ~90 minutes for 63 tools, and
the handoff's own advice — "re-run the affected tools in a clean window before
believing any of them" — is advice about re-running a FEW tools, which a sweep
that can only do all of them cannot follow. The names are catalog tools, not
necessarily default ones: a tool that has not been proven yet is not in the
default set, and proving one is exactly what the filter is for.

---

## 3. What this session changed

### 3.1 `git-lfs` — the last red Layer 2 test — is fixed, in the product

The handoff left this as a choice between changing the harness and changing
the product. **The product was changed**, because the spec settles which is
right:

> **Drifted** (state says installed, verify says missing) → with `--force` → **reinstall**
>
> 6. Delete the binary, run `aes doctor` → drift detected
> 7. Re-run `aes setup --only <tool> --force` → reinstalls

The harness's "delete the binary" drift step **is** the spec's sequence. Making
it `apt-get remove --purge <pkg>` instead would have made the test pass by
weakening it — and `--reinstall` only matters when dpkg still believes the
package is installed, so that step is the only thing exercising it, along with
the owner-package rule from `12c60ce`.

**The mechanism.** dpkg runs a package's maintainer scripts *before* it unpacks
the new files. git-lfs's pre-removal script is `git lfs uninstall
--skip-repo --system`, which needs the very binary the reinstall is restoring.
So the file has to be on disk before dpkg starts, and `--reinstall` cannot
arrange that on its own:

```
apt-get install -y --reinstall git-lfs
dpkg: warning: old git-lfs package pre-removal script subprocess returned error exit status 1
dpkg: trying script from the new package instead ...
dpkg: error processing archive .../git-lfs_3.4.1-1ubuntu0.4_amd64.deb (--unpack):
 new git-lfs package pre-removal script subprocess returned error exit status 1
E: Sub-process /usr/bin/dpkg returned an error code (1)
```

A forced apt repair now restores the missing file from the package's own
archive first, and then lets dpkg do the reinstall as usual:

```
apt-get download git-lfs
dpkg-deb --fsys-tarfile git-lfs_*.deb | tar -C / --skip-old-files -xf -
apt-get install -y --reinstall git-lfs
-> Unpacking git-lfs (3.4.1-1ubuntu0.4) over (3.4.1-1ubuntu0.4)
-> Setting up git-lfs (3.4.1-1ubuntu0.4)
```

Three properties of that restore are deliberate, and each was measured:

- **It restores the files that are MISSING and nothing else.** `--skip-old-files`
  is what does it. dpkg's own reinstall re-extracts a package's files but leaves
  a conffile the administrator edited alone, and a repair that reverted
  `/etc/bash.bashrc` would be damage, not repair. Measured conffiles among this
  catalog's apt packages: `git` owns `/etc/bash_completion.d/git-prompt`, and
  `bash` owns `/etc/bash.bashrc` plus three files under `/etc/skel`.
- **It restores the package that owns the binary, not the package the manifest
  names.** The file that went missing is the tool's binary, and dpkg says which
  package owns it — the same answer that makes `--reinstall` necessary in the
  first place. For `postgres18` that is the difference between writing back the
  client's files and writing over `/etc/postgresql/18/main`.
- **The archive has to be fetched rather than found**: apt removes it when the
  transaction ends, and the failed transaction ends with
  `/var/cache/apt/archives` empty.

The fetch is unprivileged and happens before the privilege decision; the
restore is part of the privileged command, so the user approves **one** repair
in **one** command. When the archive cannot be fetched the plain reinstall
runs — that is what every other package tool needs, and the failure that
matters is then reported by the reinstall, with dpkg's own explanation.

### 3.2 `bvr` was installed under a name the release does not ship

`aes verify bvr` reported the tool as **stale** on a machine carrying the
exact version the manifest pins. The manifest verified `bv`, and its comment
explained why: the release used to ship a file called `bv`, while the tool name
stayed `bvr` to match the repository. The release moved on — v0.25.2 ships
`bvr` in both its linux and its macos archive (measured by listing the
archives) — and the manifest's `binary:` map was updated with it. The verify
command was not.

The two names together decide what the product looks for: the release's name is
the name the installer extracts, and the verify command is the name it is
installed **as**. So asking for `bv` found a leftover from an older release:

```
bv   0.16.4    <- what the manifest asked for
bvr  0.25.2    <- what the release ships, and what the manifest pins
```

The program still calls itself `bv` in its own `--version` output
("bv v0.25.2"), which is what made the old name look right. The file the
release ships is `bvr`, and the file is what lands on PATH. `bvr` was the only
github-release tool in the catalog whose installed name differed from the
shipped one without an arch reason; `yq` differs per arch, which is what its
arch-keyed `binary:` map is for.

### 3.3 The catalog's default set is the selection, and it grew

Every tool that is `tested: true` is also `default: true` — now 67 of 68. The
four tools the previous handoff listed as unverified have joined it, each with
both layers proven (see §4.1). `pi` has not (§4.2).

`internal/catalog/tools_test.go` asserts that every `default: true` tool is
also `tested: true`, so the default set can never grow a tool nobody has
proven.

**Naming no tool and selecting nothing are different intents**, and the
difference is carried in the type rather than recomputed by each caller:
`selection.Selection.Defaults` says that an empty `Only` means the catalog's
default set, and the CLI sets it when `--only` was not given. The TUI leaves
it false, because nothing ticked means nothing.

### 3.4 The Layer 1 fixture table was missing five tools the catalog claimed

`internal/catalog/tools_test.go` carries a map of the tools the Layer 1
fixtures exercise, and its comment says it is "the mirror of the verifier's
fixture table". It listed `br`, `bvr`, `discord-cli`, `hashline` and `pi`; the
verifier's table had **none** of them. The mirror was pointing at nothing: the
tools were named as covered and exercised by nothing.

The fixtures are the measurement, and they are not all the same:

```
br 0.2.2        -> ok, above the 0.2.0 floor
bvr 0.25.2       -> ok, at the 0.25.2 floor
hashline 0.9.20  -> ok, above the 0.9.19 floor
discord 0.1.0    -> STALE, below the 0.1.2 floor
pi 0.84.2        -> STALE, below the 1.2.0 floor
```

The two stale ones are the developer's own older builds, and the rule that
already covers the ubuntu runners' psql covers them too: a host build below the
floor is the host's version, not a broken fixture.

---

## 4. What the verification actually proved

### 4.1 The four tools that were unverified

`br`, `bvr`, `discord-cli` and `hashline` are proven on **darwin/arm64**:

- **Layer 1** — the tool was on the machine and the probe read a version above
  the floor.
- **Layer 2** — the full sequence on a machine where the tool was **absent**:
  install, verify, state written, re-run changes nothing, the binary deleted is
  noticed as drift, and `--force` puts it back. Run by the host Layer 2 proof
  with a PATH that did not carry the developer's own copy of each tool: *a
  machine that already has a tool cannot prove a fresh install of it*.

```
br 5.5s    bvr 5.8s    discord-cli 5.7s    hashline 5.4s
```

`aes setup --dry-run` on this macOS now resolves to 4 tools where it resolved
to none.

### 4.2 `pi` — still blocked, and the reason is the platform's

Every published version of `@earendil-works/pi-coding-agent` declares
`engines: {node: ">=22.19.0"}` — 59 of 59, oldest to newest, so there is no
older release to fall back to. `enableCompileCache` landed in node 22.19.0, and
Ubuntu 24.04's `apt install nodejs` is node 18.19.1. The bytes install
cleanly and the binary then dies on startup, which `aes setup` reports
truthfully as "installed but verification says unknown" with the path named.

This is the same shape gemini had before bead `8tv`, and the same fix is not
available: a second node would have to be installed alongside the distro one,
which no closed strategy in this catalog does. The manifest records the whole
measurement.

### 4.3 The container proof

`git-lfs` passes the real harness: *"git-lfs absent inside the container before
the run"* → *"git-lfs present inside the container at /usr/bin/git-lfs"*, with
the full seven-step sequence (install → verify → state → idempotent → drift
detected → force reinstall → present after) in a fresh `ubuntu:24.04`.

---

## 5. Remaining work, in the order I would take it

### 5.1 The darwin evidence, for the whole catalog

`tested: true` carries a `tested_on` list of the GOOS/GOARCH pairs the tool was
proven on, and the resolver **skips** a tool that is proven somewhere but not
here. On a macOS that skip was total: every tool was proven on linux, so
`aes setup` resolved to nothing and exited 1.

**The question the previous handoff left open — "is a `tested: true` flag
allowed to be set from evidence the host asking cannot obtain?" — has an answer
that is measurable rather than philosophical: the host CAN obtain the evidence,
and the rule must not stop the run whose job is to gather it.**

The mechanism is `AllowUnproven`, and it already existed for the container
sweep: a proof run has to be willing to install a tool that is unproven here,
because a tool that can only be installed where it is already proven can never
be proven anywhere new. The host Layer 2 run did not have it, and the
consequence was a test that could not pass on the platform it existed to prove:

```
zellij: FAILED
  [FAIL] install  exit=1 | aes: nothing to install: no tool in this selection
  can be installed on darwin/arm64; proven only on linux/amd64, not on this host
```

The product's own refusal was the test's failure message. With `AllowUnproven`
the same run installs the tool, verifies it, and proves the darwin coordinate
— which is how the four tools in §4.1 were proven.

**The skip stands for a user's run, and the proof run opts past it.** What the
darwin gap still needs is the missing measurements, and those need a machine
where the tools are not already installed — a clean macOS runner, not a
developer's desk. That is a CI job, and it is the same CI matrix the publish
bead wants.

### 5.2 `9x0` — publish

Held deliberately, and it should stay held until §5.1 has produced the darwin
evidence: the DoD wants a boot that works end to end, and on a stock macOS it
does not yet.

### 5.3 `hashline` on linux/amd64

The release publishes no `linux/arm64` asset at any version (v0.9.19 and
v0.9.20 both ship linux-x86_64, macos-x64, macos-aarch64 and windows-x86_64),
so the manifest's absence is deliberate: on arm64 `aes` reports the tool as
unsupported, which is the honest answer. The linux/amd64 coordinate is
unproven, and proving it needs an amd64 container — on an Apple Silicon host
that is an emulated one, and the harness's image arch follows the local image,
so `docker pull --platform linux/amd64 ubuntu:24.04` is enough to make the
sweep build and run an amd64 `aes` (restore the native image afterwards, or
the next sweep runs emulated).

### 5.4 The five unverified tools, reduced to one

`pi` (§4.2) is the only one left, and it is blocked on a platform fact rather
than on anything AES can do.

---

## 6. Bugs found and fixed this session

| Commit | What was wrong |
|---|---|
| `2f870df` | **A forced apt repair could not repair a package whose file was deleted behind dpkg's back.** dpkg runs the maintainer scripts before it unpacks, and git-lfs's pre-removal script needs the very binary the reinstall is restoring. The repair reported success on a machine where the tool was still broken. |
| `64233d2` | **`bvr` was installed under a name the release does not ship.** `aes verify bvr` reported the tool as stale on a machine carrying the pinned version, because it was looking for a leftover from an older release. |
| `3b4b8ba` | **The host Layer 2 proof could not pass on darwin at all.** The resolver's own refusal — "proven only on linux/amd64, not on this host" — was the test's failure message, because the proof run was not allowed to install a tool that is unproven here. |
| `3b4b8ba` | The same file's hand-kept tool list named `gping`, which is not in the catalog. A second source of truth about the catalog, going stale silently. |
| `b2d3915` | **The Layer 1 fixture table was missing five tools the catalog claimed were covered.** The catalog's map is "the mirror of the verifier's fixture table"; it was a mirror of nothing. |
| `51f0037` | The Layer 2 sweep could only do all 63 tools at a time, so the handoff's own advice — re-run the affected tools — could not be followed. |

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
   chasing an `envgen` bug that did not exist. (On Git Bash the same trap waits
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

Then read §5.1, and make that decision rather than inheriting it.
