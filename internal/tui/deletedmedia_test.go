package tui

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/media"
)

// pictureBackend answers every attachment with the same bytes.
type pictureBackend struct {
	apitest.Nop
	bytes []byte
}

func (b *pictureBackend) LoadImage(context.Context, domain.RoomID, domain.EventID) ([]byte, error) {
	return b.bytes, nil
}

// picture is a message with a picture attached, deleted or not.
func picture(id domain.EventID, deleted bool) domain.Message {
	return domain.Message{
		ID: id, RoomID: "!a:x", Sender: "@dana:x", Timestamp: at(1), Redacted: deleted, Body: "solve this to stay",
		Media: &domain.Media{Type: domain.MediaImage, Name: "captcha.png", Mime: "image/png", Size: 4096},
	}
}

// withPictures is a model over a real media cache, its room holding a picture and a
// deleted one, the deleted one selected; keep is [display.deleted] keep.
func withPictures(t *testing.T, keep bool) (Model, *media.Cache) {
	t.Helper()
	display := config.Display{Deleted: config.Deleted{KeepDeleted: keep}}
	m := sized(t, withRooms(t, starterNew(&pictureBackend{bytes: []byte("the picture")}, display)))
	cache, err := media.New(t.TempDir(), -1)
	if err != nil {
		t.Fatal(err)
	}
	m = m.WithCache(cache)
	m.focus = paneTimeline
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{picture("$kept", false), picture("$gone", true)}}})
	m.timeline.selected = "$gone"
	return m, cache
}

// A deleted message's picture is not shown, fetched or offered: no chip, no picture
// rows, no inline load, not in the room's gallery; viewing or saving it from the
// timeline points to its history instead.
func TestADeletedPictureIsHiddenFromTheTimeline(t *testing.T) {
	t.Parallel()
	m, _ := withPictures(t, true)
	m.pics.graphics = graphicsBlocks
	m.pics.imageRows = map[domain.EventID][]string{"$gone": {"DRAWN"}, "$kept": {"DRAWN"}}
	gone := loadedMessage(t, m, "$gone")
	if rows := m.hangingRows(gone, nil, 10, 80); len(rows) != 0 {
		t.Errorf("a deleted picture hangs %q under its message", rows)
	}
	kept := loadedMessage(t, m, "$kept")
	if rows := m.hangingRows(kept, nil, 10, 80); len(rows) == 0 {
		t.Error("the picture still there is not drawn")
	}
	m.pics.imageRows = map[domain.EventID][]string{}
	loaded, _ := m.loadInlineImages()
	if loaded.pics.imageLoading["$gone"] {
		t.Error("a deleted picture was fetched to draw")
	}
	jobs, _ := m.gallery("")
	if slices.ContainsFunc(jobs, func(j mediaJob) bool { return j.eventID == "$gone" }) {
		t.Error("a deleted picture is in the room's gallery")
	}
	for name, act := range map[string]func(Model) Model{
		"view":     func(m Model) Model { next, _ := m.viewMedia(); return next },
		"download": func(m Model) Model { next, _ := m.download(); return next },
	} {
		said := act(m)
		if !strings.Contains(said.status(), "this message was deleted") {
			t.Errorf("%s on a deleted picture said %q, want its history pointed to", name, said.status())
		}
	}
}

// A deleted message's attachment is moved aside as soon as the deletion is seen, and
// its history opens it from there, at full size; one never cached is fetched there.
func TestADeletedPictureIsKeptAsideAndOpenedFromItsHistory(t *testing.T) {
	t.Parallel()
	m, cache := withPictures(t, true)
	if !m.pics.aside["$gone"] || m.pics.aside["$kept"] {
		t.Errorf("set aside on loading = %v, want just the deleted message's", m.pics.aside)
	}
	if _, err := cache.Write("$gone", "captcha.png", "image/png", []byte("the picture")); err != nil {
		t.Fatal(err)
	}
	m.pics.aside = nil // as before the deletion was seen
	next, cmd := m.setAsideDeleted()
	if cmd == nil {
		t.Fatal("the deleted attachment was not set aside")
	}
	cmd()
	if _, ok := cache.Read("$gone", "captcha.png", "image/png"); ok {
		t.Error("the deleted picture is still with the room's pictures")
	}
	if _, ok := cache.ReadAside("$gone", "captcha.png", "image/png"); !ok {
		t.Error("the deleted picture was not kept aside")
	}
	if _, again := next.setAsideDeleted(); again != nil {
		t.Error("the same attachment was set aside twice")
	}

	gone := loadedMessage(t, m, "$gone")
	job := m.jobFor(gone)
	if !job.deleted {
		t.Fatal("a deleted message's attachment is not marked deleted")
	}
	if path := ensureOnDisk(t.Context(), cache, (&pictureBackend{}).LoadImage, job); path != cache.AsidePath("$gone", "captcha.png", "image/png") {
		t.Errorf("viewing it opens %q, want the kept file", path)
	}
	late := picture("$late", true)
	if path := ensureOnDisk(t.Context(), cache, (&pictureBackend{bytes: []byte("fetched")}).LoadImage, m.jobFor(late)); path == "" {
		t.Error("a deleted picture never cached was not fetched")
	} else if data, _ := os.ReadFile(path); string(data) != "fetched" || path != cache.AsidePath("$late", "captcha.png", "image/png") {
		t.Errorf("fetched to %q", path)
	}

	m.history = historyState{open: true, msg: gone}
	if lines := strings.Join(m.historyLines(80, 20), "\n"); !strings.Contains(lines, "captcha.png") {
		t.Errorf("the history does not show what was attached:\n%s", lines)
	}
}

// loadedMessage is one of the timeline's messages, by ID.
func loadedMessage(t *testing.T, m Model, id domain.EventID) domain.Message {
	t.Helper()
	i := indexOfMessage(m.timeline.messages, id)
	if i < 0 {
		t.Fatalf("%s is not loaded", id)
	}
	return m.timeline.messages[i]
}

// Without [display.deleted] keep, a deletion erases the message, and its attachment's
// bytes and drawings in the media cache with it.
func TestADeletedAttachmentIsErasedWhenDeletionsAreNotKept(t *testing.T) {
	t.Parallel()
	m, cache := withPictures(t, false)
	if _, err := cache.Write("$gone", "captcha.png", "image/png", []byte("the picture")); err != nil {
		t.Fatal(err)
	}
	if err := cache.PutRender("$gone", 10, 5, []byte("drawn")); err != nil {
		t.Fatal(err)
	}
	m.pics.aside = nil
	gone := loadedMessage(t, m, "$gone")
	gone.Media = nil // the erased message no longer says what it had attached
	m.timeline.messages[indexOfMessage(m.timeline.messages, "$gone")] = gone
	_, cmd := m.setAsideDeleted()
	if cmd == nil {
		t.Fatal("the deleted attachment was not erased")
	}
	cmd()
	_, cached := cache.Read("$gone", "captcha.png", "image/png")
	_, aside := cache.ReadAside("$gone", "captcha.png", "image/png")
	_, drawn := cache.Render("$gone", 10, 5)
	if cached || aside || drawn {
		t.Errorf("kept: bytes %v, aside %v, drawing %v; want none", cached, aside, drawn)
	}
}
