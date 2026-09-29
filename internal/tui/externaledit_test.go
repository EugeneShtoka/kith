package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Opening the editor writes the draft to a file, but in a command, not in Update: the
// file appears only when the command runs, and holds the composer's text as it was.
func TestExternalEditWritesTheCurrentDraft(t *testing.T) {
	m := sized(t, withRooms(t, newModel()))
	m.openRoom = "!a:x"
	m = m.store(fieldComposer, newEditor("half a sentence"))
	m.conf.base.Composer = config.Composer{Editor: "true"}

	_, cmd := m.openExternalEditor()
	if cmd == nil {
		t.Fatal("no command produced")
	}
	msg, ok := cmd().(draftFileMsg)
	if !ok || msg.err != nil {
		t.Fatalf("the write came back %#v", msg)
	}
	t.Cleanup(func() { _ = os.Remove(msg.path) })
	if body, err := os.ReadFile(msg.path); err != nil || string(body) != "half a sentence" {
		t.Errorf("the draft file holds %q (%v), want the composer's text", body, err)
	}
	if _, run := m.handleDraftFile(msg); run == nil {
		t.Error("a written draft did not start the editor")
	}
}

// Nothing is written while Update runs: the file is the command's work.
func TestOpeningTheEditorDoesNoIOInUpdate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	m := sized(t, withRooms(t, newModel()))
	m = m.store(fieldComposer, newEditor("words"))
	if _, cmd := m.openExternalEditor(); cmd == nil {
		t.Fatal("no command produced")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("Update wrote %d file(s); the command should", len(entries))
	}
}

// A draft that cannot be written says so and runs no editor.
func TestAnUnwritableDraftRunsNoEditor(t *testing.T) {
	m := sized(t, withRooms(t, newModel()))
	next, cmd := m.handleDraftFile(draftFileMsg{err: os.ErrPermission})
	if cmd != nil {
		t.Error("an editor was started without a draft file")
	}
	if got := next.renderStatus(); !strings.Contains(got, "could not open an editor") {
		t.Errorf("status = %q, want the failure said", got)
	}
}

// The edited draft replaces the composer as one undo step.
func TestAnExternalEditCanBeUndone(t *testing.T) {
	m := sized(t, withRooms(t, newModel()))
	m.openRoom = "!a:x"
	m = m.store(fieldComposer, newEditor("before"))
	path := filepath.Join(t.TempDir(), "draft.md")
	if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	next, _ := m.handleExternalEdit(editorFinished(path)(nil).(externalEditMsg))
	undone, ok := next.editorFor(fieldComposer).undo()
	if !ok || undone.text != "before" {
		t.Errorf("undo after the editor gives %q (%v), want the draft as it was: %q", undone.text, ok, "before")
	}
}

func TestExternalEditTakesTheDraftBack(t *testing.T) {
	m := sized(t, withRooms(t, newModel()))
	m.openRoom = "!a:x"
	m = m.store(fieldComposer, newEditor("before"))

	path := filepath.Join(t.TempDir(), "draft.md")
	if err := os.WriteFile(path, []byte("first line\nsecond line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	next, _ := m.handleExternalEdit(editorFinished(path)(nil).(externalEditMsg))
	after := next

	if got := after.editorFor(fieldComposer).text; got != "first line\nsecond line" {
		t.Errorf("draft = %q, want the two lines with the trailing newline trimmed", got)
	}
	if !after.compose.insertMode {
		t.Error("editing should leave you in the composer")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the draft file was left behind")
	}
}

// A failed edit keeps the draft.
func TestExternalEditFailureKeepsTheDraft(t *testing.T) {
	for _, tc := range []struct{ name, why string }{
		{"editor-error", "exit"},
		{"unreadable", "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sized(t, withRooms(t, newModel()))
			m = m.store(fieldComposer, newEditor("precious words"))

			var editorErr error
			if tc.why == "exit" {
				editorErr = os.ErrPermission
			}
			msg := editorFinished(filepath.Join(t.TempDir(), "gone.md"))(editorErr).(externalEditMsg)
			next, _ := m.handleExternalEdit(msg)
			after := next
			if got := after.editorFor(fieldComposer).text; got != "precious words" {
				t.Errorf("draft = %q, want it untouched", got)
			}
			if !strings.Contains(after.status(), "unchanged") {
				t.Errorf("status = %q, want it to say the draft is unchanged", after.status())
			}
		})
	}
}

// Finishing an edit sends nothing.
func TestExternalEditSendsNothing(t *testing.T) {
	m := sized(t, withRooms(t, newModel()))
	m.openRoom = "!a:x"
	m = m.setMessages([]domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@bob:x", Body: "hi", Timestamp: at(1)},
	})
	before := len(m.timeline.messages)

	path := filepath.Join(t.TempDir(), "draft.md")
	if err := os.WriteFile(path, []byte("about to be sent? no\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	next, cmd := m.handleExternalEdit(editorFinished(path)(nil).(externalEditMsg))
	after := next
	if len(after.timeline.messages) != before {
		t.Error("finishing an edit added a message")
	}
	if cmd != nil {
		t.Error("finishing an edit produced a command; it should only set the draft")
	}
}

func TestEditorCommandResolution(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	if got := (config.Composer{}).EditorCommand(); got[0] != "vi" {
		t.Errorf("with nothing set: %v, want vi", got)
	}
	t.Setenv("EDITOR", "nano")
	if got := (config.Composer{}).EditorCommand(); got[0] != "nano" {
		t.Errorf("with $EDITOR: %v, want nano", got)
	}
	t.Setenv("VISUAL", "nvim")
	if got := (config.Composer{}).EditorCommand(); got[0] != "nvim" {
		t.Errorf("$VISUAL should win over $EDITOR: %v", got)
	}
	got := config.Composer{Editor: "code --wait"}.EditorCommand()
	if len(got) != 2 || got[0] != "code" || got[1] != "--wait" {
		t.Errorf("configured editor = %v, want [code --wait]", got)
	}
}

func TestFileSuffixDefault(t *testing.T) {
	cases := map[string]string{"": ".md", "txt": ".txt", ".markdown": ".markdown"}
	for in, want := range cases {
		if got := (config.Composer{FileSuffix: in}).FileSuffixOrDefault(); got != want {
			t.Errorf("suffix %q = %q, want %q", in, got, want)
		}
	}
}
