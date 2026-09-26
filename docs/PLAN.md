# agents_environment_setup — Plan chi tiết (để chốt)

> **Trạng thái:** Chờ bạn review · chưa viết dòng code nào
> **Ngày:** 2026-09-26
> **Research:** tự tra trực tiếp từ repo gốc, commit `7dd2bb3` (2026-09-25)

---

## Phần 1 — Research: ACFS thực sự là gì

Tôi clone repo gốc và đo. Dưới đây là số thật, không phải ước lượng.

### 1.1 Quy mô

| Hạng mục | Số liệu |
|---|---|
| `install.sh` | **12.289 dòng** (530 KB) |
| Tổng shell trong repo | **202.466 dòng**, 238 file `.sh` |
| `acfs.manifest.yaml` | 157 KB, **74 module** |
| `install_asset` call | **89 dòng** tra cứu cứng |
| `checksums.yaml` | 48 installer, có `url` + `sha256` |
| Version | 0.9.0 |
| Contributor | 1 người |
| Commit 90 ngày | 1 |

### 1.2 Đây là phát hiện quan trọng nhất

> **ACFS không có plugin system. Và manifest 157KB gần như là tài liệu chết.**

Tôi grep `plugin_add`, `plugin register`, `plugin_dir`, `plugin.yaml` — **không có kết quả nào**.
Mảng `ACFS_MODULE_PLUGIN_PACKAGE/VERSION/SHA256` có tồn tại nhưng **rỗng**.

Tệ hơn: manifest **không phải đường chạy thật**. Research (109 agent, có adversarial verify) phát
hiện `ACFS_GENERATED_DEFAULT_CATEGORIES=()` rỗng — nghĩa là codegen từ manifest **bị tắt mặc định**.
Production chạy các function bash viết tay. Thêm tool = sửa **cả hai file**, và test của chính dự
phải bật `ACFS_GENERATED_MIGRATED_CATEGORIES` bằng tay mới chạy được codegen.

Bằng chứng cụ thể — danh sách tool thật là array bash cứng, không phải manifest:

```bash
# install.sh:8205
local optional_pkgs=(lsd eza bat fd-find btop dust neovim htop tree ncdu
                     httpie entr mtr pv docker.io docker-compose-plugin cosign)
```

Ngoài ra `web:` metadata chiếm **30KB (18.7%)** manifest mà installer **không bao giờ đọc**.

**Kết luận:** bạn cảm giác "không dễ thêm tool" là chính xác. ACFS không chỉ thiếu plugin — nó có
một lớp trừu tượng *trông như* abstraction nhưng thực tế không nối vào đường chạy.

### 1.3 Ba lớp chặn cứng trước macOS

Không chỉ "chưa hỗ trợ" — có **3 cổng chặn cứng**, đều fail trước khi làm bất cứ việc gì:

**Cổng 1 — chặn OS.** `ensure_ubuntu()` (dòng 6512) đọc `/etc/os-release`, không có thì
`log_fatal "Cannot detect OS. ACFS supports Ubuntu 22.04+ or Arch Linux."`
macOS không có file đó → chết ngay.

**Cổng 2 — chặn shell.** Dòng 65-66 gate bash:
```bash
if [ "${BASH_VERSINFO[0]:-0}" -lt 4 ] || { [ ... -eq 4 ] && [ ... -lt 4 ]; }
```
Cần **bash ≥ 4.4** (vì `declare -A`). macOS ship **bash 3.2**. Script thậm chí không parse được.

**Cổng 3 — không có nhánh Darwin.** 12.289 dòng:

| Mẫu | Số lần |
|---|---|
| `ubuntu` | **117** |
| `apt` | 76 |
| `systemctl` | 37 |
| `/etc/apt` | 19 |
| `Darwin` / `darwin` | **0 / 0** |
| `brew` | 8 |

`uname` xuất hiện 4 lần, cả 4 đều là `uname -m` (CPU arch) — **không có `uname -s`** để rẽ nhánh OS.
Trong 4.251 dòng manifest: **0 chuỗi `darwin`/`brew`**. Có `defaults.user: ubuntu` ngay đầu file.

Không có abstraction package manager nào (`apt`/`brew`/`port`) — nên không phải "thêm nhánh Darwin"
nữa, mà là **thay cả tầng platform**.

### 1.3b Lỗi cụ thể liên quan trực tiếp tới bạn

| Lỗi | Ảnh hưởng |
|---|---|
| `ensure_ubuntu()` chết trước mọi việc | Chạy installer trên Mac = fail ngay |
| bash 4.4 gate | `install.sh` **không parse** trên macOS bash 3.2 |
| `~/.zshrc` bị ghi đè bằng `cat >` không guard | Xoá `~/.acfs` → **mọi shell báo lỗi** (khác hẳn trường hợp bạn gặp: silent) |
| `starhip` chỉ xuất hiện 1 lần, trong comment | Không bao giờ được cài |

Lưu ý dòng `source $HOME/.zshrc.local` **có** guard còn `source $HOME/.acfs/...` thì không —
một sai sót rõ ràng trong chính code upstream.

### 1.4 Schema manifest — cái đáng giữ

`acfs.manifest.yaml` có schema đúng, dù đường chạy bị tắt:

```yaml
modules:
  - id: base.system
    phase: 1
    run_as: root              # root | current | target
    optional: false
    tags: [critical]
    installed_check:          # ← verify
      run_as: current
      command: "command -v curl && command -v git"
    install:                  # ← install
      - apt-get install -y curl git jq
```

**Đây là phần duy nhất đáng port.** Schema đúng — chỉ bị nhúng trong file 157KB, không tách, và
không ai bật nó lên.

**Bài học cho thiết kế của tôi:** schema đẹp mà không nối vào đường chạy thì còn tệ hơn không có.
Tool mới của tôi phải đảm bảo manifest **là** nguồn duy nhất, không có đường vòng nào.

### 1.5 Checksum contract — cái đáng giữ nhất

`install_asset` (dòng 4630) làm việc này trước khi copy **mọi** file:

```
1. Chặn path traversal (`..`)
2. Chỉ chấp nhận source tree đã verify
3. Chặn symlink
4. Tính SHA256 file nguồn
5. So với checksums.yaml (48 entry)
6. Lệch → abort
```

Đây là thiết kế bảo mật thật sự tốt. Tool cài file vào `~/.acfs` và `~/.zshrc.local` — nếu bị
tấn công đường dẫn, nó tự chặn mình.

**Port nguyên vẹn.** Tôi đã đưa `sha256` bắt buộc vào schema plugin của mình.

### 1.6 Cái gì nên bỏ

| Của ACFS | Vì sao bỏ |
|---|---|
| Khung "agentic coding flywheel" | Bạn thấy quá cứng nhắc — core của tool nên là core của *công cụ* |
| `onboard/lessons/` 35 file .md | Tài liệu dạy Linux/ssh/tmux, không liên quan macOS |
| 13 lần `systemctl` | Không có systemd trên macOS |
| 123 `run_as: target` | Phức tạp thừa — bạn dùng 1 user |
| 9 phase | Quá nhiều; tool của bạn cần 2-3 |
| 238 file shell | Đây chính là thứ gây ra vấn đề |

### 1.7 Bảng quyết định port/drop

| Thành phần | Quyết định | Lý do |
|---|---|---|
| Schema module (id/verify/install/optional) | **PORT** | Đúng — nhưng bắt buộc nối vào đường chạy, không để chết như ACFS |
| Checksum SHA256 contract | **PORT** | Bảo mật thật, tôi đã đưa vào schema |
| `installed_check` → `verify` | **PORT** | Đúng tên gọi hơn |
| `run_as: root` → `needs_sudo` | **PORT** | Nhưng core **không** tự gọi sudo |
| `phase` | **GIẢM** còn 3 | ACFS dùng 9, 39 module dồn vào phase 9 |
| `web:` metadata | **DROP** | 30KB, installer không đọc |
| Module gộp 1 file 157KB | **TÁCH** | Mỗi tool 1 file |
| Codegen bị tắt | **KHÔNG LÀM** | Chính là lỗi kiến trúc của ACFS |
| `onboard/lessons` | **DROP** | Linux/ssh/tmux, không liên quan |
| Khung flywheel | **DROP** | Bạn yêu cầu |
| `systemctl` | **DROP** | Không portable |
| `ensure_ubuntu()` | **THAY** | Không phải port — phải viết tầng `detect` |
| Checksum monitor CI (7 workflow) | **CÂN NHẮC** | Nặng, chỉ cần khi public |

Repo có ~1.654 star, 11 issue mở, chưa archive — vẫn hoạt động. Nhưng 12k dòng bash với 1
contributor là rủi ro bảo trì thật, và người dùng duy nhất của nó là bạn.

---

## Phần 2 — Đề xuất kiến trúc

### 2.1 Nguyên tắc thiết kế

Mỗi quyết định dưới đây truy về một sự cố ACFS đã gây ra:

| Nguyên tắc | Vì sao |
|---|---|
| **Manifest là nguồn duy nhất** | ACFS có manifest 157KB + đường chạy riêng viết tay. Schema đẹp mà không nối vào hệ thống thì **tệ hơn không có** — nó tạo cảm giác an toàn giả |
| **Không tự `sudo`** | Bạn phải thấy lệnh trước khi nó chạy với quyền bạn |
| **Không sửa `~/.zshrc`** | ACFS dùng `cat >` không guard. Xoá `~/.acfs` → mọi shell báo lỗi |
| **State ghi sau verify** | State nói dối thì mọi thứ khác cũng đáng ngờ |
| **`sha256` bắt buộc** | Port trực tiếp từ checksum contract của ACFS |
| **Mỗi plugin 1 file** | 74 module chung file 157KB không scale |
| **Deterministic output** | Cùng input → cùng output, dễ debug |
| **Từ chối ghi file lạ** | `env.sh` không phải do tool sinh → không đụng |

Nguyên tắc đầu tiên là bài học lớn nhất từ research. ACFS *có* abstraction, *có* schema,
*có* manifest — nhưng đường chạy thật nằm nơi khác. Đó là mẫu hình "trông như đã modular nhưng
không modular". Tool mới phải tránh đúng cái bẫy đó: **không có đường vòng nào bypass manifest.**

### 2.2 Plugin là gì

Plugin = **một file YAML**. Không code, không binary riêng.

```yaml
name: ripgrep
description: Tìm kiếm text cực nhanh
provides: [rg]
tags: [search]

install:
  darwin:
    run: brew install ripgrep
  linux:
    run: sudo apt install -y ripgrep
    needs_sudo: true          # core in ra lệnh, KHÔNG tự chạy

verify:
  path_based: [rg]            # rẻ hơn shell
  version: rg --version       # parse để check min_version
  min_version: "14.0.0"

env:
  - name: RIPGREP_CONFIG_PATH
    value: ~/.ripgreprc
```

**Vì sao YAML thuần, không phải binary plugin:**

| | YAML | Binary plugin |
|---|---|---|
| Thêm tool | viết 1 file | viết + compile |
| Ngôn ngữ | không giới hạn | bị giới hạn theo runtime |
| Review | đọc được | phải đọc code |
| Đủ cho | ripgrep, fzf, zoxide, mise | mọi thứ |

ACFS **không** có manifest per-tool — đó mới là thứ bạn thiếu. Không cần binary plugin để bắt
đầu. Chỗ cần logic thật sự (như `newproj` của ACFS) mới đáng viết bằng code — và những thứ
đó bạn đã bỏ.

### 2.3 Cấu trúc thư mục

```
agents_environment_setup/
├── cmd/aes/main.go
├── internal/
│   ├── manifest/     # đọc + validate plugin.yaml
│   ├── detect/       # OS, arch, package manager
│   ├── exec/         # chạy lệnh, chặn sudo
│   ├── state/        # ~/.agents/state.json, atomic
│   ├── envgen/       # sinh ~/.agents/env.sh
│   └── cli/          # cây lệnh
├── plugins/          # plugin của repo này
│   ├── ripgrep/plugin.yaml
│   ├── fzf/plugin.yaml
│   ├── zoxide/plugin.yaml
│   ├── bat/plugin.yaml
│   └── direnv/plugin.yaml
└── Makefile
```

### 2.4 Bộ lệnh

| Lệnh | Việc |
|---|---|
| `aes list` | mọi plugin + trạng thái (✓ cài / ✗ thiếu / ! quá cũ) |
| `aes install [tên]` | cài 1 tên, hoặc tất cả đang thiếu |
| `aes remove <tên>` | gỡ khỏi state |
| `aes doctor` | chẩn đoán, chỉ báo cáo |
| `aes plugin add <path>` | copy plugin vào `~/.agents/plugins/` |
| `aes env --write` | sinh `~/.agents/env.sh` |
| `aes search <từ>` | lọc theo tên/tag |

### 2.5 Luồng cài

```
aes install ripgrep
  1. Load ~/.agents/plugins/ripgrep/plugin.yaml  → hỏng? báo rõ, dừng
  2. verify                                     → có rồi? in ra, bỏ qua
  3. chọn target theo OS                        → darwin | linux
  4. target cần sudo? → IN LỆNH, dừng ở đây
  5. chạy lệnh, capture stdout+stderr
  6. verify lại → pass: ghi state | fail: KHÔNG ghi, báo cáo
```

---

## Phần 3 — Những gì cần bạn chốt

Tôi đã điền sẵn đề xuất ở cột **→**. Bạn sửa hoặc ghi `đồng ý` là tôi code.

### 3.1 Quyết định kiến trúc

| # | Câu hỏi | Đề xuất | → |
|---|---|---|---|
| 1 | Plugin là YAML thuần hay binary? | **YAML thuần** | |
| 2 | Có tự sửa `~/.zshrc` không? | **Không** — chỉ sinh `env.sh` | |
| 3 | Cài bằng gì? | **brew/apt + `url`+`sha256` tải thẳng** | |
| 4 | `sha256` bắt buộc? | **Bắt buộc** (port từ ACFS) | |
| 5 | Có tự gọi sudo? | **Không** — in lệnh | |
| 6 | `run_as: target` có cần? | **Không** — bỏ, chỉ `needs_sudo` | |
| 7 | Bao nhiêu phase? | **3** (core / dev / opt-in) | |
| 8 | Tên binary? | **`aes`** | |
| 9 | Có giữ khung flywheel? | **Không** | |
| 10 | Port checksum contract? | **Có** | |

### 3.2 Câu hỏi tôi chưa tự trả lời

| # | Câu hỏi | Vì sao cần bạn | → |
|---|---|---|---|
| 11 | Tool cá nhân hay public? | Quyết định có cần versioning + validate chặt | |
| 12 | Một máy hay nhiều máy? | Nhiều máy → thêm `export`/`import` | |
| 13 | Có test Ubuntu thật không? | Tôi chỉ chạy được macOS | |
| 14 | Cài `aes` bằng cách nào? | `go install` vs GitHub Releases vs brew | |
| 15 | Phạm vi bản đầu? | Tối thiểu hay đầy đủ | |
| 16 | Tên repo? | Cần cho module path | |
| 17 | Deadline? | Tôi cắt scope cho vừa | |

### 3.3 Điều tôi đã quyết, không cần bạn trả lời

| Quyết định | Lý do |
|---|---|
| Không tự `sudo` | Nhìn thấy mới tin |
| State ghi sau verify | State nói dối thì phần còn lại cũng đáng ngờ |
| Không sửa `~/.zshrc` | ACFS đã chứng minh điều này hỏng |
| `env.sh` có header "DO NOT EDIT" | Luôn biết file nào do tool sinh |
| Refuse ghi file không phải do tool sinh | Không đụng việc tay của bạn |
| Không TUI ở bản đầu | CLI thuần test/script/log được |
| Bỏ `~/.acfs` | Thay thế trọn vẹn |
| Không `starship` | Bạn dùng p10k |
| Ghi state atomic | Crash giữa lúc ghi không được để lại JSON hỏng |

---

## Phần 4 — Kế hoạch thực thi

Chỉ bắt đầu khi bạn trả lời **§3.1** (10 câu kiến trúc). §3.2 có thể trả sau.

### Giai đoạn 1 — Nền
1. `go mod init`, Makefile, `.gitignore`
2. `internal/manifest` — struct, parse, validate
3. `internal/detect` — OS, arch, package manager
4. `internal/state` — atomic read/write
5. `internal/cli` — `list`, `plugin add`, `plugin list`
6. 5 plugin thật
7. Test: manifest hỏng → lỗi rõ; state corrupt → không coi là "chưa cài"

**Xong khi:** `aes list` hiện đúng 5 tool với trạng thái thật.

### Giai đoạn 2 — Cài
8. `internal/exec` — chạy lệnh, timeout, chặn sudo
9. `install` theo OS
10. `internal/envgen` — sinh `env.sh`
11. `aes env --write`
12. Test: verify trước/sau; fail thì không ghi state

**Xong khi:** `aes install ripgrep` chạy thật, `env.sh` đúng.

### Giai đoạn 3 — Bảo trì
13. `remove`, `doctor`
14. `search`
15. Test contract: cài hỏng → state không nói dối

### Giai đoạn 4 — Đóng gói *(chỉ khi cần)*
16. GitHub Actions: build → release khi tag
17. `curl | sh` installer
18. Homebrew tap

### Giai đoạn 5 — Để trống cố ý
Plugin config, binary download nâng cao, TUI điều khiển, export/import. **Không làm sớm** —
thêm sớm là thêm sớm chỗ sai không sửa được.

---

## Phần 5 — Rủi ro

| Rủi ro | Giảm bằng |
|---|---|
| YAML không đủ cho tool phức tạp | Không thêm `command:` cho tới khi gặp case thật |
| Chạy macOS, hỏng Ubuntu | §3.2 #13 — test thật trước khi tin |
| Module path sai khi đổi tên repo | §3.2 #16 — chốt sớm |
| Không ai dùng ngoài bạn | Không sao. Tool 1 người dùng là đủ, chỉ cần không cứng |

---

## Phụ lục — Trạng thái repo

Đã tạo, **chưa có code**:
- `docs/PLAN.md` (file này)
- `docs/QUESTIONS.md` — bản cũ, đã gộp vào plan, xoá khỏi scope

Lịch sử git: 3 commit docs.
