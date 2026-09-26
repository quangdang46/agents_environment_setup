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
	"os"
	"path/filepath"

	"github.com/quangdang46/agents_environment_setup/internal/cli"
	"github.com/quangdang46/agents_environment_setup/internal/installer"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		// Without a home directory there is nowhere to put ~/.aes, and every
		// command would fail in a more confusing way later.
		os.Stderr.WriteString("aes: cannot determine home directory: " + err.Error() + "\n")
		os.Exit(cli.ExitFailure)
	}

	repoRoot := findRepoRoot()
	app := &cli.App{
		In:   os.Stdin,
		Out:  os.Stdout,
		Err:  os.Stderr,
		Home: filepath.Join(home, ".aes"),
		// Catalog and profiles are resolved relative to the repository so a
		// checkout runs as itself. A released binary ships them alongside;
		// either way the path is decided here, at the edge, so nothing
		// below has to guess.
		CatalogRoot: filepath.Join(repoRoot, "tools"),
		ProfileDir:  filepath.Join(repoRoot, "profiles"),
		Registry:    installer.NewRegistry(),
	}
	cli.Version = version

	os.Exit(app.Run(os.Args[1:]))
}

// findRepoRoot walks up from the working directory looking for go.mod, so a
// checkout finds its own catalog and profiles.
func findRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}
