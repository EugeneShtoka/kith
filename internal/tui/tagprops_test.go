package tui

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

var off = false

// propsModel is a model with Alpha (in a space you made and a bridge's), Bravo (a
// DM) and Charlie (in the space you made), and the tags given.
func propsModel(t *testing.T, cfg config.Config) Model {
	t.Helper()
	m := configured(cfg)
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"}, {ID: "!b:x", Name: "Bravo", IsDirect: true}, {ID: "!c:x", Name: "Charlie"},
	}})
	return update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!made:x", Name: "Made", Children: []domain.RoomID{"!a:x", "!c:x"}},
		{ID: "!bridge:x", Name: "Bridge", Children: []domain.RoomID{"!a:x"}, Bridge: domain.ProtocolWhatsApp, Keeper: "@bot:x"},
	}})
}

func rowHolds(t *testing.T, m Model, key string, id domain.RoomID) bool {
	t.Helper()
	return admitsRoom(m, railRow(t, m, key), id)
}

// A silent tag's rooms count as read everywhere else: no badge, not in Unread, not
// `unread` to another tag. The silent tag itself still sees them as they are.
func TestASilentTagsRoomsCountAsRead(t *testing.T) {
	t.Parallel()
	m := propsModel(t, config.Config{Tags: []config.Tag{
		{Name: "Quiet", Rule: []string{"room:Alpha"}, CountsUnread: &off},
		{Name: "Busy", Rule: []string{"unread"}},
		{Name: "Pings", Rule: []string{"mention"}, CountsUnread: &off},
		{Name: "Unread", Rule: []string{"unread"}},
		{Name: "All", Rule: []string{"*"}},
	}})
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: "!a:x", Messages: 3, Counted: true}})
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: "!c:x", Messages: 1, Counted: true}})
	if !rowHolds(t, m, "tag:Quiet", "!a:x") {
		t.Error("the silent tag does not hold its unread room")
	}
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: "!b:x", Messages: 1, Mentions: 1, Counted: true}})
	if !rowHolds(t, m, "tag:Pings", "!b:x") {
		t.Error("a silent tag judged by its own state does not hold the mention it silences")
	}
	if rowHolds(t, m, "tag:Busy", "!a:x") || rowHolds(t, m, unreadGroupKey, "!a:x") {
		t.Error("a silenced room is still unread to another tag or to Unread")
	}
	if !rowHolds(t, m, "tag:Busy", "!c:x") {
		t.Error("an unread room not silenced left Busy")
	}
	if n, _ := m.groupUnread(railRow(t, m, homeGroupKey)); n != 1 {
		t.Errorf("All's badge = %d, want Charlie's 1 alone", n)
	}
}

// An exclusive tag shows its rooms alone, under no other tag; its spaces keep them.
// Of two exclusive tags, the first configured wins.
func TestAnExclusiveTagShowsItsRoomsAlone(t *testing.T) {
	t.Parallel()
	m := propsModel(t, config.Config{Tags: []config.Tag{
		{Name: "Fam", Rule: []string{"dm", "room:Alpha"}, Exclusive: true},
		{Name: "Later", Rule: []string{"room:Alpha"}, Exclusive: true},
		{Name: "Every", Rule: []string{"*"}},
		{Name: "All", Rule: []string{"*"}},
		{Name: "DMs", Rule: []string{"dm"}},
	}})
	for _, tc := range []struct {
		key  string
		room domain.RoomID
		want bool
	}{
		{"tag:Fam", "!b:x", true}, {"tag:Fam", "!a:x", true},
		{"tag:Later", "!a:x", false}, // Fam is first
		{"tag:Every", "!b:x", false}, {"tag:Every", "!c:x", true},
		{homeGroupKey, "!b:x", false}, {dmsGroupKey, "!b:x", false}, {homeGroupKey, "!c:x", true},
		{"Made", "!a:x", true}, {"Bridge", "!a:x", true},
	} {
		if got := rowHolds(t, m, tc.key, tc.room); got != tc.want {
			t.Errorf("%s holds %s = %t, want %t", tc.key, tc.room, got, tc.want)
		}
	}
}

// A space-exclusive tag takes its rooms out of the spaces a person made and out of
// other tags; the spaces they belong to (a bridge's) keep them, and so does another
// space-exclusive tag holding them.
func TestASpaceExclusiveTagKeepsRoomsWhereTheyBelong(t *testing.T) {
	t.Parallel()
	m := propsModel(t, config.Config{Tags: []config.Tag{
		{Name: "Out", Picked: []string{"!a:x"}, SpaceExclusive: true},
		{Name: "Every", Rule: []string{"*"}},
		{Name: "Also", Rule: []string{"room:Alpha"}, SpaceExclusive: true},
	}})
	for _, tc := range []struct {
		key  string
		room domain.RoomID
		want bool
	}{
		{"tag:Out", "!a:x", true},
		{"Made", "!a:x", false}, {"Made", "!c:x", true},
		{"Bridge", "!a:x", true},
		{"tag:Every", "!a:x", false}, {"tag:Every", "!c:x", true},
		{"tag:Also", "!a:x", true},
	} {
		if got := rowHolds(t, m, tc.key, tc.room); got != tc.want {
			t.Errorf("%s holds %s = %t, want %t", tc.key, tc.room, got, tc.want)
		}
	}
}

// sticky keeps the open room listed after it is read; hide_when_empty drops an empty
// row; first puts a row at the top unless the order places it; count_in_label says
// how many rooms a row holds.
func TestTagRowProperties(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Tags: []config.Tag{
		{Name: "Busy", Rule: []string{"unread"}, Sticky: true},
		{Name: "Nobody", Rule: []string{"room:Nobody"}, HideWhenEmpty: true},
		{Name: "Top", Rule: []string{"*"}, First: true, CountInLabel: true},
	}}
	m := propsModel(t, cfg)
	if _, ok := findGroup(m.rail.groups, "tag:Nobody"); ok {
		t.Error("an empty hide_when_empty tag has a row")
	}
	if m.rail.groups[0].key != "tag:Top" {
		t.Errorf("first row = %q, want the `first` tag", m.rail.groups[0].key)
	}
	if row := m.railRow(railRow(t, m, "tag:Top"), false, false, 30); !strings.Contains(row, "(3)") {
		t.Errorf("row = %q, want its count of 3", row)
	}

	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: "!c:x", Messages: 1, Counted: true}})
	m.rail.cursor = indexOfGroup(m.rail.groups, "tag:Busy")
	m.openRoom = "!c:x"
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: "!c:x"}})
	if !slices.ContainsFunc(m.filteredRooms(), func(r domain.Room) bool { return r.ID == "!c:x" }) {
		t.Error("a sticky tag dropped the open room once it was read")
	}

	cfg.Display.Rail.Order = []string{"Made", "tag:Top"}
	if m = propsModel(t, cfg); m.rail.groups[0].key != "Made" {
		t.Errorf("first row = %q with the order placing Top second, want the order kept", m.rail.groups[0].key)
	}
}

// Over random rooms, spaces, tags, properties and state changes: a room an exclusive
// tag claims shows under that tag alone; with a tag of every room, every room shows
// somewhere unless hidden tags claim it; a space-exclusive tag
// never takes a room out of a space it belongs to; a silenced room never counts as
// unread; and the per-room memo always answers as a fresh computation would.
func TestTagPropertiesOverRandomConfigs(t *testing.T) {
	t.Parallel()
	terms := []string{"*", "dm", "group", "unread", "mention", "draft", "space:Made", "space:Bridge", "room:R1", "not dm", "not unread", "tag:T0"}
	for seed := range uint64(150) {
		rng := rand.New(rand.NewPCG(seed, 11)) // #nosec G404 -- reproducible
		var rooms []domain.Room
		var made, bridge []domain.RoomID
		for i := range 2 + rng.IntN(6) {
			id := domain.RoomID(fmt.Sprintf("!r%d:x", i))
			rooms = append(rooms, domain.Room{ID: id, Name: fmt.Sprintf("R%d", i), IsDirect: rng.IntN(3) == 0})
			if rng.IntN(2) == 0 {
				made = append(made, id)
			}
			if rng.IntN(2) == 0 {
				bridge = append(bridge, id)
			}
		}
		var tags []config.Tag
		for i := range 1 + rng.IntN(4) {
			tag := config.Tag{Name: fmt.Sprintf("T%d", i), Exclusive: rng.IntN(3) == 0, SpaceExclusive: rng.IntN(3) == 0,
				Hidden: rng.IntN(5) == 0}
			if rng.IntN(3) == 0 {
				tag.CountsUnread = &off
			}
			for range 1 + rng.IntN(3) {
				if term := terms[rng.IntN(len(terms))]; i > 0 || term != "tag:T0" {
					tag.Rule = append(tag.Rule, term)
				}
			}
			if rng.IntN(3) == 0 {
				tag.Picked = []string{string(rooms[rng.IntN(len(rooms))].ID)}
			}
			tags = append(tags, tag)
		}
		tags = append(tags, config.Tag{Name: "Every", Rule: []string{"*"}})
		m := update(t, configured(config.Config{Tags: tags}), roomsMsg{rooms: rooms})
		m = update(t, m, spacesMsg{spaces: []domain.Space{
			{ID: "!made:x", Name: "Made", Children: made},
			{ID: "!bridge:x", Name: "Bridge", Children: bridge, Bridge: domain.ProtocolWhatsApp, Keeper: "@bot:x"},
		}})
		for step := range 6 {
			room := rooms[rng.IntN(len(rooms))].ID
			switch rng.IntN(3) {
			case 0:
				m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: room, Messages: rng.IntN(3), Mentions: rng.IntN(2), Counted: true}})
			case 1:
				m.drafts = setDraft(m.drafts, room, draft{input: "x"})
			default:
				m.drafts = setDraft(m.drafts, room, draft{})
			}
			checkTagInvariants(t, fmt.Sprintf("seed %d step %d", seed, step), m, tags)
		}
	}
}

func checkTagInvariants(t *testing.T, where string, m Model, tags []config.Tag) {
	t.Helper()
	view := m.unreadView()
	for i := range m.rooms.all {
		room := m.rooms.all[i]
		fresh := view.computeTags(room)
		if got := view.tagsOf(room); fmt.Sprint(got) != fmt.Sprint(fresh) {
			t.Fatalf("%s: memo for %s = %+v, fresh = %+v", where, room.ID, got, fresh)
		}
		var shows []string
		for _, g := range m.rail.groups {
			if g.admits(view, room) {
				shows = append(shows, g.key)
			}
		}
		if fresh.owner >= 0 {
			owner := tagGroupKey(tags[fresh.owner].Name)
			for _, key := range shows {
				if isTagGroup(key) && key != owner {
					t.Fatalf("%s: %s is claimed by %s but shows in %s (%v)", where, room.ID, owner, key, shows)
				}
			}
			if tags[fresh.owner].Hidden {
				continue // claimed into a hidden tag: the person's choice
			}
		}
		strippedIntoHidden := len(fresh.spaceExcl) > 0 && !slices.ContainsFunc(fresh.spaceExcl, func(i int) bool { return !tags[i].Hidden })
		if len(shows) == 0 && !strippedIntoHidden {
			t.Fatalf("%s: %s shows nowhere (tags %+v)", where, room.ID, fresh)
		}
		at := slices.IndexFunc(m.rooms.spaces, func(s domain.Space) bool { return s.ID == "!bridge:x" })
		if g, ok := findGroup(m.rail.groups, "Bridge"); ok && at >= 0 && slices.Contains(m.rooms.spaces[at].Children, room.ID) && !g.admits(view, room) {
			t.Fatalf("%s: %s left the bridge's space it belongs to", where, room.ID)
		}
		if fresh.silenced && view.tallies(room) {
			t.Fatalf("%s: silenced %s still counts as unread", where, room.ID)
		}
	}
}
