package domain

import (
	"slices"
	"strings"
	"testing"
)

// shownKeys draws rows as keys, a divider as "|".
func shownKeys(rows []RailRow) string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Key)
		if r.SepAfter {
			out = append(out, "|")
		}
	}
	return strings.Join(out, " ")
}

// railOf is a rail of rows by key: a tag's when the key is one, a space's otherwise; a
// key ending in "0" holds nothing, any other one room.
func railOf(keys ...string) Rail {
	var rail Rail
	for _, key := range keys {
		row := RailRow{Key: key, Label: key}
		if name, ok := TagOf(key); ok {
			row.Label = name
		}
		if !strings.HasSuffix(key, "0") {
			row.Rooms = []RoomID{mx + "!a:x"}
		}
		rail.Rows = append(rail.Rows, row)
	}
	return rail
}

// The rows drawn: hidden ones and empty ones that hide while empty dropped, the order's
// named rows first, the wildcard for the rest, a divider after the row a divider follows
// (several in a row draw one, none above the first row or under the last), rows marked
// first on top unless placed, and the first row when nothing is left.
func TestShownRowsFollowTheRailConfig(t *testing.T) {
	t.Parallel()
	first := railOf("tag:Invites", "tag:All", "Work", "Friends")
	first.Rows[0].First = true
	emptyFlagged := railOf("tag:Unread0", "tag:All", "Work")
	emptyFlagged.Rows[0].HideWhenEmpty = true
	for _, c := range []struct {
		name string
		rail Rail
		spec RailSpec
		want string
	}{
		{"no config: default order", railOf("tag:All", "Work"), RailSpec{}, "tag:All Work"},
		{"named first, the rest after", railOf("tag:All", "Work", "Friends"), RailSpec{Order: []string{"space:Friends"}},
			"Friends tag:All Work"},
		{"the wildcard in the middle", railOf("tag:All", "Work", "Friends"),
			RailSpec{Order: []string{"space:Work", "*", "tag:All"}}, "Work Friends tag:All"},
		{"the wildcard twice places once", railOf("tag:All", "Work"), RailSpec{Order: []string{"*", "*"}}, "tag:All Work"},
		{"dividers", railOf("tag:All", "Work", "Friends"), RailSpec{Order: []string{"tag:All", "-", "*"}},
			"tag:All | Work Friends"},
		{"dividers in a row draw one", railOf("tag:All", "Work", "Friends"),
			RailSpec{Order: []string{"tag:All", "-", "--", "*"}}, "tag:All | Work Friends"},
		{"a divider around a hidden row draws one", railOf("tag:All", "Work", "Friends"),
			RailSpec{Order: []string{"tag:All", "-", "space:Work", "-", "*"}, Hidden: []string{"space:Work"}},
			"tag:All | Friends"},
		{"a divider after a hidden row goes under the row before", railOf("tag:All", "Work", "Friends"),
			RailSpec{Order: []string{"tag:All", "space:Work", "-", "*"}, Hidden: []string{"space:Work"}}, "tag:All | Friends"},
		{"a divider after an entry naming nothing goes under the row before", railOf("tag:All", "Work", "Friends"),
			RailSpec{Order: []string{"tag:All", "tag:Nope", "-", "*"}}, "tag:All | Work Friends"},
		{"no divider above the first row", railOf("tag:All", "Work"), RailSpec{Order: []string{"-", "*"}}, "tag:All Work"},
		{"no divider under the last row", railOf("tag:All", "Work", "Gone0"),
			RailSpec{Order: []string{"*", "-", "space:Gone0"}, HideWhenEmpty: []string{"space:Gone0"}}, "tag:All Work"},
		{"place: names by label", railOf("tag:All", "Work"), RailSpec{Order: []string{"place:Work", "place:All"}},
			"Work tag:All"},
		{"an entry naming nothing drops out", railOf("tag:All", "Work"), RailSpec{Order: []string{"tag:Nope", "space:Work"}},
			"Work tag:All"},
		{"hidden", railOf("tag:All", "Work"), RailSpec{Hidden: []string{"tag:All"}}, "Work"},
		{"hiding every row keeps the first", railOf("tag:All", "Work"), RailSpec{Hidden: []string{"tag:All", "space:Work"}},
			"tag:All"},
		{"hide_when_empty listed", railOf("tag:Unread0", "tag:All"), RailSpec{HideWhenEmpty: []string{"tag:Unread0"}},
			"tag:All"},
		{"hide_when_empty on the row", emptyFlagged, RailSpec{}, "tag:All Work"},
		{"hide_when_empty keeps a row holding rooms", railOf("tag:Unread", "tag:All"),
			RailSpec{HideWhenEmpty: []string{"tag:Unread"}}, "tag:Unread tag:All"},
		{"first goes on top", first, RailSpec{Order: []string{"tag:All", "*"}}, "tag:Invites tag:All Work Friends"},
		{"first stays where placed", first, RailSpec{Order: []string{"tag:All", "tag:Invites", "*"}},
			"tag:All tag:Invites Work Friends"},
		{"an empty rail", Rail{}, RailSpec{}, ""},
	} {
		if got := shownKeys(ShownRows(c.rail, c.spec)); got != c.want {
			t.Errorf("%s: shown %q, want %q", c.name, got, c.want)
		}
	}
}

// ShownRows leaves the rail it was given as it was.
func TestShownRowsLeaveTheRailAlone(t *testing.T) {
	t.Parallel()
	rail := railOf("tag:All", "Work")
	_ = ShownRows(rail, RailSpec{Order: []string{"space:Work", "-", "tag:All", "-"}})
	if slices.ContainsFunc(rail.Rows, func(r RailRow) bool { return r.SepAfter }) {
		t.Errorf("drawing the rail marked its rows: %+v", rail.Rows)
	}
}

// BuildRail: a row per shown tag holding what the tag holds unless an exclusive tag claims
// it, a row per space holding its children but invitations and the rooms a tag takes out
// of it (a space they belong to only by an origin-exclusive tag), the rows renamed; a rail
// with neither gets one row of every room but invitations.
func TestBuildRailHoldsWhatEachRowLists(t *testing.T) {
	t.Parallel()
	tags := mustTags(t,
		Tag{Name: "Unread", Rule: []string{"unread"}, Sticky: true},
		Tag{Name: "Mentions", Rule: []string{"mention"}, Exclusive: Exclusive{Tags: true}},
		Tag{Name: "Quiet", Rule: []string{"room:Bots"}, Silent: true, Exclusive: Exclusive{Spaces: true}},
		Tag{Name: "Away", Rule: []string{"room:Mom"}, Exclusive: Exclusive{Origin: true}},
		Tag{Name: "Secret", Rule: []string{"*"}, Hidden: true},
	)
	bots := RailRoom{ID: mx + "!bots:x", Facts: RoomFacts{ID: mx + "!bots:x", Name: "Bots"},
		State: RoomState{Unread: true, Mention: true}}
	mom := RailRoom{ID: mx + "!mom:x", Facts: RoomFacts{ID: mx + "!mom:x", Name: "Mom"}, State: RoomState{Unread: true}}
	hot := RailRoom{ID: mx + "!hot:x", Facts: RoomFacts{ID: mx + "!hot:x", Name: "Hot"}, State: RoomState{Unread: true, Mention: true}}
	inv := RailRoom{ID: mx + "!inv:x", Facts: RoomFacts{ID: mx + "!inv:x", Name: "Inv"}, State: RoomState{Invite: true}}
	made := Space{ID: mx + "!made:x", Name: "Made", Children: []RoomID{bots.ID, mom.ID, hot.ID, inv.ID}}
	kept := Space{ID: mx + "!kept:x", Name: "Kept", Keeper: "matrix:@bot:x", Children: []RoomID{bots.ID, mom.ID}}
	home := Space{ID: "telegram:1/account", Name: "Telegram", Bridge: ProtocolTelegram, Leaving: LeftBySigningOut}
	homeOf := bots
	homeOf.Parent = made.ID
	spec := RailSpec{Tags: tags, Labels: map[string]string{"Made": "Mine"}}

	rail := BuildRail(spec, []Space{made, kept, home}, []RailRoom{homeOf, mom, hot, inv})
	got := map[string][]RoomID{}
	for _, r := range rail.Rows {
		got[r.Key+"="+r.Label] = r.Rooms
	}
	want := map[string][]RoomID{
		// Bots is silenced: read for Unread and Mentions; Hot is claimed by Mentions.
		"tag:Unread=Unread":     {mom.ID},
		"tag:Mentions=Mentions": {hot.ID},
		"tag:Quiet=Quiet":       {bots.ID},
		"tag:Away=Away":         {mom.ID},
		// Quiet takes Bots from spaces it does not belong to, but Made is its canonical home;
		// Away takes Mom from the spaces she belongs to, and she does not belong in Made.
		"Made=Mine": {bots.ID, mom.ID, hot.ID},
		// Mom belongs in a bridge-kept space, so Away takes her out; Bots belongs too.
		"Kept=Kept":         {bots.ID},
		"Telegram=Telegram": nil,
	}
	if len(got) != len(want) {
		t.Fatalf("rows %v, want %v", got, want)
	}
	for key, rooms := range want {
		if !slices.Equal(got[key], rooms) {
			t.Errorf("row %s holds %v, want %v", key, got[key], rooms)
		}
	}
	if r := rail.Rows[0]; !r.Sticky {
		t.Errorf("Unread lost its flags: %+v", r)
	}
	if r := rail.Rows[len(rail.Rows)-1]; r.Home != ProtocolTelegram || r.Owner == "" || r.Space != home.ID {
		t.Errorf("an account's space row is %+v, want its network, account and space", r)
	}
	if j := rail.Judged[bots.ID]; !j.Silenced || j.Owner != -1 || !j.Leaves.Spaces {
		t.Errorf("Bots is judged %+v", j)
	}

	none := BuildRail(RailSpec{}, nil, []RailRoom{bots, inv})
	if len(none.Rows) != 1 || none.Rows[0].Key != RailFallbackKey || !slices.Equal(none.Rows[0].Rooms, []RoomID{bots.ID}) {
		t.Errorf("a rail of nothing is %+v, want one row of every room but invitations", none.Rows)
	}
}
