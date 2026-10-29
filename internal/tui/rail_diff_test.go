package tui

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// The rail as data (domain.BuildRail, domain.ShownRows) draws the rail the TUI drew before
// it moved there (oracleRailGroups): the same rows in the same order, with the same labels,
// dividers and flags, each admitting the same rooms, and every room judged alike by the
// tags. Random tags (every rule kind and flag), spaces (shared names, bridges, accounts),
// rooms (invites, DMs, archived), states (unread, marked, highlights, drafts, either
// unread source), canonical parents, renames and [display.rail] lists.
func TestTheRailAsDataDrawsTheRailItReplaced(t *testing.T) {
	t.Parallel()
	seeds := 3000
	if testing.Short() {
		seeds = 500
	}
	for seed := range seeds {
		compareRails(t, uint64(seed))
	}
}

func compareRails(t *testing.T, seed uint64) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 0x7261696c))
	pick := func(p float64) bool { return rng.Float64() < p }
	some := func(pool []string, most int) []string {
		var out []string
		for range rng.IntN(most + 1) {
			out = append(out, pool[rng.IntN(len(pool))])
		}
		return out
	}

	rooms := diffRooms(rng)
	ids := make([]domain.RoomID, len(rooms))
	for i := range rooms {
		ids[i] = rooms[i].ID
	}
	spaces := diffSpaces(rng, ids)

	tags := diffTags(rng)
	set, _, err := setup.Tags(config.Config{Tags: tags})
	if err != nil {
		return // a generated rule the config refuses: nothing to draw
	}

	homes := []string{"tag:Unread", "tag:All", "tag:Invites", "tag:Archived", "tag:Quiet", "tag:Nope", "space:Work",
		"space:Friends", "space:Bridge", "place:Work", "place:Job", "place:DMs", "place:Unread", "place:All"}
	cfg := config.Rail{
		Order:         some(append([]string{"*", "*", "-", "-", "--"}, homes...), 8),
		Hidden:        some(homes, 2),
		HideWhenEmpty: some(homes, 2),
	}
	var names []config.DisplayName
	for range rng.IntN(3) {
		target := []string{"tag:Unread", "tag:All", "Work", "Friends", fallbackGroupKey}[rng.IntN(5)]
		names = append(names, config.DisplayName{Target: config.GroupTarget(target), Name: []string{"Job", "DMs", "", "Work"}[rng.IntN(4)]})
	}

	view := unreadView{tags: set, local: pick(0.5), counts: map[domain.RoomID]domain.Unread{}, drafts: map[domain.RoomID]draft{},
		parents: map[domain.RoomID]domain.SpaceID{}}
	places := domain.Places{
		Order: domain.NewHomeOrder(nil, cfg.Order, set, spaces, config.Display{Names: names}.SpaceLabels()),
		Tags:  set, Archives: map[domain.Protocol]string{domain.ProtocolTelegram: "Archived"},
	}
	view.roomFacts = map[domain.RoomID]domain.RoomFacts{}
	for i := range rooms {
		id := rooms[i].ID
		view.roomFacts[id] = places.Facts(rooms[i], domain.HoldersOf(id, spaces))
		if pick(0.5) {
			view.counts[id] = domain.Unread{RoomID: id, Notifications: rng.IntN(3), Highlights: rng.IntN(2),
				Messages: rng.IntN(3), Mentions: rng.IntN(2), Counted: pick(0.7), Marked: pick(0.2)}
		}
		if pick(0.3) {
			view.drafts[id] = draft{input: "x"}
		}
		if len(spaces) > 0 && pick(0.4) {
			view.parents[id] = spaces[rng.IntN(len(spaces))].ID
		}
	}

	where := fmt.Sprintf("seed %d: tags %+v\nspaces %+v\nrail %+v names %+v", seed, tags, spaces, cfg, names)
	want := oracleRailGroups(spaces, cfg, names, view, rooms)
	got := railGroups(spaces, cfg, names, view, rooms)
	if len(got) != len(want) {
		t.Fatalf("%s\nrows %v, the rail drew %v", where, groupKeys(got), oracleKeys(want))
	}
	now := view
	now.rail = railInputs{spaces: spaces, rooms: rooms, cfg: cfg, names: names}
	now.railMemo = &railMemo{}
	for i := range want {
		w, g := want[i], got[i]
		if g.key != w.key || g.label != w.label || g.sepAfter != w.sepAfter || g.sticky != w.sticky ||
			g.countInLabel != w.countInLabel || g.home != w.home || g.owner != w.owner {
			t.Fatalf("%s\nrow %d is %+v, the rail drew %+v", where, i, g, w)
		}
		for j := range rooms {
			if g.admits(now, rooms[j]) != w.admits(view, rooms[j]) {
				t.Fatalf("%s\nrow %q admits %s: %v, the rail said %v", where, g.key, rooms[j].ID, !w.admits(view, rooms[j]), w.admits(view, rooms[j]))
			}
		}
	}
	for j := range rooms {
		old := oracleTagsOf(view, rooms[j])
		judged := now.tagsOf(rooms[j])
		was := domain.RoomTags{In: old.in, Silenced: old.silenced, Owner: old.owner, Leaves: old.leaves}
		if was.In == nil {
			was.In = []bool{}
		}
		if !reflect.DeepEqual(judged, was) {
			t.Fatalf("%s\n%s is judged %+v, the tags said %+v", where, rooms[j].ID, judged, was)
		}
	}
}

func oracleKeys(groups []oracleGroup) []string {
	keys := make([]string, len(groups))
	for i := range groups {
		keys[i] = groups[i].key
	}
	return keys
}

// diffRooms is a handful of rooms on two networks: named, direct, invited, archived.
func diffRooms(rng *rand.Rand) []domain.Room {
	n := rng.IntN(7)
	rooms := make([]domain.Room, 0, n)
	for i := range n {
		id := domain.RoomID(fmt.Sprintf(mx+"!r%d:x", i))
		if rng.IntN(3) == 0 {
			id = domain.RoomID(fmt.Sprintf("telegram:1/%d", 100+i))
		}
		room := domain.Room{ID: id, Name: []string{"Alpha", "Bravo", "Work", "Gamma"}[rng.IntN(4)], IsDirect: rng.IntN(3) == 0,
			Archived: rng.IntN(4) == 0}
		if rng.IntN(5) == 0 {
			room.Membership = domain.MembershipInvite
		}
		rooms = append(rooms, room)
	}
	return rooms
}

// diffSpaces is up to four spaces over the rooms: names shared or empty, made by a person,
// kept by a bridge, an account's.
func diffSpaces(rng *rand.Rand, ids []domain.RoomID) []domain.Space {
	n := rng.IntN(5)
	spaces := make([]domain.Space, 0, n)
	for i := range n {
		s := domain.Space{ID: domain.SpaceID(fmt.Sprintf(mx+"!s%d:x", i)), Name: []string{"Work", "Friends", "Bridge", "Work", ""}[rng.IntN(5)]}
		switch rng.IntN(4) {
		case 0:
			s.Keeper = "matrix:@bot:x"
		case 1:
			s.Bridge = domain.ProtocolTelegram
			s.ID = domain.SpaceID(fmt.Sprintf("telegram:1/s%d", i))
			s.Leaving = domain.LeftBySigningOut
		case 2:
			s.Bridge = domain.ProtocolMatrix
		}
		for _, id := range ids {
			if rng.IntN(2) == 0 {
				s.Children = append(s.Children, id)
			}
		}
		spaces = append(spaces, s)
	}
	return spaces
}

// diffTags is some of a pool of tags, each with a rule of every kind and random flags.
func diffTags(rng *rand.Rand) []config.Tag {
	rules := [][]string{
		{"invite"}, {"unread"}, {"mention"}, {"draft"}, {"*"}, {"dm"}, {"space:Work"}, {"place:Work"},
		{"tag:Unread", "not dm"}, {"not tag:Quiet"}, {"protocol:telegram"}, {"room:Alpha"}, {"unread", "mention"}, nil,
	}
	exclusive := []string{config.ExclusiveTags, config.ExclusiveSpaces, config.ExclusiveOrigin}
	var tags []config.Tag
	for _, name := range []string{"Invites", "Unread", "Mentions", "Drafts", "Quiet", "Archived", "All", "DMs", "Pinned"} {
		if rng.IntN(10) < 3 {
			continue
		}
		t := config.Tag{
			Name: name, Rule: rules[rng.IntN(len(rules))],
			Hidden: rng.IntN(7) == 0, Sticky: rng.IntN(4) == 0, HideWhenEmpty: rng.IntN(3) == 0,
			First: rng.IntN(5) == 0, CountInLabel: rng.IntN(5) == 0,
		}
		if t.Rule == nil {
			t.Picked = []string{"room:Bravo"}
		}
		if rng.IntN(4) == 0 {
			no := false
			t.CountsUnread = &no
		}
		for _, what := range exclusive {
			if rng.IntN(4) == 0 {
				t.Exclusive = append(t.Exclusive, what)
			}
		}
		tags = append(tags, t)
	}
	return tags
}
