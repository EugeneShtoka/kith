package tui

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A frame for a message older than every loaded row stays out: alone at the top, it would
// hide the unloaded history between it and the next row. A loaded one is replaced, a newer
// one joins.
func TestAFrameBelowTheLoadedRowsStaysOut(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel())) // current room !a:x
	m = loadPage(t, m, []domain.Message{msgAt(mx+"$10", "ten", 10), msgAt(mx+"$11", "eleven", 11)})

	old := msgAt(mx+"$3", "three, edited", 3)
	old.Edited, old.EditedAt, old.RevisionID = true, at(50), mx+"$e3"
	m = update(t, m, incomingMsg{message: old})
	// The oldest row's frame, deleted and with an earlier time than its page gave it: still
	// the row it replaces.
	loaded := msgAt(mx+"$10", "", 9)
	loaded.Redacted = true
	m = update(t, m, incomingMsg{message: loaded})
	m = update(t, m, incomingMsg{message: msgAt(mx+"$12", "twelve", 12)})

	if got := rowIDs(m.timeline.messages); !slices.Equal(got, []domain.EventID{mx + "$10", mx + "$11", mx + "$12"}) {
		t.Fatalf("rows = %v, want $10 $11 $12: the old message stays out, the new one joins", got)
	}
	if !m.timeline.messages[0].Redacted {
		t.Errorf("the loaded $10 was not replaced by its frame: %+v", m.timeline.messages[0])
	}

	// Paging back reaches $3, read before its edit landed: the edit is not lost.
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", fromCache: true, page: domain.TimelinePage{Messages: []domain.Message{
		msgAt(mx+"$3", "three", 3), msgAt(mx+"$9", "nine", 9), msgAt(mx+"$10", "ten", 10),
	}}})
	if i := indexOfMessage(m.timeline.messages, mx+"$3"); i != 0 || m.timeline.messages[i].Body != "three, edited" {
		t.Errorf("after paging back, rows = %+v, want $3 first with its edit", m.timeline.messages)
	}
}

func rowIDs(msgs []domain.Message) []domain.EventID {
	ids := make([]domain.EventID, len(msgs))
	for i := range msgs {
		ids[i] = msgs[i].ID
	}
	return ids
}

// The open room's rows are always one unbroken stretch of its history reaching the newest
// message, each row as the cache now holds it, whatever order frames (edits, deletions and
// new messages, of loaded and unloaded messages) and pages (the cached timeline, the
// network's newest page, older cached pages, each read at some point between its ask and
// its landing) arrive in.
func TestTheLoadedRowsStayOneStretchOfTheRoom(t *testing.T) {
	t.Parallel()
	seeds := 200
	if testing.Short() {
		seeds = 40
	}
	for seed := range seeds {
		walkFrames(t, uint64(seed), 60)
	}
}

// pendingPage is a page asked for: read (a snapshot of the cache) at some step after the
// ask, landing at some step after that.
type pendingPage struct {
	kind   string // "cached", "network" or "older"
	anchor domain.EventID
	read   bool
	page   []domain.Message
}

func walkFrames(t *testing.T, seed uint64, steps int) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 0x666c6f6f72))
	var log []string
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("seed %d: %s\nsteps:\n  %s", seed, fmt.Sprintf(format, args...), strings.Join(log, "\n  "))
	}
	defer func() {
		if r := recover(); r != nil {
			fail("%v", r)
		}
	}()

	room := domain.RoomID(mx + "!a:x")
	var cache []domain.Message
	made := 0
	add := func() domain.Message {
		made++
		msg := msgAt(domain.EventID(fmt.Sprintf(mx+"$%d", made)), fmt.Sprintf("message %d", made), 10*made)
		cache = append(cache, msg)
		return msg
	}
	for range rng.IntN(40) {
		add()
	}

	m := sized(t, withRooms(t, newModel())) // opens !a:x
	if m.openRoom != room || !m.timeline.hist.loading {
		fail("the walk expects !a:x open and its first pages asked for")
	}
	pending := []*pendingPage{{kind: "cached"}, {kind: "network"}}
	newest := func(n int) []domain.Message { return slices.Clone(cache[max(len(cache)-n, 0):]) }
	read := func(p *pendingPage) {
		p.read = true
		switch p.kind {
		case "cached":
			p.page = newest(20)
		case "network":
			p.page = newest(10)
		case "older":
			// With no row loaded, the cache's newest (olderPageCmd).
			p.page = newest(20)
			if i := slices.IndexFunc(cache, func(msg domain.Message) bool { return msg.ID == p.anchor }); i >= 0 {
				p.page = slices.Clone(cache[max(i-15, 0) : i+1])
			}
		}
		log = append(log, fmt.Sprintf("the %s page is read: %v", p.kind, rowIDs(p.page)))
	}
	land := func(p *pendingPage) {
		log = append(log, "the "+p.kind+" page lands")
		switch p.kind {
		case "cached":
			m = update(t, m, cachedTimelineMsg{roomID: room, messages: p.page})
		case "network":
			m = update(t, m, timelineMsg{roomID: room, page: domain.TimelinePage{Messages: p.page}})
		default:
			m = update(t, m, timelineMsg{roomID: room, fromCache: true, page: domain.TimelinePage{Messages: p.page}})
		}
	}
	frame := func(i int, what string) {
		log = append(log, fmt.Sprintf("frame: %s %s", cache[i].ID, what))
		m = update(t, m, incomingMsg{message: cache[i]})
	}

	for step := range steps {
		switch r := rng.IntN(11); {
		case r < 2 && len(cache) > 0:
			i := rng.IntN(len(cache))
			if cache[i].Redacted {
				continue // a deleted message never regains words
			}
			cache[i].Body = fmt.Sprintf("edit %d", step)
			cache[i].Edited, cache[i].EditedAt = true, at(100000+step)
			cache[i].RevisionID = domain.EventID(fmt.Sprintf(mx+"$e%d", step))
			frame(i, "edited")
		case r == 2 && len(cache) > 0:
			i := rng.IntN(len(cache))
			cache[i].Body, cache[i].Redacted = "", true
			frame(i, "deleted")
		case r == 3:
			add()
			frame(len(cache)-1, "is new")
		case r == 10 && len(cache) > 0:
			// History backfilled: a message older than every other.
			made++
			msg := msgAt(domain.EventID(fmt.Sprintf(mx+"$%d", made)), "backfilled", int(cache[0].Timestamp.Unix())-1)
			cache = slices.Insert(cache, 0, msg)
			frame(0, "is backfilled")
		case r == 4:
			loading := m.timeline.hist.loading
			var cmd tea.Cmd
			m, cmd = m.loadOlder()
			if !loading && m.timeline.hist.loading && cmd != nil {
				p := &pendingPage{kind: "older"}
				if len(m.timeline.messages) > 0 {
					p.anchor = m.timeline.messages[0].ID
				}
				pending = append(pending, p)
				log = append(log, fmt.Sprintf("an older page is asked, before %q", p.anchor))
			}
		case r < 7:
			if i := slices.IndexFunc(pending, func(p *pendingPage) bool { return !p.read }); i >= 0 {
				read(pending[i])
			}
		default:
			ready := slices.DeleteFunc(slices.Clone(pending), func(p *pendingPage) bool { return !p.read })
			if len(ready) > 0 {
				p := ready[rng.IntN(len(ready))]
				pending = slices.DeleteFunc(pending, func(q *pendingPage) bool { return q == p })
				land(p)
			}
		}
		if err := oneStretch(m.timeline.messages, cache, m.timeline.hist.atStart); err != "" {
			fail("%s", err)
		}
	}
	for _, p := range pending {
		if !p.read {
			read(p)
		}
		land(p)
	}
	if err := oneStretch(m.timeline.messages, cache, m.timeline.hist.atStart); err != "" {
		fail("once every page landed: %s", err)
	}
	if len(cache) > 0 && len(m.timeline.messages) == 0 {
		fail("every page landed and no row is loaded")
	}
}

// oneStretch is why rows are not cache's messages from rows' first to the newest, each as
// cache holds it, starting at its first when atStart, or "".
func oneStretch(rows, cache []domain.Message, atStart bool) string {
	if len(rows) == 0 {
		return ""
	}
	i := slices.IndexFunc(cache, func(msg domain.Message) bool { return msg.ID == rows[0].ID })
	if i < 0 {
		return fmt.Sprintf("row %s is not in the room", rows[0].ID)
	}
	if atStart && i > 0 {
		return fmt.Sprintf("the room's start is loaded, yet the rows begin at %s, not %s", rows[0].ID, cache[0].ID)
	}
	want := cache[i:]
	if !slices.Equal(rowIDs(rows), rowIDs(want)) {
		return fmt.Sprintf("rows %v, want the stretch %v", rowIDs(rows), rowIDs(want))
	}
	for j := range rows {
		if rows[j].Body != want[j].Body || rows[j].Redacted != want[j].Redacted {
			return fmt.Sprintf("row %s shows %q (deleted %v), the cache holds %q (deleted %v)",
				rows[j].ID, rows[j].Body, rows[j].Redacted, want[j].Body, want[j].Redacted)
		}
	}
	return ""
}
