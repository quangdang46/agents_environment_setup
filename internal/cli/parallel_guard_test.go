package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// pathMutatingCommands are the entry points that call withBinOnPath and so
// rewrite the process's PATH for their duration.
var pathMutatingCommands = []string{
	"runSetup", "runVerify", "runList", "runDoctor", "withBinOnPath",
}

// TestNoParallelTestReachesPathMutation enforces a constraint the type system
// cannot express and the race detector cannot see.
//
// withBinOnPath rewrites process-global PATH. Go does not instrument
// os.Setenv, so a test running in parallel with one of these commands is not a
// data race the detector will report - it is a test that reads a PATH another
// test changed, and it fails intermittently and far from its cause.
//
// Nothing stops the next person adding t.Parallel() to a setup test. This
// does, by parsing the package's own test files: a function that calls
// t.Parallel() may not reach a PATH-mutating command.
func TestNoParallelTestReachesPathMutation(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v (found %d)", err, len(files))
	}

	checked := 0
	for _, path := range files {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			checked++
			parallel, hits := false, []string{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch fn := call.Fun.(type) {
				case *ast.SelectorExpr:
					if id, ok := fn.X.(*ast.Ident); ok {
						name = id.Name + "." + fn.Sel.Name
					}
				case *ast.Ident:
					name = fn.Name
				}
				switch {
				case name == "t.Parallel":
					parallel = true
				case strings.HasPrefix(name, "t."):
				default:
					for _, cmd := range pathMutatingCommands {
						if name == cmd || strings.HasSuffix(name, "."+cmd) {
							hits = append(hits, name)
						}
					}
				}
				return true
			})
			if parallel && len(hits) > 0 {
				t.Errorf("%s: test %s calls t.Parallel() and reaches %s; "+
					"a parallel test must not run alongside a PATH mutation - "+
					"go's race detector does not instrument os.Setenv, so this "+
					"fails intermittently and far from its cause",
					path, fn.Name.Name, strings.Join(hits, ", "))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no test functions examined; the guard is vacuous")
	}
	t.Logf("checked %d test functions against %d PATH-mutating commands",
		checked, len(pathMutatingCommands))
}
