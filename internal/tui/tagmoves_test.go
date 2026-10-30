package tui

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// moved is the jumping fixture (the starter's tags, `!` bound to Spam) with the filters
// having moved rooms as verdicts say.
func moved(t *testing.T, verdicts ...domain.TagVerdict) Model {
	t.Helper()
	m, _ := jumping(t).handleVerdictsLoaded(verdictsLoadedMsg{verdicts: verdicts})
	return serveRail(m)
}

func spamMove(room domain.RoomID) domain.TagVerdict {
	return domain.TagVerdict{Room: room, Tag: "spam", Rule: domain.MoveFirstMessage, Filter: "crypto"}
}

// rowHoldsRoom reports whether a rail row admits the room.
func rowHoldsRoom(t *testing.T, m Model, key string, id domain.RoomID) bool {
	t.Helper()
	g, ok := findGroup(m.rail.groups, key)
	if !ok {
		return false
	}
	room, _ := m.roomByID(id)
	return g.admits(m.unreadView(), room)
}

// A room a filter moved into the starter's Spam leaves its space and every other tag,
// as Spam's exclusive says; nothing else moves with it.
func TestAFilterMovedRoomGoesWhereItsTagSays(t *testing.T) {
	t.Parallel()
	m := moved(t, spamMove(mx+"!a:x"))
	if !rowHoldsRoom(t, m, spamGroupKey, mx+"!a:x") {
		t.Fatal("the Spam row does not hold the room a filter moved there")
	}
	for _, key := range []string{homeGroupKey, "Work"} {
		if rowHoldsRoom(t, m, key, mx+"!a:x") {
			t.Errorf("%s still lists a room the Spam tag takes from everywhere", key)
		}
	}
	if !rowHoldsRoom(t, m, homeGroupKey, mx+"!ops:x") {
		t.Error("a room no filter moved went missing from All")
	}
	if _, ok := findGroup(jumping(t).rail.groups, spamGroupKey); ok {
		t.Error("a Spam row exists with nothing in it")
	}
}

// The tag's excluded list outranks a filter's move; counts_unread = false takes a moved
// room out of the totals.
func TestExcludedOutranksAMoveAndAMutedTagStopsCounting(t *testing.T) {
	t.Parallel()
	m := moved(t, spamMove(mx+"!a:x"), spamMove(mx+"!ops:x"))
	cfg := m.conf.base.Clone()
	for i := range cfg.Tags {
		if cfg.Tags[i].Name == "Spam" {
			cfg.Tags[i].Excluded = []string{mx + "!a:x"}
		}
	}
	m, _ = m.applyConfig(cfg, "")
	m = serveRail(m)
	if rowHoldsRoom(t, m, spamGroupKey, mx+"!a:x") || !rowHoldsRoom(t, m, "Work", mx+"!a:x") {
		t.Error("an excluded room was kept in Spam by a filter's move")
	}
	m.prefs.unreadLocal = true
	m.unread = withEntry(m.unread, mx+"!ops:x", domain.Unread{Messages: 2, Counted: true})
	m.unread = withEntry(m.unread, mx+"!a:x", domain.Unread{Messages: 2, Counted: true})
	m = serveRail(m)
	if view := m.unreadView(); view.tallies(domain.Room{ID: mx + "!ops:x"}) || !view.tallies(domain.Room{ID: mx + "!a:x"}) {
		t.Error("counts_unread = false does not decide which room counts")
	}
}

// tagMarker records the moves and releases it is asked to write.
type tagMarker struct {
	apitest.Nop

	mu     sync.Mutex
	marked []domain.TagVerdict
}

func (b *tagMarker) MarkTag(_ context.Context, verdict domain.TagVerdict) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.marked = append(b.marked, verdict)
	return nil
}

func (b *tagMarker) written() []domain.TagVerdict {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]domain.TagVerdict(nil), b.marked...)
}

func spamTagOf(t *testing.T, cfg config.Config) config.Tag {
	t.Helper()
	at := slices.IndexFunc(cfg.Tags, func(tag config.Tag) bool { return tag.Name == "Spam" })
	if at < 0 {
		t.Fatal("no Spam tag")
	}
	return cfg.Tags[at]
}

// `!` files the room in Spam by hand (picked) and takes it back out; nothing is written
// to account data for a room no filter moved.
func TestTheTagKeyFilesInAndOut(t *testing.T) {
	t.Parallel()
	backend := &tagMarker{}
	m := openedFromList(t, jumping(t), mx+"!a:x")
	m.backend = backend
	m.focus, m.compose.insertMode = paneRooms, false

	next, cmd := pressed(t, m, "!")
	m = deliver(t, next, cmd)
	if got := spamTagOf(t, m.conf.base); !slices.Contains(got.Picked, mx+"!a:x") {
		t.Fatalf("Spam picked = %v, want the room", got.Picked)
	}
	if !rowHoldsRoom(t, m, spamGroupKey, mx+"!a:x") {
		t.Fatal("the room is not in Spam after the key")
	}
	m = openedFromList(t, m, mx+"!a:x")
	m.focus, m.compose.insertMode = paneRooms, false
	next, cmd = pressed(t, m, "!")
	m = deliver(t, next, cmd)
	if got := spamTagOf(t, m.conf.base); slices.Contains(got.Picked, mx+"!a:x") || slices.Contains(got.Excluded, mx+"!a:x") {
		t.Errorf("Spam picked %v, excluded %v; want the room in neither", got.Picked, got.Excluded)
	}
	if len(backend.written()) != 0 {
		t.Errorf("wrote %+v, want nothing for a room no filter moved", backend.written())
	}
}

// pressed presses one key and returns what it produced.
func pressed(t *testing.T, m Model, key string) (Model, tea.Cmd) {
	t.Helper()
	return press(t, m, keyText(key))
}

// Taking out a room a filter moved writes the tag's excluded and a release, so no filter
// on any machine moves it back; the S picker does the same.
func TestTakingOutAMovedRoomReleasesItEverywhere(t *testing.T) {
	t.Parallel()
	backend := &tagMarker{}
	m := moved(t, spamMove(mx+"!a:x"))
	m.backend = backend
	m = openedFromList(t, m, mx+"!a:x")
	m.focus, m.compose.insertMode = paneRooms, false

	next, cmd := pressed(t, m, "!")
	m = deliver(t, next, cmd)
	if got := spamTagOf(t, m.conf.base); !slices.Contains(got.Excluded, mx+"!a:x") {
		t.Fatalf("Spam excluded = %v, want the room", got.Excluded)
	}
	written := backend.written()
	if len(written) != 1 || written[0].Room != mx+"!a:x" || !written[0].Released || !domain.SameTag(written[0].Tag, "Spam") || written[0].At.IsZero() {
		t.Fatalf("written = %+v, want one timed release of !a:x from Spam", written)
	}
	if rowHoldsRoom(t, m, spamGroupKey, mx+"!a:x") {
		t.Error("the room is still in Spam")
	}
}

// A release from another machine takes the room out without a local excluded entry.
func TestAReleaseFromAnotherMachineTakesTheRoomOut(t *testing.T) {
	t.Parallel()
	m := moved(t, spamMove(mx+"!a:x"), domain.TagVerdict{Room: mx + "!ops:x", Tag: "spam", Released: true})
	if !rowHoldsRoom(t, m, spamGroupKey, mx+"!a:x") || rowHoldsRoom(t, m, spamGroupKey, mx+"!ops:x") {
		t.Error("Spam does not follow the moves and the release")
	}
	if got := m.moveReasons(domain.Room{ID: mx + "!ops:x"}); len(got) != 0 {
		t.Errorf("why for a released room = %q, want nothing", got)
	}
}

// /why names the tag, the filter and the rule, and that the tag does not notify.
func TestWhyNamesTheMoveAndTheMute(t *testing.T) {
	t.Parallel()
	m := openedFromList(t, moved(t, spamMove(mx+"!a:x")), mx+"!a:x")
	lines := strings.Join(m.whyLines(time.Now()), "\n")
	for _, want := range []string{"in Spam", "crypto", "first message", "does not notify"} {
		if !strings.Contains(lines, want) {
			t.Errorf("why lacks %q:\n%s", want, lines)
		}
	}
}

// The help lists tag keys, and one bound to a missing tag says so when pressed.
func TestTagKeysAreListedAndAMissingTagIsSaid(t *testing.T) {
	t.Parallel()
	m := jumping(t)
	if help := stripStyles(strings.Join(m.tagKeyHelp(12), "\n")); !strings.Contains(help, "!") || !strings.Contains(help, "Spam") {
		t.Errorf("help = %q, want ! and Spam", help)
	}
	dev := m.conf.device.Clone()
	dev.Keys.Tag = config.TagKeys{{Chord: "!", Tag: "Junk"}}
	m, _ = m.applyDevice(dev, "")
	m = openedFromList(t, m, mx+"!a:x")
	m.focus, m.compose.insertMode = paneRooms, false
	m, _ = pressed(t, m, "!")
	if !strings.Contains(m.status(), "Junk") {
		t.Errorf("status = %q, want the missing tag named", m.status())
	}
}

// tagRetagger records the retags it is asked for.
type tagRetagger struct {
	tagMarker
	retags [][2]string
}

func (b *tagRetagger) RetagVerdicts(_ context.Context, from, to string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.retags = append(b.retags, [2]string{from, to})
	return nil
}

// Renaming a tag carries its filter moves to the new name; deleting it drops them.
func TestRenamingOrDeletingATagCarriesItsMoves(t *testing.T) {
	t.Parallel()
	backend := &tagRetagger{}
	m := moved(t, spamMove(mx+"!a:x"))
	m.backend = backend
	m.choosing.tag = tagEditing{tag: "Spam"}
	next, cmd := m.submitTagName("Junk")
	m = deliver(t, next, cmd)
	next, cmd = m.deleteTag("Junk")
	deliver(t, next, cmd)
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if want := [][2]string{{"Spam", "Junk"}, {"Junk", ""}}; !slices.Equal(backend.retags, want) {
		t.Errorf("retags = %v, want %v", backend.retags, want)
	}
}
