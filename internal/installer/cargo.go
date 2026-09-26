package installer

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// CargoBinDir returns where cargo installs binaries: ~/.cargo/bin, or
// CARGO_HOME/bin when CARGO_HOME is set.
func CargoBinDir() string {
	if ch := os.Getenv("CARGO_HOME"); ch != "" {
		return filepath.Join(ch, "bin")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cargo", "bin")
}

// NewCargoInstaller returns the cargo strategy.
//
// The coordinate is split rather than passed through, because the version
// becomes a flag: `cargo install t@v1` is only supported by recent cargo,
// while `--version` works everywhere.
func NewCargoInstaller() *Ecosystem {
	return NewEcosystem(
		manifest.StrategyCargo,
		"cargo",
		func(coord string) string {
			name, version := splitAt(coord, "@")
			return "cargo install " + name + " --version " + version
		},
		func(context.Context) (string, error) { return CargoBinDir(), nil },
	)
}

// splitAt splits s at the final occurrence of sep, returning the whole string
// unchanged when sep is absent.
func splitAt(s, sep string) (before, after string) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i+len(sep):]
}
