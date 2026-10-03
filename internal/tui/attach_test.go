package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/filedialog"
)

// sendingBackend records what was asked of it, so a test can assert on the *path* the
// daemon would receive rather than on a status line.
type sendingBackend struct {
	apitest.Nop
	roomID  domain.RoomID
	path    string
	caption string
	media   []byte
	err     error
	// sent records the drafts that went out, for the tests about what a typed command
	// turns into.
	sent []domain.Draft
}

func (b *sendingBackend) Send(_ context.Context, roomID domain.RoomID, draft domain.Draft) error {
	b.roomID = roomID
	b.sent = append(b.sent, draft)
	return b.err
}

func (b *sendingBackend) SendFile(_ context.Context, roomID domain.RoomID, path, caption string) error {
	b.roomID, b.path, b.caption = roomID, path, caption
	return b.err
}

func (b *sendingBackend) LoadImage(context.Context, domain.RoomID, domain.EventID) ([]byte, error) {
	return b.media, b.err
}

// attaching gives a model in a room, with a fake backend to send through.
func attaching(t *testing.T) (Model, *sendingBackend) {
	t.Helper()
	backend := &sendingBackend{}
	m := sized(t, withRooms(t, starterNew(backend, config.Display{})))
	next, _ := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m = next
	m.focus, m.compose.insertMode = paneTimeline, false
	m = m.clearStatus()
	return m, backend
}

// The client resolves the path, because it is the process with the user's home and
// working directory — the daemon has neither and refuses anything relative.
func TestAttachSendsAnAbsolutePath(t *testing.T) {
	t.Parallel()

	m, backend := attaching(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "invoice.pdf")
	if err := os.WriteFile(file, []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// No portal in a test, so the key falls through to the typed prompt — which is also
	// the SSH path, and the one that understands "path | caption".
	m, _ = press(t, m, keyText("i")) // attaching is the composer's job now
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModAlt})
	m, _ = updateWith(t, m, fileChosenMsg{err: filedialog.ErrUnavailable})
	if m.prompt.kind != promptAttach {
		t.Fatalf("with no chooser to ask, A should leave a typed prompt, got %v", m.prompt.kind)
	}
	m = typeIn(t, m, file+" | here you go")

	if backend.path != file {
		t.Errorf("path = %q, want the absolute path", backend.path)
	}
	if !filepath.IsAbs(backend.path) {
		t.Error("the daemon only accepts an absolute path")
	}
	if backend.caption != "here you go" {
		t.Errorf("caption = %q, want the text after the pipe", backend.caption)
	}
	if backend.roomID != "!a:x" {
		t.Errorf("room = %q, want the open one", backend.roomID)
	}
	if !strings.Contains(m.status(), "invoice.pdf") {
		t.Errorf("status = %q, should name the file", m.status())
	}
}

// A file that is not there is refused *before* the upload, with the reason, rather than
// as a failed RPC after it.
func TestAttachRefusesWhatIsNotThere(t *testing.T) {
	t.Parallel()

	m, backend := attaching(t)
	m, _ = press(t, m, keyText("i")) // attaching is the composer's job now
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModAlt})
	m, _ = updateWith(t, m, fileChosenMsg{err: filedialog.ErrUnavailable})
	m = typeIn(t, m, filepath.Join(t.TempDir(), "nope.png"))

	if backend.path != "" {
		t.Errorf("nothing should have been sent, got %q", backend.path)
	}
	if !strings.Contains(m.status(), "nope.png") || !strings.Contains(m.status(), "no such file") {
		t.Errorf("status = %q, should name the file and say why", m.status())
	}
}

// A directory is refused the same way, and by name: "upload failed" would leave you
// wondering whether the network or the path was the problem.
func TestAttachRefusesADirectory(t *testing.T) {
	t.Parallel()

	m, backend := attaching(t)
	m, _ = press(t, m, keyText("i")) // attaching is the composer's job now
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModAlt})
	m, _ = updateWith(t, m, fileChosenMsg{err: filedialog.ErrUnavailable})
	m = typeIn(t, m, t.TempDir())

	if backend.path != "" {
		t.Errorf("a directory was handed to the daemon: %q", backend.path)
	}
	if !strings.Contains(m.status(), "directory") {
		t.Errorf("status = %q, should say it is a directory", m.status())
	}
}

// Saving is a key on the message, not a command with a target: what you are looking at
// is what gets saved.
func TestDownloadSavesTheSelectedAttachment(t *testing.T) {
	t.Parallel()

	m, backend := attaching(t)
	backend.media = []byte("PNG-ish bytes")
	dir := t.TempDir()
	m.prefs.display.Media.DownloadDir = dir
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{{
		ID: "$1", RoomID: "!a:x", Sender: "@alice:x", SenderName: "Alice", Body: "photo.png",
		Media: &domain.Media{Type: domain.MediaImage, Name: "photo.png", Mime: "image/png"},
	}}}})

	m, cmd := press(t, m, keyText("s"))
	m = drain(t, m, cmd)

	saved := filepath.Join(dir, "photo.png")
	data, err := os.ReadFile(saved)
	if err != nil {
		t.Fatalf("nothing was saved: %v", err)
	}
	if string(data) != "PNG-ish bytes" {
		t.Errorf("saved %q, want the attachment's bytes", data)
	}
	if !strings.Contains(m.status(), saved) {
		t.Errorf("status = %q, should say where it went — that is the point of the key", m.status())
	}
}

// A second file of the same name does not replace the first.
func TestDownloadNeverOverwrites(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := writeDownload(dir, "photo.png", []byte("first")); err != nil {
		t.Fatal(err)
	}
	second, err := writeDownload(dir, "photo.png", []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(second) != "photo (2).png" {
		t.Errorf("second copy = %q, want it numbered", filepath.Base(second))
	}
	first, err := os.ReadFile(filepath.Join(dir, "photo.png"))
	if err != nil || string(first) != "first" {
		t.Errorf("the first file is %q, %v — it should be untouched", first, err)
	}
}

// The name comes off a message a stranger wrote, so it names a file in the download
// directory and nothing else.
func TestDownloadNameCannotEscapeTheDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path, err := writeDownload(dir, "../../.ssh/authorized_keys", []byte("nope"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("wrote to %q, want it inside %q", path, dir)
	}
	if filepath.Base(path) != "authorized_keys" {
		t.Errorf("name = %q, want only the last element", filepath.Base(path))
	}
}

// The key says so when there is nothing to save, rather than doing nothing.
func TestDownloadWithoutAnAttachment(t *testing.T) {
	t.Parallel()

	m, _ := attaching(t)
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@alice:x", SenderName: "Alice", Body: "just text"},
	}}})
	m, _ = press(t, m, keyText("s"))
	if !strings.Contains(m.status(), "no attachment") {
		t.Errorf("status = %q, should say there is nothing to save", m.status())
	}
	if m.hasAttachment() {
		t.Error("hasAttachment should be false, so the legend does not offer the key")
	}
}

// Attaching is an editing action, so it works from inside the composer — and what you
// have typed goes with the file as its caption.
func TestAttachFromComposerUsesTypedTextAsCaption(t *testing.T) {
	t.Parallel()

	m, backend := attaching(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "plan.pdf")
	if err := os.WriteFile(file, []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m, _ = press(t, m, keyText("i")) // start composing
	if !m.compose.insertMode {
		t.Fatal("i should start composing")
	}
	m = typeInto(t, m, "here is the plan")
	m, cmd := press(t, m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModAlt})
	if cmd == nil {
		t.Fatal("alt+u should ask the desktop for a file without leaving the composer")
	}
	if !m.compose.insertMode {
		t.Error("attaching is an editing action: it must not drop you out of the composer")
	}
	// The chooser answers with the file.
	m, cmd = updateWith(t, m, fileChosenMsg{path: file})
	m = drain(t, m, cmd)

	if backend.caption != "here is the plan" {
		t.Errorf("caption = %q, want what was typed in the composer", backend.caption)
	}
	if backend.path != file {
		t.Errorf("path = %q, want the file", backend.path)
	}
	if m.compose.input != "" {
		t.Errorf("composer still holds %q — the words went with the file and would be sent twice", m.compose.input)
	}
}

// The typed spelling, for when the path is already on the clipboard: paste, enter.
func TestUploadCommand(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(file, []byte("\x89PNG\r\n\x1a\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, typed := range map[string]string{
		"space separator": "/upload " + file,
		"colon separator": "/upload:" + file,
		"with a caption":  "/upload " + file + " | look",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, backend := attaching(t)
			m, _ = press(t, m, keyText("i"))
			m = typeInto(t, m, typed)
			m, cmd := press(t, m, sendKey())
			m = drain(t, m, cmd)

			if backend.path != file {
				t.Errorf("path = %q, want the command's argument", backend.path)
			}
			if strings.Contains(name, "caption") && backend.caption != "look" {
				t.Errorf("caption = %q, want the text after the pipe", backend.caption)
			}
			if m.compose.input != "" {
				t.Errorf("composer still holds %q", m.compose.input)
			}
		})
	}
}

// Only known commands are intercepted: a message that starts with a slash is a message,
// and `//` escapes one for the case where it starts with a real command.
func TestSlashTextIsStillAMessage(t *testing.T) {
	t.Parallel()

	m, backend := attaching(t)
	m, _ = press(t, m, keyText("i"))
	m = typeInto(t, m, "/shrug")
	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	drain(t, m, cmd)
	if backend.path != "" {
		t.Errorf("an unknown command should not have uploaded %q", backend.path)
	}

	m, backend = attaching(t)
	m, _ = press(t, m, keyText("i"))
	m = typeInto(t, m, "//upload is how you attach a file")
	m, cmd = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	drain(t, m, cmd)
	if backend.path != "" {
		t.Error("an escaped command must not run")
	}
}

// A command with no path says what it needs rather than sending itself as a message.
func TestUploadCommandWithoutAPath(t *testing.T) {
	t.Parallel()

	m, backend := attaching(t)
	m, _ = press(t, m, keyText("i"))
	m = typeInto(t, m, "/upload")
	m, cmd := press(t, m, sendKey())
	m = drain(t, m, cmd)
	if backend.path != "" {
		t.Error("nothing should have been sent")
	}
	// The usage line, spelled the way you would type it — every command answers a
	// missing argument the same way (see slashCommands), which is more use than
	// "missing argument" and shorter than guessing.
	if !strings.Contains(m.status(), "/upload <path") {
		t.Errorf("status = %q, should say what it wants", m.status())
	}
}

// updateWith feeds a message in and keeps the command, which the folder/file chooser
// replies need — they are answered off the event loop and then act.
func updateWith(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := asModel(m.Update(msg))
	got := next
	return got, cmd
}

// The template makes folders, and the folders are the ones a person recognizes: the
// space, the room, who sent it.
func TestDownloadFollowsTheTemplate(t *testing.T) {
	t.Parallel()

	m, backend := attaching(t)
	backend.media = []byte("bytes")
	root := t.TempDir()
	m.prefs.display.Media.DownloadDir = root
	m.prefs.display.Media.DownloadTemplate = "{room}/{person}/{name}{ext}"
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x"}},
	}})
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{{
		ID: "$1", RoomID: "!a:x", Sender: "@alice:x", SenderName: "Alice", Body: "photo.png",
		Media: &domain.Media{Type: domain.MediaImage, Name: "photo.png", Mime: "image/png"},
	}}}})

	m, cmd := press(t, m, keyText("s"))
	m = drain(t, m, cmd)

	want := filepath.Join(root, "Alpha", "Alice", "photo.png")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("wanted %s: %v (status: %s)", want, err, m.status())
	}
}

// A per-room rule beats the settings, so "everything from this room goes there" is one
// entry rather than a setting you keep changing.
func TestDownloadRuleOverridesTheDirectory(t *testing.T) {
	t.Parallel()

	m, backend := attaching(t)
	backend.media = []byte("bytes")
	root, elsewhere := t.TempDir(), t.TempDir()
	m.prefs.display.Media.DownloadDir = root
	m.prefs.display.Media.Rules = []config.MediaRule{{Match: "!a:x", Dir: elsewhere}}
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{{
		ID: "$1", RoomID: "!a:x", Sender: "@alice:x", SenderName: "Alice", Body: "doc.pdf",
		Media: &domain.Media{Type: domain.MediaFile, Name: "doc.pdf", Mime: "application/pdf"},
	}}}})

	m, cmd := press(t, m, keyText("s"))
	m = drain(t, m, cmd)

	if _, err := os.Stat(filepath.Join(elsewhere, "doc.pdf")); err != nil {
		t.Errorf("the rule's directory should have it: %v (status: %s)", err, m.status())
	}
}

// Save-as asks the **desktop** for a folder — the XDG portal, the same interface
// Firefox asks through — rather than drawing a file browser of our own.
func TestSaveAsRemembersWhatIsBeingSaved(t *testing.T) {
	t.Parallel()

	m, backend := attaching(t)
	backend.media = []byte("bytes")
	root := t.TempDir()
	m.prefs.display.Media.DownloadDir = root
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{{
		ID: "$1", RoomID: "!a:x", Sender: "@alice:x", SenderName: "Alice", Body: "bill.pdf",
		Media: &domain.Media{Type: domain.MediaFile, Name: "bill.pdf", Mime: "application/pdf"},
	}}}})

	m, _ = press(t, m, keyText("S"))
	if m.pendingSave.msg.ID != "$1" || m.pendingSave.target.Name != "bill.pdf" {
		t.Fatalf("S left %+v pending, want the attachment under the cursor", m.pendingSave.target)
	}

	// The chooser answers.
	chosen := filepath.Join(root, "invoices")
	if err := os.Mkdir(chosen, 0o700); err != nil {
		t.Fatal(err)
	}
	m, cmd := updateWith(t, m, folderChosenMsg{dir: chosen})
	m = drain(t, m, cmd)

	if _, err := os.Stat(filepath.Join(chosen, "bill.pdf")); err != nil {
		t.Errorf("the file should be in the chosen folder: %v (status: %s)", err, m.status())
	}
	if m.pendingSave.msg.ID != "" {
		t.Error("the pending save should be cleared once it has happened")
	}
}

// Changing your mind in the chooser saves nothing and says nothing.
func TestSaveAsCanceled(t *testing.T) {
	t.Parallel()

	m, _ := attaching(t)
	m.prefs.display.Media.DownloadDir = t.TempDir()
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{{
		ID: "$1", RoomID: "!a:x", Sender: "@alice:x", SenderName: "Alice", Body: "bill.pdf",
		Media: &domain.Media{Type: domain.MediaFile, Name: "bill.pdf", Mime: "application/pdf"},
	}}}})
	m, _ = press(t, m, keyText("S"))
	m, cmd := updateWith(t, m, folderChosenMsg{err: filedialog.ErrCanceled})
	drain(t, m, cmd)

	if m.pendingSave.msg.ID != "" || m.pendingSave.target.Name != "" {
		t.Errorf("a canceled save left %+v pending", m.pendingSave.target)
	}
	if m.status() != "" {
		t.Errorf("status = %q, want nothing said about a change of mind", m.status())
	}
}

// With no portal to ask, attaching falls back to typing a path — which is the only
// thing that works over SSH, where there is no desktop at all.
func TestAttachFallsBackToTypingWithoutAPortal(t *testing.T) {
	t.Parallel()

	m, _ := attaching(t)
	m, _ = press(t, m, keyText("i")) // attaching is the composer's job now
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModAlt})
	m, _ = updateWith(t, m, fileChosenMsg{err: filedialog.ErrUnavailable})
	if m.prompt.kind != promptAttach {
		t.Fatalf("no portal should leave a typed prompt, got %v", m.prompt.kind)
	}
	if !strings.Contains(m.status(), "type a path") {
		t.Errorf("status = %q, should say why it is asking", m.status())
	}
}

// What the chooser answers is sent, with whatever is in the composer as the caption —
// read when the answer arrives, because you may have typed it while the chooser was up.
func TestChosenFileIsSentWithTheComposerAsCaption(t *testing.T) {
	t.Parallel()

	m, backend := attaching(t)
	file := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(file, []byte("\x89PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, _ = press(t, m, keyText("i"))
	m = typeInto(t, m, "from the chooser")
	m, cmd := updateWith(t, m, fileChosenMsg{path: file})
	m = drain(t, m, cmd)

	if backend.path != file {
		t.Errorf("path = %q, want what the chooser answered", backend.path)
	}
	if backend.caption != "from the chooser" {
		t.Errorf("caption = %q, want the composer's text", backend.caption)
	}
	if m.compose.input != "" {
		t.Errorf("composer still holds %q", m.compose.input)
	}
}

// ctrl+u belongs to the line editor — "delete back to the start of the line", a
// readline key old enough that taking it away from a text field is a bug however well
// the replacement is documented.
func TestCtrlUClearsTheLineRatherThanAttaching(t *testing.T) {
	t.Parallel()

	m, _ := attaching(t)
	m, _ = press(t, m, keyText("i"))
	m = typeInto(t, m, "half a thought")

	// A command comes back either way — typing tells the room you are typing — so what
	// says the chooser did *not* open is the status line it would have set.
	cleared, _ := press(t, m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if strings.Contains(cleared.status(), "choosing a file") {
		t.Error("ctrl+u opened the file chooser; it should only clear the line")
	}
	if got := cleared.editorFor(fieldComposer).text; got != "" {
		t.Errorf("composer holds %q after ctrl+u, want it cleared back to the start", got)
	}
	if !cleared.compose.insertMode {
		t.Error("ctrl+u dropped out of the composer")
	}
}

// A file refused for want of encryption says why, not the error chain.
func TestAttachRefusedWithoutEncryptionSaysWhy(t *testing.T) {
	t.Parallel()
	m, _ := attaching(t)

	next, _ := m.handleAttachSent(attachSentMsg{
		name: "plan.pdf", err: fmt.Errorf("matrix: !a:x is encrypted: %w", api.ErrNoEncryption),
	})
	got := next.renderStatus()
	if !strings.Contains(got, "encryption is not running") || strings.Contains(got, "matrix:") {
		t.Errorf("status = %q, want the plain reason", got)
	}
}
