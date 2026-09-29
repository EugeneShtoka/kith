package daemon

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// Both ends of the sentinel funnel, enforced rather than asserted in a comment.
func TestBothEndsOfTheFunnel(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list sources: %v", err)
	}
	seen := 0
	for _, name := range sources {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		seen++
		base := filepath.Base(name)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch {
			// Server side: CodeInternal is rpcErr's business.
			case base != "server.go" && isSelector(call.Fun, "connect", "NewError") &&
				len(call.Args) > 0 && isSelector(call.Args[0], "connect", "CodeInternal"):
				t.Errorf("%s:%d: connect.NewError(CodeInternal, …) — use rpcErr(err).\n"+
					"Raw, it drops the Mx-Sentinel header, so errors.Is against the "+
					"contract's sentinels is false on every client.",
					base, fset.Position(call.Pos()).Line)

			// Client side: "daemon: …" is the shape callErr produces. Writing it by hand
			// means the daemon's tag was not read back, so the sentinel arrives as text.
			case strings.HasPrefix(base, "remote") && isSelector(call.Fun, "fmt", "Errorf") &&
				len(call.Args) > 0 && literalStartsWith(call.Args[0], `"daemon: `):
				t.Errorf("%s:%d: fmt.Errorf(\"daemon: …\") — use callErr(op, err).\n"+
					"By hand it keeps the message and loses the identity, which is the "+
					"half of the promise nobody notices is gone.",
					base, fset.Position(call.Pos()).Line)
			}
			return true
		})
	}
	if seen == 0 {
		t.Fatal("parsed no sources; the walk is broken")
	}
}

// isSelector reports whether an expression is exactly pkg.Name.
func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == pkg
}

// literalStartsWith reports whether an expression is a string literal with this prefix.
func literalStartsWith(e ast.Expr, prefix string) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Kind == token.STRING && strings.HasPrefix(lit.Value, prefix)
}
