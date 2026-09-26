# agents_environment_setup — Implementation Plan

> **Trạng thái:** Draft để review · 2026-09-26
> **Tác giả:** Claude (thay đổi bởi bạn bất cứ lúc nào)
>
> Dựa trên: ACFS (`Dicklesworthstone/agentic_coding_flywheel_setup`) đã dùng 3 tháng,
> mất `~/.acfs` ngày 25/09 và shell chết âm thầm. Bài học rút ra quyết định kiến trúc bên dưới.

---

## 1. Vấn đề đang giải

ACFS là Bash installer khoảng 8.000 dòng, Ubuntu-first, ép mọi người vào một khuôn
"agentic coding flywheel". Ba điểm gãy thật:

| Vấn đề | Hậu quả thực tế |
|---|---|
| `~/.zshrc` delegate qua `if [[ -f ]]` không có `else` | Mất `~/.acfs` → shell rơi về zsh trần, **không lỗi, không cảnh báo** |
| Danh sách tool hardcode trong `install.sh` | Thêm tool = sửa script 8000 dòng |
| `apt`/`systemd` hardcode | Chạy được trên Ubuntu, vô dụng trên macOS |

Người dùng muốn: **dễ thêm tool, dễ cài, không ép khuôn mẫu, chạy được cả macOS lẫn Ubuntu.**

---

## 2. Mục tiêu & ràng buộc

**Làm**
- Core + plugin tách rời. Thêm tool = thêm 1 file, không sửa core.
- macOS (Apple Silicon) + Ubuntu, cùng một binary.
- Tôn trọng Homebrew/apt hiện có — **không cài lại thứ đã có**.
- Không tự ý sửa file của người dùng nếu không được hỏi.
- Tool có TUI là plugin hạng nhất, không phải ngoại lệ.

**Không làm** (ghi rõ để tránh phình scope)
- TUI đồ họa — CLI thuần, dễ script hơn
- Quản lý dotfiles
- Docker/dev containers
- Secrets/tokens
- Cài app GUI, IDE, driver

**Ràng buộc kỹ thuật**
- Go 1.26+, single static binary, zero runtime dependency
- Không shell out tới `bash` cho logic — chỉ spawn khi cần chạy lệnh cài

---

## 3. Kiến trúc

```
agents_environment_setup/
├── cmd/aes/main.go              # entrypoint
├── internal/
│   ├── cli/                     # cobra command tree
│   ├── manifest/                # đọc & validate plugin.yaml
│   ├── detect/                  # phát hiện OS, package manager, công cụ đã có
│   ├── exec/                    # chạy lệnh cài, an toàn
│   ├── state/                   # đọc/ghi state.json
│   └── envgen/                  # sinh ~/.agents/env.sh
├── plugins/                     # BẢN THÂN repository — plugin của bên thứ ba
│   ├── ripgrep/plugin.yaml
│   ├── fzf/plugin.yaml
│   └── zoxide/plugin.yaml
├── docs/
└── Makefile
```

Binary tên `aes`, state ở `~/.agents/`.

### 3.1 Plugin là gì

Plugin = **một thư mục chứa `plugin.yaml`**. Không code Go, không phải binary riêng.
Cài tool = đọc YAML → biết lệnh cài theo OS → chạy → verify.

Đây là quyết định quan trọng nhất. Lý do:

- Thêm tool mới: tạo 1 file YAML. Không sửa, không compile, không version.
- `aes plugin add <path>` copy thư mục vào `~/.agents/plugins/`.
- Nếu sau này cần logic, mở rộng được bằng `hooks:` — không phải viết lại.
- Người khác đóng góp được ngay, không cần hiểu Go.

**Trade-off thừa nhận:** plugin không tự chứa logic phức tạp. Cần thì thêm `hooks:` chạy
shell command, hoặc chuyển sang `command:` (xem §8, hướng B).

### 3.2 Schema `plugin.yaml`

```yaml
name: ripgrep                      # bắt buộc, kebab-case, unique
description: Tìm kiếm text cực nhanh
homepage: https://github.com/BurntSushi/ripgrep

provides:                         # binary cần kiểm tra
  - rg

# Cài theo OS. `run:` mặc định = shell chạy string trên.
install:
  darwin:
    run: brew install ripgrep
  linux:
    run: sudo apt install -y ripgrep
    # hoặc dùng cơ chế cài binary không cần root:
    #   url: https://github.com/BurntSushi/ripgrep/releases/download/14.1.1/...
    #   bin: rg
    #   sha256: abc123...

verify:                           # cách kiểm tra đã cài chưa
  run: command -v rg
  # hoặc
  # version: rg --version        # parse semver, so sánh min_version

# PATH sau khi cài (nếu cài vào ~/.local/bin chẳng hạn)
env:
  - PATH: $HOME/.local/bin

tags: [search, rust]               # cho `aes search`
```

`min_version` trong `verify` để `doctor` báo "có nhưng quá cũ".

### 3.3 State

`~/.agents/state.json`, JSON thuần, version có field để migrate:

```json
{
  "version": 1,
  "installed": {
    "ripgrep": {
      "method": "brew",
      "version": "14.1.1",
      "installed_at": "2026-09-26T13:00:00Z"
    }
  }
}
```

Ghi **sau khi** verify pass. Verify fail thì không ghi — state không được nói dối.

---

## 4. Lệnh CLI

| Lệnh | Việc | Ưu tiên |
|---|---|---|
| `aes list` | mọi plugin + trạng thái (✓ cài, ✗ thiếu, ! quá cũ) | Cao |
| `aes install [tên]` | cài 1 tên, hoặc tất cả thiếu | Cao |
| `aes remove <tên>` | gỡ | TB |
| `aes doctor` | chẩn đoán, chỉ báo cáo | Cao |
| `aes plugin add <path>` | copy plugin vào `~/.agents/plugins/` | Cao |
| `aes plugin list` | các plugin đã cài | TB |
| `aes search <từ>` | lọc theo tên/tag/description | Thấp |
| `aes env` | in ra env.sh, hoặc ghi file | Cao |
| `aes update` | chạy lại install cho tool đang có (nâng version) | Thấp |

Mặc định chạy không cần subcommand → in help. Không có `aes` trần cưỡi thêm việc.

---

## 5. Tích hợp shell

**Quyết định: chỉ sinh `~/.agents/env.sh`, không tự sửa `~/.zshrc`.**

```bash
# ~/.agents/env.sh — được GENERATE, không bao giờ tự ghi đè tay
# Header ghi nguồn + thời điểm, để bạn luôn biết ai sinh ra
export AGENTS_ENV_HOME="/Users/x/.agents"
export PATH="$AGENTS_ENV_HOME/bin:$PATH"
```

Bạn thêm đúng 1 dòng vào `~/.zshrc`:
```bash
source ~/.agents/env.sh
```

**Vì sao không tự sửa `~/.zshrc`:** ACFS đã chứng minh tool tự quyết định sẽ mắc kẹt
khi file đích đổi. Giữ quyền kiểm soát cho bạn; tool chỉ sinh file, không chỉnh file bạn.

`~/.agents/env.sh` có thể không tồn tại (người dùng mới xoá cả thư mục) — nên `~/.zshrc`
thêm guard có `else` báo lỗi. Lần này **lỗi nằm ở file của bạn, do bạn kiểm soát**,
không phải bên thứ ba viết vào khi bạn không hay biết.

---

## 6. Luồng cài đặt

```
aes install ripgrep
  1. Load ~/.agents/plugins/ripgrep/plugin.yaml      → fail thì báo rõ, dừng
  2. Chạy verify                                    → đã có? in ra, hỏi cài lại? (mặc định: bỏ qua)
  3. Chọn install theo uname                         → darwin | linux
  4. Nếu cần sudo: IN RA LỆNH cho user tự chạy, không tự gọi sudo
  5. Chạy lệnh, capture stdout/stderr
  6. Verify lại                                     → pass: ghi state; fail: KHÔNG ghi, báo cáo
```

**Không bao giờ tự `sudo`.** `aes install` in ra:
```
Cần quyền root để cài ripgrep trên Ubuntu. Chạy lệnh này:

  sudo apt install -y ripgrep

Xong rồi chạy lại: aes install ripgrep
```
Bạn thấy đúng nó định làm gì trước khi nó chạy với quyền bạn.

---

## 7. Cross-platform

| | macOS (Apple Silicon) | Ubuntu |
|---|---|---|
| Detect | `runtime.GOOS == "darwin"` | `"linux"` |
| Homebrew | `/opt/homebrew` | `/home/linuxbrew/.linuxbrew` hoặc `/usr` |
| Package mặc định | brew | apt |
| Không root | brew không cần sudo | cần → in lệnh |
| Tool tải binary | tải về `~/.agents/bin/` | tải về `~/.agents/bin/` |

`brew` và `apt` chỉ được coi là **một trong nhiều installer**. Manifest có thể khai báo
nhiều phương án, agent chọn theo thứ tự ưu tiên và báo rõ đang chọn gì.

---

## 8. Câu hỏi cần bạn quyết

**Cách trả lời:** viết sau `→`. Trả lời trực tiếp trong file này, hoặc nhắn lại tôi theo
số mục. ⛔ = chặn tôi bắt đầu code · 🟡 = ảnh hưởng kiến trúc · ⟢ = chi tiết, tôi tự chọn được

Tổng 24 câu. Không cần trả hết cùng lúc — **6 câu ⛔ là đủ để tôi bắt tay vào Giai đoạn 1.**

---

### 8.1 ⛔ Plugin mô tả tool bằng gì?

| | Bạn viết | Được | Mất |
|---|---|---|---|
| **A** | 1 file YAML | Nhanh nhất, ai cũng làm | Tool có logic riêng phải viết vòng ngoài |
| **B** | YAML + `hooks:` shell | Đủ cho hầu hết | Vẫn phải viết shell |
| **C** | Plugin là binary riêng | Mạnh nhất — Go/Python/TS đều được | Nặng, phức tạp |

Tôi đề xuất **B**. Đủ cho ripgrep/fzf/zoxide, mở đường lên C mà không phá cái đã có.
→

### 8.2 ⛔ Quan hệ với `~/.zshrc`

| | Cách | Hệ quả |
|---|---|---|
| **A** | Tôi sinh `env.sh`, bạn tự thêm 1 dòng `source` | Bạn kiểm soát 100% *(tôi nghiêng về đây)* |
| **B** | Tôi tự thêm dòng vào `~/.zshrc`, backup trước | Tiện hơn, nhưng tool có quyền ghi file bạn |
| **C** | Không sinh gì, chỉ in lệnh để copy | An toàn nhất |

Lý do tôi nghiêng A: ACFS đã chứng minh tool tự quyết sẽ mắc kẹt khi file đích đổi.
→

### 8.3 ⛔ Cài bằng gì?

Máy bạn đã có 106 brew formula — phần lớn tool dùng được Homebrew. Nhưng `atuin`,
`zoxide` có bản tải thẳng cũng tốt.

- **A.** Chỉ brew/apt.
- **B.** Thêm `url:` + `sha256:` để tải thẳng vào `~/.agents/bin/`. *(tôi nghiêng về B)*

→

### 8.4 ⛔ Đây là tool cá nhân hay để chia sẻ?

- **A.** Chỉ bạn dùng, máy này. → thiết kế gọn, tôi tự quyết nhiều hơn
- **B.** Public GitHub, người khác dùng được. → cần validate schema, versioning, tài liệu
- **C.** Private, có đồng nghiệp dùng

→

### 8.5 ⛔ Bao nhiêu máy cần đồng bộ?

- **A.** Chỉ máy này
- **B.** Có máy Ubuntu nữa
- **C.** Nhiều máy, cần export/import bộ plugin

Nếu chọn C, tôi thêm `aes export` / `aes import` vào Giai đoạn 2.
→

### 8.6 ⛔ Có test Ubuntu thật không?

Tôi chỉ chạy được trên máy này (macOS). Để chắc Ubuntu không hỏng:

- **A.** Bạn test trên Ubuntu thật sau khi tôi giao
- **B.** Tôi dùng Docker chạy Ubuntu 24.04 để test CI
- **C.** Không cần, chạy được trên macOS là đủ

→

### 8.7 🟡 Plugin lưu ở đâu?

- **A.** `~/.agents/plugins/` — copy thư mục vào, không cần git
- **B.** Một repo riêng chứa plugin, `aes plugin sync` kéo về
- **C.** Cả hai

→

### 8.8 🟡 Thêm tool mới bằng cách nào?

- **A.** Copy thư mục plugin vào `~/.agents/plugins/`
- **B.** `aes plugin add <git-url>` tự tải
- **C.** `aes new-plugin <tên>` sinh skeleton rồi bạn điền
- **D.** Viết file YAML trong repo `plugins/`, `aes install --all` đọc từ đó

Chọn nhiều được.
→

### 8.9 🟡 Plugin có cần tham số riêng không?

Ví dụ: plugin `docker` cần nhớ bạn dùng colima hay docker-desktop; plugin `mise` cần
biết bạn quản lý version nào.

- **A.** Không cần, mọi plugin giống nhau
- **B.** Cần, có `aes plugin config <tên>` để sửa

→

### 8.10 🟡 Plugin có cần `min_version` không?

Để `doctor` báo "đã có nhưng quá cũ".

- **A.** Không, cài là được
- **B.** Có, khai trong manifest, `doctor` cảnh báo

→

### 8.11 🟡 Có cần `aes update` nâng version không?

- **A.** Không, tôi tự `brew upgrade` như thường
- **B.** Có, `aes update` chạy lại install cho tool đang có

Nếu A, tôi bỏ lệnh này khỏi scope.
→

### 8.12 🟡 `aes remove` gỡ tới đâu?

- **A.** Chỉ gỡ khỏi state, không đụng binary
- **B.** Gọi `brew uninstall` / xoá binary thật
- **C.** Hỏi từng lần

→

### 8.13 🟡 `doctor` nên kiểm tra gì?

Ngoài "tool đã cài chưa":
- ⬜ PATH có hợp lệ, không trùng lặp
- ⬜ Có tool cài 2 bản ở 2 nơi
- ⬜ Version quá cũ so với `min_version`
- ⬜ Thiếu dependency giữa các tool
- ⬜ `~/.zshrc` có dòng `source ~/.agents/env.sh` chưa
- ⬜ Shell đang chạy có hấp thụ PATH mới chưa
- ⬜ Có gì khác: ___

→

### 8.14 🟡 Có `doctor --fix` tự sửa không?

- **A.** Không, chỉ báo cáo *(tôi nghiêng về A — tool tự sửa thì bạn không biết nó đã đổi gì)*
- **B.** Có, opt-in qua cờ

→

### 8.15 🟡 Cần cài tool cần `sudo` không, xử lý sao?

Ví dụ `apt install`, `systemctl`.

- **A.** In ra lệnh để bạn tự chạy *(an toàn, bạn thấy rõ nó làm gì)*
- **B.** Hỏi mật khẩu sudo rồi tự chạy
- **C.** Không hỗ trợ, chỉ cài được thứ không cần root

→

### 8.16 🟡 Có cần shell completion không?

Gõ `aes` rồi Tab. ACFS có nhưng chưa bao giờ test được.

- **A.** Cần
- **B.** Không cần

→

### 8.17 🟡 Tên binary là gì?

Tôi đặt `aes` (viết tắt). Đổi sau cũng dễ nhưng nên chốt sớm vì xuất hiện ở mọi tài liệu.

Gợi ý: `aes` · `agents-env` · `aenv` · `envsetup`
→

### 8.18 🟡 Bạn cài binary `aes` bằng cách nào?

| | Cách | Đánh đổi |
|---|---|---|
| **A** | `go install github.com/.../cmd/aes@latest` | Quen Go, cần mạng mỗi lần update |
| **B** | Tải từ GitHub Releases | Nhanh, 1 file, cần CI build |
| **C** | `brew install` từ tap riêng | Tiện nếu có Homebrew |
| **D** | Cả A và B | A lúc dev, B khi phát hành |

→
*(nếu chọn B hoặc C, tôi thêm GitHub Actions vào Giai đoạn 4)*

### 8.19 🟡 Phạm vi bản đầu?

| | Bao gồm |
|---|---|
| **Tối thiểu** | `list` + `doctor` + `plugin add` + `install` qua brew/apt, 5 plugin thật |
| **Đầy đủ** | Thêm `remove`, `update`, `search`, `env`, CI release |

→

### 8.20 ⟢ Có cần hỗ trợ bash / fish không?

ACFS chỉ zsh. Tôi nghiêng về chỉ zsh, thiết kế để thêm bash sau.

- **A.** Chỉ zsh *(tôi đề xuất)*
- **B.** zsh + bash
- **C.** zsh + bash + fish

→

### 8.21 ⟢ Có cần quản lý `mise` / runtime versions không?

Máy bạn chưa có `mise`. Có cần tool này lo node/python/go version không?

- **A.** Không, để ngoài phạm vi
- **B.** Có, làm plugin `version-manager`

→

### 8.22 ⟢ Có cần hỗ trợ `direnv` không?

Direnv tự kích hoạt env theo thư mục project. Bạn đã có direnv qua brew.

- **A.** Không đụng, để bạn tự cấu hình
- **B.** Có, `aes` khởi tạo `.envrc` cho plugin

→

### 8.23 ⟢ Địa chỉ repo ở đâu?

Tên module Go cần dạng `github.com/<user>/<repo>`. Ví dụ:
`github.com/tranquangdang21/agents-environment-setup`

- ⬜ Đúng, dùng luôn
- ⬜ Khác: ___

→

### 8.24 ⟢ Có deadline không?

Nếu có, tôi cắt scope cho vừa.
→

---

### Đã tự quyết, không cần bạn trả lời

| Quyết định | Lý do |
|---|---|
| Không tự `sudo` | Nhìn thấy mới tin |
| State ghi sau verify, không ghi trước | State nói dối thì mọi thứ khác cũng đáng ngờ |
| Không sửa `~/.zshrc` | ACFS đã chứng minh điều này hỏng |
| `env.sh` có header ghi nguồn | Luôn biết file nào do tool sinh |
| Không TUI ở bản đầu | CLI thuần test được, script được, đọc log được |
| Bỏ khung "flywheel" | Bạn muốn vậy; core của tool nên là core của *công cụ*, không phải của triết lý |
| Không giữ ACFS | `~/.acfs` bỏ đi khi cái mới chạy được |
| Không `starship` | Bạn dùng p10k |

---

## 9. Kế hoạch thực thi

Tôi chỉ bắt đầu code khi bạn trả lời §8.1–8.3 (ba câu ⛔).

### Giai đoạn 1 — Nền (viết được, chạy được, chưa cài gì)
1. Khởi tạo module, Makefile, cấu trúc thư mục
2. `internal/manifest` — struct + parse + validate YAML
3. `internal/detect` — OS, PATH, package manager
4. `internal/state` — đọc/ghi atomic
5. CLI: `list`, `plugin add`, `plugin list`
6. 5 plugin thật: `ripgrep`, `fzf`, `zoxide`, `direnv`, `bat`
7. Test: parse manifest hỏng → lỗi rõ; state đọc/ghi/concurrent

**Xong khi:** `aes list` hiện đúng 5 tool với trạng thái thật trên máy bạn.

### Giai đoạn 2 — Cài
8. `internal/exec` — chạy lệnh, timeout, capture
9. `install` theo OS, chặn sudo
10. CLI: `install`, `env`
11. Test: verify trước khi cài; verify lại sau; fail thì không ghi state

**Xong khi:** `aes install ripgrep` chạy thật, `aes env` sinh file đúng.

### Giai đoạn 3 — Bảo trì
12. `remove`, `doctor`
13. `search` (lọc `plugins/`)
14. Test contract: cài hỏng → state không nói dối

### Giai đoạn 4 — Đóng gói *(chỉ khi cần)*
15. GitHub Actions: build → phát hành khi tag
16. `curl | sh` installer một file
17. Homebrew tap

### Giai đoạn 5 — Mở rộng (chỉ khi cần, KHÔNG làm sớm)
- Plugin nhận tham số riêng (`aes plugin config`)
- Đọc `url:` + `sha256:` tải binary
- TUI điều khiển
- Export/import bộ plugin sang máy khác

**Giai đoạn 5 cố tình để trống.** Thêm sớm là thêm sớm chỗ sai không sửa được.

---

## 10. Những gì tôi quyết định sẵn (bạn đổi được)

| Quyết định | Lý do |
|---|---|
| Không tự `sudo` | Nhìn thấy mới tin |
| State ghi sau verify, không ghi trước | State nói dối thì mọi thứ còn lại cũng đáng ngờ |
| Không sửa `~/.zshrc` | ACFS đã chứng minh điều này hỏng |
| `env.sh` có header ghi nguồn | Luôn biết file nào do tool sinh |
| Plugin là YAML, không phải code | Thêm tool phải rẻ, không phải build |
| Không TUI ở bản đầu | CLI thuần test được, script được, đọc log được |
| Bỏ hẳn khung "flywheel" | Bạn muốn vậy; core của tool nên là core của *công cụ*, không phải của triết lý |

---

## 11. Rủi ro

| Rủi ro | Giảm bằng |
|---|---|
| Manifest không đủ biểu đạt tool phức tạp | §8.1 chọn hướng B hoặc C ngay từ đầu |
| Chạy được trên macOS nhưng hỏng trên Ubuntu | §8.6 — test thật trước khi tin |
| Plugin store phình thành bảng tra cứu khổng lồ như ACFS | Chỉ vài chục plugin, từ chối plugin không có ích rõ |
| Không ai dùng ngoài bạn | Không sao — tool 1 người dùng là đủ, chỉ cần nó không cứng |

---

## 12. Phụ lục

### Trạng thái nghiên cứu ACFS

Deep-research về repo gốc ACFS đang chạy (`/workflows` để xem). Khi xong tôi sẽ bổ sung
vào plan, chỉ rõ cái gì đáng port và cái gì nên bỏ.

Câu hỏi research trả lời giúp — **bạn không cần trả lời**:
- `install.sh` dài bao nhiêu, cấu trúc ra sao
- `acfs.manifest.yaml` 160KB chứa gì
- Ubuntu bị hardcode ở đâu
- có macOS support không
- project còn maintain không, có complaint gì về shell-based

