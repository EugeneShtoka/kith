package matrix

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// kithIDs and matrixIDs are the ID types on either side of the seam.
var (
	kithIDs = map[string]bool{
		"github.com/EugeneShtoka/kith/internal/domain.RoomID":  true,
		"github.com/EugeneShtoka/kith/internal/domain.EventID": true,
		"github.com/EugeneShtoka/kith/internal/domain.SpaceID": true,
	}
	matrixIDs = map[string]bool{
		"maunium.net/go/mautrix/id.RoomID":  true,
		"maunium.net/go/mautrix/id.EventID": true,
		"maunium.net/go/mautrix/id.UserID":  true,
	}
)

func typeName(t types.Type) string {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return ""
	}
	return named.Obj().Pkg().Path() + "." + named.Obj().Name()
}

func isKithID(t types.Type) bool   { return kithIDs[typeName(t)] }
func isMatrixID(t types.Type) bool { return matrixIDs[typeName(t)] }

// fromMautrix reports whether obj is declared in a mautrix package.
func fromMautrix(pkg *types.Package) bool {
	return pkg != nil && strings.HasPrefix(pkg.Path(), "maunium.net/go/mautrix")
}

// Every ID crossing between kith's form and Matrix's goes through ids.go, so the
// shape of a kith Matrix ID is decided there alone. Outside it: no conversion to an ID
// type, no Matrix ID turned into a string (it would leave as kith's), and no kith ID
// turned into a string where mautrix takes it (a state key, a request field). A kith
// ID as a string elsewhere (a log, a key in kith's own maps) stays kith's and is fine.
// What this cannot see is an ID read out of a raw string (a pill, account data): those
// go through the seam's roomOf/eventOf/userOf by hand.
func TestIDsCrossOnlyThroughTheSeam(t *testing.T) {
	t.Parallel()

	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
		packages.NeedTypes | packages.NeedTypesInfo}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(pkgs) != 1 || len(pkgs[0].Errors) > 0 {
		t.Fatalf("load: %v", pkgs[0].Errors)
	}
	pkg := pkgs[0]
	info := pkg.TypesInfo
	// kithString reports whether e is a kith ID converted to a string.
	kithString := func(e ast.Expr) bool {
		call, ok := e.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return false
		}
		tv, ok := info.Types[call.Fun]
		return ok && tv.IsType() && types.Identical(tv.Type, types.Typ[types.String]) &&
			isKithID(info.TypeOf(call.Args[0]))
	}
	seen := 0
	for _, file := range pkg.Syntax {
		name := filepath.Base(pkg.Fset.Position(file.Pos()).Filename)
		if name == "ids.go" {
			continue
		}
		seen++
		ast.Inspect(file, func(n ast.Node) bool {
			if n == nil {
				return false
			}
			line := pkg.Fset.Position(n.Pos()).Line
			switch n := n.(type) {
			case *ast.CompositeLit:
				if t := info.TypeOf(n); t == nil || !fromMautrix(namedPkg(t)) {
					return true
				}
				for _, elt := range n.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						elt = kv.Value
					}
					if kithString(elt) {
						t.Errorf("%s:%d: a kith ID handed to mautrix as a string outside ids.go", name, line)
					}
				}
			case *ast.CallExpr:
				if tv, conv := info.Types[n.Fun]; conv && tv.IsType() && len(n.Args) == 1 {
					arg := info.Types[n.Args[0]]
					switch {
					case arg.Value != nil:
						// A constant names a fixed value, not an ID that came from somewhere;
						// a conversion within one side (a space ID as a room ID) crosses nothing.
					case isKithID(tv.Type) && !isKithID(arg.Type), isMatrixID(tv.Type) && !isMatrixID(arg.Type):
						t.Errorf("%s:%d: conversion to %s outside ids.go", name, line, tv.Type)
					case types.Identical(tv.Type, types.Typ[types.String]) && isMatrixID(arg.Type):
						t.Errorf("%s:%d: %s turned into a string outside ids.go", name, line, arg.Type)
					}
					return true
				}
				sel, isSel := n.Fun.(*ast.SelectorExpr)
				if isSel && sel.Sel.Name == "String" && len(n.Args) == 0 && isMatrixID(info.TypeOf(sel.X)) {
					t.Errorf("%s:%d: %s.String() outside ids.go", name, line, info.TypeOf(sel.X))
				}
				if isSel && fromMautrix(objPkg(info, sel.Sel)) {
					for _, arg := range n.Args {
						if kithString(arg) {
							t.Errorf("%s:%d: a kith ID handed to mautrix as a string outside ids.go", name, line)
						}
					}
				}
			}
			return true
		})
	}
	if seen == 0 {
		t.Fatal("inspected no files; the walk is broken")
	}
}

// namedPkg is the package declaring t (through a pointer), or nil.
func namedPkg(t types.Type) *types.Package {
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		t = p.Elem()
	}
	if named, ok := types.Unalias(t).(*types.Named); ok {
		return named.Obj().Pkg()
	}
	return nil
}

// objPkg is the package declaring what ident names, or nil.
func objPkg(info *types.Info, ident *ast.Ident) *types.Package {
	if obj := info.Uses[ident]; obj != nil {
		return obj.Pkg()
	}
	return nil
}
