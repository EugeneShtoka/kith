package domain

import (
	"maps"
	"strings"
)

// A room's homes are where it lives, for everything that picks one: its spaces and
// the tags holding it (as tag:<name>). The first decides which name rule applies, a
// download's {space}, a notification's {space}, and which place rule ranks as the
// room's space. They are ranked by HomeOrder.

// HomeOrder is the order a room's homes are chosen in, most preferred first: what
// [display] priority names, in its order; then what the rail order names, in its
// order, where "*" — every group the rail does not name — is the tags in config
// order, then the spaces you made, then a network's own spaces (a bridge's, or the
// account or workspace its rooms come from: Managed or IsBridged), which say where a
// room comes from, not where you put it. A tag of every room (All) comes last: it is the same
// place as everywhere, so it is a room's home only when nothing else is.
type HomeOrder struct {
	// Priority is [display] priority: space names and tag:<name>s.
	Priority []string
	// Rail is [display.rail] order: space names, tag:<name>s, "*" and "-".
	Rail []string
	// Tags is every tag's name, in config order.
	Tags []string
	// Every is the tags holding every room, and Managed the network's own spaces, by
	// lower-cased name.
	Every   map[string]bool
	Managed map[string]bool
}

// NewHomeOrder is the home order the config gives (priority, rail order, tags), with
// the network's own spaces among spaces marked; spaces may be nil where they are not
// known yet (Places.Facts marks a room's own from its holders).
func NewHomeOrder(priority, rail []string, tags TagSet, spaces []Space) HomeOrder {
	o := HomeOrder{Priority: priority, Rail: rail, Every: map[string]bool{}, Managed: map[string]bool{}}
	for i := range tags.Len() {
		name := tags.At(i).Name
		o.Tags = append(o.Tags, strings.ToLower(strings.TrimSpace(name)))
		if tags.HoldsEvery(i) {
			o.Every[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}
	return o.WithManaged(spaces)
}

// WithManaged is o with the network's own spaces among spaces marked too; o itself
// is left as it was (its map may be shared).
func (o HomeOrder) WithManaged(spaces []Space) HomeOrder {
	marked := false
	for i := range spaces {
		if spaces[i].Managed() || spaces[i].IsBridged() {
			if !marked {
				managed := make(map[string]bool, len(o.Managed)+len(spaces))
				maps.Copy(managed, o.Managed)
				o.Managed, marked = managed, true
			}
			o.Managed[strings.ToLower(strings.TrimSpace(spaces[i].DisplayName()))] = true
		}
	}
	return o
}

// railRest is the rail entry standing for every group it does not name.
const railRest = "*"

// homeRank is where one home goes: by tier, then position, then within a position.
type homeRank struct{ tier, at, within int }

func (r homeRank) less(o homeRank) bool {
	if r.tier != o.tier {
		return r.tier < o.tier
	}
	if r.at != o.at {
		return r.at < o.at
	}
	return r.within < o.within
}

// rank places one home (a space name or a tag:<name>).
func (o HomeOrder) rank(home string) homeRank {
	key := strings.ToLower(strings.TrimSpace(home))
	tag, isTag := TagOf(home)
	if isTag && o.Every[strings.ToLower(tag)] {
		return homeRank{tier: 3}
	}
	if at := indexFold(o.Priority, key); at >= 0 {
		return homeRank{tier: 0, at: at}
	}
	if at := indexFold(o.Rail, key); at >= 0 {
		return homeRank{tier: 1, at: at}
	}
	// The rest go where "*" stands, or after the rail when it has none.
	rest := indexFold(o.Rail, railRest)
	if rest < 0 {
		rest = len(o.Rail)
	}
	switch {
	case isTag:
		return homeRank{tier: 1, at: rest, within: indexFold(o.Tags, strings.ToLower(tag))}
	case o.Managed[key]:
		return homeRank{tier: 1, at: rest, within: len(o.Tags) + 1}
	default:
		return homeRank{tier: 1, at: rest, within: len(o.Tags)}
	}
}

// Sort is homes in this order. Homes that rank alike (two spaces neither priority nor
// the rail names) go by name, so the order never depends on the order they arrive in:
// the client and the daemon list a room's spaces each their own way, and must agree.
func (o HomeOrder) Sort(homes []string) []string {
	out := make([]string, len(homes))
	copy(out, homes)
	ranks := make(map[string]homeRank, len(out))
	for _, h := range out {
		ranks[h] = o.rank(h)
	}
	before := func(a, b string) bool {
		if ranks[a] != ranks[b] {
			return ranks[a].less(ranks[b])
		}
		return strings.ToLower(a) < strings.ToLower(b)
	}
	// Insertion sort: a room has a handful of homes.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && before(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// indexFold is where key (lower-cased already) is among entries, case-insensitively;
// -1 when it is not.
func indexFold(entries []string, key string) int {
	for i, e := range entries {
		if strings.ToLower(strings.TrimSpace(e)) == key {
			return i
		}
	}
	return -1
}

// Homes is a room's spaces and tags in home order; a tag is TagEntry(name), so a tag
// and a space of the same name stay apart. tags is in config order.
func Homes(spaces, tags []string, order HomeOrder) []string {
	homes := make([]string, 0, len(spaces)+len(tags))
	homes = append(homes, spaces...)
	for _, tag := range tags {
		homes = append(homes, TagEntry(tag))
	}
	return order.Sort(homes)
}

// HomeLabel is a home as a person reads it: a tag by its name.
func HomeLabel(home string) string {
	if name, ok := TagOf(home); ok {
		return name
	}
	return strings.TrimSpace(home)
}
