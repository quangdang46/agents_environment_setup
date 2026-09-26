package installer

import (
	"context"
	"os"
	"path/filepath"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// UVBinDir returns where `uv tool install` places executables:
// ~/.local/bin, or UV_TOOL_BIN_DIR when set.
func UVBinDir() string {
	if dir := os.Getenv("UV_TOOL_BIN_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "bin")
}

// NewUVInstaller returns the uv strategy.
//
// uv ships as a single binary with no system dependencies, so there is no
// separate toolchain to gate on: `uv` itself is the toolchain.
func NewUVInstaller() *Ecosystem {
	return NewEcosystem(
		manifest.StrategyUV,
		"uv",
		func(coord string) string { return "uv tool install " + coord },
		func(context.Context) (string, error) { return UVBinDir(), nil },
	)
}
