package installer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/quangdang46/agents_environment_setup/internal/exec"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// Outcome reports what an uninstall actually did.
//
// The distinction is the point. "I removed it" and "I am not able to remove
// it" are different facts, and a command that reports the second as the first
// leaves the user with a broken install they did not ask for.
type Outcome struct {
	// Removed is true only when the binary is genuinely gone. It is never
	// set optimistically: a strategy that cannot verify its own removal
	// returns false with a reason instead.
	Removed bool
	// Reason explains a non-removal. It is shown to the user verbatim, so
	// it should say what to do instead, not just what failed.
	Reason string
}

// Removed is the successful outcome.
func Removed() Outcome { return Outcome{Removed: true} }

// NotRemoved builds an outcome for a strategy that declines to remove.
func NotRemoved(reason string) Outcome { return Outcome{Reason: reason} }

// Remover is implemented by strategies that can genuinely remove a tool.
//
// It is separate from Installer rather than an extra method on it, because
// not every strategy has one — and a strategy that cannot remove should say
// so rather than being handed a removal it does not implement. The registry
// lookup is what decides, so a missing Remover is an honest "no" instead of
// a silent success.
type Remover interface {
	Uninstall(ctx context.Context, a Action) (Outcome, error)
}

// Uninstall resolves the strategy and removes the tool, if it can.
//
// A strategy with no Remover returns an outcome explaining that, never a
// silent success. The caller is responsible for verifying the result before
// dropping the state entry.
func (r *Registry) Uninstall(ctx context.Context, a Action) (Outcome, error) {
	inst, err := r.Get(a.Target.Strategy)
	if err != nil {
		return Outcome{}, err
	}
	remover, ok := inst.(Remover)
	if !ok {
		return NotRemoved(fmt.Sprintf("the %s strategy has no removal path", a.Target.Strategy)), nil
	}
	return remover.Uninstall(ctx, a)
}

// Uninstall deletes the binary AES itself placed.
//
// This is the honest case: AES put the file there, so AES can take it away.
// It refuses if the binary is not where it expects, rather than reporting a
// removal that did not happen.
func (g *GithubRelease) Uninstall(ctx context.Context, a Action) (Outcome, error) {
	// installName, NOT binaryName. Install writes installName() — preferring
	// the Action's Name, which the CLI sets from the manifest's own
	// verify.command — so asking for anything else here looks for a file that
	// was never created.
	//
	// For a tool whose archive member is named per platform the two differ:
	// yq ships yq_darwin_arm64 and is installed as `yq`. Uninstalling used to
	// hunt for yq_darwin_arm64, not find it, report "not present", and return
	// success — a command that removes nothing and exits 0.
	name := a.installName()
	path := filepath.Join(g.Dest, name)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return NotRemoved(fmt.Sprintf("%s is not present at %s", name, path)), nil
		}
		return Outcome{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.Remove(path); err != nil {
		return Outcome{}, fmt.Errorf("remove %s: %w", path, err)
	}
	return Removed(), nil
}

// Uninstall removes a tool installed by a system package manager.
//
// brew needs no privilege. apt goes through the same print-then-confirm
// model as installation (I14): the command is shown before anything runs, and
// a non-interactive run uses `sudo -n` and reports rather than blocking.
func (p *PackageInstaller) Uninstall(ctx context.Context, a Action) (Outcome, error) {
	pkg := a.Target.Package
	if strings.TrimSpace(pkg) == "" {
		return NotRemoved("the manifest names no package"), nil
	}

	switch a.Target.Manager {
	case manifest.ManagerBrew:
		if _, err := p.unprivileged()(ctx, "brew uninstall "+pkg); err != nil {
			return Outcome{}, err
		}
		return Removed(), nil

	case manifest.ManagerApt:
		base := "apt remove -y " + pkg

		// Already root: no escalation question arises, and `base` carries no
		// sudo — so it is an ordinary command and goes down the unprivileged
		// path rather than claiming an Authorization it never needed.
		if p.root() {
			if _, err := p.unprivileged()(ctx, base); err != nil {
				return Outcome{}, err
			}
			return Removed(), nil
		}

		if p.NonInteractive {
			auth := exec.Authorization{
				Confirmed:      true,
				NonInteractive: true,
				Reason:         "apt remove (non-interactive)",
			}
			if _, err := p.privileged()(ctx, "sudo -n "+base, auth); err != nil {
				plain := strings.TrimPrefix(base, "")
				p.printf("aes: could not remove %s automatically. Run this yourself:\n\n    sudo %s\n\n", a.Tool, plain)
				return Outcome{}, &exec.PrivilegeError{Command: "sudo " + plain, NonInteractive: true}
			}
			return Removed(), nil
		}

		// Interactive: print first, then ask, then run.
		p.printf("aes: removing this needs elevated privileges. The command is:\n\n    sudo %s\n\n", base)
		if err := p.authorize(base); err != nil {
			return Outcome{}, err
		}
		auth := exec.Authorization{
			Confirmed: true,
			Reason:    "apt remove (confirmed by the user)",
		}
		if _, err := p.privileged()(ctx, "sudo "+base, auth); err != nil {
			return Outcome{}, err
		}
		return Removed(), nil

	default:
		return NotRemoved(fmt.Sprintf("unknown package manager %q", a.Target.Manager)), nil
	}
}

// Uninstall reports that the ecosystem offers no clean per-package removal.
//
// This is the case the bead exists to get right. `go install`, `npm -g`,
// `cargo install` and `uv tool install` all scatter a binary, a module cache
// entry and possibly a shim, and none of them offer a supported way to undo
// exactly one of them. Pretending otherwise by deleting the binary would
// leave the user with a half-removed install that looks fine and is not.
//
// The reason names what the user can actually do, so this is an answer rather
// than a refusal.
func (e *Ecosystem) Uninstall(ctx context.Context, a Action) (Outcome, error) {
	coord, err := e.coordinate(a)
	if err != nil {
		return NotRemoved(err.Error()), nil
	}
	return NotRemoved(fmt.Sprintf(
		"%s offers no clean per-package removal, so aes will not pretend otherwise. "+
			"%s did not remove it. To remove it yourself, uninstall %s and delete any leftover binary.",
		e.name, titleName(e.name), coord)), nil
}

func titleName(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
