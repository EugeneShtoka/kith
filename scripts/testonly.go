//go:build ignore

// testonly fails when an exported function or method is named by tests alone: no
// non-test file in the module refers to it. `go tool deadcode` (arch-check) finds the
// unreachable functions, but keeps every exported method of a type that reaches an
// interface or reflection alive, which is most of them here. Tests build their own
// fixtures; code that exists for a test is not code (CONTRIBUTING, "tests don't shape
// code").
//
// A method called only through an interface another package declares (the standard
// library's, a dependency's) is named nowhere here; those are listed in implemented,
// each with the interface.
//
// Run: go run scripts/testonly.go (make arch-check does).
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// implicit are method names an interface of the standard library calls.
var implicit = map[string]bool{
	"String": true, "Error": true, "Unwrap": true, "Is": true, "As": true, "Format": true,
	"MarshalJSON": true, "UnmarshalJSON": true, "MarshalText": true, "UnmarshalText": true,
	"ServeHTTP": true, "Len": true, "Less": true, "Swap": true, "Read": true, "Write": true, "Close": true,
}

// implemented are methods a dependency's interface calls, by name, with that interface.
var implemented = map[string]string{
	"WrapUnary":             "connect.Interceptor",
	"WrapStreamingClient":   "connect.Interceptor",
	"WrapStreamingHandler":  "connect.Interceptor",
	"WithAttrs":             "slog.Handler",
	"WithGroup":             "slog.Handler",
	"VerificationReady":     "verificationhelper.RequiredCallbacks",
	"VerificationCancelled": "verificationhelper.RequiredCallbacks",
	"ShowSAS":               "verificationhelper.ShowSASCallbacks",
	"Init":                  "tea.Model",
	"Update":                "tea.Model",
	"View":                  "tea.Model",
}

func main() {
	var files []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (strings.HasPrefix(d.Name(), ".") && path != "." || d.Name() == "node_modules" || d.Name() == "notes" || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") && path != filepath.Join("scripts", "testonly.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "testonly:", err)
		os.Exit(2)
	}
	fset := token.NewFileSet()
	named := map[string]int{} // identifier → uses in non-test code, declarations included
	type decl struct{ name, at string }
	var decls []decl
	for _, path := range files {
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution|parser.ParseComments)
		if perr != nil {
			fmt.Fprintln(os.Stderr, "testonly:", perr)
			os.Exit(2)
		}
		generated := ast.IsGenerated(f)
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				named[id.Name]++
			}
			return true
		})
		if generated {
			continue
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() || fn.Name.Name == "main" {
				continue
			}
			decls = append(decls, decl{fn.Name.Name, fset.Position(fn.Pos()).String()})
		}
	}
	var found []string
	for _, d := range decls {
		if implicit[d.name] || implemented[d.name] != "" || named[d.name] > 1 {
			continue
		}
		found = append(found, d.at+": "+d.name)
	}
	if len(found) == 0 {
		return
	}
	slices.Sort(found)
	fmt.Println(strings.Join(found, "\n"))
	fmt.Println("testonly: exported functions only tests name (delete them, or move them into a _test.go file of their package)")
	os.Exit(1)
}
