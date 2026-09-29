package tui

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// spamming is the jumping fixture with `[spam]` set from a config.
func spamming(t *testing.T, spam config.Spam) Model {
	t.Helper()
	m := jumping(t)
	cfg := m.conf.base.Clone()
	cfg.Spam = spam
	next, _ := m.applyConfig(cfg, "", "")
	out := next
	return out.clearStatus()
}

// A room in Spam leaves the group it was in (unlike Archive).
func TestSpamIsAMoveNotATag(t *testing.T) {
	t.Parallel()

	m := spamming(t, config.Spam{Rooms: []string{"!a:x"}})

	spam, ok := findGroup(m.rail.groups, spamGroupKey)
	if !ok {
		t.Fatal("no Spam group with a room in it")
	}
	if !spam.admits(m.unreadView(), domain.Room{ID: "!a:x", Name: "Alpha"}) {
		t.Error("the Spam group does not hold the room that was marked")
	}
	for _, key := range []string{"home", "Work"} {
		g, found := findGroup(m.rail.groups, key)
		if !found {
			t.Fatalf("no %q group", key)
		}
		if g.admits(m.unreadView(), domain.Room{ID: "!a:x", Name: "Alpha"}) {
			t.Errorf("%q still lists a room that is in Spam — this is a move, not a tag", key)
		}
	}
	// And nothing else moved with it.
	home, _ := findGroup(m.rail.groups, "home")
	if !home.admits(m.unreadView(), domain.Room{ID: "!ops:x", Name: "Ops"}) {
		t.Error("a room nobody marked went missing from All")
	}
}

// With nothing caught there is no Spam group.
func TestNoSpamGroupUntilSomethingIsCaught(t *testing.T) {
	t.Parallel()

	if _, ok := findGroup(jumping(t).rail.groups, spamGroupKey); ok {
		t.Fatal("a Spam group exists with nothing in it")
	}
}

// The exemption outranks everything, which is what makes releasing a room final.
func TestTheExemptionOutranksTheList(t *testing.T) {
	t.Parallel()

	m := spamming(t, config.Spam{Rooms: []string{"dm"}, Except: []string{"!dana:x"}})
	view := m.unreadView()
	if view.isSpam(domain.Room{ID: "!dana:x", Name: "Dana Levi", IsDirect: true}) {
		t.Error("an exempt room was caught by an entry naming its kind")
	}
	if _, ok := findGroup(m.rail.groups, spamGroupKey); ok {
		t.Error("a Spam group exists though the only room matching is exempt")
	}
}

// The exemption beats a rule.
func TestTheExemptionOutranksARule(t *testing.T) {
	t.Parallel()

	m := spamming(t, config.Spam{Except: []string{"!a:x"}})
	m.rail.caught = map[domain.RoomID]domain.SpamVerdict{
		"!a:x":   {Room: "!a:x", Rule: domain.SpamFirstMessage, Filter: "crypto"},
		"!ops:x": {Room: "!ops:x", Rule: domain.SpamMostly, Filter: "crypto"},
	}
	m = m.refreshPlaces()

	view := m.unreadView()
	if view.isSpam(domain.Room{ID: "!a:x", Name: "Alpha"}) {
		t.Error("a rule outranked an exemption")
	}
	if !view.isSpam(domain.Room{ID: "!ops:x", Name: "Ops"}) {
		t.Error("a room a rule caught is not in Spam")
	}
}

// A caught room is out of the counts too.
func TestSpamStopsCounting(t *testing.T) {
	t.Parallel()

	m := spamming(t, config.Spam{Rooms: []string{"!a:x"}})
	// Counted locally.
	m.prefs.unreadLocal = true
	m.unread[domain.RoomID("!a:x")] = domain.Unread{Messages: 4, Counted: true}
	m.unread[domain.RoomID("!ops:x")] = domain.Unread{Messages: 2, Counted: true}

	view := m.unreadView()
	if view.tallies(domain.Room{ID: "!a:x", Name: "Alpha"}) {
		t.Error("a room in Spam still counts toward the totals")
	}
	if !view.tallies(domain.Room{ID: "!ops:x", Name: "Ops"}) {
		t.Error("a room nobody marked stopped counting")
	}
}

// Marking lists the room; releasing exempts it.
func TestTheSpamKeyWritesBothDirections(t *testing.T) {
	t.Parallel()

	m := openedFromList(t, jumping(t), "!a:x")
	next, _ := m.toggleSpam()
	marked := next
	if !marked.rail.spam.Lists("!a:x") {
		t.Fatalf("[spam] rooms = %v, want the room listed", marked.rail.spam.Entries)
	}
	if !marked.unreadView().isSpam(domain.Room{ID: "!a:x", Name: "Alpha"}) {
		t.Error("the room is not in Spam after the key")
	}
	// Marking moves the pane on (the room left the list), so release from inside Spam.
	marked = openedFromList(t, marked, "!a:x")

	next, _ = marked.toggleSpam()
	released := next
	if released.rail.spam.Lists("!a:x") {
		t.Error("the room is still listed after being released")
	}
	// The half that matters: releasing writes a carve-out, so no rule can put it back.
	if !slices.Contains(released.rail.spam.Except, "!a:x") {
		t.Fatalf("[spam] except = %v, want the released room exempt", released.rail.spam.Except)
	}
}

// The explanation names the rule and the filter.
func TestWhyNamesTheRuleThatCaughtIt(t *testing.T) {
	t.Parallel()

	m := openedFromList(t, spamming(t, config.Spam{}), "!a:x")
	m.rail.caught = map[domain.RoomID]domain.SpamVerdict{
		"!a:x": {Room: "!a:x", Rule: domain.SpamFirstMessage, Filter: "crypto"},
	}
	m = m.refreshPlaces()

	lines := strings.Join(m.whyLines(time.Now()), "\n")
	if !strings.Contains(lines, "in Spam") {
		t.Fatalf("why does not mention Spam:\n%s", lines)
	}
	if !strings.Contains(lines, "crypto") {
		t.Errorf("why does not name the filter that caught it:\n%s", lines)
	}
	if !strings.Contains(lines, "first message") {
		t.Errorf("why does not name the rule:\n%s", lines)
	}
}

// spamMarker records the verdicts it is asked to write to account data.
type spamMarker struct {
	apitest.Nop

	mu     sync.Mutex
	marked []domain.SpamVerdict
}

func (b *spamMarker) MarkSpam(_ context.Context, verdict domain.SpamVerdict) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.marked = append(b.marked, verdict)
	return nil
}

func (b *spamMarker) written() []domain.SpamVerdict {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]domain.SpamVerdict(nil), b.marked...)
}

// Releasing a rule-caught room is also written to account data, for other machines.
func TestReleasingARuleCaughtRoomReleasesItEverywhere(t *testing.T) {
	t.Parallel()

	m := spamming(t, config.Spam{})
	backend := &spamMarker{}
	m.backend = backend
	m, _ = m.handleSpamLoaded(spamLoadedMsg{caught: []domain.SpamVerdict{
		{Room: "!a:x", Rule: domain.SpamFirstMessage, Filter: "crypto"},
	}})
	m = openedFromList(t, m, "!a:x")
	if !m.unreadView().isSpam(domain.Room{ID: "!a:x", Name: "Alpha"}) {
		t.Fatal("the caught room is not in Spam")
	}

	next, cmd := m.toggleSpam()
	released := deliver(t, next, cmd)

	written := backend.written()
	if len(written) != 1 || written[0].Room != "!a:x" || !written[0].Released || written[0].Spam() {
		t.Fatalf("account data written = %+v, want one release of !a:x", written)
	}
	if written[0].At.IsZero() {
		t.Error("the release carries no time")
	}
	// The local exemption stays.
	if !slices.Contains(released.rail.spam.Except, "!a:x") {
		t.Errorf("[spam] except = %v, want the released room exempt", released.rail.spam.Except)
	}
	if released.unreadView().isSpam(domain.Room{ID: "!a:x", Name: "Alpha"}) {
		t.Error("the released room is still in Spam")
	}
}

// A release from another machine takes the room out of Spam here, with no local
// exemption written.
func TestAReleaseFromAnotherMachineTakesTheRoomOut(t *testing.T) {
	t.Parallel()

	m := spamming(t, config.Spam{})
	m, _ = m.handleSpamLoaded(spamLoadedMsg{caught: []domain.SpamVerdict{
		{Room: "!a:x", Rule: domain.SpamFirstMessage, Filter: "crypto"},
		{Room: "!ops:x", Released: true},
	}})
	view := m.unreadView()
	if !view.isSpam(domain.Room{ID: "!a:x", Name: "Alpha"}) {
		t.Error("a caught room is not in Spam")
	}
	if view.isSpam(domain.Room{ID: "!ops:x", Name: "Ops"}) {
		t.Error("a room released elsewhere is in Spam")
	}
	if got := m.spamReason(domain.Room{ID: "!ops:x", Name: "Ops"}); got != "" {
		t.Errorf("why for a released room = %q, want nothing", got)
	}
}
