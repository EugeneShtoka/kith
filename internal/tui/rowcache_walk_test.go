package tui

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Every row the timeline caches must be keyed by everything its render reads. The
// named cases in rowcache_test.go change one input each, and a key missing an input
// nobody thought of passes them (focus, which decides whether pictures are drawn, was
// found by reading the code). This walks random sequences of every input a row reads:
// focus, insert mode, width, stars, reveals, reactions (added, removed, and swapped at
// the same count), pictures, fetched quotes, the cursor, the room's spaces (a space
// rule shapes sender names) and its name (a tracked-word rule can be scoped by it). A frame is drawn between steps, and asModel's staleRow check fails the
// first Update that leaves a cached row unlike a fresh render. A new render input
// without a key fails here without a new case.
func TestTheRowCacheHoldsOverAnyUpdateSequence(t *testing.T) {
	t.Parallel()
	seeds, steps := 120, 40
	if testing.Short() {
		seeds = 25
	}
	if n, err := strconv.Atoi(os.Getenv("KITH_ROWCACHE_SEEDS")); err == nil {
		seeds, steps = n, 80 // a deeper sweep, by hand
	}
	for seed := range seeds {
		walkRowInputs(t, uint64(seed), steps)
	}
}

func walkRowInputs(t *testing.T, seed uint64, steps int) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 0x726f77))
	var log []string
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("seed %d: %v\nsteps:\n  %s", seed, r, strings.Join(log, "\n  "))
		}
	}()

	display := config.Display{
		Media:      config.Media{Mode: "inline"},
		SpaceRules: []config.SpaceRule{{Space: "Work", FirstNameOnly: true}},
		Tracked:    config.Tracked{Rules: []config.TrackedRule{{Words: []string{"word3"}, In: []string{"room:Alpha"}}}},
	}
	m := update(t, starterNew(&quoteBackend{}, display),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	m, _ = m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m.focus = paneTimeline
	msgs := walkMessages()
	m, _ = routed(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: msgs}})
	if m.pics.graphics == graphicsNone {
		t.Skip("no inline graphics in this environment; pictures are an input the walk needs")
	}
	pick := func() domain.EventID { return msgs[rng.IntN(len(msgs))].ID }
	pictures := []domain.EventID{"$2", "$7"}
	reaction := 0
	var live []domain.Reaction // added and not yet removed
	react := func(r domain.Reaction, removed bool) tea.Msg {
		return reactionUpdateMsg{u: domain.ReactionUpdate{Reaction: r, Removed: removed}}
	}
	fresh := func(target domain.EventID) domain.Reaction {
		reaction++
		return domain.Reaction{ID: domain.EventID(fmt.Sprintf("$r%d", reaction)), RoomID: "!a:x",
			Target: target, Sender: "@dana:x", Key: []string{"👍", "🎉", "❤"}[rng.IntN(3)]}
	}

	for range steps {
		var msg tea.Msg = settleMsg{}
		switch r := rng.IntN(15); r {
		case 0:
			m.focus = []pane{paneTimeline, paneRooms}[rng.IntN(2)]
			log = append(log, fmt.Sprintf("focus %v", m.focus))
		case 1:
			m.compose.insertMode = !m.compose.insertMode
			log = append(log, fmt.Sprintf("insert mode %v", m.compose.insertMode))
		case 2:
			msg = tea.WindowSizeMsg{Width: 50 + rng.IntN(100), Height: 20 + rng.IntN(30)}
			log = append(log, fmt.Sprintf("resize %+v", msg))
		case 3:
			ids := []domain.EventID{pick(), pick()}
			msg = starredInMsg{roomID: "!a:x", ids: ids}
			log = append(log, fmt.Sprintf("stars %v", ids))
		case 4:
			m.timeline.selected = pick()
			m, _ = m.toggleStar()
			log = append(log, fmt.Sprintf("toggle star on %s", m.timeline.selected))
		case 5:
			m.timeline.selected = "$4"
			m, _ = m.toggleReveal()
			log = append(log, "toggle reveal on $4")
		case 6:
			r := fresh(pick())
			live = append(live, r)
			msg = react(r, false)
			log = append(log, fmt.Sprintf("reaction %s on %s", r.Key, r.Target))
		case 11:
			if len(live) == 0 {
				continue
			}
			i := rng.IntN(len(live))
			gone := live[i]
			live = append(live[:i], live[i+1:]...)
			msg = react(gone, true)
			log = append(log, fmt.Sprintf("reaction %s on %s removed", gone.Key, gone.Target))
		case 12:
			// Remove one and add another on the same message: the count stays.
			if len(live) == 0 {
				continue
			}
			i := rng.IntN(len(live))
			gone := live[i]
			r := fresh(gone.Target)
			live = append(live[:i], live[i+1:]...)
			live = append(live, r)
			m, _ = routed(t, m, react(gone, true))
			msg = react(r, false)
			log = append(log, fmt.Sprintf("reaction on %s swapped %s for %s", gone.Target, gone.Key, r.Key))
		case 13:
			var spaces []domain.Space
			if rng.IntN(2) == 0 {
				spaces = []domain.Space{{ID: "!s:x", Name: "Work", Children: []domain.RoomID{"!a:x"}}}
			}
			msg = spacesMsg{spaces: spaces}
			log = append(log, fmt.Sprintf("the room is in %d spaces", len(spaces)))
		case 14:
			name := []string{"Alpha", "Beta"}[rng.IntN(2)]
			msg = roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: name}}}
			log = append(log, "the room is named "+name)
		case 7:
			id := pictures[rng.IntN(len(pictures))]
			rows := make([]string, 1+rng.IntN(4))
			for i := range rows {
				rows[i] = fmt.Sprintf("PIX-%s-%d", id, i)
			}
			msg = imageLoadedMsg{eventID: id, rows: rows}
			log = append(log, fmt.Sprintf("picture for %s, %d rows", id, len(rows)))
		case 8:
			msg = fetchedQuoteMsg{roomID: "!a:x", message: domain.Message{
				ID: "$old", RoomID: "!a:x", Sender: "@dana:x", SenderName: "Dana", Body: "the message being answered"}}
			log = append(log, "the quoted $old arrives")
		case 9:
			m.timeline.selected = pick()
			log = append(log, fmt.Sprintf("cursor to %s", m.timeline.selected))
		default:
			log = append(log, "draw")
		}
		m, _ = routed(t, m, msg)
		if rng.IntN(3) > 0 {
			_ = m.View() // fills the cache for the rows on screen
		}
	}
}

// walkMessages is a timeline with every kind of row: two days (date dividers),
// pictures, a deleted message (revealable), replies to a loaded and to an unloaded
// message, and bodies long enough to wrap.
func walkMessages() []domain.Message {
	day := func(d, sec int) time.Time { return time.Unix(int64(d*86400+sec), 0) }
	msgs := make([]domain.Message, 0, 12)
	for i := range 12 {
		msg := domain.Message{
			ID: domain.EventID(fmt.Sprintf("$%d", i)), RoomID: "!a:x",
			Sender: []string{"@me:x", "@dana:x", "@liv:x"}[i%3], Timestamp: day(i/6, 60*i),
			SenderName: []string{"Me Myself", "Dana Scully", "Liv Tyler"}[i%3],
			Body:       strings.Repeat(fmt.Sprintf("word%d ", i), 1+i%4*6),
		}
		switch msg.ID {
		case "$2", "$7":
			msg.Body = ""
			msg.Media = &domain.Media{Type: domain.MediaImage, Name: fmt.Sprintf("pic%d.jpg", i), Width: 4, Height: 4}
		case "$4":
			msg.Redacted = true
		case "$5":
			msg.ReplyTo = "$1"
		case "$9":
			msg.ReplyTo = "$old"
		}
		msgs = append(msgs, msg)
	}
	return msgs
}

// Pictures are drawn only while the timeline has the focus. Moving it away and back
// must redraw the rows around a picture, not keep them as they were cached.
func TestPictureRowsFollowTheFocus(t *testing.T) {
	t.Parallel()
	m := update(t, starterNew(&quoteBackend{}, config.Display{Media: config.Media{Mode: "inline"}}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	m, _ = m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m.focus = paneTimeline
	m, _ = routed(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: walkMessages()}})
	if m.pics.graphics == graphicsNone {
		t.Skip("no inline graphics in this environment")
	}
	m, _ = routed(t, m, imageLoadedMsg{eventID: "$2", rows: []string{"PIX-1", "PIX-2"}})
	_ = m.View()
	m.focus = paneRooms
	m, _ = routed(t, m, settleMsg{}) // asModel's staleRow check fails a stale picture row
	_ = m.View()
}
