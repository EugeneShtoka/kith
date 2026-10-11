package domain_test

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// placeWorld is rooms in spaces and tags that share names: a space Work renamed Desk, a
// tag Work, a forum Board shown as Work too, a Slack workspace Acme, and a space
// Friends. Each room's spaces and tags are given by name.
type placeRoom struct {
	id           domain.RoomID
	spaces, tags []string
}

var (
	placeLabels = map[string]string{"Work": "Desk", "Board": "Work", "Acme": "acme corp"}
	placeRooms  = []placeRoom{
		{id: "!w:x", spaces: []string{"Work"}},
		{id: "!t:x", tags: []string{"Work"}},
		{id: "!b:x", spaces: []string{"Board"}},
		{id: "!a:x", spaces: []string{"Acme"}},
		{id: "!f:x", spaces: []string{"Friends"}, tags: []string{"Pinned"}},
		{id: "!n:x"},
	}
	placeSpaces = []domain.Space{
		{ID: "!sw:x", Name: "Work", Children: []domain.RoomID{"!w:x"}},
		{ID: "!sb:x", Name: "Board", Bridge: domain.ProtocolTelegram, Leaving: domain.LeftWhole, Children: []domain.RoomID{"!b:x"}},
		{ID: "!sa:x", Name: "Acme", Bridge: domain.ProtocolSlack, Leaving: domain.LeftBySigningOut, Children: []domain.RoomID{"!a:x"}},
		{ID: "!sf:x", Name: "Friends", Children: []domain.RoomID{"!f:x"}},
	}
)

// placeFacts is each room as every scope reads it.
func placeFacts(t *testing.T) map[domain.RoomID]domain.RoomFacts {
	t.Helper()
	var tags []domain.Tag
	for _, name := range []string{"Work", "Pinned"} {
		tag := domain.Tag{Name: name}
		for _, r := range placeRooms {
			if slices.Contains(r.tags, name) {
				tag.Picked = append(tag.Picked, string(r.id))
			}
		}
		tags = append(tags, tag)
	}
	set, _, err := domain.NewTagSet(tags)
	if err != nil {
		t.Fatal(err)
	}
	places := domain.Places{Order: domain.NewHomeOrder(nil, nil, set, placeSpaces, placeLabels), Tags: set}
	out := map[domain.RoomID]domain.RoomFacts{}
	for _, r := range placeRooms {
		out[r.id] = places.Facts(domain.Room{ID: r.id}, domain.HoldersOf(r.id, placeSpaces))
	}
	return out
}

// place: names the rooms of every row shown so: a space by its name or its rename, a
// tag, a forum, a workspace, in any case, several at once; space: and tag: only their
// own kind, by name; a bare name nothing.
func TestAPlaceEntryNamesEveryRowShownSo(t *testing.T) {
	t.Parallel()
	facts := placeFacts(t)
	for _, c := range []struct {
		entry string
		want  []domain.RoomID
	}{
		{"place:Work", []domain.RoomID{"!w:x", "!t:x", "!b:x"}}, // the space, the tag, the forum shown as Work
		{"place:WORK", []domain.RoomID{"!w:x", "!t:x", "!b:x"}},
		{"place:desk", []domain.RoomID{"!w:x"}},  // Work's rename
		{"place:Board", []domain.RoomID{"!b:x"}}, // the forum's own name
		{"place:Acme Corp", []domain.RoomID{"!a:x"}},
		{"place:pinned", []domain.RoomID{"!f:x"}},
		{"place:Nobody", nil},
		{"space:Work", []domain.RoomID{"!w:x"}},
		{"space:Desk", nil}, // space: reads the name, not the label
		{"tag:Work", []domain.RoomID{"!t:x"}},
		{"Work", nil},
	} {
		var got []domain.RoomID
		for _, r := range placeRooms {
			if facts[r.id].Names(c.entry) {
				got = append(got, r.id)
			}
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s names %v, want %v", c.entry, got, c.want)
		}
	}
	if kind, ok := domain.ParseEntry("place:Work"); !ok || kind != domain.EntryClass {
		t.Errorf("ParseEntry(place:Work) = %v, %t; want a class of rooms", kind, ok)
	}
}

// NamesHome over every entry kind and home kind: the rail, the home order, the space
// rules and the per-place rules all read an entry through it.
func TestNamesHomeKeepsEachKindApart(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		entry, home, label string
		want               bool
	}{
		{"space:Work", "Work", "", true},
		{"SPACE: work ", "Work", "", true},
		{"space:Work", "tag:Work", "", false},
		{"space:Desk", "Work", "Desk", false},
		{"tag:Work", "tag:Work", "", true},
		{"tag:work", "tag:Work", "", true},
		{"tag:Work", "Work", "", false},
		{"place:Work", "Work", "", true},
		{"place:Work", "tag:Work", "", true},
		{"place:desk", "Work", "Desk", true},
		{"place:Work", "Work", "Desk", true}, // still its name
		{"place:Desk", "tag:Work", "", false},
		{"place:", "Work", "", false},
		{"space:", "Work", "", false},
		{"Work", "Work", "", false},
		{"room:Work", "Work", "", false},
		{"*", "Work", "", false},
	} {
		if got := domain.NamesHome(c.entry, c.home, c.label); got != c.want {
			t.Errorf("NamesHome(%q, %q, %q) = %t, want %t", c.entry, c.home, c.label, got, c.want)
		}
	}
}

// A tag's rule may name rows by place:; a tag of that name is read through the set,
// so a tag naming itself that way is a cycle like tag: (warned, matching nothing).
func TestATagRuleReadsPlaceThroughTheSet(t *testing.T) {
	t.Parallel()
	set, warnings, err := domain.NewTagSet([]domain.Tag{
		{Name: "Busy", Rule: []string{"place:Desk", "place:Friends"}},
		{Name: "Friends", Picked: []string{"!t:x"}},
		{Name: "Loop", Rule: []string{"place:loop", "dm"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 {
		t.Errorf("warnings = %v, want one: Loop names itself", warnings)
	}
	places := domain.Places{Order: domain.NewHomeOrder(nil, nil, set, placeSpaces, placeLabels), Tags: set}
	in := func(id domain.RoomID, tag string) bool {
		f := places.Facts(domain.Room{ID: id, IsDirect: id == "!n:x"}, domain.HoldersOf(id, placeSpaces))
		return slices.Contains(f.Tags, tag)
	}
	for _, c := range []struct {
		room domain.RoomID
		tag  string
		want bool
	}{
		{"!w:x", "Busy", true},  // in the space shown as Desk
		{"!f:x", "Busy", true},  // in the space Friends
		{"!t:x", "Busy", true},  // in the tag Friends
		{"!a:x", "Busy", false}, // in neither
		{"!n:x", "Loop", true},  // dm; its own name matches nothing
		{"!w:x", "Loop", false},
	} {
		if got := in(c.room, c.tag); got != c.want {
			t.Errorf("%s in %s = %t, want %t", c.room, c.tag, got, c.want)
		}
	}
	if set.Automatic(0) {
		t.Error("a rule naming places is not automatic")
	}
}

// A per-place rule (read, media, threads) names the room's first home by any prefix; a
// bare name names nothing.
func TestARuleNamesTheFirstHomeByPrefix(t *testing.T) {
	t.Parallel()
	space := domain.Scope{RoomID: "!r:x", Space: "Work", SpaceLabel: "Desk"}
	tag := domain.Scope{RoomID: "!r:x", Space: "tag:Work"}
	for _, c := range []struct {
		place string
		scope domain.Scope
		want  domain.Rank
	}{
		{"space:Work", space, domain.RankSpace},
		{"place:desk", space, domain.RankSpace},
		{"place:Work", space, domain.RankSpace},
		{"tag:Work", space, domain.NoMatch},
		{"Work", space, domain.NoMatch},
		{"tag:Work", tag, domain.RankSpace},
		{"place:work", tag, domain.RankSpace},
		{"space:Work", tag, domain.NoMatch},
		{"tag:Work", domain.Scope{RoomID: "!r:x"}, domain.NoMatch},
		{"!r:x", space, domain.RankRoom},
	} {
		if got := (domain.Match{Place: c.place}).Rank(c.scope); got != c.want {
			t.Errorf("%s in %+v ranks %v, want %v", c.place, c.scope, got, c.want)
		}
	}
}
