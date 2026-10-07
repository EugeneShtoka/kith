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
	"github.com/EugeneShtoka/kith/internal/notify"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// counting builds a room list where the two definitions of "unread" disagree: Alpha is
// a muted room — the homeserver says nothing would have notified you, while three
// messages sit there unread — and Bravo is an ordinary noisy one.
func counting(t *testing.T, display config.Display) Model {
	t.Helper()
	m := update(t, starterNew(&bulkReader{}, display), roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
		{ID: "!b:x", Name: "Bravo"},
	}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x", "!b:x"}},
	}})
	m = update(t, m, unreadMsg{list: []domain.Unread{
		{RoomID: "!a:x", Notifications: 0, Highlights: 0, Messages: 3, Mentions: 1, Counted: true},
		{RoomID: "!b:x", Notifications: 2, Highlights: 0, Messages: 2, Counted: true},
	}})
	return sized(t, m.clearStatus())
}

func roomByName(t *testing.T, m Model, id domain.RoomID) domain.Room {
	t.Helper()
	room, ok := m.roomByID(id)
	if !ok {
		t.Fatalf("room %s is not in the list", id)
	}
	return room
}

// The badge counts what has not been read.
func TestBadgeCountsUnreadMessagesNotNotifications(t *testing.T) {
	t.Parallel()

	m := counting(t, config.Display{})
	badge, highlight := m.unreadBadge(roomByName(t, m, "!a:x"))
	if badge != "●3" || !highlight {
		t.Errorf("muted room badge = %q (highlight %v), want ●3 highlighted", badge, highlight)
	}

	// And the old meaning is still available, exactly as it was.
	server := counting(t, config.Display{Unread: "notifications"})
	if badge, _ := server.unreadBadge(roomByName(t, server, "!a:x")); badge != "" {
		t.Errorf("with unread=notifications the muted room badged %q, want nothing", badge)
	}
}

// picking is m with these rooms in the named tag's picked list, applied as the
// filing picker applies it.
func picking(t *testing.T, m Model, tag string, rooms ...string) Model {
	t.Helper()
	cfg := m.conf.base.Clone()
	found := false
	for i := range cfg.Tags {
		if cfg.Tags[i].Name == tag {
			cfg.Tags[i].Picked, found = rooms, true
		}
	}
	if !found {
		t.Fatalf("no tag %q in the config", tag)
	}
	next, _ := m.applyConfig(cfg, "")
	return next.clearStatus()
}

// The starter's Archived tag keeps the room and takes it out of the totals.
func TestArchivedRoomKeepsItsRowAndLeavesTheTotals(t *testing.T) {
	t.Parallel()

	m := picking(t, counting(t, config.Display{}), "Archived", "!a:x")
	badge, highlight := m.unreadBadge(roomByName(t, m, "!a:x"))
	if badge != "·3" || highlight {
		t.Errorf("archived room badge = %q (highlight %v), want ·3 unhighlighted", badge, highlight)
	}

	// The group sums only what counts, and the Unread group holds only what counts.
	work := m.rail.groups[indexOfGroup(m.rail.groups, "Work")]
	if n, _ := m.groupUnread(work); n != 2 {
		t.Errorf("Work group total = %d, want 2 — the archived room's three excluded", n)
	}
	if rooms := m.unreadIn(m.rail.groups[indexOfGroup(m.rail.groups, unreadGroupKey)]); len(rooms) != 1 || rooms[0] != "!b:x" {
		t.Errorf("Unread group = %v, want just the unarchived room", rooms)
	}
}

// A space whose only unread room is archived is marked read, not refused as having
// nothing unread: the room stays in its own network's space, and its row says it has.
func TestMarkingASpaceReadReadsItsArchivedRooms(t *testing.T) {
	t.Parallel()
	m := picking(t, counting(t, config.Display{}), "Archived", "!a:x")
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x", "!b:x"}, Bridge: domain.ProtocolTelegram},
	}})
	m.unread["!b:x"] = domain.Unread{RoomID: "!b:x"}
	m.rail.cursor = indexOfGroup(m.rail.groups, "Work")
	m, _ = m.askMarkGroupRead()
	if m.confirm.action != pendingMarkGroupRead || !slices.Equal(m.confirm.rooms, []domain.RoomID{"!a:x"}) {
		t.Errorf("confirm = %+v, status %q; want to mark the archived room read", m.confirm, m.status())
	}
}

// /tag is a toggle and says which way it went, because nothing else on screen changes
// enough to tell; a new name makes the tag.
func TestTagCommandTogglesAndReportsWhichWay(t *testing.T) {
	t.Parallel()

	m := counting(t, config.Display{})
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, homeGroupKey)
	m, _ = m.selectRoom(roomByName(t, m, "!a:x"))

	m, _ = m.toggleTag("archived", roomByName(t, m, "!a:x"))
	if got := m.conf.base.Tags[tagIndex(t, m, "Archived")].Picked; len(got) != 1 || got[0] != "!a:x" {
		t.Fatalf("after /tag Archived the tag picks %v, want the room's ID", got)
	}
	if !strings.Contains(m.status(), "Alpha is in Archived") {
		t.Errorf("status = %q, want it to say the room went in", m.status())
	}
	// Archived is exclusive, so the room left All, and the selection moved to its
	// neighbor rather than staying on a row that is no longer there.
	if m.openRoom == "!a:x" {
		t.Fatal("the open room is one the list no longer holds")
	}

	m, _ = m.toggleTag("Archived", roomByName(t, m, "!a:x"))
	if got := m.conf.base.Tags[tagIndex(t, m, "Archived")]; len(got.Picked) != 0 || len(got.Excluded) != 0 {
		t.Errorf("after /tag Archived again the tag is %+v, want it to say nothing about the room", got)
	}
	if !strings.Contains(m.status(), "Alpha is out of Archived") {
		t.Errorf("status = %q, want it to say the room came out", m.status())
	}
	// A name no tag has makes the tag, with the room in it.
	m, _ = m.toggleTag("Later", roomByName(t, m, "!a:x"))
	if got := m.conf.base.Tags[tagIndex(t, m, "Later")].Picked; len(got) != 1 || got[0] != "!a:x" {
		t.Errorf("after /tag Later the new tag picks %v, want the room's ID", got)
	}
	if !strings.Contains(m.status(), "Alpha is in Later") {
		t.Errorf("status = %q, want it to say the room went in", m.status())
	}
}

func tagIndex(t *testing.T, m Model, name string) int {
	t.Helper()
	for i := range m.conf.base.Tags {
		if m.conf.base.Tags[i].Name == name {
			return i
		}
	}
	t.Fatalf("no tag %q", name)
	return -1
}

// A room a tag holds by its rule is taken out by excluding that room alone: the rule
// keeps its other rooms.
func TestTakingARoomOutOfARuleExcludesOnlyIt(t *testing.T) {
	t.Parallel()

	m := counting(t, config.Display{})
	cfg := m.conf.base.Clone()
	cfg.Tags = append(cfg.Tags, config.Tag{Name: "Job", Rule: []string{"space:Work"}})
	m, _ = m.applyConfig(cfg, "")

	m, _ = m.toggleTag("Job", roomByName(t, m, "!a:x"))
	job := m.conf.base.Tags[tagIndex(t, m, "Job")]
	if len(job.Rule) != 1 || len(job.Excluded) != 1 || job.Excluded[0] != "!a:x" {
		t.Errorf("Job = %+v, want its rule kept and Alpha excluded", job)
	}
	if got := roomsIn(t, m, "tag:Job"); len(got) != 1 || got[0] != "!b:x" {
		t.Errorf("Job holds %v, want Bravo alone", got)
	}
}

// spaced builds a rail with both kinds of space: one a bridge made (every room it
// carries lives there) and one filed by hand.
func spaced(t *testing.T, display config.Display) Model {
	t.Helper()
	m := update(t, starterNew(&bulkReader{}, display), roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
		{ID: "!b:x", Name: "Bravo"},
	}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!wa:x", Name: "WhatsApp UK", Children: []domain.RoomID{"!a:x", "!b:x"}},
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x", "!b:x"}},
	}})
	return sized(t, m.clearStatus())
}

// hasGroup reports whether the rail carries a group with this key.
func hasGroup(groups []group, key string) bool {
	for _, g := range groups {
		if g.key == key {
			return true
		}
	}
	return false
}

// roomsIn lists what a named rail group shows.
func roomsIn(t *testing.T, m Model, key string) []domain.RoomID {
	t.Helper()
	if !hasGroup(m.rail.groups, key) {
		t.Fatalf("no rail group %q; have %v", key, groupKeys(m.rail.groups))
	}
	g := m.rail.groups[indexOfGroup(m.rail.groups, key)]
	var ids []domain.RoomID
	for i := range m.rooms.all {
		if g.admits(m.unreadView(), m.rooms.all[i]) {
			ids = append(ids, m.rooms.all[i].ID)
		}
	}
	return ids
}

// Archived (space_exclusive) takes a room out of the spaces you filed it into, All
// and DMs; the space it belongs to keeps it.
func TestArchivedRoomLeavesEverythingButItsOwnSpace(t *testing.T) {
	t.Parallel()

	m := picking(t, spaced(t, config.Display{}), "Archived", "!a:x")
	// The canonical parent as the daemon would resolve it: the space the bridge put the
	// room in when it created the portal.
	m = update(t, m, parentsMsg{parents: map[domain.RoomID]domain.SpaceID{"!a:x": "!wa:x"}})

	if got := roomsIn(t, m, "Work"); len(got) != 1 || got[0] != "!b:x" {
		t.Errorf("Work = %v, want only the unarchived room — it was filed there, not born there", got)
	}
	if got := roomsIn(t, m, "WhatsApp UK"); len(got) != 2 {
		t.Errorf("WhatsApp UK = %v, want both rooms — this is where the archived one lives", got)
	}
	if got := roomsIn(t, m, homeGroupKey); len(got) != 1 || got[0] != "!b:x" {
		t.Errorf("All = %v, want the archived room gone from it too", got)
	}
	if got := roomsIn(t, m, archivedGroupKey); len(got) != 1 || got[0] != "!a:x" {
		t.Errorf("Archived = %v, want the archived room", got)
	}
}

// Before the answer arrives — and for a room that has no canonical parent at all, which
// is the ordinary case for a room you added to a space yourself — the archived room is
// in exactly one place.
func TestArchivedRoomWithNoKnownParentIsOnlyInTheArchivedGroup(t *testing.T) {
	t.Parallel()

	m := picking(t, spaced(t, config.Display{}), "Archived", "!a:x")
	for _, key := range []string{homeGroupKey, "Work", "WhatsApp UK"} {
		if got := roomsIn(t, m, key); len(got) != 1 || got[0] != "!b:x" {
			t.Errorf("%s = %v, want only the unarchived room", key, got)
		}
	}
	if got := roomsIn(t, m, archivedGroupKey); len(got) != 1 || got[0] != "!a:x" {
		t.Errorf("Archived = %v, want the archived room", got)
	}
}

// The starter's Archived row hides while empty: with nothing filed away the rail does
// not show it, and it appears the moment a room goes in, without a restart.
func TestArchivedGroupExistsOnlyWhileSomethingIsArchived(t *testing.T) {
	t.Parallel()

	quiet := spaced(t, config.Display{})
	if hasGroup(quiet.rail.groups, archivedGroupKey) {
		t.Errorf("rail = %v, want no archived group with nothing archived", groupKeys(quiet.rail.groups))
	}
	m, _ := quiet.toggleTag("Archived", roomByName(t, quiet, "!a:x"))
	if !hasGroup(m.rail.groups, archivedGroupKey) {
		t.Errorf("rail = %v, want an archived group after archiving", groupKeys(m.rail.groups))
	}
}

// parentAsker records which rooms were asked about and answers for one of them, so the
// test can watch the resolution happen rather than injecting its result.
type parentAsker struct {
	apitest.Nop
	mu     sync.Mutex
	asked  []domain.RoomID
	answer map[domain.RoomID]domain.SpaceID
}

func (p *parentAsker) CanonicalParent(_ context.Context, roomID domain.RoomID) (domain.SpaceID, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, roomID)
	return p.answer[roomID], nil
}

func (p *parentAsker) askedRooms() []domain.RoomID {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]domain.RoomID(nil), p.asked...)
}

// Only rooms a space-exclusive tag holds are asked about, and each only once:
// resolving where a room lives costs the daemon room state, and asking for all of them
// would turn a rail rebuild into a hierarchy walk.
func TestParentsAreResolvedOnlyForArchivedRooms(t *testing.T) {
	t.Parallel()

	asker := &parentAsker{answer: map[domain.RoomID]domain.SpaceID{"!a:x": "!wa:x"}}
	m := picking(t, starterNew(asker, config.Display{}), "Archived", "!a:x")
	m = update(t, m, roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}, {ID: "!b:x", Name: "Bravo"}}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!wa:x", Name: "WhatsApp UK", Children: []domain.RoomID{"!a:x", "!b:x"}},
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x", "!b:x"}},
	}})
	m = sized(t, m)

	// The resolve command rides with the room list; run it and feed the answer back.
	_, cmd := m.handleRooms(roomsMsg{rooms: m.rooms.all})
	if cmd == nil {
		t.Fatal("no command to resolve the archived room's parent")
	}
	m = update(t, m, cmd())

	if got := asker.askedRooms(); len(got) != 1 || got[0] != "!a:x" {
		t.Errorf("asked about %v, want only the archived room", got)
	}
	if m.parents["!a:x"] != "!wa:x" {
		t.Errorf("parents = %v, want the resolved space", m.parents)
	}
	if got := roomsIn(t, m, "WhatsApp UK"); len(got) != 2 {
		t.Errorf("WhatsApp UK = %v, want the archived room back once its parent is known", got)
	}

	// Asked once: a second pass over the same rooms has nothing left to ask.
	if cmd := m.resolveParentsCmd(); cmd != nil {
		t.Error("a resolved room was queued for resolution again")
	}
}

// The starter's Pinned tag is additive: a pinned room is in Pinned **and** in its own
// space.
func TestPinnedGroupIsAdditive(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x"}},
	}})
	m = picking(t, m, "Pinned", "!a:x")

	if got := roomsIn(t, m, pinnedGroupKey); len(got) != 1 || got[0] != "!a:x" {
		t.Errorf("Pinned = %v, want the pinned room", got)
	}
	if got := roomsIn(t, m, "Work"); len(got) != 1 || got[0] != "!a:x" {
		t.Error("pinning took the room out of its own space; it is a hand-raise, not a move")
	}
}

// A tag is a place a rule can name, and it ranks as a class — which is what lets a
// pinned room through a do-not-disturb without any pierce concept at all.
func TestATagIsNarrowerThanGlobalSilence(t *testing.T) {
	t.Parallel()

	none, all := notify.LevelNone, notify.LevelAll
	rules := []notify.Rule{
		// Do-not-disturb: global scope, temporary, the newer statement.
		{Name: "dnd", Show: &none, Temp: true},
		// And the standing rule that says pinned conversations still reach you.
		{Name: "pinned", Match: "tag:Pinned", Show: &all},
	}
	pinned := setup.Place{Room: domain.RoomFacts{ID: "!a:x", Tags: []string{"Pinned"}}}
	got := notify.Resolve(rules, notify.Scope{Room: pinned}, time.Now())
	if got.Show != notify.LevelAll {
		t.Errorf("a pinned room was silenced by a global DND; the rule naming it is narrower")
	}
	// And an unpinned one stays silent, which is what makes the first answer mean
	// something.
	plain := setup.Place{Room: domain.RoomFacts{ID: "!b:x"}}
	quiet := notify.Resolve(rules, notify.Scope{Room: plain}, time.Now())
	if quiet.Show != notify.LevelNone {
		t.Error("an unpinned room got through the silence too")
	}
}
