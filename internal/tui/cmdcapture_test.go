package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A tea.Cmd closure that mentions the receiver captures it: the whole Model (16 KB and
// every map it holds) stays alive for as long as the command runs, a sync stream or a
// long poll among them. Commands take what they need as locals on the event loop
// (ctx, backend := m.ctx, m.backend) and mention m nowhere. TestCommandsReachOnlyTheBackend
// says which fields a command may use; this says how it may hold them.
func TestCommandsDoNotCaptureTheModel(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		files = append(files, f)
	}
	isOffLoop := offLoopHelpers(t, files)

	var bad []string
	closures := 0
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || receiverOf(fn) == "" || fn.Body == nil {
				continue
			}
			recv := receiverOf(fn)
			check := func(lit *ast.FuncLit) {
				closures++
				ast.Inspect(lit.Body, func(n ast.Node) bool {
					if id, ok := n.(*ast.Ident); ok && id.Name == recv {
						bad = append(bad, fset.Position(id.Pos()).String()+" in "+fn.Name.Name)
						return false
					}
					return true
				})
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.FuncLit:
					if returnsTeaMsg(node.Type) {
						check(node)
						return false
					}
				case *ast.CallExpr:
					if isOffLoop(node.Fun) {
						for _, arg := range node.Args {
							if lit, ok := arg.(*ast.FuncLit); ok {
								check(lit)
							}
						}
					}
				}
				return true
			})
		}
	}
	if closures < 50 {
		t.Fatalf("found only %d command closures; the scan is not seeing the package", closures)
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		t.Errorf("%d command closures capture the Model; take the fields as locals first:\n  %s",
			len(bad), strings.Join(bad, "\n  "))
	}
}
