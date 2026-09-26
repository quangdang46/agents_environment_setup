// Command aes takes a machine from nothing to a complete, verified AI coding
// environment.
//
// The North Star is one command: `aes setup`. Everything else here exists to
// either set it up or show what it did.
//
// Exit codes are a public contract, not an implementation detail — they are
// how an agent parses this tool. See internal/cli for the table.
package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	aessetup "github.com/quangdang46/agents_environment_setup"
	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/cli"
	"github.com/quangdang46/agents_environment_setup/internal/installer"
	"github.com/quangdang46/agents_environment_setup/internal/plan"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
	"github.com/quangdang46/agents_environment_setup/internal/tui"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	aesHome, err := resolveHome()
	if err != nil {
		fmt.Fprintf(os.Stderr, "aes: %v\n", err)
		os.Exit(cli.ExitFailure)
	}

	catalogRoot, profileDir, err := resolveData(aesHome)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aes: %v\n", err)
		os.Exit(cli.ExitInvalidCatalog)
	}

	app := &cli.App{
		In:          os.Stdin,
		Out:         os.Stdout,
		Err:         os.Stderr,
		Home:        aesHome,
		CatalogRoot: catalogRoot,
		ProfileDir:  profileDir,
		Registry:    installer.NewRegistry(),
	}
	cli.Version = version
	app.RunTUI = func() error { return runTUI(context.Background(), app, catalogRoot, profileDir) }

	os.Exit(app.Run(os.Args[1:]))
}

// resolveHome returns AES_HOME, defaulting to ~/.aes.
//
// AES_HOME is overridable because the sandbox tests need it. A test that
// installs real tools has to be able to point aes at a throwaway home, or it
// writes to the developer's real ~/.aes and then asserts - against that same
// directory - that it did not. That assertion cannot be made honestly while the
// thing under test is the one being mutated.
//
// It is also the honest way to try a release without touching a live install,
// which is worth having for a tool whose entire job is modifying a machine.
func resolveHome() (string, error) {
	if home := os.Getenv("AES_HOME"); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Without a home directory there is nowhere to put ~/.aes, and every
		// command would fail later in a less obvious way.
		return "", fmt.Errorf("cannot determine home directory (set AES_HOME to override): %w", err)
	}
	return filepath.Join(home, ".aes"), nil
}

// runTUI opens the human frontend.
//
// The TUI collects a selection and hands it back through App.SetupSelection,
// which runs the same pipeline `aes setup --only <selection>` runs. The TUI
// itself cannot install anything — it does not import the installer — so this
// is the only way anything gets installed from the interactive path.
func runTUI(ctx context.Context, app *cli.App, catalogRoot, profileDir string) error {
	cat, err := catalog.Load(catalogRoot)
	if err != nil {
		return fmt.Errorf("load catalog: %w", err)
	}
	host, err := platform.Current()
	if err != nil {
		return fmt.Errorf("detect host: %w", err)
	}

	m := tui.New(cat, host)
	m.OnInstall = func(sel plan.Selection) {
		// A fresh screen first: the setup summary is longer than a TUI
		// frame, and interleaving it with the list is unreadable.
		fmt.Print("\x1b[H\x1b[2J")
		if err := app.SetupSelection(ctx, sel, &cli.Flags{NonInteractive: false}); err != nil {
			m.Err = err.Error()
		} else {
			m.Result = "installed " + sel.String()
		}
	}
	return tui.Run(ctx, m, os.Stdin, os.Stdout, os.Stderr)
}

// resolveData decides where the catalog and profiles come from.
//
// A source checkout is preferred when there is one, so editing a tool.yaml
// takes effect without a rebuild. Otherwise the assets embedded in the binary
// are materialised under AES_HOME and used from there — which is the only
// option for an installed binary, and the only one that makes
// `curl … | sh` work.
//
// Materialising rather than reading straight out of the embed is deliberate:
// catalog.Load and profile.LoadDir take filesystem paths, and teaching them
// an fs.FS would push an abstraction through four packages to save a copy of
// a few hundred kilobytes. The cache is keyed by version, so a new binary
// never reads a stale catalog written by an older one.
func resolveData(aesHome string) (catalogRoot, profileDir string, err error) {
	if root := findRepoRoot(); root != "" {
		return filepath.Join(root, "tools"), filepath.Join(root, "profiles"), nil
	}

	share := filepath.Join(aesHome, "share", version)
	catalogRoot = filepath.Join(share, "tools")
	profileDir = filepath.Join(share, "profiles")

	// Already materialised by this version: nothing to do.
	if isPopulated(catalogRoot) && isPopulated(profileDir) {
		return catalogRoot, profileDir, nil
	}

	if err := materialise(aessetup.Tools, catalogRoot); err != nil {
		return "", "", fmt.Errorf("preparing the tool catalog: %w", err)
	}
	if err := materialise(aessetup.Profiles, profileDir); err != nil {
		return "", "", fmt.Errorf("preparing the profiles: %w", err)
	}
	return catalogRoot, profileDir, nil
}

// materialise writes an embedded tree to dir.
//
// It writes to a sibling temp directory and renames, so an interrupted run
// cannot leave a half-copied catalog that later looks like a valid but
// incomplete one — the same reasoning as every other atomic write here.
func materialise(src func() (fs.FS, error), dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	staging := dir + ".tmp"
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}

	tree, err := src()
	if err != nil {
		return err
	}
	if err := fs.WalkDir(tree, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(staging, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(tree, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		os.RemoveAll(staging)
		return err
	}

	if err := os.RemoveAll(dir); err != nil {
		os.RemoveAll(staging)
		return err
	}
	return os.Rename(staging, dir)
}

// isPopulated reports whether a directory actually has entries. An empty
// directory is treated as absent so a failed earlier run self-heals.
func isPopulated(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

// findRepoRoot returns the checkout root when aes is run from inside one, or
// "" when it is not.
//
// Walking up from the working directory is right for a developer and wrong
// for an installed binary, which is why the embedded assets exist at all.
func findRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
