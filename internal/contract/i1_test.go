package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Invariant I1: every tool goes catalog -> resolver -> actions -> installer.
// No `switch tool.Name`, no per-tool special case.
//
// This had no guard at all, which an independent investigation demonstrated
// by inserting `if a.Tool == "jq"` into production code: all sixteen packages
// stayed green. The codebase is clean; nothing would have caught a regression.
// A test asserting correct behaviour cannot catch this, because a special case
// added in the middle of a working path changes nothing observable — the
// defect is architectural, so the guard has to be architectural too.
//
// The rule is narrow on purpose. Comparing a field named Name or Tool against
// a non-empty string LITERAL is a tool-name special case, whatever the
// variable happens to be called. Comparing against a variable, against a field with
// another name, or against the empty string is not: `t.Name == ""` asks
// whether a name was supplied, which is a presence check, and flagging it would
// train a reader to ignore the guard.
func TestNoToolNameSpecialCasing(t *testing.T) {
	fset := token.NewFileSet()
	var checked int

	err := filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "testdata", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // a file that does not compile is another test's problem
		}
		checked++

		ast.Inspect(file, func(n ast.Node) bool {
			bin, ok := n.(*ast.BinaryExpr)
			if !ok || bin.Op != token.EQL {
				return true
			}
			sel, ok := bin.X.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// Two fields hold a tool name: manifest.Tool.Name and
			// installer.Action.Tool. Checking only Name missed the first
			// planted special case, because the installer spells it Tool.
			switch sel.Sel.Name {
			case "Name", "Tool":
			default:
				return true
			}
			lit, ok := bin.Y.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			// An empty-string comparison is a presence check
			// ("is a name set?"), not a special case for a particular tool.
			if lit.Value == `""` || lit.Value == "``" {
				return true
			}
			t.Errorf("%s: compares .Name against the literal %s — "+
				"invariant I1 forbids special-casing a tool by name; resolve "+
				"behaviour through a strategy, a verify block or the manifest",
				fset.Position(bin.Pos()), lit.Value)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if checked == 0 {
		t.Fatal("no Go files examined; the guard is vacuous")
	}
	t.Logf("checked %d production files for per-tool special cases", checked)
}
