//go:build ignore

// durationcheck fails when a time.Duration is handed to fmt or log as it is. Go
// prints one as "1m0s"; a person reads "1m" (human.Duration). Log attributes need
// nothing: internal/logging formats every duration value itself.
//
// Tests are left out: their messages are for whoever reads a failure.
//
// Run: go run scripts/durationcheck.go (make arch-quick does).
package main

import (
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

func main() {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo,
		BuildFlags: []string{"-tags=goolm"},
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		fmt.Fprintln(os.Stderr, "durationcheck:", err)
		os.Exit(2)
	}
	if packages.PrintErrors(pkgs) > 0 {
		os.Exit(2)
	}
	wd, _ := os.Getwd()
	var found []string
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			if ast.IsGenerated(f) {
				continue
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !printsArgs(p.TypesInfo, call) {
					return true
				}
				for _, arg := range call.Args {
					if t := p.TypesInfo.TypeOf(arg); t != nil && t.String() == "time.Duration" {
						found = append(found, strings.TrimPrefix(p.Fset.Position(arg.Pos()).String(), wd+"/"))
					}
				}
				return true
			})
		}
	}
	if len(found) == 0 {
		return
	}
	slices.Sort(found)
	fmt.Println(strings.Join(found, "\n"))
	fmt.Println(`durationcheck: a time.Duration printed as Go writes it ("1m0s"); wrap it in human.Duration`)
	os.Exit(1)
}

// printsArgs reports whether call is a function or method of fmt or log, the ones
// that render their arguments.
func printsArgs(info *types.Info, call *ast.CallExpr) bool {
	var id *ast.Ident
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		id = fun.Sel
	case *ast.Ident:
		id = fun
	default:
		return false
	}
	fn, ok := info.Uses[id].(*types.Func)
	if !ok || fn.Pkg() == nil {
		return false
	}
	path := fn.Pkg().Path()
	return path == "fmt" || path == "log"
}
