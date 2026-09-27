package sandbox

import (
	"context"
	"fmt"
	"strings"
)

// ContainerRunner executes a command inside the sandbox container and returns
// its combined output.
//
// It is a function rather than a docker dependency so this package stays
// testable without a daemon. The container harness supplies the real
// implementation; the tests supply a fake, which is the point — the property
// asserted here is about WHERE a check ran, not about docker.
type ContainerRunner func(ctx context.Context, argv ...string) (string, error)

// Presence is the result of asking one environment whether a binary resolves.
type Presence struct {
	// Found is whether the binary resolved.
	Found bool
	// Evidence is the output that decided it. It is kept because "the tool was
	// absent" is a claim the next person should be able to check, and a bare
	// boolean invites the reader to assume someone measured.
	Evidence string
	// Where names the environment the check ran in, so a result can never be
	// silently attributed to the wrong machine.
	Where string
}

// AbsentInside asks the CONTAINER whether a binary resolves, and reports the
// answer together with the evidence.
//
// This exists because "the tool is absent" is a claim about a specific machine,
// and a harness that only knows about the host cannot make it honestly. The
// failure mode is concrete: a developer machine that already has the tool
// satisfies every host-side absence check, the container inherits that state,
// and the sequence passes without a byte crossing the container boundary. The
// run looks perfect and proves nothing — the same shape as a green main with
// no CI.
//
// Measuring inside is the fix. `command -v` in the container can only find
// what the container has, and the output comes back so the claim is checkable
// after the fact rather than taken on trust.
func AbsentInside(ctx context.Context, run ContainerRunner, bin string) (Presence, error) {
	if run == nil {
		return Presence{Where: "container"}, fmt.Errorf("sandbox: no container runner")
	}
	if strings.TrimSpace(bin) == "" {
		return Presence{Where: "container"}, fmt.Errorf("sandbox: empty binary name")
	}

	out, err := run(ctx, "sh", "-c", "command -v "+shellQuote(bin))
	if err != nil {
		// A non-zero exit may mean "not found", which is the answer we came
		// for. Anything else — the container refusing to start, sh missing —
		// is a real error and must not read as an absent tool.
		if isNotFound(out) {
			return Presence{Found: false, Evidence: strings.TrimSpace(out), Where: "container"}, nil
		}
		return Presence{Where: "container"},
			fmt.Errorf("sandbox: probing %q in container: %w (%s)", bin, err, firstLine(out))
	}
	return Presence{Found: true, Evidence: strings.TrimSpace(out), Where: "container"}, nil
}

// isNotFound distinguishes `command -v` reporting absence from the command
// failing for any other reason.
//
// Without this, a container that cannot start produces output that reads as
// "not found", the error is swallowed, and the harness concludes the tool is
// absent — then installs into a container that was never there. A false
// absence is worse than a loud failure: it is the precondition for every
// subsequent step being meaningless.
func isNotFound(out string) bool {
	// `command -v` prints NOTHING and exits 1 on a plain miss — silence is the
	// miss. Any output at all on the error path means the shell said something
	// other than a bare path, which is a different condition: a dead daemon
	// prints "Cannot connect", a missing shell prints "not found". Laundering
	// either into "the tool is absent" would let the harness install into a
	// container that was never running.
	return strings.TrimSpace(out) == ""
}

// shellQuote wraps a token in single quotes for POSIX sh, escaping any embedded
// single quote. Binary names come from a manifest, and a manifest is data — but
// data still should not be concatenated into a shell unquoted.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
