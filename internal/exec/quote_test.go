package exec

import (
	"os/exec"
	"testing"
)

// ShellQuote is the single implementation now shared by three packages. The
// three copies it replaced were byte-identical, which is exactly the shape
// that lets a fix land in one and not the others — so the test asserts
// ROUND-TRIPPING through a real shell rather than the expected string.
//
// A string-equality test would pass for any implementation that happens to
// produce today's answer, including a broken one. The contract is that the
// value survives a shell unchanged.
func TestShellQuoteRoundTripsThroughAShell(t *testing.T) {
	t.Parallel()

	for _, s := range []string{
		"plain",
		"with space",
		"with'quote",
		`double"quote`,
		"back\\slash",
		"$(echo pwned)",
		"`id`",
		"semi;colon",
		"pipe|and&&more",
		"new\nline",
		"tab\there",
		"*",
		"~/tilde",
		"a'b'c'd",
		"'",
		"",
	} {
		t.Run(s, func(t *testing.T) {
			t.Parallel()
			// printf %s of the quoted word must reproduce s byte for byte.
			got, err := exec.Command("sh", "-c", "printf %s "+ShellQuote(s)).Output()
			if err != nil {
				t.Fatalf("sh -c with %q quoted: %v", s, err)
			}
			if string(got) != s {
				t.Errorf("round trip changed the value:\n  in:  %q\n  out: %q", s, string(got))
			}
		})
	}
}
