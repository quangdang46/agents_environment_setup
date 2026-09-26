# agents_environment_setup — Bộ câu hỏi thiết kế

> **Cách trả lời:** Sửa trực tiếp file này, viết sau dấu `→`. Không cần trả hết — chỉ cần
> các mục **⛔ Blocking** để tôi bắt đầu code. Các mục còn lại tôi sẽ tự đề xuất mặc định
> để bạn duyệt.
>
> **Ký hiệu:** ⛔ chặn tiến độ · 🟡 quan trọng · ⟢ nice-to-have · 🔬 tôi tự tra (đang chạy research)

---

## A. Bối cảnh — hiện tại bạn đang ở đâu

Trước khi thiết kế, tôi cần biết chính xác tình trạng để không hỏi sai:

**A1.** Máy macOS này giờ đã có gì chạy được từ ACFS rồi?
→ (ghi: oh-my-zsh, p10k, 13 plugin, gitstatusd, 2 file ~/.acfs — đúng không? cái gì còn thiếu bạn muốn có?)

**A2.** Ngoài ACFS, bạn đang dùng gì để quản lý công cụ? Homebrew, mise, nix, hay cài tay?
→ (Homebrew chắc rồi — còn mise/asdf/nix không? cái nào đang hoạt động tốt, cái nào thất bại?)

**A3.** Bạn có bao nhiêu máy cần đồng bộ?
→ (chỉ máy này, hay có con Ubuntu nữa, hay con Mac thứ hai?)

---

## B. Phạm vi — làm gì và không làm gì

**B1.** ⛔ `agents_environment_setup` thay thế ACFS **một đời** (xoá hẳn) hay **cùng tồn tại** (ACFS làm fallback)?
→

**B2.** ⛔ Bản đầu tiên cần **cài** tool, hay chỉ cần **kiểm tra + báo cáo** cái gì đã có/còn thiếu?
→ _(nếu cài: cài bằng brew/apt/direct download? cần hỏi chi tiết ở mục D)_

**B3.** ⟡ Ngoài tool CLI, bạn có cần nó quản lý **cấu hình** không? Ví dụ: tự sinh `~/.zshrc`, tự đặt biến môi trường, tự viết keybindings.
→ _(ACFS có làm và đó là chỗ nó hỏng nặng nhất — bạn muốn giữ quyền đó hay bỏ hẳn?)_

**B4.** ⟢ Có cần hỗ trợ fish / bash không, hay chỉ zsh?
→ _(ACFS chỉ zsh. Tôi nghiêng về chỉ zsh + thiết kế để thêm bash sau)_

---

## C. Kiến trúc plugin — phần cốt lõi nhất

Đây là phần quyết định toàn bộ codebase. Tôi đưa 3 hướng, mỗi hướng là một cách khác nhau để tool của bạn "biết" cách được cài và kiểm tra.

**C1.** ⛔ **Tool được mô tả thế nào?** Chọn 1:

<details>
<summary><b>Hướng A — Manifest thuần khai báo (YAML)</b></summary>

```yaml
# plugins/ripgrep/plugin.yaml
name: ripgrep
description: Tìm kiếm text cực nhanh
provides: rg
install:
  darwin: brew install ripgrep
  linux:  sudo apt install -y ripgrep
verify: command -v rg
```
- **Được:** thêm tool = thêm 1 file YAML, không cần viết code, ai cũng làm được
- **Mất:** không làm được việc cần logic (patch file config, chạy post-install, hỏi user)
- **Không** import được, không viết được bằng Python/TS

</details>

<details>
<summary><b>Hướng B — Plugin là chương trình, giao tiếp qua JSON (khuyến nghị)</b></summary>

```yaml
name: my-tui
install:
  darwin: brew install my-tui
```
Plugin tự quyết định hành vi qua subcommand:
```
aes plugin check my-tui → exit 0/1
aes plugin install my-tui → tự làm
aes plugin env my-tui → in ra JSON {"env": {...}}
```
- **Được:** tool của bạn (TUI) tự lo hết vòng đời, viết bằng Go/Python/TS đều được
- **Được:** có thể cần logic phức tạp
- **Mất:** phải viết chút code cho mỗi plugin

</details>

<details>
<summary><b>Hướng C — Plugin là package Go, import trực tiếp</b></summary>

```go
// plugin/ripgrep/ripgrep.go
package ripgrep
func Install(ctx context.Context) error
func Verify(ctx context.Context) (bool, error)
```
- **Được:** mạnh nhất, type-safe
- **Mất:** chỉ viết được bằng Go, phải compile, không linh hoạt

</details>

→ **Chọn hướng:**

**C2.** ⟡ ⛔ Thêm tool mới — bạn muốn quy trình nào?
→ _(viết tay file trong thư mục `plugins/` · `aes plugin add <git-url>` tự tải về · `aes new-plugin <tên>` sinh skeleton? — chọn cả hai cũng được)_

**C3.** ⟡ ⛔ **Ai là người viết plugin?** Chỉ bạn, hay đồng nghiệp/bạn bè cũng viết?
→ _(nếu chỉ bạn thì thiết kế đơn giản, còn nếu người khác viết thì cần validate/schema/versioning)_

**C4.** ⟡ Plugin có cần **phiên bản** không? (vd: `aes plugin add ripgrep@v2`)
→

**C5.** ⟢ Plugin có cần tham số riêng không? (vd: plugin `docker` cần nhớ bạn dùng colima hay docker-desktop)
→

---

## D. Cài đặt — cơ chế thực thi

**D1.** ⛔ Mỗi OS cài bằng cách nào?

| OS | Hiện tại của bạn | Bạn muốn dùng |
|---|---|---|
| macOS | Homebrew | ⬜ Homebrew ⬜ tải binary trực tiếp ⬜ cả hai (ưu tiên brew) |
| Ubuntu | (chưa rõ) | ⬜ apt ⬜ snap ⬜ mise/asdf ⬜ tải binary |

→

**D2.** ⟡ ⛔ Tool **binary tải từ internet** (không có trong apt/brew) xử lý thế nào?
→ _(Tự tải về `~/.agents/bin/`? Ghi hash để verify? Đây là chỗ ACFS yếu nhất)_

**D3.** 🟡 Có tool nào cần chạy **với quyền root** (sudo) không?
→ _(nếu có: `aes install` có hỏi mật khẩu sudo không, hay in ra lệnh để bạn tự chạy? Tôi nghiêng về **in ra lệnh** — an toàn hơn, và bạn thấy đúng nó làm gì)_

**D4.** 🟡 Cài xong cần **khởi động lại shell** không, hay có cách áp dụng ngay?
→

**D5.** 🟢 Có cần hỗ trợ `mise` (quản lý version của chính các tool: node, python, go) không?
→

---

## E. Trạng thái & kiểm tra

**E1.** ⛔ ⛔ Lệnh con của CLI bạn cần? Gợi ý ban đầu, bạn sửa/xoá thoải mái:

| Lệnh | Việc | Cần? |
|---|---|---|
| `aes list` | xem tất cả tool + trạng thái | ⬜ |
| `aes install <tên>` | cài 1 tool | ⬜ |
| `aes install` | cài tất cả đang thiếu | ⬜ |
| `aes remove <tên>` | gỡ | ⬜ |
| `aes doctor` | chẩn đoán sức khoẻ môi trường | ⬜ |
| `aes update` | cập nhật các tool đã cài | ⬜ |
| `aes plugin add/list/rm` | quản lý plugin | ⬜ |
| `aes search <từ khóa>` | tìm plugin theo mô tả | ⬜ |
| `aes tui` | giao diện TUI duy nhất | ⬜ |
| `aes <cái khác>` | | |

→ **Sửa bảng trên theo ý bạn:**

**E2.** 🟡 Lệnh bạn muốn chạy **hàng ngày** (nhiều nhất)?
→ _(cái nào bạn gõ mỗi ngày thì phải nhanh, cái nào hiếm dùng thì chậm cũng được)_

**E3.** 🟡 `doctor` nên kiểm tra những gì? Ngoài "tool đã cài chưa":
→ _(PATH hợp lệ? version quá cũ? xung đột giữa 2 bản? thiếu dependency? shell config có đúng?)_

**E4.** 🟢 Có cần `aes doctor --fix` tự sửa luôn không?
→ _(hay chỉ báo cáo, bạn tự quyết định? Tôi nghiêng về: **báo cáo mặc định, `--fix` là opt-in**)_

**E5.** 🟡 Trạng thái lưu ở đâu? Đề xuất: `~/.agents/state.json` (người dùng) — có cần tách riêng từng máy không (vd: cấu hình khác giữa Mac và Ubuntu)?
→

---

## F. Tích hợp shell

**F1.** ⛔ ⛔ Quan hệ với `~/.zshrc`?
Đây là chỗ làm ACFS hỏng. Bạn chọn:

| | Cách làm | Đặc điểm |
|---|---|---|
| ⬜ | **A.** Chỉ sinh `~/.agents/env.sh`, bạn tự thêm 1 dòng `source` | Bạn kiểm soát 100%, tool không bao giờ đụng file của bạn |
| ⬜ | **B.** Tự thêm `source` vào `~/.zshrc` (có backup trước) | Tiện hơn, nhưng tool có quyền ghi file của bạn |
| ⬜ | **C.** Không đụng gì, chỉ in lệnh để bạn copy | An toàn nhất |

→

**F2.** 🟡 Shell completion (gõ `aes` rồi Tab)?
→ _(có cần không; ACFS có nhưng chưa bao giờ test được)_

**F3.** 🟡 Có cần hàm/helper nào trong shell không? Ví dụ: `aec` để reload env, `aeshell`?
→

**F4.** 🟢 Có cần hỗ trợ `direnv` (tự kích hoạt env theo thư mục/project) không?
→

---

## G. Đóng gói & phân phối

**G1.** ⛔ ⛔ Bạn muốn dùng tool qua cách nào?

| | Cách | Đặc điểm |
|---|---|---|
| ⬜ | **A.** `go install github.com/.../cmd/aes@latest` | Quen thuộc Go, cần mạng mỗi lần update |
| ⬜ | **B.** Tải binary từ GitHub Releases | Nhanh, 1 file, cần CI build |
| ⬜ | **C.** `brew install` từ tap riêng | Tiện nếu có Homebrew sẵn |
| ⬜ | **D.** Cả A và B | Chọn A lúc dev, B khi phát hành |

→

**G2.** 🟡 Repo đặt ở đâu? Public (GitHub) hay private?
→ _(public thì tôi thiết kế cho người lạ dùng được, private thì thiết kế cho đúng máy bạn)_

**G3.** 🟡 Có cần hỗ trợ **CI/CD** không? (tự build + phát hành khi có tag)
→ _(nếu chọn G1-B thì bắt buộc cần)_

**G4.** 🟢 Ai là người duy trì? Chỉ bạn, hay có cộng tác?
→

---

## H. Tương thích ngược — có cần giữ ACFS không?

**H1.** 🟡 Bạn có còn tool nào khác phụ thuộc vào ACFS không?
→ _(tôi biết: `am` command ở `~/.local/bin` trỏ vào `~/.acfs/bin/am` đã xoá. Còn gì nữa? shell aliases? workflow nào? script nào? tmux config?)_

**H2.** 🟢 Khi tool mới chạy được, bạn có muốn xoá `~/.acfs` hẳn không?
→ _(hay giữ lại làm fallback khi cần)_

---

## I. Ngoài phạm vi — cố tình KHÔNG làm

Để tôi không xây nhầm. Những thứ này tôi sẽ **không** làm trừ khi bạn nói thêm:
- TUI đồ họa (scoop, lazygit kiểuu) — tôi nghiêng về CLI thuần
- Quản lý file cấu hình dotfiles (symlink repo dotfiles)
- Docker dev / containers
- Quản lý secrets, tokens, API keys
- Cài IDE, cài driver, cài app GUI (ngoài CLI)

**Có mục nào trong danh sách trên bạn muốn CÓ không?**
→

---

## J. Ưu tiên — làm cái gì trước

**J1.** 🟡 Bản đầu tiên "xong" nghĩa là gì? Ví dụ:
- ⬜ Chạy được `aes list` + `aes doctor` + đọc plugin YAML (chưa cài gì)
- ⬜ Cài được tool qua Homebrew + `aes install ripgrep` thật
- ⬜ Đầy đủ: add/remove/update/doctor/TUI

→ **Bản đầu tiên cần tới mức nào:**

**J2.** ⟢ Có deadline không?
→ _(có thì tôi cắt scope cho vừa)_

---

## K. Câu hỏi mở

Chỗ nào bạn chưa nghĩ tới, viết ở đây:
→

---

## Phụ lục: Tôi tự tra, không cần bạn trả lời

🔬 Research đang chạy trên repo gốc ACFS sẽ trả lời giúp tôi:
- install.sh dài bao nhiêu, cấu trúc ra sao
- `acfs.manifest.yaml` (160KB) thực chất chứa gì
- Ubuntu bị hardcode ở đâu
- có hỗ trợ macOS không, tình hình thế nào
- project còn maintain không, có complaint nào về shell-based không mở rộng được

Tôi sẽ dùng kết quả đó để đề xuất bạn giữ gì / bỏ gì khi port.
