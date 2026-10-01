package daemon

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api"
)

// Every sentinel the api contract declares survives the socket.
func TestEverySentinelInTheContractCrossesTheWire(t *testing.T) {
	t.Parallel()

	// The declared sentinels, by the name they carry in the source.
	declared := sentinelsDeclaredInAPI(t)
	if len(declared) == 0 {
		t.Fatal("parsed internal/api/errors.go and found no Err* declarations; the walk is broken")
	}

	// Each one, resolved to the value it names. Adding an api.Err* without adding it
	// here is a compile-clean omission, which is why the parse above exists to catch it.
	byName := map[string]error{
		"ErrNoSpaceParent":    api.ErrNoSpaceParent,
		"ErrNoPower":          api.ErrNoPower,
		"ErrNoEncryption":     api.ErrNoEncryption,
		"ErrNoKeyBackup":      api.ErrNoKeyBackup,
		"ErrBadRecoveryKey":   api.ErrBadRecoveryKey,
		"ErrBadKeyFile":       api.ErrBadKeyFile,
		"ErrNoRoomKeys":       api.ErrNoRoomKeys,
		"ErrKeyBackupExists":  api.ErrKeyBackupExists,
		"ErrBadPassword":      api.ErrBadPassword,
		"ErrSessionRejected":  api.ErrSessionRejected,
		"ErrUnreachable":      api.ErrUnreachable,
		"ErrSpellUnavailable": api.ErrSpellUnavailable,
		"ErrEditsRemain":      api.ErrEditsRemain,
		"ErrSeatTaken":        api.ErrSeatTaken,
		"ErrNetworkOff":       api.ErrNetworkOff,
		"ErrNotOnNetwork":     api.ErrNotOnNetwork,
	}

	for _, name := range declared {
		sentinel, known := byName[name]
		if !known {
			t.Errorf("api.%s is declared but this test does not know it — add it to byName, "+
				"and add it to daemon.sentinels unless there is a written reason its identity "+
				"need not cross the socket", name)
			continue
		}
		if _, ok := sentinelName(sentinel); !ok {
			t.Errorf("api.%s does not survive the socket: it is not in the sentinels map, so "+
				"errors.Is against it is false on every client", name)
		}
	}
}

// A wrapped sentinel keeps its identity through the round trip, which is the property the
// map exists for.
func TestASentinelSurvivesBeingWrapped(t *testing.T) {
	t.Parallel()

	for name, sentinel := range sentinels {
		got, ok := sentinelName(wrapped{sentinel})
		if !ok {
			t.Errorf("%s: a wrapped sentinel was not recognized", name)
			continue
		}
		if got != name {
			t.Errorf("%s: wrapped sentinel reported as %q", name, got)
		}
	}
}

type wrapped struct{ err error }

func (w wrapped) Error() string { return "daemon: while doing something: " + w.err.Error() }
func (w wrapped) Unwrap() error { return w.err }

// sentinelsDeclaredInAPI parses internal/api/errors.go and returns every Err* it
// declares at package level.
func sentinelsDeclaredInAPI(t *testing.T) []string {
	t.Helper()

	path := filepath.Join("..", "api", "errors.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var names []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, ident := range value.Names {
				if len(ident.Name) > 3 && ident.Name[:3] == "Err" {
					names = append(names, ident.Name)
				}
			}
		}
	}
	return names
}
