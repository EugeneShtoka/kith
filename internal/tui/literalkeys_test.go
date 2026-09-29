package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Every key is a binding with a default (keymap.go and the config's key table), so a
// user can rebind it: a handler comparing a pressed key with a literal ("q", "esc")
// would override the binding silently. This fails on a comparison or switch case of a
// key's String() (or a variable holding one) against a string literal.
func TestNoHandlerComparesAKeyWithALiteral(t *testing.T) {
	t.Parallel()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BinaryExpr:
				if n.Op != token.EQL && n.Op != token.NEQ {
					return true
				}
				for _, pair := range [][2]ast.Expr{{n.X, n.Y}, {n.Y, n.X}} {
					if lit, ok := stringLiteral(pair[1]); ok && isPressedKey(pair[0]) {
						t.Errorf("%s: a pressed key compared with %q — make it a binding", fset.Position(n.Pos()), lit)
					}
				}
			case *ast.SwitchStmt:
				if n.Tag == nil || !isPressedKey(n.Tag) {
					return true
				}
				for _, stmt := range n.Body.List {
					clause, ok := stmt.(*ast.CaseClause)
					if !ok {
						continue
					}
					for _, e := range clause.List {
						if lit, ok := stringLiteral(e); ok {
							t.Errorf("%s: a pressed key switched on %q — make it a binding", fset.Position(e.Pos()), lit)
						}
					}
				}
			}
			return true
		})
	}
}

func stringLiteral(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// isPressedKey reports whether e reads as a key press: key.String(), msg.String(), or a
// variable named press or pressed.
func isPressedKey(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.CallExpr:
		sel, ok := e.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "String" || len(e.Args) != 0 {
			return false
		}
		if id, ok := sel.X.(*ast.Ident); ok {
			return id.Name == "key" || id.Name == "msg" || id.Name == "k"
		}
	case *ast.Ident:
		return e.Name == "press" || e.Name == "pressed"
	}
	return false
}
