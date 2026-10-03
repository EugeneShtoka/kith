package tui

import (
	"strings"
	"testing"

	"golang.org/x/text/unicode/bidi"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// fileHit is a search hit that carries an attachment, which is the only thing that
// separates the file list's rows from any other search result.
func fileHit(room domain.RoomID, event domain.EventID, name, file, caption string, agoHours int) domain.SearchHit {
	h := hit(room, event, name, caption, agoHours)
	h.FileName = file
	return h
}

func browsingFiles(t *testing.T, hits ...domain.SearchHit) (Model, *searchBackend) {
	t.Helper()
	backend := &searchBackend{hits: hits}
	m := update(t, sized(t, starterNew(backend, config.Display{})),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}, {ID: "!b:x", Name: "Bravo"}}})
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	// Standing in the room, which is what makes "this room" the honest default scope —
	// from the rail the question is about a space, and openFiles says so.
	m.focus = paneTimeline
	return m.clearStatus(), backend
}

// gf opens the list with no terms typed: the clause is the whole question, so the
// answer is on screen before anything is asked.
func TestFilesKeyOpensTheListOnThisRoom(t *testing.T) {
	t.Parallel()

	m, backend := browsingFiles(t,
		fileHit("!a:x", "$2", "dana", "report.pdf", "report.pdf", 1),
		fileHit("!a:x", "$1", "sam", "photo.png", "photo.png", 30),
	)
	m, cmd := chord(t, m, "g", "f")
	m = deliver(t, m, cmd)

	if !m.search.active || m.search.showing() != (filesList{}) {
		t.Fatal("gf did not open the file list")
	}
	if m.prompt.kind != promptFiles {
		t.Errorf("prompt = %d, want the files prompt", m.prompt.kind)
	}
	if got := m.prompt.label(); !strings.Contains(got, "narrow") {
		t.Errorf("label = %q, should say typing narrows the list", got)
	}
	if len(backend.requests) == 0 {
		t.Fatal("no search was issued")
	}
	req := backend.requests[len(backend.requests)-1]
	if !req.Filter.HasFile {
		t.Error("the request did not carry the file clause")
	}
	if req.Filter.Mentioned {
		t.Error("the file list is not the mentions list")
	}
	// The question is about a conversation, so it starts on the open room rather than
	// everywhere — the opposite default from the mentions list.
	if req.Rooms.All || len(req.Rooms.IDs) != 1 || req.Rooms.IDs[0] != "!a:x" {
		t.Errorf("rooms = %+v, want just the open room", req.Rooms)
	}
}

// :files follows the same scope ladder the key does, because the command line reads the
// pane the colon was typed in.
func TestFilesCommandFollowsThePane(t *testing.T) {
	t.Parallel()

	m, backend := browsingFiles(t, fileHit("!b:x", "$1", "sam", "deck.pdf", "deck.pdf", 2))
	m.focus = paneRail
	mdl, cmd := m.runCommandLine(":files")
	m = mdl
	m = deliver(t, m, cmd)

	if m.search.showing() != (filesList{}) {
		t.Fatal(":files did not open the file list")
	}
	if len(backend.requests) == 0 {
		t.Fatal("no search was issued")
	}
	if rooms := backend.requests[len(backend.requests)-1].Rooms; !rooms.All {
		t.Errorf("rooms = %+v, want every room from the rail", rooms)
	}
}

// …and from the timeline the same command means the room you are reading.
func TestFilesCommandFromTheTimelineIsTheRoom(t *testing.T) {
	t.Parallel()

	m, backend := browsingFiles(t, fileHit("!a:x", "$1", "sam", "deck.pdf", "deck.pdf", 2))
	m.focus = paneTimeline
	mdl, cmd := m.runCommandLine(":files")
	m = mdl
	m = deliver(t, m, cmd)

	if len(backend.requests) == 0 {
		t.Fatal("no search was issued")
	}
	rooms := backend.requests[len(backend.requests)-1].Rooms
	if rooms.All || len(rooms.IDs) != 1 || rooms.IDs[0] != "!a:x" {
		t.Errorf("rooms = %+v, want just the open room", rooms)
	}
}

// The summary names files rather than matches, and an empty list is a fact rather than
// a question that failed.
func TestFilesSummarySpeaksOfFiles(t *testing.T) {
	t.Parallel()

	m, _ := filing(t)
	m, cmd := chord(t, m, "g", "f")
	m = deliver(t, m, cmd)
	if got := m.searchSummary(); !strings.Contains(got, "no files") {
		t.Errorf("summary = %q, want it to say there are none", got)
	}

	m2, _ := browsingFiles(t, fileHit("!a:x", "$1", "dana", "a.pdf", "a.pdf", 1))
	m2, cmd2 := chord(t, m2, "g", "f")
	m2 = deliver(t, m2, cmd2)
	if got := m2.searchSummary(); !strings.Contains(got, "1 file") {
		t.Errorf("summary = %q, want it to count files", got)
	}
	if got := m2.searchTitle(); !strings.HasPrefix(got, "Files") {
		t.Errorf("title = %q, want it to say Files", got)
	}
}

// A row in the file list leads with the file.
func TestFileRowLeadsWithTheFileName(t *testing.T) {
	t.Parallel()

	m, _ := browsingFiles(t,
		fileHit("!a:x", "$2", "dana", "budget.xlsx", "here is the budget", 1),
		fileHit("!a:x", "$1", "sam", "photo.png", "photo.png", 2),
	)
	m, cmd := chord(t, m, "g", "f")
	m = deliver(t, m, cmd)

	withCaption := m.searchRow(m.search.hits[0], false, 120)
	if !strings.Contains(withCaption, "budget.xlsx") {
		t.Errorf("row = %q, want the file name", withCaption)
	}
	if !strings.Contains(withCaption, "here is the budget") {
		t.Errorf("row = %q, want a real caption kept beside the name", withCaption)
	}

	nameOnly := m.searchRow(m.search.hits[1], false, 120)
	if strings.Count(nameOnly, "photo.png") != 1 {
		t.Errorf("row = %q, want the name once when the body only repeats it", nameOnly)
	}
}

// A Hebrew caption beside an ASCII file name must still read right-to-left.
func TestFileRowReordersAnRTLCaptionBesideAnASCIIName(t *testing.T) {
	t.Parallel()

	const hebrew = "שלום עולם"
	m, _ := browsingFiles(t, fileHit("!a:x", "$1", "dana", "image.jpg", hebrew, 1))
	m, cmd := chord(t, m, "g", "f")
	m = deliver(t, m, cmd)

	row := m.searchRow(m.search.hits[0], false, 120)
	if !strings.Contains(row, "image.jpg") {
		t.Fatalf("row = %q, want the file name", row)
	}
	// reorder is what the timeline applies to an RTL run; the row must carry its
	// output, not the logical order it was stored in.
	want := reorder(hebrew, bidi.RightToLeft)
	if want == hebrew {
		t.Fatal("fixture is not actually reordered; the test would prove nothing")
	}
	if !strings.Contains(row, want) {
		t.Errorf("row = %q\nwant it to contain the reordered caption %q", row, want)
	}
	if strings.Contains(row, hebrew) {
		t.Errorf("row = %q still carries the caption in logical order", row)
	}
}

// A Hebrew sender reads the right way round in a result row.
func TestSearchRowReordersTheSenderName(t *testing.T) {
	t.Parallel()

	const sender = "מני רגול"
	m, _ := browsingFiles(t, fileHit("!a:x", "$1", sender, "image.jpg", "image.jpg", 1))
	m, cmd := chord(t, m, "g", "f")
	m = deliver(t, m, cmd)

	row := m.searchRow(m.search.hits[0], false, 160)
	want := displayName(sender)
	if want == sender {
		t.Fatal("fixture is not actually reordered; the test would prove nothing")
	}
	if !strings.Contains(row, want) {
		t.Errorf("row = %q\nwant the sender reordered as %q", row, want)
	}
	if strings.Contains(row, sender) {
		t.Errorf("row = %q still carries the sender in logical order", row)
	}
}

// A room label is cut before it is reordered, not after: truncating the display form
// takes the visually-leftmost characters, which for an RTL name is its *end*.
func TestSearchRowCapsTheRoomLabelBeforeReordering(t *testing.T) {
	t.Parallel()

	const room = "חדר ארוך מאוד עם שם שלא נגמר"
	backend := &searchBackend{hits: []domain.SearchHit{
		fileHit("!a:x", "$1", "dana", "image.jpg", "image.jpg", 1),
	}}
	m := update(t, sized(t, starterNew(backend, config.Display{})),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: room}, {ID: "!b:x", Name: "Bravo"}}})
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m.focus = paneTimeline
	m = m.clearStatus()

	// The global list is the one that shows a room column at all, and the rail is the
	// rung that means everywhere (see defaultSearchScope).
	m.focus = paneRail
	mdl, cmd := m.runCommandLine(":files")
	m = mdl
	m = deliver(t, m, cmd)

	row := m.searchRow(m.search.hits[0], false, 200)
	want := displayName(truncateLogical(room, searchRoomWidth))
	if !strings.Contains(row, want) {
		t.Errorf("row = %q\nwant the label capped-then-reordered as %q", row, want)
	}
}
