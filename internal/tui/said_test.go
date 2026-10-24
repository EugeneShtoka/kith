package tui

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

const (
	danaMatrix   = "matrix:@dana:example.org"
	danaWhatsApp = "whatsapp:15550100001@s.whatsapp.net"
)

// saidBackend holds a cache of messages and answers pages of it as the cache does, keeping
// every request.
type saidBackend struct {
	apitest.Nop
	mu       sync.Mutex
	msgs     []domain.Message
	asked    []domain.SaidRequest
	searched []domain.SearchRequest
	people   []domain.Member
}

func (b *saidBackend) MessagesFrom(_ context.Context, req domain.SaidRequest) ([]domain.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.asked = append(b.asked, req)
	place := func(m domain.Message) int {
		return cmp.Or(m.Timestamp.Compare(req.Before.Timestamp), cmp.Compare(m.ID, req.Before.ID), cmp.Compare(m.RoomID, req.Before.RoomID))
	}
	var page []domain.Message
	for i := range b.msgs {
		m := &b.msgs[i]
		if slices.Contains(req.Senders, m.Sender) && (req.Rooms.All || slices.Contains(req.Rooms.IDs, m.RoomID)) &&
			(req.Before.ID == "" || place(*m) < 0) {
			page = append(page, *m)
		}
	}
	slices.SortFunc(page, func(a, c domain.Message) int {
		return cmp.Or(a.Timestamp.Compare(c.Timestamp), cmp.Compare(a.ID, c.ID), cmp.Compare(a.RoomID, c.RoomID))
	})
	return page[max(len(page)-req.Limit, 0):], nil
}

func (b *saidBackend) SearchMessages(_ context.Context, req domain.SearchRequest) ([]domain.SearchHit, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.searched = append(b.searched, req)
	return nil, nil
}

func (b *saidBackend) SearchSenders(context.Context, domain.RoomSet, int) ([]domain.Member, error) {
	return b.people, nil
}

func (b *saidBackend) last() domain.SaidRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.asked[len(b.asked)-1]
}

// danaSays is n messages from Dana, alternating her two accounts and two rooms.
func danaSays(n int) []domain.Message {
	out := make([]domain.Message, n)
	for i := range out {
		sender, room := danaMatrix, domain.RoomID(mx+"!a:x")
		if i%2 == 1 {
			sender, room = danaWhatsApp, mx+"!b:x"
		}
		out[i] = domain.Message{
			ID: domain.EventID(fmt.Sprintf(mx+"$d%03d", i)), RoomID: room, Sender: sender,
			Body: fmt.Sprintf("said %d", i), Timestamp: at(100 + i),
		}
	}
	return out
}

// saidModel is Alpha open on a message of Dana's, her two accounts one identity, b behind it.
func saidModel(t *testing.T, b *saidBackend, cfg config.Config) Model {
	t.Helper()
	cfg.Display.Identities = append(cfg.Display.Identities, config.Identity{Alias: "Dana", IDs: []string{danaMatrix, danaWhatsApp}})
	m := New(context.Background(), b, cfg.Display).WithConfigFile("", cfg)
	m = sized(t, withRooms(t, m))
	next, _ := m.selectRoom(domain.Room{ID: mx + "!a:x", Name: "Alpha"})
	m = next.setMessages([]domain.Message{
		{ID: mx + "$x", RoomID: mx + "!a:x", Sender: "matrix:@ezra:example.org", Body: "hi", Timestamp: at(1)},
		{ID: mx + "$y", RoomID: mx + "!a:x", Sender: danaMatrix, Body: "hello", Timestamp: at(2)},
	})
	m.timeline.selected = mx + "$y"
	m.focus = paneTimeline
	return m
}

// A key on a message lists everything its sender said, by both of her accounts, in both
// rooms, newest at the bottom, under the room each run was in.
func TestEverythingSomeoneSaidOpensOnTheSender(t *testing.T) {
	t.Parallel()

	b := &saidBackend{msgs: danaSays(4)}
	m := saidModel(t, b, config.Config{})
	m, cmd := chord(t, m, "g", "I")
	if !m.said.open {
		t.Fatal("gI did not open what she said")
	}
	m = deliver(t, m, cmd)
	req := b.last()
	if !slices.Contains(req.Senders, danaMatrix) || !slices.Contains(req.Senders, danaWhatsApp) || !req.Rooms.All {
		t.Fatalf("asked %+v, want both of her accounts, everywhere", req)
	}
	pane := stripStyles(m.renderTimeline(80, 28))
	for _, want := range []string{"Everything Dana said", "Alpha", "Bravo", "said 0", "said 3"} {
		if !strings.Contains(pane, want) {
			t.Errorf("the view is missing %q:\n%s", want, pane)
		}
	}
	if strings.Index(pane, "said 3") < strings.Index(pane, "said 0") {
		t.Errorf("the newest is not at the bottom:\n%s", pane)
	}
	if got, ok := m.said.selected(); !ok || got.ID != mx+"$d003" {
		t.Errorf("cursor on %v, want the newest", got.ID)
	}
	m, _ = press(t, m, keyText("esc"))
	if m.said.open {
		t.Error("esc did not close it")
	}
}

// Going up reads further back, from before the oldest held; an empty page is where the
// cache starts, and says so.
func TestEverythingSomeoneSaidPagesBackToTheStartOfTheCache(t *testing.T) {
	t.Parallel()

	b := &saidBackend{msgs: danaSays(saidPage + 30)}
	m := saidModel(t, b, config.Config{})
	m, cmd := m.saidOfSender()
	m = deliver(t, m, cmd)
	if n := len(m.said.msgs); n != saidPage {
		t.Fatalf("first page holds %d, want %d", n, saidPage)
	}
	oldest := m.said.msgs[0]
	m, cmd = chord(t, m, "g", "g")
	if cmd == nil {
		t.Fatal("reaching the top read nothing older")
	}
	m = deliver(t, m, cmd)
	if before := b.last().Before; before.ID != oldest.ID || !before.Timestamp.Equal(oldest.Timestamp) {
		t.Errorf("asked before %+v, want the oldest held %s", before, oldest.ID)
	}
	if n := len(m.said.msgs); n != saidPage+30 {
		t.Fatalf("after the second page %d held, want %d", n, saidPage+30)
	}
	if got, _ := m.said.selected(); got.ID != oldest.ID {
		t.Errorf("cursor moved to %s with the page; want it kept on %s", got.ID, oldest.ID)
	}
	m, cmd = chord(t, m, "g", "g")
	m = deliver(t, m, cmd)
	if !m.said.atStart {
		t.Fatal("an empty page did not mark the start of the cache")
	}
	if pane := stripStyles(strings.Join(m.saidLines(80, 26), "\n")); !strings.Contains(pane, "the start of what kith has cached") ||
		!strings.Contains(m.saidSummary(), "from the oldest kith has cached") {
		t.Errorf("the start of the cache is not shown as one:\n%s", pane)
	}
}

// The scope key narrows to the selected message's room, then its network, then back to
// everywhere; a page asked for before a narrowing is dropped.
func TestEverythingSomeoneSaidNarrowsToARoomAndANetwork(t *testing.T) {
	t.Parallel()

	b := &saidBackend{msgs: danaSays(6)}
	m := saidModel(t, b, config.Config{})
	m, cmd := m.saidOfSender()
	stale := cmd
	m = deliver(t, m, cmd)
	// The newest is her WhatsApp account's, in Bravo.
	m, cmd = press(t, m, keyCode(tea.KeyTab))
	m = deliver(t, m, cmd)
	if req := b.last(); req.Rooms.All || !slices.Equal(req.Rooms.IDs, []domain.RoomID{mx + "!b:x"}) {
		t.Fatalf("first narrowing asked %+v, want Bravo only", req.Rooms)
	}
	if !strings.Contains(m.saidTitle(), "in Bravo") {
		t.Errorf("title %q does not say where", m.saidTitle())
	}
	m, cmd = press(t, m, keyCode(tea.KeyTab))
	m = deliver(t, m, cmd)
	if req := b.last(); !req.Rooms.All || !slices.Equal(req.Senders, []string{danaWhatsApp}) {
		t.Fatalf("second narrowing asked %+v, want her WhatsApp account everywhere", req)
	}
	held := len(m.said.msgs)
	m = deliver(t, m, stale)
	if len(m.said.msgs) != held {
		t.Error("a page asked for before the narrowing was filed")
	}
	m, cmd = press(t, m, keyCode(tea.KeyTab))
	m = deliver(t, m, cmd)
	if req := b.last(); !req.Rooms.All || len(req.Senders) != 2 {
		t.Fatalf("third press asked %+v, want everything again", req)
	}
}

// enter goes to the message in its room.
func TestEverythingSomeoneSaidGoesToAMessage(t *testing.T) {
	t.Parallel()

	b := &saidBackend{msgs: danaSays(3)}
	m := saidModel(t, b, config.Config{})
	m, cmd := m.saidOfSender()
	m = deliver(t, m, cmd)
	m, _ = press(t, m, keyText("k")) // $d001, in Bravo
	m, _ = press(t, m, keyCode(tea.KeyEnter))
	if m.said.open || m.openRoom != mx+"!b:x" || m.jump.to != mx+"$d001" {
		t.Errorf("open=%v room=%s jump=%s, want Bravo with the message armed", m.said.open, m.openRoom, m.jump.to)
	}
}

// Someone a muted tag hides is shown here, and the view says why the rest of kith does not.
func TestEverythingAHiddenPersonSaidIsShownMarked(t *testing.T) {
	t.Parallel()

	off := false
	b := &saidBackend{msgs: danaSays(2)}
	m := saidModel(t, b, config.Config{Tags: []config.Tag{{Name: "Spam", Notifies: &off, People: []string{danaWhatsApp}}}})
	m, cmd := m.openSaid(danaMatrix, "Dana")
	m = deliver(t, m, cmd)
	if len(m.said.msgs) != 2 {
		t.Fatalf("held %d, want both: hidden people are listed", len(m.said.msgs))
	}
	if !strings.Contains(m.saidSummary(), "hidden in every room") {
		t.Errorf("summary %q does not say she is hidden", m.saidSummary())
	}
}

// `:from ` offers the people who have written; accepting one opens what they said.
func TestFromCompletesAPersonAndOpensWhatTheySaid(t *testing.T) {
	t.Parallel()

	b := &saidBackend{msgs: danaSays(2), people: []domain.Member{
		{UserID: "matrix:@ezra:example.org", DisplayName: "Ezra"}, {UserID: danaWhatsApp, DisplayName: "Dana Levi"},
	}}
	m := saidModel(t, b, config.Config{})
	m, _ = press(t, m, keyText(":"))
	var cmd tea.Cmd
	for _, r := range "from " {
		m, cmd = press(t, m, keyText(string(r)))
	}
	if !m.completion.active || m.completion.trigger != fromTrigger {
		t.Fatalf("`:from ` opened no person popup (active=%v trigger=%q)", m.completion.active, m.completion.trigger)
	}
	m = deliver(t, m, cmd)
	m, _ = press(t, m, keyText("D"))
	labels := make([]string, 0, len(m.completion.candidates))
	for _, c := range m.completion.candidates {
		labels = append(labels, stripIsolates(c.label))
	}
	if !slices.Contains(labels, "Dana") {
		t.Fatalf("offered %v, want Dana", labels)
	}
	m, cmd = press(t, m, keyCode(tea.KeyTab))
	m = deliver(t, m, cmd)
	if !m.said.open || !slices.Contains(m.said.ids, danaMatrix) {
		t.Fatalf("accepting opened %+v, want what Dana said", m.said)
	}
}

// A phonebook card has a row for everything its person said.
func TestACardOpensEverythingItsPersonSaid(t *testing.T) {
	t.Parallel()

	m, _ := carding(t, nil)
	m = cardOf(t, m, "eli")
	m = pickLabel(t, m, "Everything they said")
	if !m.said.open || m.said.id != "matrix:@eli:x" || m.picker.active() {
		t.Errorf("said=%v on %q, picker up=%v; want what Eli said, the card closed", m.said.open, m.said.id, m.picker.active())
	}
}

// Search's from: with an ID is the person, by every ID kith links to them.
func TestSearchFromAnIDIsThePerson(t *testing.T) {
	t.Parallel()

	b := &saidBackend{}
	m := saidModel(t, b, config.Config{})
	m, cmd := m.openSearchFor(true, "from:"+danaMatrix)
	_ = deliver(t, m, cmd)
	if len(b.searched) == 0 {
		t.Fatal("nothing was searched")
	}
	f := b.searched[len(b.searched)-1].Filter
	if f.Sender != "" || !slices.Contains(f.Senders, danaWhatsApp) || !slices.Contains(f.Senders, danaMatrix) {
		t.Errorf("searched %+v, want both of her accounts", f)
	}
}
