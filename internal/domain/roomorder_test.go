package domain

import (
	"reflect"
	"testing"
)

// One entry of a chain: the key and the "~" that reverses it.
func TestParseSortKey(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   string
		want SortKey
		ok   bool
	}{
		{"unread", SortKey{Key: SortUnread}, true},
		{"  Unread  ", SortKey{Key: SortUnread}, true},
		{"~unread", SortKey{Key: SortUnread, Rev: true}, true},
		{"~recent", SortKey{Key: SortRecent, Rev: true}, true},
		{"nonsense", SortKey{}, false},
		{"~nonsense", SortKey{}, false},
		{"alphabetical", SortKey{}, false},
		{"", SortKey{}, false},
	} {
		got, ok := ParseSortKey(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseSortKey(%q) = %+v, %v; want %+v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// A whole chain, with every unknown entry reported at once.
func TestParseSortChain(t *testing.T) {
	t.Parallel()

	chain, unknown := ParseSortChain([]string{"mentions", "unread", "~recent", "name"})
	want := []SortKey{{Key: SortMentions}, {Key: SortUnread}, {Key: SortRecent, Rev: true}, {Key: SortName}}
	if !reflect.DeepEqual(chain, want) {
		t.Errorf("chain = %+v, want %+v", chain, want)
	}
	if len(unknown) != 0 {
		t.Errorf("unknown = %v, want none", unknown)
	}

	chain, unknown = ParseSortChain([]string{"unread", "nonsense", "recent", "rubbish"})
	if len(unknown) != 2 || unknown[0] != "nonsense" || unknown[1] != "rubbish" {
		t.Errorf("unknown = %v, want both bad entries", unknown)
	}
	if len(chain) != 2 {
		t.Errorf("chain = %+v, want the two good keys kept", chain)
	}

	// A repeated key is dropped; it cannot change the order.
	chain, _ = ParseSortChain([]string{"recent", "unread", "recent"})
	if len(chain) != 2 || chain[0].Key != SortRecent || chain[1].Key != SortUnread {
		t.Errorf("chain = %+v, want the duplicate dropped and the first position kept", chain)
	}
}

// A rule replaces the group's chain whole.
func TestARuleReplacesTheChainWhole(t *testing.T) {
	t.Parallel()

	rules := []RoomListRule{{Group: "space:work", Sort: []string{"name"}}, {Group: "place:Pals", Sort: []string{"recent"}}}
	// Group names match case-insensitively.
	list := ResolveRoomList([]string{"unread", "recent"}, rules, "Work", "")
	if len(list.Chain) != 1 || list.Chain[0].Key != SortName {
		t.Errorf("Work chain = %+v, want the rule's chain alone", list.Chain)
	}
	for _, c := range []struct{ group, label string }{{"Friends", ""}, {"tag:Work", ""}, {"Work Chat", ""}} {
		if other := ResolveRoomList([]string{"unread", "recent"}, rules, c.group, c.label); len(other.Chain) != 2 {
			t.Errorf("%s chain = %+v, want the global chain", c.group, other.Chain)
		}
	}
	// place: names the group by what the rail shows.
	if pals := ResolveRoomList([]string{"unread", "recent"}, rules, "Friends", "Pals"); len(pals.Chain) != 1 || pals.Chain[0].Key != SortRecent {
		t.Errorf("Friends shown as Pals chain = %+v, want the place: rule's", pals.Chain)
	}
}

// The sort keys edit one half of the chain and leave the other alone.
func TestChainEdits(t *testing.T) {
	t.Parallel()

	list := RoomList{Chain: DefaultRoomChain()} // unread, recent, name
	byName := list.WithTail(SortName)
	if got := keysOf(byName); !reflect.DeepEqual(got, []string{"unread", "name"}) {
		t.Errorf("WithTail(name) = %v, want the band kept", got)
	}
	back := byName.WithTail(SortRecent, SortName)
	if got := keysOf(back); !reflect.DeepEqual(got, []string{"unread", "recent", "name"}) {
		t.Errorf("WithTail(recent, name) = %v", got)
	}
	if got := keysOf(back.Without(SortUnread)); !reflect.DeepEqual(got, []string{"recent", "name"}) {
		t.Errorf("Without(unread) = %v", got)
	}
	if got := keysOf(back.WithFront(SortMentions)); !reflect.DeepEqual(got,
		[]string{"mentions", "unread", "recent", "name"}) {
		t.Errorf("WithFront(mentions) = %v", got)
	}
	// A key already in the chain moves to the front rather than appearing twice.
	if got := keysOf(back.WithFront(SortRecent)); !reflect.DeepEqual(got,
		[]string{"recent", "unread", "name"}) {
		t.Errorf("WithFront(recent) = %v", got)
	}
	if !back.Has(SortUnread) || back.Has(SortDrafts) {
		t.Error("Has disagrees with the chain")
	}
}

// The label is the whole chain in words.
func TestChainLabel(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		chain []string
		want  string
	}{
		{[]string{"unread", "recent", "name"}, "unread first, newest first, then by name"},
		{[]string{"name"}, "by name"},
		{[]string{"drafts", "~recent"}, "drafts first, then oldest first"},
		{[]string{"mentions", "unread"}, "mentions first, then unread first"},
	} {
		parsed, _ := ParseSortChain(tc.chain)
		if got := (RoomList{Chain: parsed}).Label(); got != tc.want {
			t.Errorf("Label(%v) = %q, want %q", tc.chain, got, tc.want)
		}
	}
}

func keysOf(l RoomList) []string {
	out := make([]string, 0, len(l.Chain))
	for _, k := range l.Chain {
		out = append(out, k.String())
	}
	return out
}
