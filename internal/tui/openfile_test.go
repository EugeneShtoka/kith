package tui

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// One key for every attachment, which is the shape the question has: standing on a
// message, "open this" is one gesture, and which program answers it is a fact about the
// file rather than a decision for the reader's fingers.
func TestTheOpenKeyDispatchesOnWhatIsAttached(t *testing.T) {
	t.Parallel()

	withMedia := func(kind domain.MediaType) Model {
		m := sized(t, withRooms(t, starterNew(apitest.Nop{}, config.Display{})))
		next, _ := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
		m = next
		m = m.setMessages([]domain.Message{{
			ID: "$m", RoomID: "!a:x", Sender: "@her:x", Timestamp: at(1),
			Media: &domain.Media{Type: kind, Name: "thing", Mime: "application/pdf"},
		}})
		m.timeline.selected, m.focus = "$m", paneTimeline
		return m.clearStatus()
	}

	// A file is the kind that had no key at all before: it goes to the desktop.
	next, cmd := withMedia(domain.MediaFile).viewMedia()
	m := next
	if cmd == nil {
		t.Error("opening a file asked for nothing")
	}
	if !strings.Contains(m.status(), "opening") {
		t.Errorf("status = %q, want it to say the file is being opened", m.status())
	}

	// A voice note belongs to the play key alone — it is the one attachment with a
	// player *inside* the client — so this says which key plays it rather than quietly
	// starting it.
	next, cmd = withMedia(domain.MediaAudio).viewMedia()
	voice := next
	if cmd != nil {
		t.Error("the open key started something for a voice note")
	}
	if !strings.Contains(voice.status(), "voice note") || !strings.Contains(voice.status(), "p ") {
		t.Errorf("status = %q, want it to point at the play key", voice.status())
	}

	// A message carrying nothing says so rather than opening the room's pictures.
	bare := withMedia(domain.MediaFile)
	bare = bare.setMessages([]domain.Message{{
		ID: "$m", RoomID: "!a:x", Sender: "@her:x", Body: "just words", Timestamp: at(1),
	}})
	next, _ = bare.viewMedia()
	said := next
	if !strings.Contains(said.status(), "nothing attached") {
		t.Errorf("status = %q, want it to say there is nothing attached", said.status())
	}
}

// Where the gallery opens is told to the viewers that can be told, and arranged for the
// ones that cannot — which is what keeps "forward" reaching the pictures sent after the
// one you opened.
func TestTheViewerIsToldWhereToStart(t *testing.T) {
	t.Parallel()

	files := []string{"a.png", "b.png", "c.png", "d.png"}

	command, out := viewerStart([]string{"nsxiv", "-s", "f"}, false, files, 2)
	if strings.Join(command, " ") != "nsxiv -s f -n 3" {
		t.Errorf("nsxiv = %v, want it told to start at the third file", command)
	}
	if strings.Join(out, ",") != "a.png,b.png,c.png,d.png" {
		t.Errorf("the list was reordered for a viewer that can count: %v", out)
	}

	command, out = viewerStart([]string{"feh", "--scale-down"}, false, files, 1)
	if strings.Join(command, " ") != "feh --scale-down --start-at b.png" {
		t.Errorf("feh = %v, want it told which file to start at", command)
	}
	if strings.Join(out, ",") != "a.png,b.png,c.png,d.png" {
		t.Errorf("feh's list was reordered: %v", out)
	}

	// A viewer that cannot be told gets the list rotated instead, so the chosen picture
	// is first and forward still reaches everything.
	command, out = viewerStart([]string{"eog"}, false, files, 2)
	if len(command) != 1 {
		t.Errorf("eog = %v, want no invented flag", command)
	}
	if strings.Join(out, ",") != "c.png,d.png,a.png,b.png" {
		t.Errorf("rotated list = %v, want the chosen picture first and the rest behind it", out)
	}

	// Opening on the first picture needs neither.
	if command, out := viewerStart([]string{"eog"}, false, files, 0); len(command) != 1 || out[0] != "a.png" {
		t.Errorf("start 0 changed something: %v %v", command, out)
	}
}

// A viewer the user configured is run as written: no flag is added that it may not
// understand, even when its name is one we know. It is told where to start only through
// a placeholder it was given; without one the list is rotated instead.
func TestAConfiguredViewerGetsNoInventedFlag(t *testing.T) {
	t.Parallel()

	files := []string{"a.png", "b.png", "c.png", "d.png"}

	for _, configured := range [][]string{{"nsxiv", "-b"}, {"feh"}, {"imv"}, {"my-viewer"}} {
		command, out := viewerStart(configured, true, files, 2)
		if strings.Join(command, " ") != strings.Join(configured, " ") {
			t.Errorf("configured %v became %v, want it run as written", configured, command)
		}
		if strings.Join(out, ",") != "c.png,d.png,a.png,b.png" {
			t.Errorf("configured %v got %v, want the chosen picture first", configured, out)
		}
	}

	command, out := viewerStart([]string{"feh", "--scale-down", "--start-at", "{file}"}, true, files, 1)
	if strings.Join(command, " ") != "feh --scale-down --start-at b.png" {
		t.Errorf("{file} = %v, want the chosen picture's path", command)
	}
	if strings.Join(out, ",") != "a.png,b.png,c.png,d.png" {
		t.Errorf("a placeholder viewer's list was reordered: %v", out)
	}

	command, _ = viewerStart([]string{"nsxiv", "-n", "{index}"}, true, files, 2)
	if strings.Join(command, " ") != "nsxiv -n 3" {
		t.Errorf("{index} = %v, want the 1-based position", command)
	}
	// Inside a word too, and at the first picture, where nothing else would be filled in.
	command, _ = viewerStart([]string{"v", "--at={index}"}, true, files, 0)
	if strings.Join(command, " ") != "v --at=1" {
		t.Errorf("{index} at the start = %v, want it filled in anyway", command)
	}
}

// The window around a long history keeps both sides of the picture you opened rather
// than the last N of the room.
func TestTheGalleryWindowIsCentered(t *testing.T) {
	t.Parallel()

	jobs := make([]mediaJob, 100)
	for i := range jobs {
		jobs[i] = mediaJob{eventID: domain.EventID(string(rune('a' + i%26)))}
	}
	out, start := trimAround(jobs, 50, 10)
	if len(out) != 10 {
		t.Fatalf("kept %d, want 10", len(out))
	}
	if start != 5 {
		t.Errorf("start = %d, want the chosen picture in the middle", start)
	}
	// Near the ends the window slides rather than running off them.
	if _, at := trimAround(jobs, 2, 10); at != 2 {
		t.Errorf("start = %d near the beginning, want it kept where it is", at)
	}
	if _, at := trimAround(jobs, 98, 10); at != 8 {
		t.Errorf("start = %d near the end, want the last window", at)
	}
}
