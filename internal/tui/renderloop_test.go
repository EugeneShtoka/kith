package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// View() must run on the Update goroutine: it reads Model.derived, which Update writes
// and every model copy shares, so rendering alongside an Update is a silent data race.
// Asserted against the framework's source: View() has one call site, inside
// Program.render, and render is never started with `go`. cmdreach_test.go guards the
// tea.Cmd side of the same invariant.
func TestRenderRunsOnTheUpdateGoroutine(t *testing.T) {
	t.Parallel()

	dir := bubbleteaSource(t)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(dir, "tea.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse the framework's tea.go: %v", err)
	}

	var viewCallers, renderInGoroutine []string

	inspectEnclosing(file, func(fn *ast.FuncDecl, node ast.Node) {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		if sel.Sel.Name == "View" {
			viewCallers = append(viewCallers, funcName(fn)+" ("+fset.Position(call.Pos()).String()+")")
		}
	})

	ast.Inspect(file, func(n ast.Node) bool {
		g, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}
		ast.Inspect(g, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok &&
				(sel.Sel.Name == "render" || sel.Sel.Name == "View") {
				renderInGoroutine = append(renderInGoroutine, fset.Position(call.Pos()).String())
			}
			return true
		})
		return true
	})

	if len(viewCallers) != 1 {
		t.Fatalf("View() has %d call sites in the framework, want exactly one:\n  %s\n\n"+
			"Model.derived is written by Update, read by View and shared by every copy of the model. A second "+
			"call site is a second scheduling context, and the row cache and color slots become "+
			"a data race — see the comment on Model.derived.",
			len(viewCallers), strings.Join(viewCallers, "\n  "))
	}
	if !strings.Contains(viewCallers[0], "(*Program).render") {
		t.Errorf("View() is called from %s, want (*Program).render — the function the "+
			"event loop calls inline after Update", viewCallers[0])
	}
	if len(renderInGoroutine) != 0 {
		t.Errorf("the framework starts render/View with `go` at:\n  %s\n\n"+
			"Rendering has moved off the Update goroutine, so View's reads of Model.derived "+
			"now race Update's writes.", strings.Join(renderInGoroutine, "\n  "))
	}
}

// bubbletea stays replaced by the fork, so the test above asserts against source we
// control. (Deleting the replace fails the build; this catches repointing it.)
func TestBubbleTeaStaysPinnedToTheFork(t *testing.T) {
	t.Parallel()

	mod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	const want = "replace charm.land/bubbletea/v2 => github.com/EugeneShtoka/bubbletea/v2 "
	if !strings.Contains(string(mod), want) {
		t.Fatalf("go.mod no longer replaces bubbletea with the fork.\n\n"+
			"The fork is what makes TestRenderRunsOnTheUpdateGoroutine a guarantee rather than "+
			"an observation: upstream has shipped renderer designs where View() does not run on "+
			"the Update goroutine, and View reads Model.derived while Update writes it. Dropping the replace "+
			"means reading that test's assertions against whatever upstream does now.\n\n"+
			"want a line beginning: %s", want)
	}
}

// bubbleteaSource is the directory the framework is compiled from.
func bubbleteaSource(t *testing.T) string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(),
		"go", "list", "-m", "-f", "{{.Dir}}", "charm.land/bubbletea/v2").Output()
	if err != nil {
		t.Fatalf("locate the bubbletea module: %v", err)
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		t.Fatal("the toolchain reports no directory for charm.land/bubbletea/v2")
	}
	if _, err := os.Stat(filepath.Join(dir, "tea.go")); err != nil {
		t.Fatalf("no tea.go under %s: %v", dir, err)
	}
	return dir
}

// inspectEnclosing walks every node with its enclosing top-level function.
func inspectEnclosing(file *ast.File, visit func(fn *ast.FuncDecl, node ast.Node)) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			if n != nil {
				visit(fn, n)
			}
			return true
		})
	}
}

// funcName names a declaration as a reader would: (*Program).render.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	var b strings.Builder
	switch t := fn.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		b.WriteString("(*")
		if id, ok := t.X.(*ast.Ident); ok {
			b.WriteString(id.Name)
		}
		b.WriteString(")")
	case *ast.Ident:
		b.WriteString(t.Name)
	}
	b.WriteString("." + fn.Name.Name)
	return b.String()
}
