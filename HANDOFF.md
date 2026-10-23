# AES — handoff

Written at the end of a working session, for picking this up somewhere else and
finishing it. Everything below is measured, not remembered; where a number is a
claim rather than a measurement, it says so.

- **Repo:** `github.com/quangdang46/agents_environment_setup`
- **Branch:** `main` · **History:** 196 commits · **Tree:** clean at the commit
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
| Test suite | green, **424 test functions**, 16 packages, `-race` clean |
| Bootstrap test | `sh scripts/install_test.sh` — **14 pass, 0 fail** (12 before this session) |
| Catalog | **68 tools** — github-release 41, package/apt 20, npm 7 |
| `tested: true` | **67**, each carrying a `tested_on` list |
| `default: true` | **67** — the same 67 (see §3.3) |
| Not verified | **1**: `pi` (see §4.2) |
| Proven `darwin/arm64` | **54** — measured by the proof run, not by hand |
| Proven `linux/amd64` | **64** |
| Proven `linux/arm64` | **38** |
| Beads open | `kn7` (P3, pi), `igf` (P3, hashline/amd64), `9x0` (P3, publish — released, DoD not fully ticked) |
| Published | **v0.1.1** on 2026-10-23 — 4 assets + `checksums.txt`, workflow green |

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

### 3.5 `aes setup` and `aes verify` could not see where three strategies install

Both commands prepended only `$AES_HOME/bin` to PATH, which is where the
github-release strategy puts binaries. `go install` writes `$GOBIN` or
`$GOPATH/bin`, cargo `~/.cargo/bin`, uv and npm their own — and none of those
is on PATH until the user sources `env.sh`. So a successful install was
reported `installed but verification says missing`, and state was never
written, because state is only written for a tool that verified.

Measured on `srps`, whose darwin target is `go install`:

```
$ aes setup --only srps --allow-unproven --non-interactive
✖ srps failed: installed but verification says missing

$ ls $HOME/go/bin/sysmoni
-rwxr-xr-x  5.8M  sysmoni          <- the install worked
```

**It is invisible on a developer's machine**, where `~/go/bin` is already on
PATH, and appears wherever `HOME` is not the developer's: a sandbox, a CI
runner, or anyone who redirects `HOME`. The container sweeps missed it because
the base image has no Go toolchain, so `srps` was proven on linux
(github-release) and skipped on darwin.

It had to be fixed twice, and the second time is the lesson: after `setup`
learned to put the strategy's directory on PATH, the install passed and
**verify still said missing** — verify is a separate process and had the same
hard-coded line. Fixing one half moves a failure one step later rather than
away. Both now share `binOnPathForStrategies`, which asks the installer
registry the same question `envgen` asks when it writes `env.sh`.

### 3.6 v0.1.1, and the bootstrap bug it was almost shipped with

While preparing the release I ran the command a user actually runs:

```
$ sh install.sh
==> downloading aes-latest-darwin-arm64.tar.gz
curl: (56) The requested URL returned error: 404
```

`install.sh` interpolated `$VERSION` into the asset name, and the default
`$VERSION` is the word "latest". Release assets are named after the tag they
were built from, so `aes-latest-*` does not exist and cannot — it would have
to be renamed on every release. **The default `curl | sh` invocation was
broken**, while every other invocation worked, because `--version` pins a
concrete number and every test in the suite passed one.

The fix separates the base URL (which may say `latest`; GitHub redirects it to
the newest release) from the asset *name* (which may not). The concrete
version is read from the release's own `checksums.txt` — the file already
being fetched and verified a moment later, so no API call and no `jq`, and it
travels through the same redirect as the assets.

`install_test.sh` gained case `0b`, which is the branch the defect lived in and
which nothing covered. Proven both ways: with the fix removed the resolved
value is `latest` and the install 404s; with it, `0b` passes and so does the
real install.

Verified after publishing, against the **public** release rather than a
fixture: `install.sh` → `aes 0.1.1` → `aes list` shows all 68 tools →
`aes setup --dry-run` resolves 54.

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

**Three of the four are now also proven on linux/arm64**, each in the container
sweep on 2026-10-22 — a fresh `ubuntu:24.04` per tool, the full seven-step
sequence, no sequence failure:

```
br 45.47s    bvr 48.09s    discord-cli 49.61s
```

Their manifests now carry both coordinates in `tested_on`. `hashline` is the
exception, and its evidence is the negative kind: the release publishes no
linux/arm64 asset at any version, so the manifest deliberately carries none —
and the product refuses correctly on that arch, in two layers:

```
aes setup --only hashline
  -> "no tool in this selection can be installed on linux/arm64;
      proven only on darwin/arm64, not on this host"

aes setup --only hashline --allow-unproven
  -> "no asset for this architecture: install hashline has no asset
      for arm64 (declared: amd64)", exit 6, in 0.0s
```

The refusal is the proof that the absence is declared rather than missing. Its
linux/amd64 coordinate stays unproven until an amd64 container run proves it
(bead `igf`).

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

The same sweep also proved, with zero sequence failures: the first 24 tools in
catalog order (through `git-lfs`), plus `gnupg` (55.80s), `jq` (55.36s),
`postgres18` (101.74s) and `vault` (101.23s) — the latter two exercising the
owner-package reinstall rule from `12c60ce` and the third-party apt-source
path. A dedicated sweep for the new archive-restore repair re-proved
`git-lfs` (57.40s) after the fix, where the same sequence against the pre-fix
binary failed at the repair step with exit code 100 and dpkg's pre-removal
script error.

Separately, a proof workflow (`.github/workflows/proof.yml`) now exists that
actually runs this evidence on demand: the linux container sweep, the darwin
host proof, and the apt-coordinates check. `workflow_dispatch` only — a
ninety-minute proof that installs real software is not a pull-request gate.
No workflow ran the Layer 2 proof before; ci.yml's comment claimed
release.yml did, and it did not.

### 4.4 The darwin proof, on a real macOS runner

Run 39878020977, `workflow_dispatch` with `platform=darwin`:

**54 tools PASS, 0 FAIL**, in 411s. That includes `srps` (20.84s), the tool
whose failure exposed both PATH defects in §3.5 — the run before it failed on
that one tool alone.

Each of the 54 manifests now carries `darwin/arm64`, and the match was checked
mechanically in both directions rather than by eye:

```
declared: 54 | declared-but-not-proven: 0 | proven-but-not-declared: 0
```

That second direction is the one that matters. A `tested_on` entry for a
platform that has not run both layers is exactly what `AGENTS.md` calls "a
claim that outruns the evidence": the resolver trusts the flag, so `aes` would
install a tool nobody verified there and report the run as done.

Fourteen tools were skipped because the runner already ships them (§5.4), and
`pi` is not among the 54 for its own reason (§4.2).

The effect a user sees: `aes setup` on that macOS resolves to 54 tools, where
before this session it resolved to none.

---

## 5. Remaining work, in the order I would take it

v0.1.1 is published, so none of this blocks a release. It is ordered by what
would change a user's experience first.

### 5.1 `linux/arm64` evidence — the biggest gap, and the cheapest to close

Measured on the shipped catalog: `linux/amd64` 64 tools, `darwin/arm64` 54,
**`linux/arm64` 38**. Every container sweep in this session ran on an Apple
Silicon host, so every linux proof it produced was `linux/arm64`, and the
catalog's `linux/amd64` flags came from earlier sessions.

An ARM Linux user therefore sees the resolver skip 30 tools that a Debian
laptop gets, for a reason that is about where the runner happened to live.
Nothing is broken; the evidence is just missing.

The proof workflow already runs on `ubuntu-latest`, which is `amd64`, so
`gh workflow run proof.yml -f platform=linux` closes this — roughly 90
minutes, one fresh container per tool. **Do this before believing the
catalog's linux coverage is uniform.**

### 5.2 `hashline` on linux/amd64 — bead `igf`

The release publishes no `linux/arm64` asset at any version (v0.9.19 and
v0.9.20 both ship linux-x86_64, macos-x64, macos-aarch64, windows-x86_64), so
the manifest deliberately declares none, and on arm64 `aes` reports the tool
as unsupported — the honest answer.

Its `linux/amd64` coordinate is still unproven. §5.1's ubuntu-latest sweep
covers it in the same run; it is listed separately because the manifest's
comment promises a proof that has not happened yet.

### 5.3 `pi` — bead `kn7`, and it is a product decision

Every published version of `@earendil-works/pi-coding-agent` declares
`engines: {node: ">=22.19.0"}` — 59 of 59, oldest to newest, so there is no
older release to fall back to. `enableCompileCache` landed in node 22.19.0;
Ubuntu 24.04's `apt install nodejs` is 18.19.1. The bytes install cleanly and
the binary then dies on startup, which `aes setup` reports truthfully as
"installed but verification says unknown" with the path named.

The question is **whether the `node` tool owes its users a newer node than the
distribution's.** If yes, the node strategy needs a second coordinate —
nodejs.org publishes github-release-shaped tarballs, and the manifest's
per-platform floor map already exists to carry a number that differs by
platform. If no, `pi` stays in the catalog uninstallable on a stock Ubuntu,
which is honest and leaves it out of the default set forever.

Either answer is defensible; leaving the floor where it is while calling the
tool unproven for a reason nobody chose is not.

### 5.4 14 tools the darwin runner cannot prove

`bash`, `gh`, `git`, `git-lfs`, `gnupg`, `go`, `jq`, `node`, `npm`, `python`,
`rust`, `yq`, `zsh` skip the darwin proof because a stock `macos-latest`
already ships them, and a machine that already has a tool cannot prove a fresh
install of it. This is `requireAbsent` working, not a gap in the proof.

They are not blocked on anything AES can do — only on a macOS machine that
does not carry them. Their `linux` evidence is unaffected.

### 5.5 `9x0` — publish, and what is left of its DoD

The release itself is done: v0.1.1, four platform assets, `checksums.txt`, and
the bootstrap verified end-to-end against the public release rather than a
fixture. What remains unticked is measurement, not machinery:

- *40–50 tool definitions, ≥10 tested on both layers* — 68 definitions, 67
  tested, but "both layers" per platform is only fully true for the 54
  darwin-proven and the sets in §5.1.
- *`aes setup` bootstraps end to end in one command* — it does, and the
  refusal on a machine with no proof is the correct answer rather than a bug.

Close the bead when §5.1 has run, because that is the measurement its DoD is
asking for.

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
| `9d8aa8e` | **`aes setup` put only `$AES_HOME/bin` on PATH**, so a tool installed by the `go` strategy landed in `$HOME/go/bin` and was reported "installed but verification says missing". Invisible on a developer's machine, where `~/go/bin` is already on PATH; the macOS CI runner found it. |
| `cb7c328` | **`aes verify` had the same hole, one process later.** After the setup fix the install passed and verify still said missing, because verify reconstructed PATH from `$AES_HOME/bin` alone. Both now share one registry lookup. |
| `5cb599e` | **The default `curl \| sh` bootstrap was broken.** install.sh built the asset name from the word "latest", so it asked for `aes-latest-darwin-arm64.tar.gz` and got a 404 — while every test passed, because every test passed `--version`. Found by running the command a user runs, minutes before tagging. |

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
sh scripts/install_test.sh                                     # the bootstrap, 14 cases, no network for most
docker version                                                  # needed for the gated tests
AES_CATALOG_CHECK=1 go test -v -run TestAptCoordinatesMatchWhatAptOffers ./internal/sandbox/
go run ./cmd/aes setup --dry-run                                # read the refusal carefully
```

The full Layer 2 sweep is opt-in and slow; run it when you need evidence, not
as a smoke test:

```bash
# the evidence CI produces, on the runner that produces it
gh workflow run proof.yml -f platform=all
# or locally, narrowed: a full sweep is ~90 min for 63 tools
AES_LAYER2=1 AES_LAYER2_TOOL=git-lfs,bvr go test -v -run TestLayer2Container ./internal/sandbox/
# the host proof (darwin), which is how the 54 darwin coordinates were measured
AES_LAYER2=1 AES_LAYER2_TOOL=all go test -v -run TestLayer2RealInstall ./internal/sandbox/
```

**One thing to check before trusting a green build:** nothing in CI runs the
Layer 2 proof on a push. `proof.yml` is `workflow_dispatch`, because a
ninety-minute run that installs real software is not a pull-request gate. So a
green check on a PR says the unit and contract tests pass — it says nothing
about whether the catalog's `tested:` flags still match reality.

Then read §5.1: the `linux/arm64` gap is the largest missing measurement and
the cheapest to close.
