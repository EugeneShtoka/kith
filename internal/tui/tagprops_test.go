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

// propsModel has Alpha (in a user-made space and a bridge's), Bravo (a DM) and Charlie
// (in the user-made space), with the given tags.
func propsModel(t *testing.T, cfg config.Config) Model {
	t.Helper()
	m := configured(cfg)
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: mx + "!a:x", Name: "Alpha"}, {ID: mx + "!b:x", Name: "Bravo", IsDirect: true}, {ID: mx + "!c:x", Name: "Charlie"},
	}})
	return update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: mx + "!made:x", Name: "Made", Children: []domain.RoomID{mx + "!a:x", mx + "!c:x"}},
		{ID: mx + "!bridge:x", Name: "Bridge", Children: []domain.RoomID{mx + "!a:x"}, Bridge: domain.ProtocolWhatsApp, Keeper: "matrix:@bot:x"},
	}})
}

func rowHolds(t *testing.T, m Model, key string, id domain.RoomID) bool {
	t.Helper()
	return admitsRoom(m, railRow(t, m, key), id)
}

// A silent tag's rooms count as read everywhere else (no badge, not in Unread, not
// `unread` to another tag); the silent tag itself sees their real state.
func TestASilentTagsRoomsCountAsRead(t *testing.T) {
	t.Parallel()
	m := propsModel(t, config.Config{Tags: []config.Tag{
		{Name: "Quiet", Rule: []string{"room:Alpha"}, CountsUnread: &off},
		{Name: "Busy", Rule: []string{"unread"}},
		{Name: "Pings", Rule: []string{"mention"}, CountsUnread: &off},
		{Name: "Unread", Rule: []string{"unread"}},
		{Name: "All", Rule: []string{"*"}},
	}})
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: mx + "!a:x", Messages: 3, Counted: true}})
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: mx + "!c:x", Messages: 1, Counted: true}})
	if !rowHolds(t, m, "tag:Quiet", mx+"!a:x") {
		t.Error("the silent tag does not hold its unread room")
	}
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: mx + "!b:x", Messages: 1, Mentions: 1, Counted: true}})
	if !rowHolds(t, m, "tag:Pings", mx+"!b:x") {
		t.Error("a silent tag judged by its own state does not hold the mention it silences")
	}
	if rowHolds(t, m, "tag:Busy", mx+"!a:x") || rowHolds(t, m, unreadGroupKey, mx+"!a:x") {
		t.Error("a silenced room is still unread to another tag or to Unread")
	}
	if !rowHolds(t, m, "tag:Busy", mx+"!c:x") {
		t.Error("an unread room not silenced left Busy")
	}
	if n, _ := m.groupUnread(railRow(t, m, homeGroupKey)); n != 1 {
		t.Errorf("All's badge = %d, want Charlie's 1 alone", n)
	}
}

// A tag exclusive from tags shows its rooms under no other tag; its spaces keep them.
// Of two, the first configured wins.
func TestAnExclusiveTagShowsItsRoomsAlone(t *testing.T) {
	t.Parallel()
	m := propsModel(t, config.Config{Tags: []config.Tag{
		{Name: "Fam", Rule: []string{"dm", "room:Alpha"}, Exclusive: []string{"tags"}},
		{Name: "Later", Rule: []string{"room:Alpha"}, Exclusive: []string{"tags"}},
		{Name: "Every", Rule: []string{"*"}},
		{Name: "All", Rule: []string{"*"}},
		{Name: "DMs", Rule: []string{"dm"}},
	}})
	for _, tc := range []struct {
		key  string
		room domain.RoomID
		want bool
	}{
		{"tag:Fam", mx + "!b:x", true}, {"tag:Fam", mx + "!a:x", true},
		{"tag:Later", mx + "!a:x", false}, // Fam is first
		{"tag:Every", mx + "!b:x", false}, {"tag:Every", mx + "!c:x", true},
		{homeGroupKey, mx + "!b:x", false}, {dmsGroupKey, mx + "!b:x", false}, {homeGroupKey, mx + "!c:x", true},
		{"Made", mx + "!a:x", true}, {"Bridge", mx + "!a:x", true},
	} {
		if got := rowHolds(t, m, tc.key, tc.room); got != tc.want {
			t.Errorf("%s holds %s = %t, want %t", tc.key, tc.room, got, tc.want)
		}
	}
}

// exclusive = ["spaces"] takes rooms out of spaces they do not belong to, "origin" out
// of the ones they do (a bridge's); other tags keep them unless "tags" is listed too.
func TestExclusiveTakesRoomsFromWhatItLists(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		exclusive          []string
		made, bridge, tags bool // the room still shows there
	}{
		{nil, true, true, true},
		{[]string{"spaces"}, false, true, true},
		{[]string{"origin"}, true, false, true},
		{[]string{"tags"}, true, true, false},
		{[]string{"tags", "spaces", "origin"}, false, false, false},
	} {
		m := propsModel(t, config.Config{Tags: []config.Tag{
			{Name: "Out", Picked: []string{mx + "!a:x"}, Exclusive: tc.exclusive},
			{Name: "Every", Rule: []string{"*"}},
		}})
		for key, want := range map[string]bool{"Made": tc.made, "Bridge": tc.bridge, "tag:Every": tc.tags, "tag:Out": true} {
			if got := rowHolds(t, m, key, mx+"!a:x"); got != want {
				t.Errorf("exclusive %v: %s holds it = %t, want %t", tc.exclusive, key, got, want)
			}
		}
		if !rowHolds(t, m, "Made", mx+"!c:x") || !rowHolds(t, m, "tag:Every", mx+"!c:x") {
			t.Errorf("exclusive %v took a room the tag does not hold", tc.exclusive)
		}
	}
}

// sticky keeps the open room listed after it is read; hide_when_empty drops an empty
// row; first puts a row at the top unless the order places it; count_in_label shows
// the room count.
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

	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: mx + "!c:x", Messages: 1, Counted: true}})
	m.rail.cursor = indexOfGroup(m.rail.groups, "tag:Busy")
	m.openRoom = mx + "!c:x"
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: mx + "!c:x"}})
	if !slices.ContainsFunc(m.filteredRooms(), func(r domain.Room) bool { return r.ID == mx+"!c:x" }) {
		t.Error("a sticky tag dropped the open room once it was read")
	}

	cfg.Display.Rail.Order = []string{"Made", "tag:Top"}
	if m = propsModel(t, cfg); m.rail.groups[0].key != "Made" {
		t.Errorf("first row = %q with the order placing Top second, want the order kept", m.rail.groups[0].key)
	}
}

// Over random rooms, spaces, tags, properties and state changes: an exclusive tag's
// room shows under it alone; with an all-rooms tag every room shows somewhere unless a
// hidden tag claims it; a room leaves a space exactly when a tag holding it lists that
// kind of space; a silenced room never counts unread; and the memo matches a fresh
// computation.
func TestTagPropertiesOverRandomConfigs(t *testing.T) {
	t.Parallel()
	terms := []string{"*", "dm", "group", "unread", "mention", "draft", "space:Made", "space:Bridge", "room:R1", "not dm", "not unread", "tag:T0"}
	for seed := range uint64(150) {
		rng := rand.New(rand.NewPCG(seed, 11)) // #nosec G404 -- reproducible
		var rooms []domain.Room
		var made, bridge []domain.RoomID
		for i := range 2 + rng.IntN(6) {
			id := domain.RoomID(fmt.Sprintf(mx+"!r%d:x", i))
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
			tag := config.Tag{Name: fmt.Sprintf("T%d", i), Hidden: rng.IntN(5) == 0}
			for _, what := range []string{"tags", "spaces", "origin"} {
				if rng.IntN(3) == 0 {
					tag.Exclusive = append(tag.Exclusive, what)
				}
			}
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
			{ID: mx + "!made:x", Name: "Made", Children: made},
			{ID: mx + "!bridge:x", Name: "Bridge", Children: bridge, Bridge: domain.ProtocolWhatsApp, Keeper: "matrix:@bot:x"},
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
		if len(shows) == 0 {
			t.Fatalf("%s: %s shows nowhere (tags %+v)", where, room.ID, fresh)
		}
		var leaves domain.Exclusive
		for j := range tags {
			if tag := &tags[j]; fresh.in[j] {
				leaves.Spaces = leaves.Spaces || tag.ExclusiveFrom("spaces")
				leaves.Origin = leaves.Origin || tag.ExclusiveFrom("origin")
			}
		}
		for _, space := range m.rooms.spaces {
			g, ok := findGroup(m.rail.groups, space.Name)
			if !ok || !slices.Contains(space.Children, room.ID) {
				continue
			}
			left := leaves.Spaces
			if space.Keeper != "" {
				left = leaves.Origin
			}
			if g.admits(view, room) == left {
				t.Fatalf("%s: %s in %s = %t, want %t (leaves %+v)", where, room.ID, space.Name, left, !left, leaves)
			}
		}
		if fresh.silenced && view.tallies(room) {
			t.Fatalf("%s: silenced %s still counts as unread", where, room.ID)
		}
	}
}
