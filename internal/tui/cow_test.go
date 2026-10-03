package tui

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
)

// sharedOnPurpose are pointer fields a value receiver may write through: state every
// Model copy shares by design (see cow.go).
var sharedOnPurpose = map[string]bool{
	"derived": true, // *derivedCache, the render cache
}

// TestNoValueReceiverWritesAMap holds cow.go's rule mechanically: a method on a value
// receiver writes no map reached through the receiver (index assignment, ++/--,
// delete, clear, maps.Copy), unless the path crosses a field in sharedOnPurpose. Writes through a
// local alias (x := m.a; x[k] = v) are not seen; build a new map with withEntry.
func TestNoValueReceiverWritesAMap(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var found []string
	scanned := 0
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil || len(fn.Recv.List[0].Names) == 0 {
				continue
			}
			if _, pointer := fn.Recv.List[0].Type.(*ast.StarExpr); pointer {
				continue
			}
			scanned++
			recv := fn.Recv.List[0].Names[0].Name
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if write := mapWrite(n, recv); write != nil {
					found = append(found, fmt.Sprintf("%s: %s writes %s in place",
						fset.Position(write.Pos()), fn.Name.Name, exprString(write)))
				}
				return true
			})
		}
	}
	if scanned < 500 {
		t.Fatalf("scanned %d value-receiver methods; the scan is broken", scanned)
	}
	for _, f := range found {
		t.Error(f)
	}
}

// mapWrite is the map expression n writes in place through recv, or nil.
func mapWrite(n ast.Node, recv string) ast.Expr {
	switch node := n.(type) {
	case *ast.AssignStmt:
		if node.Tok == token.DEFINE {
			return nil
		}
		for _, lhs := range node.Lhs {
			if index, ok := lhs.(*ast.IndexExpr); ok && throughReceiver(index.X, recv) {
				return index.X
			}
		}
	case *ast.IncDecStmt:
		if index, ok := node.X.(*ast.IndexExpr); ok && throughReceiver(index.X, recv) {
			return index.X
		}
	case *ast.CallExpr:
		if len(node.Args) > 0 && writesFirstArg(node.Fun) && throughReceiver(node.Args[0], recv) {
			return node.Args[0]
		}
	}
	return nil
}

// throughReceiver reports whether e is a field path rooted at recv (recv.a.b) that
// crosses no field shared on purpose. An array field (m.edits[f]) is a value and is
// indexed like a map; the scan cannot tell them apart, so arrays are listed in
// valueArrays.
func throughReceiver(e ast.Expr, recv string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if valueArrays[sel.Sel.Name] {
		return false
	}
	for {
		if sharedOnPurpose[sel.Sel.Name] {
			return false
		}
		switch x := sel.X.(type) {
		case *ast.Ident:
			return x.Name == recv
		case *ast.SelectorExpr:
			sel = x
		default:
			return false
		}
	}
}

// valueArrays are fixed-size array fields: indexing one in a value receiver writes
// the receiver's own copy.
var valueArrays = map[string]bool{
	"edits": true, // [fieldCount]editHistory
}

// exprString is e as source, for the failure message.
func exprString(e ast.Expr) string {
	var b strings.Builder
	var walk func(ast.Expr)
	walk = func(e ast.Expr) {
		switch x := e.(type) {
		case *ast.Ident:
			b.WriteString(x.Name)
		case *ast.SelectorExpr:
			walk(x.X)
			b.WriteString("." + x.Sel.Name)
		default:
			b.WriteString("…")
		}
	}
	walk(e)
	return b.String()
}

// writesFirstArg reports a call that writes the map it is handed first: the delete and
// clear builtins, and maps.Copy/maps.DeleteFunc.
func writesFirstArg(fun ast.Expr) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name == "delete" || f.Name == "clear"
	case *ast.SelectorExpr:
		pkg, ok := f.X.(*ast.Ident)
		return ok && pkg.Name == "maps" && (f.Sel.Name == "Copy" || f.Sel.Name == "DeleteFunc")
	}
	return false
}

// Binding user commands in WithConfigFile (a value receiver) leaves the Model it was
// called on as it was: the keymap's maps are copied before they are written.
func TestWithConfigFileLeavesItsReceiversKeymapAlone(t *testing.T) {
	t.Parallel()
	before := starterNew(apitest.Nop{}, config.Display{}).WithKeys(config.DefaultKeys())
	prefixes, scripts := len(before.keys.prefixes), len(before.keys.scripts)
	cfg := config.Config{}
	cfg.Commands.Scripts = []config.Script{{Name: "deploy", Keys: "f9 f10"}}
	after := before.WithConfigFile("", cfg)
	if len(before.keys.prefixes) != prefixes || len(before.keys.scripts) != scripts {
		t.Errorf("the receiver's keymap changed: prefixes %d → %d, scripts %d → %d",
			prefixes, len(before.keys.prefixes), scripts, len(before.keys.scripts))
	}
	if len(after.keys.scripts) != scripts+1 {
		t.Errorf("the new Model has %d script bindings, want the one added", len(after.keys.scripts))
	}
}
