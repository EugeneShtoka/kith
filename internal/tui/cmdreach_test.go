package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// Bubble Tea runs every tea.Cmd on its own goroutine. The model is a value and its
// maps are copy-on-write (cow.go), but the derived cache is shared by every copy, and
// derivedFor() writes through m.derived on the event loop. So inside a tea.Cmd the
// model may only be used for its immutable, concurrency-safe fields; everything else
// is read on the event loop and captured as a local first.
var cmdMayRead = map[string]bool{
	"backend": true, // daemon client, safe for concurrent use
	"ctx":     true, // app-lifetime context, written once
	"log":     true, // *slog.Logger, safe for concurrent use, set once at startup
}

// offLoop are the package's command builders. Every function they are handed runs on
// the command's goroutine even when it does not itself return a tea.Msg (fire's call
// returns an error, fetch's call a value), so those arguments are scanned too.
var offLoop = []string{"fetch", "fire", "listen"}

// TestCommandsReachOnlyTheBackend walks every tea.Cmd closure in the package and
// fails on any model field it reads outside cmdMayRead, following calls to the
// model's own methods.
func TestCommandsReachOnlyTheBackend(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	methods := map[string]*ast.FuncDecl{}
	var files []*ast.File

	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
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
			fn, ok := decl.(*ast.FuncDecl)
			if ok && receiverOf(fn) != "" {
				methods[methodKey(fn)] = fn
			}
		}
	}
	if len(methods) == 0 {
		t.Fatal("no Model methods found; the scan would pass vacuously")
	}
	isOffLoop := offLoopHelpers(t, files)

	var bad []string
	closures, helperArgs := 0, 0
	for _, f := range files {
		ast.Inspect(f, func(node ast.Node) bool {
			decl, ok := node.(*ast.FuncDecl)
			if !ok {
				return true
			}
			recv := receiverOf(decl)
			if recv == "" {
				return true
			}
			ast.Inspect(decl.Body, func(inner ast.Node) bool {
				switch node := inner.(type) {
				case *ast.FuncLit:
					if returnsTeaMsg(node.Type) {
						closures++
						bad = append(bad, offenses(decl.Name.Name, node.Body, recv, receiverType(decl), methods)...)
						bad = append(bad, sharedCaptures(decl, node.Body)...)
					}
				case *ast.CallExpr:
					if !isOffLoop(node.Fun) {
						return true
					}
					for _, arg := range node.Args {
						switch arg := arg.(type) {
						case *ast.FuncLit:
							// A tea.Msg-returning one is also scanned above; duplicates are compacted.
							helperArgs++
							bad = append(bad, offenses(decl.Name.Name, arg.Body, recv, receiverType(decl), methods)...)
							bad = append(bad, sharedCaptures(decl, arg.Body)...)
						case *ast.SelectorExpr:
							// m.method handed over as a value runs its body off the loop.
							key := receiverType(decl) + "." + arg.Sel.Name
							if id, ok := arg.X.(*ast.Ident); ok && id.Name == recv && methods[key] != nil {
								helperArgs++
								method := methods[key]
								bad = append(bad, offenses(decl.Name.Name+" via m."+arg.Sel.Name,
									method.Body, receiverOf(method), receiverType(method), methods)...)
							}
						}
					}
				}
				return true
			})
			return true
		})
	}
	if closures < 50 {
		t.Fatalf("found only %d tea.Cmd closures; the scan is not seeing the package", closures)
	}
	if helperArgs < 10 {
		t.Fatalf("found only %d functions handed to %v; the scan is not seeing them", helperArgs, offLoop)
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		t.Errorf("a tea.Cmd runs on its own goroutine while Update writes the model.\n"+
			"These reach past the backend and the context:\n  %s\n"+
			"Read the field on the event loop and capture the value, or — if it is "+
			"genuinely immutable and concurrency-safe — add it to cmdMayRead with the "+
			"reason.", strings.Join(slices.Compact(bad), "\n  "))
	}
}

// offenses is one report line per disallowed field a command body reaches.
func offenses(where string, body ast.Node, recv, recvType string, methods map[string]*ast.FuncDecl) []string {
	var out []string
	for _, name := range reaches(body, recv, recvType, methods, map[string]bool{}) {
		out = append(out, where+" reads m."+name)
	}
	return out
}

// sharedAccessors are the Model's ways to the shared cache: a local taken from one
// and used in a command reaches it off the loop as surely as m.derived would.
var sharedAccessors = map[string]bool{"derived": true, "derivedFor": true, "walkFor": true}

// sharedCaptures reports locals of decl assigned from the shared cache (x := m.derived,
// d, w := m.walkFor()) that a command body then uses. Model maps are copy-on-write
// (cow.go), so a captured map is a snapshot; the cache is the one thing shared.
func sharedCaptures(decl *ast.FuncDecl, body ast.Node) []string {
	recv := receiverOf(decl)
	tainted := map[string]bool{}
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 || !fromShared(assign.Rhs[0], recv) {
			return true
		}
		for _, lhs := range assign.Lhs {
			if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
				tainted[id.Name] = true
			}
		}
		return true
	})
	var out []string
	ast.Inspect(body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && tainted[id.Name] {
			out = append(out, decl.Name.Name+" captures "+id.Name+", taken from the shared cache")
		}
		return true
	})
	return out
}

// fromShared reports whether e reads the shared cache through recv: recv.derived...,
// or a call to recv.derivedFor / recv.walkFor.
func fromShared(e ast.Expr, recv string) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == recv && sharedAccessors[sel.Sel.Name] {
				found = true
			}
		}
		return !found
	})
	return found
}

// offLoopHelpers checks that every offLoop name is still a package function (so a
// rename cannot quietly drop it from the scan) and matches a call to one, generic
// instantiation included.
func offLoopHelpers(t *testing.T, files []*ast.File) func(ast.Expr) bool {
	t.Helper()
	declared := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
				declared[fn.Name.Name] = true
			}
		}
	}
	for _, name := range offLoop {
		if !declared[name] {
			t.Fatalf("offLoop names %s, which is no longer a package function", name)
		}
	}
	return func(fun ast.Expr) bool {
		switch f := fun.(type) {
		case *ast.IndexExpr:
			fun = f.X
		case *ast.IndexListExpr:
			fun = f.X
		}
		id, ok := fun.(*ast.Ident)
		return ok && slices.Contains(offLoop, id.Name)
	}
}

// reaches is every disallowed model field a body reads, following calls to the
// receiver type's own methods (keyed by type, as several types share method names);
// seen breaks recursion.
func reaches(body ast.Node, recv, recvType string, methods map[string]*ast.FuncDecl, seen map[string]bool) []string {
	var out []string
	ast.Inspect(body, func(node ast.Node) bool {
		sel, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != recv {
			return true
		}
		name := sel.Sel.Name
		key := recvType + "." + name
		switch {
		case cmdMayRead[name]:
		case methods[key] != nil:
			if !seen[key] {
				seen[key] = true
				inner := methods[key]
				out = append(out, reaches(inner.Body, receiverOf(inner), receiverType(inner), methods, seen)...)
			}
		default:
			out = append(out, name)
		}
		// m.a.b.c is one reach at m.a.
		return false
	})
	return out
}

// receiverOf is a method's receiver name, or "" for a function or unnamed receiver.
func receiverOf(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 || len(fn.Recv.List[0].Names) == 0 {
		return ""
	}
	return fn.Recv.List[0].Names[0].Name
}

// receiverType is a method's receiver type name, without a pointer.
func receiverType(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	t := fn.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// methodKey is how methods are looked up: Type.name.
func methodKey(fn *ast.FuncDecl) string { return receiverType(fn) + "." + fn.Name.Name }

// returnsTeaMsg reports whether a function literal returns exactly one tea.Msg
// (a tea.Cmd or a tea.Tick callback).
func returnsTeaMsg(t *ast.FuncType) bool {
	if t.Results == nil || len(t.Results.List) != 1 {
		return false
	}
	sel, ok := t.Results.List[0].Type.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Msg"
}
