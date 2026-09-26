# agents_environment_setup (AES) — Implementation Plan

> **Trạng thái:** Kiến trúc đã chốt. Sẵn sàng code.
> **Ngày:** 2026-09-26 · Research nền: ACFS `7dd2bb3`
> **Quy ước:** mỗi giai đoạn liệt kê file cụ thể, contract, và test chứng minh nó chạy.

---

## North Star

> **`aes setup` — một lệnh, đưa máy từ "chưa có gì" → "AI coding environment hoàn chỉnh đã verify", exit 0 chỉ khi xong.**

```
ONE COMMAND → ZERO MANUAL INSTALL → ALL TOOLS → ALL DEPS
           → CONFIGURE ENV → VERIFY EVERYTHING → READY
```

**Core identity:** AES biết *"để có AI coding machine hoàn chỉnh cần những gì, trên OS này cài
thế nào, verify ra sao, chạy lại thì làm gì."* Người dùng chỉ gõ `aes setup`.

### Hai kênh, một core

| | Agent / CI | Human |
|---|---|---|
| Chạy | `aes setup --yes --non-interactive --json` | `aes` (TUI) |
| Cùng core | ✓ | ✓ |

TUI không chứa business logic. Cùng nguyên tắc ACFS đã chốt ở `MANIFEST_SCHEMA_VNEXT.md:112`:
*"No UI surface should maintain a separate dependency graph."*

### Bootstrap tầng 0

```
curl → GitHub Release → AES binary → ~/.aes/bin/ → aes setup
```

Go/brew không phải dependency để có AES. Ưu tiên `github-release` khi provision: độc lập
package manager, checksum rõ, không sudo. `brew`/`apt` là fallback.

---

## Định nghĩa đã chốt

| # | Quyết định | Kết quả |
|---|---|---|
| 1 | Positioning | **Agent Developer Environment Manager** |
| 2 | Catalog | 40–50 definitions, **10–15 verified** integration test |
| 3 | `env` | Minimal trong MVP |
| 4 | `needs_sudo` | **Bỏ khỏi `tool.yaml`** → thuộc tính strategy |
| 5 | Shell | Không sửa mặc định; `--link-shell` opt-in có backup |
| 6 | Thư mục | **`~/.aes/`** — không đụng `~/.agents/` |
| 7 | Platform | macOS (arm64/x64) + Ubuntu (amd64/arm64) |
| 8 | Test | unit · dry-run · **real sandbox** |
| 9 | Arbitrary shell | **KHÔNG** — strategy đóng |
| 10 | `phase` | **Bỏ** — `tags` + `dependencies` |
| 11 | `aes plan` | **Bỏ khỏi scope** — resolve vẫn chạy nội bộ |
| 12 | ACFS trong CLI | **Không** — AES không biết ACFS tồn tại |
| 13 | Tên binary | `aes` |

---

## Kiến trúc

```
              aes setup  /  aes (TUI)
                       │
              ┌────────▼─────────┐
              │  Config/Profile  │
              └────────┬─────────┘
              ┌────────▼─────────┐
              │  Catalog         │  tool.yaml
              └────────┬─────────┘
              ┌────────▼─────────┐
              │  Resolver        │  deps DAG · platform · cycle
              └────────┬─────────┘
              ┌────────▼─────────┐
              │  Actions         │  plan nội bộ
              └────────┬─────────┘
    ┌──────────┬───────┼───────┬──────────┐
    ▼          ▼       ▼       ▼          ▼
  Github    Brew     Apt     Go      Npm/Cargo
  Release
    └──────────┴───────┼───────┴──────────┘
              ┌────────▼─────────┐
              │  Verify          │  ← sự thật
              └────────┬─────────┘
              ┌────────▼─────────┐
              │  State (cache)   │  ← KHÔNG phải sự thật
              └────────┬─────────┘
              ┌────────▼─────────┐
              │  ~/.aes/         │
              └──────────────────┘
```

Đường chạy duy nhất — không bypass `tool.yaml`.

---

## Invariant

| # | Invariant | Chứng minh bằng |
|---|---|---|
| I1 | Mọi tool qua catalog→resolver→actions→installer | Không `switch tool.Name`; qua strategy registry |
| I2 | `needs_sudo` là thuộc tính strategy | `tool.yaml` không có field này |
| I3 | Không arbitrary shell | Validate reject field ngoài schema |
| I4 | State ghi **sau** verify | Install fail → state không đổi |
| I5 | State là cache | `doctor` phát hiện drift |
| I6 | `--dry-run` không mutate | Sau dry-run không file đổi |
| I7 | Không đụng `~/.agents/` | Sau mọi lệnh, `~/.agents` không đổi |
| I8 | Cùng input → cùng Actions | Hash 2 lần, so sánh |
| I9 | Manifest hỏng ≠ "chưa cài" | YAML sai → lỗi |
| I10 | Không tự sudo | Không `sudo` nào exec |
| I11 | Cycle → lỗi không treo | A→B→A báo cycle |
| I12 | Platform không hỗ trợ → skip | Tool `darwin`-only, chạy linux → skip |
| I13 | TUI ≡ CLI | Cùng input → cùng Actions |

---

## Schema `tool.yaml`

```yaml
name: ripgrep
description: Fast recursive search
category: search
default: true
tags: [cli, search]
provides: [rg]
dependencies: []

verify:
  command: rg
  version:
    command: rg --version
    min: "14.0.0"

install:
  darwin:
    strategy: github-release
    repository: BurntSushi/ripgrep
    asset:
      arm64: ripgrep-14.1.1-aarch64-apple-darwin.tar.gz
  linux:
    strategy: package
    manager: apt
    package: ripgrep
```

Không `run` / `needs_sudo` / `phase`. Privilege suy ra từ `(strategy, manager)`:

| Strategy | sudo | đích |
|---|---|---|
| `github-release` | không | `~/.aes/bin` |
| `package` + brew | không | `/opt/homebrew` |
| `package` + apt | **có** | hệ thống |
| `go`/`npm`/`cargo` | không | prefix user |

`sha256` bắt buộc **chỉ với `github-release`**. `brew`/`apt` có integrity riêng — ép sha256
vào đó là metadata giả.

---

## Cấu trúc repo

```
aes/
├── cmd/aes/main.go
├── internal/
│   ├── manifest/     # schema + validate
│   ├── catalog/      # load tool.yaml, index
│   ├── resolver/     # deps DAG, platform, actions
│   ├── platform/     # OS/arch/manager detect
│   ├── installer/    # registry + strategies
│   ├── verifier/
│   ├── state/
│   ├── envgen/
│   └── cli/
├── profiles/         # minimal, developer, ai, full
├── tools/
├── docs/
└── Makefile
```

---

## Giai đoạn chi tiết

### GĐ1 — Manifest + Platform

**File:**
- `internal/manifest/manifest.go` — struct `Tool`, `Install`, `Strategy`, `Verify`, `Validate()`
- `internal/manifest/manifest_test.go`
- `internal/platform/platform.go` — `Host{OS,Arch,PkgManager}`, `Current()`, `Supports(tool)`
- `internal/platform/detect_test.go`

**Contract:**
- `Validate()` reject: thiếu name/description/install, field lạ, `run` (không còn hợp lệ),
  `github-release` thiếu sha256, cycle deps
- `Platform.Supports(tool)` — tool chỉ có `darwin`, chạy linux → false

**Test (contract, không source-grep):**
- Manifest thiếu field bắt buộc → lỗi nêu đúng tên field
- Manifest có field không nhận diện (typo) → lỗi, KHÔNG im lặng bỏ qua
- `github-release` không `sha256` → lỗi
- Tool `darwin`-only, `Supports` trên linux → false (table test)
- Tool không có `install` cho platform hiện tại → `Supports` false
- `runtime.GOOS`/`GOARCH` mapping đúng — table test, không assert host hiện tại

**Done:** `go test ./internal/manifest/ ./internal/platform/` xanh.

### GĐ2 — Catalog + Resolver

**File:**
- `internal/catalog/catalog.go` — load mọi `tool.yaml`, index theo name/category/tag
- `internal/catalog/catalog_test.go`
- `internal/resolver/resolver.go` — `Resolve(profile, only, exclude, host) ([]Action, error)`
- `internal/resolver/dag.go` — cycle detect + topo sort
- `internal/resolver/dag_test.go`

**Contract:**
- Resolver luôn: profile → only/exclude → dependency closure → platform filter → actions
- **Dependency thắng selection**: `--only claude` vẫn kéo deps. Exclude phá dep → lỗi nêu ai cần
- Actions **deterministic**: sort theo name, cùng input → byte-identical

**Test:**
- A→B→A (cycle) → lỗi cycle, KHÔNG treo
- Diamond A→B,A→C,B→D,C→D → D install 1 lần
- `--only claude` với claude cần git → actions chứa cả git
- `--exclude node` mà claude cần node → lỗi "node required by claude"
- Cùng input 2 lần → actions identical (byte compare)
- Empty profile → tất cả `default: true`

**Done:** resolver trả về đúng thứ tự, cycle không treo, deterministic.

### GĐ3 — Verifier + Platform detect mở rộng

**File:**
- `internal/verifier/verifier.go` — `Verify(tool) (Result, error)`
- `internal/verifier/verifier_test.go`

**Contract:**
- `command` phải có trên PATH → OK
- `version.min` → parse semver, dưới min → `Result{stale}`, không phải error
- Verify là **nguồn sự thật**, không đọc state

**Test:**
- Command có/không trên PATH → OK/NotFound
- Version dưới/đúng min → Stale/OK
- `provides` binary thiếu dù command có → phát hiện

**Done:** verify phân biệt được OK / NotFound / Stale.

### GĐ4 — Installer (github-release + package)

**File:**
- `internal/installer/installer.go` — `Strategy` interface + registry
- `internal/installer/github_release.go` — tải asset theo OS/arch, verify sha256, giải nén vào `~/.aes/bin`
- `internal/installer/package.go` — brew/apt, **apt thì in lệnh chứ không exec sudo**
- `internal/installer/installer_test.go`

**Contract:**
- Registry: `strategy → installer`. Không `switch tool.Name`.
- `github-release`: tải → verify sha256 → **fail thì dừng, KHÔNG giải nén file hỏng** → `chmod +x`
- `package`+apt: **in lệnh `sudo apt install ...`, KHÔNG exec** — user chạy tay
- Timeout mọi lệnh. Output capture, cap 1MB.

**Test:**
- sha256 sai → abort, file `~/.aes/bin` KHÔNG tạo
- `package`+apt → `Installer` trả `ErrNeedsPrivilege`, KHÔNG spawn `sudo`
- Timeout → lỗi nêu "timed out", không treo
- Registry resolve strategy lạ → lỗi rõ

**Done:** sha256 sai không bao giờ tạo binary; apt không bao giờ tự sudo.

### GĐ5 — State + Env

**File:**
- `internal/state/state.go` — `~/.aes/state.json`, atomic write
- `internal/state/state_test.go`
- `internal/envgen/envgen.go` — `~/.aes/env.sh`, atomic
- `internal/envgen/envgen_test.go`

**Contract:**
- State ghi **sau verify pass**. Fail → không ghi.
- Atomic: temp + rename. Crash giữa lúc ghi không để lại JSON hỏng.
- `env.sh` deterministic (không timestamp trong body). Refuse overwrite file không do AES sinh.

**Test:**
- State corrupt → lỗi, KHÔNG coi là "chưa cài" (I9)
- Save atomic: `.tmp` không còn sau success
- Version cao hơn → lỗi nêu version, KHÔNG xoá data
- `env.sh` 2 lần cùng input → byte-identical (deterministic)
- `env.sh` tồn tại không có header AES → refuse ghi

**Done:** state/env atomic, deterministic, corrupt ≠ rỗng.

### GĐ6 — CLI + Setup

**File:**
- `internal/cli/root.go` — cây lệnh
- `internal/cli/setup.go` — `aes setup` orchestration
- `internal/cli/list.go` · `verify.go` · `doctor.go` · `env.go`
- `cmd/aes/main.go`

**Contract:**
- `setup` chạy: detect → load profile → resolve → verify hiện tại → install thiếu → verify lại → state → env → summary
- `--dry-run`: in actions, **không mutate gì**
- `--yes`/`--non-interactive`: không hỏi, deterministic
- `--json`: output parse được
- `doctor`: phát hiện drift (state=cài, binary=mất)

**Test (10 assertion DoD):**
1. `list` hiện trạng thái từ verify
2. `setup --dry-run` in actions, không file đổi
3. install đúng strategy
4. verify pass → state ghi
5. chạy lại idempotent
6. state corrupt → lỗi
7. manifest hỏng → lỗi, không exec
8. platform không hỗ trợ → skip
9. strategy cần privilege → in lệnh
10. 10–15 tool verified: cài thật + verify

**Done:** 10 assertion xanh trên máy thật.

### GĐ7 — Catalog thật

**File:** `tools/<category>/<name>/tool.yaml` × 40–50

- 10–15 tool **verified**: cài thật, verify thật, so sánh version
- 25–35 tool: definition hợp lệ (validate pass) nhưng chưa chạy thật
- Mỗi tool có `tested: true|false`; `doctor` cảnh báo phần chưa test

**Category:** shell · search · terminal · git · runtime · ai · agent · infra · utility

**Done:** `aes list` hiện catalog đúng, ≥10 tool verified trên máy thật.

### GĐ8 — Sandbox test (GĐ sau)

- `aes test` — container Ubuntu 24.04: apt thật, binary thật, verify thật
- macOS: host-safe mode + VM tùy chọn
- `--keep-sandbox` để debug

### GĐ9 — TUI (GĐ sau)

- `aes` mở TUI. **Cùng resolver, cùng engine** (I13)
- Không business logic trong TUI

### GĐ10 — Đóng gói (GĐ sau)

- `curl|sh` bootstrap (tầng 0)
- CI: build matrix macOS/Ubuntu, release khi tag
- Homebrew tap (convenience)

---

## CLI surface

**Core (2):**
```bash
aes setup      # --yes --non-interactive --dry-run --json --profile --only --exclude --force --verbose
aes            # TUI
```

**Maintenance:** `install` · `uninstall` · `update` · `list` · `verify` · `doctor` · `env` · `test` · `search` · `profile`

MVP chỉ: `setup`, `list`, `verify`, `doctor`, `env`.

---

## Ngoài phạm vi

TUI đồ họa ngoài terminal · dotfiles · Docker quản lý · secrets/tokens · IDE/app GUI ·
orchestration (beads, swarm, am) — project khác. **Đừng để AES thành ACFS 2.0.**

---

## Rủi ro

| Rủi ro | Giảm bằng |
|---|---|
| 40–50 YAML chưa verify = lời hứa chưa kiểm | `tested: true/false`; `doctor` cảnh báo |
| Catalog phình, resolver rối | Category là field, không phải directory |
| Ubuntu hỏng không biết tới gian | GĐ8 sandbox container; CI matrix |
| GitHub Release URL vỡ khi đổi tên asset | Asset theo OS/arch + pin version |
| Plan đúng, thực tế sai | `--dry-run` + `doctor` drift + sandbox |
| AES vô tình thành ACFS 2.0 | Invariant I1: không bypass manifest |

---

## Definition of Done (toàn dự án)

- [ ] 13 invariant có test
- [ ] 10 assertion MVP xanh
- [ ] 40–50 tool definitions, ≥10 verified thật
- [ ] `aes setup` một lệnh bootstrap trọn vẹn, exit 0 chỉ khi verify xong
- [ ] `~/.aes/` là ranh giới duy nhất; `~/.agents/` không bị đụng
- [ ] TUI ≡ CLI (cùng Actions)
- [ ] Sandbox chứng minh hoạt động trên Ubuntu thật
