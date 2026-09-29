package setup_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every binary that reads a config also checks it.
func TestEveryBinaryThatLoadsAConfigChecksIt(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir("../../cmd")
	if err != nil {
		t.Fatalf("read cmd/: %v", err)
	}
	checked := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := "../../cmd/" + entry.Name()
		loads, validates := mentions(t, dir, "config", "Load"), mentions(t, dir, "setup", "Validate")
		if !loads {
			continue
		}
		checked++
		if !validates {
			t.Errorf("cmd/%s calls config.Load and never setup.Validate", entry.Name())
		}
	}
	if checked == 0 {
		t.Fatal("found no binary calling config.Load; the walk is broken")
	}
}

// mentions reports whether any non-test file in dir calls pkg.Name.
func mentions(t *testing.T, dir, pkg, name string) bool {
	t.Helper()
	fset := token.NewFileSet()
	sources, err := filepath.Glob(dir + "/*.go")
	if err != nil {
		t.Fatalf("list %s: %v", dir, err)
	}
	found := false
	for _, src := range sources {
		if strings.HasSuffix(src, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, src, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", src, perr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != name {
				return true
			}
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == pkg {
				found = true
			}
			return true
		})
	}
	return found
}
