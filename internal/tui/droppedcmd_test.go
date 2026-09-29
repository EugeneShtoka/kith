package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// A tea.Cmd that is thrown away is work that silently never happens, while the model
// already records it as done (an offer marked asked, typing marked stopped). Every
// call to a package function whose last result is a tea.Cmd must keep that result.
func TestNoCommandIsDropped(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var files []*ast.File
	returnsCmd := map[string]bool{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		files = append(files, f)
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				name := fn.Name.Name
				last := lastResultIsCmd(fn.Type)
				if seen, dup := returnsCmd[name]; dup {
					last = last && seen // a name shared by a non-Cmd function is ambiguous
				}
				returnsCmd[name] = last
			}
		}
	}

	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.AssignStmt:
				if len(s.Rhs) != 1 {
					return true
				}
				if name := calleeName(s.Rhs[0]); returnsCmd[name] {
					if id, ok := s.Lhs[len(s.Lhs)-1].(*ast.Ident); ok && id.Name == "_" {
						t.Errorf("%s: the tea.Cmd from %s is discarded", fset.Position(s.Pos()), name)
					}
				}
			case *ast.ExprStmt:
				if name := calleeName(s.X); returnsCmd[name] {
					t.Errorf("%s: the result of %s, a tea.Cmd, is discarded", fset.Position(s.Pos()), name)
				}
			}
			return true
		})
	}
}

// lastResultIsCmd reports whether a function's last result is a tea.Cmd.
func lastResultIsCmd(ft *ast.FuncType) bool {
	if ft.Results == nil || len(ft.Results.List) == 0 {
		return false
	}
	sel, ok := ft.Results.List[len(ft.Results.List)-1].Type.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Cmd" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "tea"
}

// calleeName is the function or method a call expression names, or "".
func calleeName(e ast.Expr) string {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return ""
	}
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}
