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
| I14 | `sudo` không bao giờ chạy ngầm | Chỉ chạy sau khi in lệnh + xác nhận |
| I15 | Default profile **chỉ chứa `tested: true`** | Profile chứa tool chưa test → validate fail |

### Về terminology

Không có `plan` trong codebase. Object giữa resolver và installer tên **`Actions`**.
Cấm tên `InstallPlan` · `PlanCommand` · `PlanRenderer` · `PlanJSON` — feature đã bị loại,
đừng để nó mọc lại dưới tên khác. `--dry-run` **giữ nguyên**: đó là cơ chế an toàn khi
chạy pipeline, không phải một product surface riêng.

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

| Strategy | Privilege | Đích |
|---|---|---|
| `github-release` | không | `~/.aes/bin` |
| `package` + brew | không | `/opt/homebrew` |
| `package` + apt | **có** | hệ thống |
| `go` / `npm` / `cargo` | không | prefix user |

`sha256` bắt buộc **chỉ với `github-release`**. `brew`/`apt` có integrity riêng — ép sha256
vào đó là metadata giả. Ví dụ đầy đủ:

```yaml
install:
  darwin:
    strategy: github-release
    repository: BurntSushi/ripgrep
    asset:
      arm64: ripgrep-14.1.1-aarch64-apple-darwin.tar.gz
      amd64: ripgrep-14.1.1-x86_64-apple-darwin.tar.gz
    sha256:
      arm64: 4e0c2b0e8f1a...   # bắt buộc — thiếu thì validate fail
      amd64: 9a3f1d2c7b4e...
```

---

## Privilege — câu trả lời cho mâu thuẫn `apt`/sudo

Có mâu thuẫn thật: North Star nói *zero manual install*, còn security model nói *không tự sudo*.

**Quyết định: A — prompt rõ ràng, không bao giờ chạy ngầm.**

Lập luận: rủi ro của `sudo` **không phải là sudo**, mà là *ai chọn cái lệnh đó*. Nếu `sudo`
chạy âm thầm, một `tool.yaml` độc hại kéo theo `apt install <đồ rác>` với mật khẩu của bạn.
Nếu `sudo` **hiện lệnh rồi hỏi**, bạn thấy đúng nó sắp làm gì trước khi gõ mật khẩu.

Vậy nguyên tắc là: **không bao giờ chạy ngầm — nhưng không từ chối cả.**

```
github-release  → không cần sudo → chạy tự do
brew / go / npm  → không cần sudo → chạy tự do
package + apt   → sudo
      ├─ interactive: in lệnh, hỏi xác nhận, rồi chạy
      └─ non-interactive: thử `sudo -n` (CI NOPASSWD hoặc credential cache)
                          → thành công: chạy
                          → thất bại: in lệnh, dừng, exit code riêng
```

Ba điều khoản phiên bản, tất cả đều test được:

1. **Không bao giờ `sudo` khi không hỏi.** `exec.Command("sudo", ...)` chỉ chạy sau khi in
   lệnh và (interactive) nhận xác nhận.
2. **`--non-interactive` dùng `sudo -n`.** Không bao giờ treo chờ mật khẩu. CI có
   `NOPASSWD` thì chạy trọn; không thì báo cáo thay vì treo.
3. **Cần thao tác tay → exit code riêng + state không đánh dấu done.** Agent parse được,
   không giả vờ thành công.

Điều này giữ được cả hai vế: `aes setup` vẫn là **một lệnh chạy đến hết** trên máy có
`sudo` bình thường, và vẫn không bao giờ tự leo thang quyền.

**Giảm tần suất cần sudo:** catalog ưu tiên `github-release` cho mọi tool có release chính thức.
`apt` chỉ là fallback cho thư viện hệ thống và tool không có release.

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

> Thứ tự đổi so với bản trước: **bootstrap (GĐ0) lên đầu**, vì nó là entry point thật.
> Không có đường `máy trắng → AES` thì các giai đoạn sau chỉ là code trên giấy.

### GĐ0 — Bootstrap (làm trước tiên)

**File:**
- `install.sh` — tầng 0: detect OS/arch → tải binary từ GitHub Release → verify sha256 →
  đặt vào `~/.aes/bin/aes`
- `scripts/build.sh` — cross-compile `darwin/{arm64,amd64}` + `linux/{amd64,arm64}`
- `.github/workflows/release.yml` — build matrix, đính kèm checksum

**Contract:**
- Chỉ làm 5 việc: detect, download, verify, đặt binary, báo cáo. **Không cài tool gì.**
  Phần còn lại là `aes setup` lo.
- Binary luôn tự chứa — không phụ thuộc Go/brew/apt ở máy đích
- Checksum bắt buộc, từ `checksums.txt` trong release

**Test:**
- Asset tồn tại + checksum khớp → binary chạy được
- Checksum **sai** → abort, KHÔNG đặt binary
- OS/arch không có asset → báo rõ, không tải file rác

**Done:** máy sạch Ubuntu → `curl … | sh` → `aes --version` chạy được.

### GĐ1 — Manifest + Platform

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
- `package`+apt: theo mô hình privilege — in lệnh, xác nhận (interactive) hoặc `sudo -n`
  (non-interactive). **Không bao giờ `sudo` khi không in trước.**
- `package`+brew: chạy thẳng, không cần privilege
- Timeout mọi lệnh. Output capture, cap 1MB.

**Test:**
- sha256 sai → abort, file `~/.aes/bin` KHÔNG tạo
- `package`+apt **interactive**: không có input confirm → `sudo` KHÔNG chạy (I14)
- `package`+apt **non-interactive**: `sudo -n` fail → in lệnh + exit code riêng, KHÔNG treo
- `package`+brew: chạy, không hỏi
- Timeout → lỗi nêu "timed out", không treo
- Registry resolve strategy lạ → lỗi rõ

**Done:** sha256 sai không tạo binary; `sudo` không bao giờ chạy mà không in trước.

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
9. strategy cần privilege → in lệnh trước, `sudo` chỉ chạy sau xác nhận
10. 10–15 tool verified: cài thật + verify

**Done:** 10 assertion xanh trên máy thật.

### GĐ7 — Catalog thật

**File:** `tools/<category>/<name>/tool.yaml` × 40–50

- 10–15 tool **verified**: cài thật, verify thật, so sánh version → `tested: true`
- 25–35 tool: definition hợp lệ, `tested: false` — **pool để mở rộng, không vào default profile**
- Mỗi tool có `tested: true|false`; `doctor` cảnh báo phần chưa test

**Category:** shell · search · terminal · git · runtime · ai · agent · infra · utility

**Default profile chỉ chứa `tested: true`** (I14). Nếu không, North Star
"environment hoàn chỉnh **đã verify**" là nói dối — 25 tool chưa chạy thật chính là chỗ
sẽ vỡ trên máy người dùng đầu tiên.

**Done:** `aes list` hiện catalog đúng; ≥10 tool verified; `aes setup` cài trọn default
profile trên máy sạch mà không lỗi.

### GĐ8 — Sandbox test (GĐ sau)

- `aes test` — container Ubuntu 24.04: apt thật, binary thật, verify thật
- macOS: host-safe mode + VM tùy chọn
- `--keep-sandbox` để debug

### GĐ9 — TUI (GĐ sau)

`aes` mở TUI. Menu: `Setup` · `Tools` · `Profiles` · `Doctor` · `Environment`

**Tools:** search/filter, tick chọn nhiều → Enter → cùng resolver, cùng installer, cùng verify.
Chọn `rust` thì resolver tự kéo `cargo`. Không khác gì `aes setup --only rust`, chỉ khác cách chọn.

**Ràng buộc cứng — 3 điều TUI không được làm:**

| Không được | Vì sao |
|---|---|
| Nhận command tự do kiểu `brew install xxx` | Phá I1/I3; YAML đã là data, đừng mở lại shell |
| Có fast-path bypass resolver | TUI và CLI phải cho **cùng** Actions, nếu không I13 vỡ |
| Chứa business logic cài đặt | Mọi quyết định thuộc về core |

TUI chỉ là: **thu thập lựa chọn → gọi core → hiển thị kết quả.** Giống hệt cách `ssh` không
tự cấu hình port forwarding.

> Agent: `aes setup` · Human: mở `aes`, chọn đúng cái mình cần. **Hai cách dùng, một engine.**

### GĐ11 — Đóng gói tiện ích (GĐ sau)

Bootstrap đã ở GĐ0. Phần còn lại chỉ là phương tiện thay thế, không phải đường chính:
- Homebrew tap (`brew install aes`)
- `go install github.com/…/cmd/aes@latest`

Cả hai đều là *convenience method*. Đường chuẩn vẫn là `curl … | sh` → `aes setup`.

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

- [ ] 15 invariant có test
- [ ] 10 assertion MVP xanh
- [ ] 40–50 tool definitions, ≥10 verified thật
- [ ] `aes setup` một lệnh bootstrap trọn vẹn, exit 0 chỉ khi verify xong
- [ ] `~/.aes/` là ranh giới duy nhất; `~/.agents/` không bị đụng
- [ ] TUI ≡ CLI (cùng Actions)
- [ ] Sandbox chứng minh hoạt động trên Ubuntu thật
