package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// readOnlyRoom opens a room this account may read but not post in.
func readOnlyRoom(t *testing.T) (Model, *sendingBackend) {
	t.Helper()
	backend := &sendingBackend{}
	m := sized(t, update(t, starterNew(backend, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: mx + "!news:x", Name: "News", ReadOnly: true},
	}}))
	next, _ := m.selectRoom(domain.Room{ID: mx + "!news:x", Name: "News", ReadOnly: true})
	m = next
	m.focus, m.compose.insertMode = paneTimeline, false
	return m.clearStatus(), backend
}

// A read-only room says so in the composer, and a message, a scheduled one or a file
// typed there is not sent: the status says why and the draft stays.
func TestARoomYouCannotWriteInSaysSo(t *testing.T) {
	t.Parallel()

	m, _ := readOnlyRoom(t)
	if tag, _ := m.composerPrefix(); !strings.Contains(tag, "read-only here") {
		t.Errorf("composer gutter = %q; want it to say read-only here", tag)
	}

	file := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(file, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, typed := range map[string]string{
		"a message":   "hello all",
		"a scheduled": "/in 1h hello later",
		"a file":      "/upload " + file,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, backend := readOnlyRoom(t)
			m, _ = press(t, m, keyText("i"))
			m = typeInto(t, m, typed)
			m, cmd := press(t, m, sendKey())
			m = drain(t, m, cmd)
			if len(backend.sent) != 0 || backend.path != "" {
				t.Errorf("sent %v %q into a read-only room", backend.sent, backend.path)
			}
			if !strings.Contains(m.status(), "read-only here") {
				t.Errorf("status = %q; want it to say why", m.status())
			}
			if name == "a message" && m.compose.input != typed {
				t.Errorf("composer = %q; want the draft kept", m.compose.input)
			}
		})
	}
}
