package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A room can opt out of auto-fetching pictures.
func TestARoomCanRefuseToFetchPicturesUnasked(t *testing.T) {
	t.Parallel()

	disp := config.Display{Media: config.Media{
		Mode:  "inline",
		Rules: []config.MediaRule{{Match: mx + "!a:x", Auto: new(false)}},
	}}
	m := sized(t, withRooms(t, starterNew(apitest.Nop{}, disp)))
	m.focus = paneTimeline
	m = update(t, m, oneImage())
	if len(m.pics.imageLoading) != 0 {
		t.Errorf("a room with auto = false fetched %d picture(s)", len(m.pics.imageLoading))
	}
	// The chip still says what was not fetched.
	if rows := strings.Join(m.layoutRows(), "\n"); !strings.Contains(rows, "cat.jpg") {
		t.Errorf("the attachment is not named anywhere:\n%s", rows)
	}
}

// A room rule beats a space rule, which beats the global setting (same precedence as save
// locations).
func TestMediaRulesResolveNarrowestFirst(t *testing.T) {
	t.Parallel()

	rules := []domain.MediaRule{
		{Match: "space:Work", Auto: new(false), Cache: new(false)},
		{Match: mx + "!room:x", Auto: new(true)},
	}
	for _, tt := range []struct {
		name        string
		place       domain.DownloadPlace
		auto, cache bool
	}{
		{
			name:  "the room's own rule wins, and inherits what it does not mention",
			place: domain.DownloadPlace{RoomID: mx + "!room:x", Space: "Work"},
			auto:  true, cache: true,
		},
		{
			name:  "a room with no rule of its own follows its space",
			place: domain.DownloadPlace{RoomID: mx + "!other:x", Space: "Work"},
			auto:  false, cache: false,
		},
		{
			name:  "a room in no named space follows the settings above",
			place: domain.DownloadPlace{RoomID: mx + "!loose:x", Space: "Social"},
			auto:  true, cache: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := domain.ResolveMedia(domain.MediaPolicy{Auto: true, Cache: true, Speed: 1}, rules, tt.place)
			if got.Auto != tt.auto || got.Cache != tt.cache {
				t.Errorf("ResolveMedia = %+v, want auto=%v cache=%v", got, tt.auto, tt.cache)
			}
		})
	}
}

// The viewer gets the picture under the cursor first and older ones behind it, so "next" walks
// back through history.
func TestViewingListsThePictureUnderTheCursorFirst(t *testing.T) {
	t.Parallel()

	m := inlineModel(t)
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{
		Messages: []domain.Message{
			img(mx+"$1", 1), img(mx+"$2", 2),
			{ID: mx + "$3", RoomID: mx + "!a:x", Sender: "matrix:@a:x", Timestamp: at(3), Body: "just words"},
			img(mx+"$4", 4),
		},
	}})

	// The gallery is the room's pictures in send order, both directions from the opened one.
	jobs, start := m.gallery(mx + "$4")
	if got := ids(jobs); strings.Join(got, ",") != mx+"$1,"+mx+"$2,"+mx+"$4" {
		t.Errorf("the gallery listed %v, want the pictures in the order they were sent", got)
	}
	if start != 2 {
		t.Errorf("start = %d, want the gallery to open on $4", start)
	}
	// From the middle, later pictures are reachable forward.
	if _, at := m.gallery(mx + "$2"); at != 1 {
		t.Errorf("start = %d, want the gallery to open on $2", at)
	}
	// A message with no picture opens the newest.
	if _, at := m.gallery(mx + "$3"); at != 2 {
		t.Errorf("start = %d from a text message, want the newest picture", at)
	}
	// A room with no pictures at all has nothing to view.
	empty := inlineModel(t)
	if got, _ := empty.gallery(""); len(got) != 0 {
		t.Errorf("a room with no pictures offered %d to view", len(got))
	}
}

// Every picture carries the room's caching rule, so a no-cache room is honored when viewing.
func TestViewingCarriesTheRoomsCachingRule(t *testing.T) {
	t.Parallel()

	disp := config.Display{Media: config.Media{
		Mode:  "inline",
		Rules: []config.MediaRule{{Match: mx + "!a:x", Cache: new(false)}},
	}}
	m := sized(t, withRooms(t, starterNew(apitest.Nop{}, disp)))
	m.focus = paneTimeline
	m = update(t, m, oneImage())
	jobs, _ := m.gallery("")
	if len(jobs) != 1 {
		t.Fatalf("%d pictures to view, want 1", len(jobs))
	}
	if jobs[0].cache {
		t.Error("a room that forbids caching still asked for its picture to be kept")
	}
}

func img(id domain.EventID, minute int) domain.Message {
	return domain.Message{
		ID: id, RoomID: mx + "!a:x", Sender: "matrix:@a:x", Timestamp: at(minute),
		Media: &domain.Media{Type: domain.MediaImage, Name: string(id) + ".jpg", Mime: "image/jpeg", Width: 40, Height: 40},
	}
}

func ids(jobs []mediaJob) []string {
	out := make([]string, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, string(j.eventID))
	}
	return out
}

// Picture size follows the window.
func TestPictureSizeFollowsTheWindow(t *testing.T) {
	t.Parallel()

	m := inlineModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 200, Height: 60})
	tall, tallW := m.imageMaxHeight(), m.imageTargetWidth()

	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 24})
	short, shortW := m.imageMaxHeight(), m.imageTargetWidth()

	if tall <= short {
		t.Errorf("a taller window allowed %d rows and a shorter one %d; want the taller to allow more", tall, short)
	}
	if tallW <= shortW {
		t.Errorf("a wider window allowed %d columns and a narrower one %d", tallW, shortW)
	}
	// Never so small that a picture stops being one.
	if short < minImageCells || shortW < minImageCells {
		t.Errorf("a small window gave a %dx%d picture, below the %d floor", shortW, short, minImageCells)
	}
	// Never so tall that it hides the conversation.
	m = update(t, m, tea.WindowSizeMsg{Width: 300, Height: 200})
	if got := m.imageMaxHeight(); got > maxAutoImageRows {
		t.Errorf("a very tall window allowed %d rows, past the %d ceiling", got, maxAutoImageRows)
	}
}

// A configured size is taken as written.
func TestConfiguredPictureSizeWins(t *testing.T) {
	t.Parallel()

	disp := config.Display{Media: config.Media{Mode: "inline", MaxHeight: 5, MaxWidth: 11}}
	m := sized(t, withRooms(t, starterNew(apitest.Nop{}, disp)))
	m = update(t, m, tea.WindowSizeMsg{Width: 200, Height: 60})
	if got := m.imageMaxHeight(); got != 5 {
		t.Errorf("max_height = %d, want the configured 5", got)
	}
	if got := m.imageTargetWidth(); got != 11 {
		t.Errorf("max_width = %d, want the configured 11", got)
	}
}

// Auto-detected viewers are told to fit pictures to the window, not open at 1:1.
func TestAutoDetectedViewersAreToldToFit(t *testing.T) {
	t.Parallel()

	for _, candidate := range viewerCandidates {
		if candidate[0] == "feh" && strings.Join(candidate[1:], " ") != "--scale-down" {
			t.Errorf("feh is run as %v, want it told to scale down to the window", candidate)
		}
		if len(candidate[0]) == 0 {
			t.Error("a viewer candidate has no program to run")
		}
	}
}

// A configured viewer replaces the whole command, flags and all.
func TestConfiguredViewerIsTakenAsWritten(t *testing.T) {
	t.Parallel()

	disp := config.Display{Media: config.Media{Viewer: "sh -c true"}}
	m := starterNew(apitest.Nop{}, disp)
	got, err := m.viewerCommand()
	if err != nil {
		t.Fatalf("viewerCommand: %v", err)
	}
	if strings.Join(got, " ") != "sh -c true" {
		t.Errorf("viewerCommand = %v, want the configured command with its flags", got)
	}
}

// From the room list there is no message cursor, so viewing starts at the newest picture.
func TestViewingFromTheRoomListStartsAtTheNewest(t *testing.T) {
	t.Parallel()

	m := inlineModel(t)
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{
		Messages: []domain.Message{img(mx+"$1", 1), img(mx+"$2", 2), img(mx+"$3", 3)},
	}})
	m.focus = paneRooms
	m.timeline.selected = mx + "$1" // a stale cursor from whenever the room was last read
	jobs, start := m.gallery("")
	if got := ids(jobs); strings.Join(got, ",") != mx+"$1,"+mx+"$2,"+mx+"$3" {
		t.Errorf("viewing from the room list listed %v, want the room's pictures in order", got)
	}
	if start != 2 {
		t.Errorf("start = %d, want the newest picture when no message is pointed at", start)
	}
}
