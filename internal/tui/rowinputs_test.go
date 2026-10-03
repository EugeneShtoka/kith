package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
)

// rowReads classifies every piece of Model state the row renderer reads, found by
// following renderEntry through every Model method it reaches. Each read is keyed by
// the row cache (rowInputs, per message), keyed for the whole timeline (derivedKey:
// the room, the messages' revision, the config revision, the unread and place
// fingerprints, the width through prepareRows), or not cached at all. A read the table
// does not name fails TestTheRowRendererReadsOnlyKeyedState until someone decides which
// it is: a key narrower than the render is the bug three audits in a row found.
var rowReads = map[string]string{
	// rowInputs, per message.
	"timeline.reactions": "rowInputs.reactions (a fingerprint of the chips)",
	"pics.rowsFor":       "rowInputs.images (the rows slice, replaced whole)",
	"timeline.starred":   "rowInputs.starred",
	"timeline.revealed":  "rowInputs.revealed",
	"quotes.known":       "rowInputs.quoted",
	"focus":              "rowInputs.pictures (showsPictures)",
	"pics.graphics":      "rowInputs.pictures (showsPictures); set with the config (apply.go), which bumps conf.rev",
	// derivedKey, for the whole timeline.
	"timeline.messages":          "derivedKey.rev: the loaded messages, set only through setMessages (a quoted reply read in place)",
	"timeline.opened.unreadFrom": "derivedKey.room: set only when a room opens",
	"rooms.byID":                 "derivedKey.place (placeFingerprint of the open room's facts)",
	"rooms.spaceNames":           "derivedKey.place",
	"rooms.spaces":               "derivedKey.place",
	"rail.pinned":                "derivedKey.place (Pinned); pins are set with the config",
	"rail.roomFacts":             "derivedKey.place (the open room's facts, its tags included, recomputed from the same inputs)",
	"rail.tags":                  "derivedKey.place (Tags in the facts); tags are set with the config, which bumps conf.rev",
	"openRoom":                   "derivedKey.room",
	"timeline.layout.room":       "derivedKey.rtl (mirrored)",
	"timeline.layout.rtl":        "derivedKey.rtl (mirrored)",
	// Set only by applyConfig, which bumps conf.rev (derivedKey.cfg).
	"theme":             "derivedKey.cfg",
	"prefs.display":     "derivedKey.cfg",
	"prefs.identities":  "derivedKey.cfg",
	"prefs.roomAliases": "derivedKey.cfg",
	"prefs.tracked":     "derivedKey.cfg",
}

// TestTheRowRendererReadsOnlyKeyedState walks the renderer's calls and reads.
func TestTheRowRendererReadsOnlyKeyedState(t *testing.T) {
	t.Parallel()
	reads, passed := rendererReads(t, "renderEntry")
	for _, read := range slices.Sorted(maps.Keys(reads)) {
		if classify(read) == "" {
			t.Errorf("the row renderer reads m.%s (in %s), which no key covers: key it (rowInputs or "+
				"derivedKey) or name it in rowReads with why it needs none", read, reads[read])
		}
	}
	for entry := range rowReads {
		if !slices.ContainsFunc(slices.Collect(maps.Keys(reads)), func(read string) bool {
			return read == entry || strings.HasPrefix(read, entry+".")
		}) {
			t.Errorf("rowReads names m.%s, which the renderer no longer reads: drop it", entry)
		}
	}
	for _, where := range passed {
		t.Errorf("%s passes the Model to a function the walk cannot follow; make it a method", where)
	}
}

// classify is the entry naming read or a prefix of it, or "".
func classify(read string) string {
	for prefix := read; prefix != ""; {
		if why, ok := rowReads[prefix]; ok {
			return why
		}
		i := strings.LastIndexByte(prefix, '.')
		if i < 0 {
			break
		}
		prefix = prefix[:i]
	}
	return ""
}

// rendererReads follows root through every Model method it calls, and answers each
// m.field.field… chain read (with the method it was read in), and each place the Model
// itself is handed to something else.
func rendererReads(t *testing.T, root string) (map[string]string, []string) {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	methods := map[string]*ast.FuncDecl{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || len(fn.Recv.List[0].Names) != 1 {
				continue
			}
			if id, ok := fn.Recv.List[0].Type.(*ast.Ident); ok && id.Name == "Model" {
				methods[fn.Name.Name] = fn
			}
		}
	}
	reads := map[string]string{}
	var passed []string
	seen := map[string]bool{}
	queue := []string{root}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		fn := methods[name]
		if fn == nil || seen[name] {
			continue
		}
		seen[name] = true
		recv := fn.Recv.List[0].Names[0].Name
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				path, ok := chain(n, recv)
				if !ok {
					return true
				}
				first, _, _ := strings.Cut(path, ".")
				if methods[first] != nil {
					queue = append(queue, first)
				} else if _, had := reads[path]; !had {
					reads[path] = name
				}
				return false
			case *ast.CallExpr:
				for _, arg := range n.Args {
					if id, ok := arg.(*ast.Ident); ok && id.Name == recv {
						passed = append(passed, fset.Position(n.Pos()).String())
					}
				}
			}
			return true
		})
	}
	return reads, passed
}

// chain is sel as a field path from the receiver ("timeline.reactions"), or false when
// it is rooted elsewhere.
func chain(sel *ast.SelectorExpr, recv string) (string, bool) {
	var parts []string
	var x ast.Expr = sel
	for {
		switch e := x.(type) {
		case *ast.SelectorExpr:
			parts = append(parts, e.Sel.Name)
			x = e.X
		case *ast.Ident:
			if e.Name != recv {
				return "", false
			}
			slices.Reverse(parts)
			return strings.Join(parts, "."), true
		default:
			return "", false
		}
	}
}

// derivedReads is every Model field the derived cache's computation reads (computeDerived
// and what it calls), each with the derivedKey field that covers it. The row cache's
// walk (rowReads) starts at renderEntry and never reaches these.
var derivedReads = map[string]string{
	"openRoom":          "derivedKey.room",
	"thread":            "derivedKey.thread (open is root != \"\")",
	"timeline.messages": "derivedKey.rev (setMessages)",
	"unread":            "derivedKey.unread (the open room's fingerprint; collapse reads only it)",
	"rooms.byID":        "derivedKey.place",
	"rooms.spaceNames":  "derivedKey.place",
	"rail.roomFacts":    "derivedKey.place (the open room's facts, its tags included)",
	"prefs.display":     "derivedKey.cfg",
	"prefs.identities":  "derivedKey.cfg",
	"theme":             "derivedKey.cfg",
	"me":                "none needed: set once (WithRules) before the program starts",
	"selves":            "derivedKey.selves",
}

// TestTheDerivedCacheReadsOnlyKeyedState walks computeDerived's calls and reads, as
// TestTheRowRendererReadsOnlyKeyedState walks the row renderer's.
func TestTheDerivedCacheReadsOnlyKeyedState(t *testing.T) {
	t.Parallel()
	reads, passed := rendererReads(t, "computeDerived")
	covered := func(read string) bool {
		for prefix := read; prefix != ""; {
			if _, ok := derivedReads[prefix]; ok {
				return true
			}
			i := strings.LastIndexByte(prefix, '.')
			if i < 0 {
				return false
			}
			prefix = prefix[:i]
		}
		return false
	}
	for _, read := range slices.Sorted(maps.Keys(reads)) {
		if !covered(read) {
			t.Errorf("the derived cache reads m.%s (in %s), which derivedKey does not cover: key it, "+
				"or name it in derivedReads with why it needs none", read, reads[read])
		}
	}
	for entry := range derivedReads {
		if !slices.ContainsFunc(slices.Collect(maps.Keys(reads)), func(read string) bool {
			return read == entry || strings.HasPrefix(read, entry+".")
		}) {
			t.Errorf("derivedReads names m.%s, which the derived cache no longer reads: drop it", entry)
		}
	}
	for _, where := range passed {
		t.Errorf("%s passes the Model to a function the walk cannot follow; make it a method", where)
	}
}
