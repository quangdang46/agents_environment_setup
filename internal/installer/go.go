package installer

import (
	"context"
	"os"
	"path/filepath"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// GoBinDir returns where the go toolchain installs binaries.
//
// GOBIN wins when it is set, then GOPATH/bin, then ~/go/bin — the same
// precedence `go install` itself uses. Guessing wrong here would put a
// directory on PATH that the toolchain never writes to, which is the failure
// this whole package exists to avoid.
func GoBinDir() string {
	if b := os.Getenv("GOBIN"); b != "" {
		return b
	}
	if gp := os.Getenv("GOPATH"); gp != "" {
		return filepath.Join(gp, "bin")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "go", "bin")
}

// NewGoInstaller returns the go strategy.
func NewGoInstaller() *Ecosystem {
	return NewEcosystem(
		manifest.StrategyGo,
		"go",
		func(coord string) string { return "go install " + coord },
		func(context.Context) (string, error) { return GoBinDir(), nil },
	)
}
