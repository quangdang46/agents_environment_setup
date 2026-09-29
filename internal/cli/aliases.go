package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/state"
	"github.com/quangdang46/agents_environment_setup/internal/verifier"
)

// aliasDecl is one alias read out of a shell rc file.
type aliasDecl struct {
	// Name is the command the alias shadows.
	Name string
	// Target is what the alias runs instead, unquoted.
	Target string
	// File is where it was declared, so a finding can point at something the
	// user can open and change.
	File string
}

// readShellAliases extracts alias declarations from a shell rc file.
//
// # It reads, it does not run
//
// The honest way to learn what an interactive shell will do is to ask it. That
// is also the one thing this must not do. An rc file is arbitrary user code:
// it can prompt, block on input, start a tmux server, launch a browser, or
// leave a prompt-toolkit waiting forever. `aes doctor` runs with no TTY and a
// timeout budget, and turning a diagnostic into something that executes a
// user's startup files would make it able to hang the command that exists to
// explain hangs.
//
// # What that costs, stated rather than hidden
//
// This sees the rc file and nothing else. An alias contributed by oh-my-zsh, a
// plugin, a framework, or a file the rc sources is invisible here. So the
// finding says "an alias named X is declared in Y" — a claim about a file —
// and never "your shell will run Z", which would be a claim about a shell
// nothing has run.
//
// A missing file is not an error: plenty of machines have no ~/.zshrc, and
// reporting one as a problem would be noise dressed as a diagnosis.
func readShellAliases(path string) map[string]aliasDecl {
	out := map[string]aliasDecl{}
	if path == "" {
		return out
	}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// An rc file can contain a very long line (a base64 blob, a minified
	// config). The default 64 KiB limit would make the scanner stop early and
	// silently drop every alias after the long line, which is a wrong answer
	// rather than a loud one.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for sc.Scan() {
		if decl, ok := parseAliasLine(sc.Text(), path); ok {
			// A later declaration replaces an earlier one, which is what a
			// shell does when it reads the same rc twice.
			out[decl.Name] = decl
		}
	}
	return out
}

// parseAliasLine reads one line, if it declares an alias.
//
// It is deliberately not a shell parser. A commented-out alias is skipped
// because reporting one would be a false positive about something the user
// turned off, and the value is only unquoted well enough to be printed back to
// a human — nothing downstream executes it.
func parseAliasLine(line, file string) (aliasDecl, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return aliasDecl{}, false
	}
	// No explicit comment check, and that is deliberate rather than an
	// oversight. A commented-out alias cannot reach the branch below: the
	// keyword prefix check rejects any line whose first non-space character is
	// `#`, because it is not `a` of `alias`. The guard existed and was
	// mutation-tested — it turned out nothing could exercise it, which is the
	// same finding as a declared-and-unused constant: a branch that cannot
	// change behaviour is a comment claiming a protection the code gets for
	// free somewhere else.
	rest, ok := strings.CutPrefix(trimmed, "alias")
	if !ok {
		return aliasDecl{}, false
	}
	// "aliasfoo=1" is not an alias; the keyword has to stand alone.
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return aliasDecl{}, false
	}
	rest = strings.TrimSpace(rest)

	name, value, found := strings.Cut(rest, "=")
	if !found {
		return aliasDecl{}, false
	}
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	// A name is a shell word. Anything with whitespace, a quote or a path
	// separator in it is not a command name, and treating it as one produces a
	// finding about a thing that cannot be typed.
	if name == "" || !isAliasName(name) {
		return aliasDecl{}, false
	}
	return aliasDecl{Name: name, Target: unquote(value), File: file}, true
}

// isAliasName reports whether s could be a command name.
//
// Kept to the characters that appear in real ones. The cost of a permissive
// check here is a finding about a name nobody can type, which is worse than
// missing a real one: one is noise, the other is a gap the reader cannot see.
func isAliasName(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.' || r == '+':
		default:
			return false
		}
	}
	return true
}

// unquote removes one layer of matching quotes and trims the rest.
//
// A trailing shell comment is not stripped. Trimming a value that legitimately
// contains `#` — a colour code, a path fragment — would report a different
// command than the user wrote, and the whole value of the finding is that it
// can be read back and checked.
func unquote(v string) string {
	if len(v) >= 2 {
		first, last := v[0], v[len(v)-1]
		if (first == '\'' && last == '\'') || (first == '"' && last == '"') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// shadowMessage renders the finding for one shadowed tool.
//
// It names the file rather than the shell, for the reason above: this read a
// file, and saying "your shell" would be claiming more than it looked at.
func shadowMessage(name string, decl aliasDecl, binaryPath string) string {
	msg := "an alias for " + name + " is declared in " + decl.File +
		" and runs `" + decl.Target + "` instead"
	if binaryPath != "" {
		msg += "; aes verified the binary at " + binaryPath
	}
	// The remedy is the alias and nothing else. `aes forget` drops the state
	// entry, which does not touch the user's shell — an earlier version of
	// this message suggested it, which is advice that cannot address the
	// problem and sends the user looking in the wrong tool.
	return msg + ". Edit the alias in that file to run " + binaryPath + " instead."
}

// shadowedAliases returns the aliases in decls that shadow a name in tracked.
//
// A shadow only matters where the alias target is NOT simply the command's own
// name. `alias rg=rg` changes nothing, and reporting it would be the kind of
// finding that teaches a reader to skim past doctor output.
func shadowedAliases(decls map[string]aliasDecl, tracked map[string]bool) map[string]aliasDecl {
	out := map[string]aliasDecl{}
	for name, decl := range decls {
		if !tracked[name] || decl.Target == "" || decl.Target == name {
			continue
		}
		out[name] = decl
	}
	return out
}

// aliasScopeFiles returns the rc files worth reading for this user.
//
// One file: the one the shell would read. Reading .bashrc when SHELL is zsh
// produces findings about a shell the user is not running, which is a claim
// about a machine that does not exist.
func aliasScopeFiles() []string {
	rc, err := shellRCPath()
	if err != nil {
		return nil
	}
	// Report the name as the user knows it, not as an absolute path that may
	// be a temporary HOME.
	home, herr := os.UserHomeDir()
	if herr == nil {
		if rel, rerr := filepath.Rel(home, rc); rerr == nil && !strings.HasPrefix(rel, "..") {
			return []string{filepath.Join("~", rel)}
		}
	}
	return []string{rc}
}

// addAliasFindings reports tools that verify but whose names are aliased.
//
// It is a WARNING, not an error. The binary is present and passed every check;
// the finding is that the user's shell may run something else. Nothing here is
// broken in a way aes should refuse to run, and a diagnostic that turns a
// healthy machine red for a stylistic choice teaches people to ignore it.
func addAliasFindings(app *App, rc *runContext, st *state.Store, add func(string, driftKind, string, string)) {
	files := aliasScopeFiles()
	if len(files) == 0 {
		// No readable rc: either the shell is one aes does not parse, or there
		// is no rc. Both are ordinary, and neither is worth a finding.
		return
	}

	var decls map[string]aliasDecl
	// The File recorded is the readable path so the parser is testable against
	// a temp file; the message should name what the user calls it.
	for _, shown := range files {
		real := expandHome(shown)
		for name, decl := range readShellAliases(real) {
			decl.File = shown
			if decls == nil {
				decls = map[string]aliasDecl{}
			}
			decls[name] = decl
		}
	}
	if len(decls) == 0 {
		return
	}

	tracked := map[string]bool{}
	paths := map[string]string{}
	for _, t := range rc.Catalog.All() {
		if !rc.Host.Supports(t) {
			continue
		}
		r := report(t, st, rc.target())
		if verifier.Status(r.Status) != verifier.StatusOK {
			// Only a tool that VERIFIES can be shadowed in a way that
			// misleads: one that is already missing has a bigger problem, and
			// two findings for one tool buries the first.
			continue
		}
		for _, b := range binariesFor(t) {
			tracked[b] = true
			paths[b] = r.Path
		}
	}
	for name, decl := range shadowedAliases(decls, tracked) {
		add("warning", driftShadowed, name, shadowMessage(name, decl, paths[name]))
	}
}

// expandHome turns a leading ~/ back into a real path.
func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// binariesFor is every command name a tool answers to: the one verify checks,
// plus each entry in provides.
//
// verifier has an unexported copy and this is not the same question — that one
// asks what must resolve for the tool to count as present, this one asks what a
// user might plausibly have aliased. A tool that provides both `rg` and a
// wrapper name is shadowable through either.
func binariesFor(t *manifest.Tool) []string {
	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	if t.Verify != nil {
		add(t.Verify.Command)
	}
	for _, p := range t.Provides {
		add(p)
	}
	return out
}
