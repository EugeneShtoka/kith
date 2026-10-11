package domain

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// tag:<name> matches the tag's rooms, judged without state, wherever a place entry is read.
func TestATagIsAPlace(t *testing.T) {
	t.Parallel()
	set := mustTags(t,
		Tag{Name: "Family", Rule: []string{"dm"}},
		Tag{Name: "Busy", Rule: []string{"unread"}},
		Tag{Name: "Quiet", Rule: []string{"not unread", "not dm"}},
	)
	places := Places{Tags: set}
	dm := places.Facts(Room{ID: "!mom:x", Name: "Mom", IsDirect: true}, nil)
	group := places.Facts(Room{ID: "!g:x", Name: "Group"}, nil)
	if !dm.Names("tag:Family") || !dm.Names("TAG:family") || group.Names("tag:Family") {
		t.Errorf("tag:Family on a DM = %t, on a group = %t", dm.Names("tag:Family"), group.Names("tag:Family"))
	}
	if dm.Names("tag:Busy") || group.Names("tag:Busy") {
		t.Error("a state word matched as a place")
	}
	if !group.Names("tag:Quiet") || dm.Names("tag:Quiet") {
		t.Error("`not` a state word does not match everything not otherwise excluded")
	}
	if kind, ok := ParseEntry("tag:Family"); !ok || kind != EntryClass {
		t.Errorf("ParseEntry(tag:Family) = %v, %t; want a class of rooms", kind, ok)
	}
}

// A room's homes are its spaces and tags in the person's priority; a tag and a space
// of one name stay apart; a tag reads by its name.
func TestHomesFollowThePriority(t *testing.T) {
	t.Parallel()
	got := Homes([]string{"Work", "Family"}, []string{"Family", "Busy"}, HomeOrder{Priority: []string{"tag:Family", "space:Work"}})
	if want := []string{"tag:Family", "Work", "tag:Busy", "Family"}; !slices.Equal(got, want) {
		t.Errorf("Homes = %v, want %v", got, want)
	}
	if HomeLabel("tag:Family") != "Family" || HomeLabel("Work") != "Work" {
		t.Error("HomeLabel does not read a tag by its name")
	}
	if got := expandDownloadTemplate("{space}/{name}{ext}", DownloadPlace{Space: "tag:Family"}, "a.jpg"); got != "Family/a.jpg" {
		t.Errorf("a download's {space} = %q, want the tag's name", got)
	}
}

// Over random configs, a room's first home follows these rules regardless of input
// order (client and daemon must agree):
//
//  1. what [display] priority names, in its order;
//  2. then by rail position: a named entry where it stands, everything it does not
//     name where "*" stands (after the rail when it has none);
//  3. at "*": tags (config order), then your spaces, then a network's own spaces;
//  4. a tag of every room only when nothing else is a home.
//
// Entries are space:, tag: and place: (a space shown under its rename, a space and a
// tag sharing a name, any case); homes one entry places tie by name.
func TestARoomsHomeFollowsTheRulesInAnyOrder(t *testing.T) {
	t.Parallel()
	for seed := range uint64(500) {
		rng := rand.New(rand.NewPCG(seed, 11))
		spaceNames := []string{"Work", "Friends", "Family", "Acme", "WhatsApp Home"}
		var spaces []Space
		for _, name := range spaceNames {
			s := Space{ID: SpaceID("!" + name), Name: name}
			if rng.IntN(2) == 0 {
				s.Original = true // the network's own
			}
			spaces = append(spaces, s)
		}
		tagDefs := []Tag{{Name: "DMs", Rule: []string{"dm"}}, {Name: "Pinned"}, {Name: "Clients"}, {Name: "Family"}, {Name: "All", Rule: []string{"*"}}}
		rng.Shuffle(len(tagDefs), func(i, j int) { tagDefs[i], tagDefs[j] = tagDefs[j], tagDefs[i] })
		tagDefs = tagDefs[:rng.IntN(len(tagDefs)+1)] // no tags at all, too
		set := mustTags(t, tagDefs...)
		var entries []string
		for _, name := range spaceNames {
			entries = append(entries, "space:"+name)
		}
		entries = append(entries, "tag:DMs", "tag:Pinned", "tag:Clients", "tag:family", "tag:All",
			"place:Family", "place:clients", "place:Pals", "place:Work", "place:whatsapp home", "place:All", "Work")
		pick := func(extra ...string) []string {
			var out []string
			for _, e := range append(slices.Clone(entries), extra...) {
				if rng.IntN(3) == 0 {
					out = append(out, e)
				}
			}
			rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
			return out
		}
		labels := map[string]string{"Acme": "Clients", "Friends": "Pals"}
		order := NewHomeOrder(pick(), pick("*", "-"), set, spaces, labels)

		// A room: some spaces, some tags.
		var roomSpaces, roomTags []string
		for _, name := range spaceNames {
			if rng.IntN(2) == 0 {
				roomSpaces = append(roomSpaces, name)
			}
		}
		for i := range set.Len() {
			if rng.IntN(2) == 0 {
				roomTags = append(roomTags, set.At(i).Name)
			}
		}
		if len(roomSpaces)+len(roomTags) == 0 {
			continue
		}
		got := Homes(roomSpaces, roomTags, order)
		for range 4 { // any arrival order, the same homes in the same order
			s, tg := slices.Clone(roomSpaces), slices.Clone(roomTags)
			rng.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })
			if again := Homes(s, tg, order); !slices.Equal(again, got) {
				t.Fatalf("seed %d: homes %v, but %v when the spaces arrive as %v", seed, got, again, s)
			}
		}
		if want := expectedHome(order, labels, roomSpaces, roomTags, spaces); got[0] != want {
			t.Errorf("seed %d: first home %q, want %q\n  priority %v\n  rail %v\n  spaces %v tags %v", seed, got[0], want, order.Priority, order.Rail, roomSpaces, roomTags)
		}
	}
}

// expectedHome is the rules above, applied one at a time; labels are the spaces' renames.
func expectedHome(order HomeOrder, labels map[string]string, spaces, tags []string, all []Space) string {
	var homes []string
	homes = append(homes, spaces...)
	for _, tag := range tags {
		homes = append(homes, "tag:"+tag)
	}
	every := func(h string) bool { name, ok := TagOf(h); return ok && name == "All" }
	candidates := slices.DeleteFunc(slices.Clone(homes), every)
	if len(candidates) == 0 {
		return "tag:All"
	}
	names := func(entry, h string) bool {
		kind, name, _ := strings.Cut(strings.ToLower(entry), ":")
		tag, isTag := strings.CutPrefix(h, "tag:")
		switch kind {
		case "space":
			return !isTag && strings.EqualFold(name, h)
		case "tag":
			return isTag && strings.EqualFold(name, tag)
		case "place":
			if isTag {
				return strings.EqualFold(name, tag)
			}
			return strings.EqualFold(name, h) || strings.EqualFold(name, labels[h])
		}
		return false // a bare name names nothing
	}
	position := func(list []string, h string) int {
		return slices.IndexFunc(list, func(e string) bool { return names(e, h) })
	}
	byName := func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) }
	// 1. priority; homes one entry places tie by name.
	if at := slices.MinFunc(candidates, func(a, b string) int {
		pa, pb := position(order.Priority, a), position(order.Priority, b)
		if pa < 0 {
			pa = len(order.Priority)
		}
		if pb < 0 {
			pb = len(order.Priority)
		}
		return pa - pb
	}); position(order.Priority, at) >= 0 {
		first := position(order.Priority, at)
		return slices.MinFunc(slices.DeleteFunc(slices.Clone(candidates), func(h string) bool {
			return position(order.Priority, h) != first
		}), byName)
	}
	// 2. rail position.
	rest := slices.Index(order.Rail, "*")
	if rest < 0 {
		rest = len(order.Rail)
	}
	railAt := func(h string) int {
		if at := position(order.Rail, h); at >= 0 {
			return at
		}
		return rest
	}
	least := slices.MinFunc(candidates, func(a, b string) int { return railAt(a) - railAt(b) })
	at := railAt(least)
	tied := slices.DeleteFunc(slices.Clone(candidates), func(h string) bool { return railAt(h) != at })
	if at != rest {
		return slices.MinFunc(tied, byName) // placed by an entry
	}
	// 3. at "*": tags in config order, then your spaces, then the network's own, each
	// group by name.
	class := func(h string) (int, int) {
		if name, ok := TagOf(h); ok {
			return 0, slices.IndexFunc(order.Tags, func(t string) bool { return strings.EqualFold(t, name) })
		}
		if slices.ContainsFunc(all, func(s Space) bool { return s.Name == h && (s.Managed() || s.IsBridged()) }) {
			return 2, 0
		}
		return 1, 0
	}
	return slices.MinFunc(tied, func(a, b string) int {
		ca, ia := class(a)
		cb, ib := class(b)
		if ca != cb {
			return ca - cb
		}
		if ia != ib {
			return ia - ib
		}
		return strings.Compare(strings.ToLower(a), strings.ToLower(b))
	})
}

// A bare name in a home order names nothing; place: names every home shown so, space:
// and tag: only their own kind.
func TestAHomeOrderNamesHomesOnlyByPrefix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		rail []string
		want string
	}{
		{[]string{"Services", "space:Work"}, "Work"},
		{[]string{"tag:Services", "space:Work"}, "tag:Services"},
		{[]string{"space:Services", "space:Work"}, "Services"},
		{[]string{"place:services", "space:Work"}, "Services"}, // both; a space sorts before tag:
		{[]string{"place:Desk", "space:Work"}, "Work"},         // Desk is Work's rename
		{[]string{"space:Desk", "place:Services"}, "Services"}, // space: reads the name, not the label
	}
	for _, c := range cases {
		o := NewHomeOrder(nil, c.rail, TagSet{}, nil, map[string]string{"Work": "Desk"})
		if got := Homes([]string{"Work", "Services"}, []string{"Services"}, o); got[0] != c.want {
			t.Errorf("rail %q: homes = %q, want %q first", c.rail, got, c.want)
		}
	}
}
