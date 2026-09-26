package installer

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// npmPrefixTimeout bounds the `npm prefix -g` probe. It is a local query, so
// anything slower than this means npm is not answering.
const npmPrefixTimeout = 10 // seconds

// NPMBinDir returns the directory npm installs global packages into.
//
// npm does not have a fixed answer to this: the global prefix depends on the
// node install and on whether the prefix is writable without privilege. So
// the only honest way to learn it is to ask npm.
//
// On failure it returns "" — not a guess. envgen skips an empty directory,
// and a wrong path on PATH is worse than a missing one.
func NPMBinDir(ctx context.Context, run func(context.Context, string) (string, error)) string {
	if run == nil {
		run = defaultProbe
	}
	out, err := run(ctx, "npm prefix -g")
	if err != nil {
		return ""
	}
	prefix := strings.TrimSpace(out)
	if prefix == "" {
		return ""
	}
	// npm reports the prefix; global packages land in its bin subdirectory.
	return filepath.Join(prefix, "bin")
}

// defaultProbe runs a short local query and returns its stdout.
func defaultProbe(ctx context.Context, cmd string) (string, error) {
	out, err := exec.CommandContext(ctx, "sh", "-c", cmd).Output()
	return string(out), err
}

// NewNPMInstaller returns the npm strategy.
func NewNPMInstaller() *Ecosystem {
	e := &Ecosystem{
		name:      manifest.StrategyNPM,
		Toolchain: "npm",
		Command:   func(coord string) string { return "npm install -g " + coord },
	}
	// The bin-dir probe runs the same injected runner as installs, so a test
	// that records invocations sees both and can tell them apart by command.
	e.probe = func(ctx context.Context, cmd string) (string, error) {
		res, err := e.run()(ctx, cmd)
		return res.Stdout, err
	}
	e.binDir = func(ctx context.Context) (string, error) {
		return NPMBinDir(ctx, e.probe), nil
	}
	return e
}
