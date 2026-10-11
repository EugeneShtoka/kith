package domain

import (
	"maps"
	"strings"
)

// A room's homes are its spaces and tags (as tag:<name>). The first one picks the name
// rule, a download's or notification's {space}, and the room's space for place rules.

// HomeOrder ranks homes: [display] priority, then the rail order, where "*" stands for
// unnamed tags (config order), then user spaces, then network-managed spaces. A tag
// holding every room comes last.
type HomeOrder struct {
	// Priority is [display] priority: space names and tag:<name>s.
	Priority []string
	// Rail is [display.rail] order: space names, tag:<name>s, "*" and "-".
	Rail []string
	// Tags is every tag's name, in config order.
	Tags []string
	// Every is the tags holding every room, Managed the network's own spaces, Spaces
	// every known space; keys are lower-cased names.
	Every   map[string]bool
	Managed map[string]bool
	Spaces  map[string]bool
}

// NewHomeOrder builds the order from config; spaces may be nil when not yet known
// (Places.Facts marks them later).
func NewHomeOrder(priority, rail []string, tags TagSet, spaces []Space) HomeOrder {
	o := HomeOrder{Priority: priority, Rail: rail, Every: map[string]bool{}, Managed: map[string]bool{}, Spaces: map[string]bool{}}
	for i := range tags.Len() {
		name := tags.At(i).Name
		o.Tags = append(o.Tags, strings.ToLower(strings.TrimSpace(name)))
		if tags.HoldsEvery(i) {
			o.Every[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}
	return o.WithManaged(spaces)
}

// WithManaged returns o with spaces' managed ones marked; o's maps are not mutated
// (they may be shared).
func (o HomeOrder) WithManaged(spaces []Space) HomeOrder {
	if len(spaces) > 0 {
		known := make(map[string]bool, len(o.Spaces)+len(spaces))
		maps.Copy(known, o.Spaces)
		for i := range spaces {
			known[strings.ToLower(strings.TrimSpace(spaces[i].DisplayName()))] = true
		}
		o.Spaces = known
	}
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

// homeRank orders by tier, then position, then within a position.
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

func (o HomeOrder) rank(home string) homeRank {
	key := strings.ToLower(strings.TrimSpace(home))
	tag, isTag := TagOf(home)
	if isTag && o.Every[strings.ToLower(tag)] {
		return homeRank{tier: 3}
	}
	if at := o.indexIn(o.Priority, key, tag, isTag); at >= 0 {
		return homeRank{tier: 0, at: at}
	}
	if at := o.indexIn(o.Rail, key, tag, isTag); at >= 0 {
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

// indexIn finds a home in list; a tag also matches its bare name unless a space has it.
func (o HomeOrder) indexIn(list []string, key, tag string, isTag bool) int {
	if at := indexFold(list, key); at >= 0 || !isTag {
		return at
	}
	bare := strings.ToLower(strings.TrimSpace(tag))
	if o.Spaces[bare] {
		return -1
	}
	return indexFold(list, bare)
}

// Sort orders homes; ties go by name so client and daemon agree whatever their input
// order.
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

// indexFold finds lower-cased key in entries case-insensitively; -1 if absent.
func indexFold(entries []string, key string) int {
	for i, e := range entries {
		if strings.ToLower(strings.TrimSpace(e)) == key {
			return i
		}
	}
	return -1
}

// Homes is a room's spaces and tags (as TagEntry) in home order.
func Homes(spaces, tags []string, order HomeOrder) []string {
	homes := make([]string, 0, len(spaces)+len(tags))
	homes = append(homes, spaces...)
	for _, tag := range tags {
		homes = append(homes, TagEntry(tag))
	}
	return order.Sort(homes)
}

// HomeLabel is a home's display label: a tag's bare name.
func HomeLabel(home string) string {
	if name, ok := TagOf(home); ok {
		return name
	}
	return strings.TrimSpace(home)
}
