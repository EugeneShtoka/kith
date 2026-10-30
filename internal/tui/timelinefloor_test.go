package tui

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
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

// A thread's root and replies from before the loaded rows show in the thread, not the room,
// and a thread page read before an edit of one of them lands with the edit.
func TestAThreadFromFurtherBackStaysInTheThread(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel())) // current room !a:x
	reply := func(id domain.EventID, body string, sec int) domain.Message {
		msg := msgAt(id, body, sec)
		msg.ThreadRoot = mx + "$1"
		if id == mx+"$11" {
			msg.ReplyTo = mx + "$5"
		}
		return msg
	}
	m = loadPage(t, m, []domain.Message{msgAt(mx+"$10", "ten", 10), reply(mx+"$11", "a late reply", 11), msgAt(mx+"$12", "twelve", 12)})
	m, _ = m.enterThread(mx+"$1", "") // from $11's summary row; its root is fetched
	m, _ = m.loadOlderInThread()

	edited := reply(mx+"$5", "an early reply, edited", 5)
	edited.Edited, edited.EditedAt, edited.RevisionID = true, at(50), mx+"$e5"
	m = update(t, m, incomingMsg{message: edited})
	m = update(t, m, threadPageMsg{roomID: mx + "!a:x", root: mx + "$1", page: domain.TimelinePage{Messages: []domain.Message{
		reply(mx+"$5", "an early reply", 5), reply(mx+"$11", "a late reply", 11), // read before the edit
	}}})
	shown := m.derivedFor().shown
	if got := rowIDs(shown); !slices.Equal(got, []domain.EventID{mx + "$5", mx + "$11"}) {
		t.Fatalf("the thread shows %v, want $5 $11", got)
	}
	if shown[0].Body != "an early reply, edited" {
		t.Errorf("the thread shows $5 as %q, want its edit", shown[0].Body)
	}
	m = update(t, m, fetchedEventMsg{roomID: mx + "!a:x", message: msgAt(mx+"$1", "the root", 1)})
	if got := rowIDs(m.derivedFor().shown); !slices.Equal(got, []domain.EventID{mx + "$1", mx + "$5", mx + "$11"}) {
		t.Errorf("once its root lands the thread shows %v, want $1 $5 $11", got)
	}
	// The thread shows $5, so going to it from $11 needs no page of the room.
	m.timeline.selected = mx + "$11"
	if m, _ = m.goToReplied(); m.timeline.selected != mx+"$5" || m.timeline.hist.loading {
		t.Errorf("going from $11 to the $5 it answers selected %s, paging the room: %v", m.timeline.selected, m.timeline.hist.loading)
	}
	m, _ = m.closeThread()
	if got := rowIDs(m.derivedFor().main); !slices.Equal(got, []domain.EventID{mx + "$10", mx + "$12"}) {
		t.Errorf("the room shows %v, want $10 $12: the thread's root and early reply are from before them", got)
	}
}

// Going to the message a reply answers, loaded only as a thread's root from before the
// rows, pages the room down to it rather than selecting a message the room does not show;
// so does closing that thread.
func TestGoingToAMessageFromFurtherBackPagesDownToIt(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel())) // current room !a:x
	m.focus = paneTimeline
	answer := msgAt(mx+"$12", "twelve", 12)
	answer.ReplyTo = mx + "$1"
	late := msgAt(mx+"$11", "a late reply", 11)
	late.ThreadRoot = mx + "$1"
	m = loadPage(t, m, []domain.Message{msgAt(mx+"$10", "ten", 10), late, answer})
	m, _ = m.enterThread(mx+"$1", "")
	m = update(t, m, fetchedEventMsg{roomID: mx + "!a:x", message: msgAt(mx+"$1", "the root", 1)})
	m, _ = m.closeThread()
	if indexOfMessage(m.shownMessages(), m.timeline.selected) < 0 && m.timeline.selected != "" {
		t.Errorf("closing the thread selected %s, which the room does not show", m.timeline.selected)
	}

	m.timeline.selected = mx + "$12"
	m, _ = m.goToReplied()
	page := func(msgs ...domain.Message) {
		t.Helper()
		if !m.timeline.hist.loading {
			t.Fatalf("selected %s without paging down to $1 (shown: %v)", m.timeline.selected, rowIDs(m.shownMessages()))
		}
		m = update(t, m, timelineMsg{roomID: mx + "!a:x", fromCache: true, page: domain.TimelinePage{Messages: msgs}})
	}
	page(msgAt(mx+"$5", "five", 5), msgAt(mx+"$10", "ten", 10))
	page(msgAt(mx+"$1", "the root", 1), msgAt(mx+"$5", "five", 5))
	if m.timeline.selected != mx+"$1" || indexOfMessage(m.shownMessages(), mx+"$1") < 0 {
		t.Errorf("selected %s, want $1 shown in the room (shown: %v)", m.timeline.selected, rowIDs(m.shownMessages()))
	}
}

// From inside a thread, going to a room message from before the rows pages the room, not
// the thread, until it shows.
func TestGoingFromAThreadToAMessageFromFurtherBackPagesTheRoom(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel())) // current room !a:x
	m.focus = paneTimeline
	late := msgAt(mx+"$11", "a late reply", 11)
	late.ThreadRoot, late.ReplyTo = mx+"$1", mx+"$2"
	m = loadPage(t, m, []domain.Message{msgAt(mx+"$10", "ten", 10), late, msgAt(mx+"$12", "twelve", 12)})
	m, _ = m.enterThread(mx+"$2", "") // $2 is loaded only as this fetch brings it
	m = update(t, m, fetchedEventMsg{roomID: mx + "!a:x", message: msgAt(mx+"$2", "two", 2)})
	m, _ = m.closeThread()
	m, _ = m.enterThread(mx+"$1", mx+"$11")

	m, _ = m.goToReplied()
	for _, msgs := range [][]domain.Message{
		{msgAt(mx+"$5", "five", 5), msgAt(mx+"$10", "ten", 10)},
		{msgAt(mx+"$2", "two", 2), msgAt(mx+"$5", "five", 5)},
	} {
		if !m.timeline.hist.loading {
			t.Fatalf("the room is not paged (the thread is: %v); selected %s", m.thread.loading, m.timeline.selected)
		}
		m = update(t, m, timelineMsg{roomID: mx + "!a:x", fromCache: true, page: domain.TimelinePage{Messages: msgs}})
	}
	if m.thread.open() || m.timeline.selected != mx+"$2" || indexOfMessage(m.shownMessages(), mx+"$2") < 0 {
		t.Errorf("thread %q open, selected %s, want $2 shown in the room", m.thread.root, m.timeline.selected)
	}
}

func rowIDs(msgs []domain.Message) []domain.EventID {
	ids := make([]domain.EventID, len(msgs))
	for i := range msgs {
		ids[i] = msgs[i].ID
	}
	return ids
}

// The open room shows one unbroken stretch of its history reaching the newest message, each
// message as the cache now holds it, whatever order frames (edits, deletions and new
// messages, of loaded and unloaded messages, in threads or not) and pages arrive in: the
// cached timeline, the network's newest page, older cached pages, a thread's root fetched on
// entering it and the thread's pages, each read at some point between its ask and its
// landing. A thread's root and replies fetched from further back show in the thread, not
// in the room.
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
	kind   string // "cached", "network", "older", "root" or "thread"
	anchor domain.EventID
	read   bool
	page   []domain.Message
}

// anchorBackend records where the client asked for an older cached page.
type anchorBackend struct {
	apitest.Nop
	anchor domain.EventID
}

func (b *anchorBackend) MessagesAround(_ context.Context, _ domain.RoomID, event domain.EventID, _, _ int) ([]domain.Message, error) {
	b.anchor = event
	return nil, nil
}

func (b *anchorBackend) CachedTimeline(context.Context, domain.RoomID) ([]domain.Message, error) {
	b.anchor = ""
	return nil, nil
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
	pos := func(id domain.EventID) int {
		return slices.IndexFunc(cache, func(msg domain.Message) bool { return msg.ID == id })
	}
	roots := func() []domain.EventID {
		var ids []domain.EventID
		for i := range cache {
			if cache[i].ThreadRoot == "" {
				ids = append(ids, cache[i].ID)
			}
		}
		return ids
	}
	made := 0
	add := func() domain.Message {
		made++
		msg := msgAt(domain.EventID(fmt.Sprintf(mx+"$%d", made)), fmt.Sprintf("message %d", made), 10*made)
		if ids := roots(); len(ids) > 0 && rng.IntN(2) == 0 {
			msg.ThreadRoot = ids[rng.IntN(min(len(ids), 4))] // a reply in one of the oldest threads, which grow long
		} else if len(cache) > 0 && rng.IntN(3) == 0 {
			msg.ReplyTo = cache[rng.IntN(len(cache))].ID
		}
		cache = append(cache, msg)
		return msg
	}
	for range rng.IntN(40) {
		add()
	}

	backend := &anchorBackend{}
	m := sized(t, withRooms(t, starterNew(backend, config.Display{}))) // opens !a:x
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
			if i := pos(p.anchor); i >= 0 {
				p.page = slices.Clone(cache[max(i-15, 0) : i+1])
			}
		case "root":
			p.page = []domain.Message{cache[pos(p.anchor)]}
		case "thread":
			p.page = slices.DeleteFunc(slices.Clone(cache), func(msg domain.Message) bool { return msg.ThreadRoot != p.anchor })
		}
		log = append(log, fmt.Sprintf("the %s page is read: %v", p.kind, rowIDs(p.page)))
	}
	// deepest is the oldest message a room page brought: the room shows from it on.
	var deepest domain.EventID
	land := func(p *pendingPage) {
		log = append(log, "the "+p.kind+" page lands")
		if room := p.kind == "cached" || p.kind == "network" || p.kind == "older"; room && len(p.page) > 0 &&
			(deepest == "" || pos(p.page[0].ID) < pos(deepest)) {
			deepest = p.page[0].ID
		}
		switch p.kind {
		case "cached":
			m = update(t, m, cachedTimelineMsg{roomID: room, messages: p.page})
		case "network":
			m = update(t, m, timelineMsg{roomID: room, page: domain.TimelinePage{Messages: p.page}})
		case "older":
			m = update(t, m, timelineMsg{roomID: room, fromCache: true, page: domain.TimelinePage{Messages: p.page}})
		case "root":
			if rng.IntN(3) == 0 {
				log = append(log, "  (it failed)")
				m = update(t, m, fetchedEventMsg{roomID: room, err: errors.New("unreachable")})
				return
			}
			m = update(t, m, fetchedEventMsg{roomID: room, message: p.page[0]})
		case "thread":
			m = update(t, m, threadPageMsg{roomID: room, root: p.anchor, page: domain.TimelinePage{Messages: p.page}})
		}
		// Paging down to a jump's target, the client asked for the next page itself.
		if m.timeline.hist.loading && !slices.ContainsFunc(pending, func(q *pendingPage) bool {
			return q.kind != "root" && q.kind != "thread"
		}) {
			m.olderPageCmd(room)() // records where it reads from
			pending = append(pending, &pendingPage{kind: "older", anchor: backend.anchor})
			log = append(log, fmt.Sprintf("an older page is asked for the jump, before %q", backend.anchor))
		}
		// What the thread asked for shows in it.
		if (p.kind == "root" || p.kind == "thread") && m.thread.root == p.anchor {
			shown := m.derivedFor().shown
			for i := range p.page {
				if c := cache[pos(p.page[i].ID)]; !c.Redacted && indexOfMessage(shown, c.ID) < 0 {
					fail("the thread %s does not show %s, which its %s page brought", p.anchor, c.ID, p.kind)
				}
			}
		}
	}
	// pick is a message to change: often one a page already read holds, so the page lands
	// older than the frame.
	pick := func() int {
		var read []domain.EventID
		for _, p := range pending {
			if p.read {
				read = append(read, rowIDs(p.page)...)
			}
		}
		if len(read) > 0 && rng.IntN(2) == 0 {
			return pos(read[rng.IntN(len(read))])
		}
		return rng.IntN(len(cache))
	}
	// selectedHidden is the selected message when the pane shows it nowhere: not drawn, nor
	// in the room folded under a thread's summary row (a reply moves into the thread of the
	// message it answers once that loads).
	selectedHidden := func() domain.EventID {
		sel := m.timeline.selected
		if sel == "" || indexOfMessage(m.shownMessages(), sel) >= 0 {
			return ""
		}
		if root := domain.ThreadRootOf(m.timeline.messages, sel); root != "" && root != sel && !m.thread.open() {
			return ""
		}
		return sel
	}
	frame := func(i int, what string) {
		log = append(log, fmt.Sprintf("frame: %s %s", cache[i].ID, what))
		m = update(t, m, incomingMsg{message: cache[i]})
	}

	for step := range steps {
		switch r := rng.IntN(15); {
		case r < 2 && len(cache) > 0:
			i := pick()
			if cache[i].Redacted {
				continue // a deleted message never regains words
			}
			cache[i].Body = fmt.Sprintf("edit %d", step)
			cache[i].Edited, cache[i].EditedAt = true, at(100000+step)
			cache[i].RevisionID = domain.EventID(fmt.Sprintf(mx+"$e%d", step))
			frame(i, "edited")
		case r == 2 && len(cache) > 0:
			i := pick()
			cache[i].Body, cache[i].Redacted = "", true
			frame(i, "deleted")
		case r == 3:
			add()
			frame(len(cache)-1, fmt.Sprintf("is new (in thread %q)", cache[len(cache)-1].ThreadRoot))
		case r == 10 && len(cache) > 0:
			// History backfilled: a message older than every other.
			made++
			msg := msgAt(domain.EventID(fmt.Sprintf(mx+"$%d", made)), "backfilled", int(cache[0].Timestamp.Unix())-1)
			cache = slices.Insert(cache, 0, msg)
			frame(0, "is backfilled")
		case r == 11 && len(m.derivedFor().main) > 0:
			// From a message the room shows or a thread's summary row, whose root may be
			// older than the loaded rows.
			d := m.derivedFor()
			ids := rowIDs(d.main)
			if len(d.threads) > 0 && rng.IntN(2) == 0 {
				ids = ids[:0] // a summary row, most often a thread reaching further back
				for i := range d.threads {
					ids = append(ids, d.threads[i].Root)
				}
			}
			root := ids[rng.IntN(len(ids))]
			fetching := m.thread.fetching
			m, _ = m.enterThread(root, "")
			log = append(log, fmt.Sprintf("the thread %s opens", root))
			if !fetching && m.thread.fetching {
				pending = append(pending, &pendingPage{kind: "root", anchor: root})
				log = append(log, "its root is fetched")
			}
		case r == 12 && m.thread.open():
			loading := m.thread.loading
			m, _ = m.loadOlderInThread()
			if !loading && m.thread.loading {
				pending = append(pending, &pendingPage{kind: "thread", anchor: m.thread.root})
				log = append(log, fmt.Sprintf("a page of the thread %s is asked", m.thread.root))
			}
		case r == 13 && m.thread.open():
			m, _ = m.closeThread()
			log = append(log, "the thread closes")
		case r == 14:
			replies := slices.DeleteFunc(slices.Clone(m.shownMessages()), func(msg domain.Message) bool { return msg.ReplyTo == "" })
			if len(replies) == 0 {
				continue
			}
			m.timeline.selected = replies[rng.IntN(len(replies))].ID
			log = append(log, fmt.Sprintf("from %s, go to the message it answers", m.timeline.selected))
			loading := m.timeline.hist.loading
			var cmd tea.Cmd
			m, cmd = m.goToReplied()
			if !loading && m.timeline.hist.loading && cmd != nil {
				cmd() // records where it reads from
				pending = append(pending, &pendingPage{kind: "older", anchor: backend.anchor})
				log = append(log, fmt.Sprintf("an older page is asked for it, before %q", backend.anchor))
			}
		case r == 4:
			loading := m.timeline.hist.loading
			var cmd tea.Cmd
			m, cmd = m.loadOlder()
			if !loading && m.timeline.hist.loading && cmd != nil {
				cmd() // records where it reads from
				pending = append(pending, &pendingPage{kind: "older", anchor: backend.anchor})
				log = append(log, fmt.Sprintf("an older page is asked, before %q", backend.anchor))
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
		if err := oneStretch(m, cache, deepest); err != "" {
			fail("%s", err)
		}
		if sel := selectedHidden(); sel != "" {
			fail("%s is selected, but not shown (thread %q shows %v)", sel, m.thread.root, rowIDs(m.shownMessages()))
		}
	}
	drain := func() {
		for len(pending) > 0 {
			p := pending[0]
			pending = pending[1:]
			if !p.read {
				read(p)
			}
			land(p)
		}
	}
	drain()
	if err := oneStretch(m, cache, deepest); err != "" {
		fail("once every page landed: %s", err)
	}
	if len(cache) > 0 && len(m.timeline.messages) == 0 {
		fail("every page landed and no row is loaded")
	}
	// Paging back reaches the room's start, every message shown.
	if m.thread.open() {
		m, _ = m.closeThread() // a thread pages itself
	}
	for range len(cache) + 2 {
		if m.timeline.hist.atStart {
			break
		}
		var cmd tea.Cmd
		if m, cmd = m.loadOlder(); cmd == nil {
			fail("paging back stopped before the room's start")
		}
		cmd()
		pending = append(pending, &pendingPage{kind: "older", anchor: backend.anchor})
		log = append(log, fmt.Sprintf("an older page is asked, before %q", backend.anchor))
		drain()
	}
	if !m.timeline.hist.atStart {
		fail("paging back never reached the room's start")
	}
	if m.jump.to != "" {
		fail("the jump to %s never resolved", m.jump.to)
	}
	if sel := selectedHidden(); sel != "" {
		fail("%s is selected, but not shown (thread %q shows %v)", sel, m.thread.root, rowIDs(m.shownMessages()))
	}
	if err := oneStretch(m, cache, deepest); err != "" {
		fail("paged back to the start: %s", err)
	}
}

// oneStretch is why the room m shows is not cache's main timeline from deepest to the newest
// (from the room's first message once m has loaded its start, from its first row before
// any page brought one), or why a loaded message is not as cache holds it, or "".
func oneStretch(m Model, cache []domain.Message, deepest domain.EventID) string {
	for j := range m.timeline.messages {
		msg := &m.timeline.messages[j]
		i := slices.IndexFunc(cache, func(c domain.Message) bool { return c.ID == msg.ID })
		if i < 0 {
			return fmt.Sprintf("%s is loaded but not in the room", msg.ID)
		}
		if msg.Body != cache[i].Body || msg.Redacted != cache[i].Redacted {
			return fmt.Sprintf("%s is loaded as %q (deleted %v), the cache holds %q (deleted %v)",
				msg.ID, msg.Body, msg.Redacted, cache[i].Body, cache[i].Redacted)
		}
	}
	main := m.derivedFor().main
	var from int
	switch {
	case m.timeline.hist.atStart:
	case deepest != "":
		from = slices.IndexFunc(cache, func(c domain.Message) bool { return c.ID == deepest })
	case len(main) == 0:
		return ""
	default:
		from = slices.IndexFunc(cache, func(c domain.Message) bool { return c.ID == main[0].ID })
	}
	want, _ := domain.CollapseThreads(cache[from:])
	if !slices.Equal(rowIDs(main), rowIDs(want)) {
		return fmt.Sprintf("the room shows %v, want the stretch %v", rowIDs(main), rowIDs(want))
	}
	return ""
}
