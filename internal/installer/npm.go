package installer

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/quangdang46/agents_environment_setup/internal/exec"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// npmPrefixTimeout bounds the `npm prefix -g` probe. It is a local query, so
// anything slower than this means npm is not answering.
//
// The bound was declared for a long time and used by nothing: defaultProbe ran
// a bare `sh -c` with no timeout at all. A comment describing behaviour the
// code does not have is worse than no comment, because the next reader assumes
// somebody checked.
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
//
// It goes through exec.Run rather than a bare `sh -c` for the two properties
// that bare invocation does not have: the default timeout, and the 1 MiB output
// cap. This function used to exec directly, which made it the last place in
// the tree that spawned a shell without either — in a codebase whose central
// claim is that one mechanism runs everything, a fallback that does it
// differently is the fallback someone eventually relies on.
func defaultProbe(ctx context.Context, cmd string) (string, error) {
	res, err := exec.Run(ctx, cmd, exec.Options{
		Timeout: npmPrefixTimeout * time.Second,
	})
	return res.Stdout, err
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
