package domain

import (
	"maps"
	"slices"
	"strings"
	"time"
)

// Across bridges, the verdicts of the networks that keep none of their own are
// registers of the mesh (planning/design-mesh.md, Verdicts): a room's verdict on a tag,
// and a person's. Of two values of one, a release wins over any move, as no filter may
// put back what a person took out; otherwise the later wins. A value that neither moves
// nor releases is the verdict cleared (its tag renamed or deleted), which the cache
// keeps as no row, and which ends a release made before it. Two bridges' filters firing on one message write the same verdict,
// and the same verdict made at two times is no conflict.

// MeshVerdicts are the verdicts the mesh holds, tags lower-cased, and each tag renamed
// or deleted (Retags: the old name's lower-cased form, to the new name, "" when
// deleted).
type MeshVerdicts struct {
	Tags   []TagVerdict
	People []PersonVerdict
	Retags map[string]string
}

// verdictValue is what orders and compares two values of one verdict.
type verdictValue struct {
	released bool
	at       time.Time
	rule     MoveRule
	filter   string
}

// beats reports whether a wins over b: a release over a move, else the later (so a
// clearing made after a release ends it), else (made in the same millisecond) the one
// every bridge picks alike.
func (a verdictValue) beats(b verdictValue) bool {
	switch {
	case a.released != b.released && (a.rule != MoveNone || b.rule != MoveNone):
		return a.released
	case !a.at.Equal(b.at):
		return a.at.After(b.at)
	case a.released != b.released:
		return a.released
	case a.rule != b.rule:
		return a.rule > b.rule
	}
	return a.filter > b.filter
}

// same reports whether a and b are one verdict, whenever each was made.
func (a verdictValue) same(b verdictValue) bool {
	return a.released == b.released && (a.released || a.rule == b.rule && a.filter == b.filter)
}

func (v TagVerdict) value() verdictValue {
	if v.Released {
		return verdictValue{released: true, at: v.At}
	}
	return verdictValue{at: v.At, rule: v.Rule, filter: v.Filter}
}

func (v PersonVerdict) value() verdictValue {
	if v.Released {
		return verdictValue{released: true, at: v.At}
	}
	return verdictValue{at: v.At, rule: v.Rule, filter: v.Filter}
}

// Beats reports whether v wins over o, a value of the same room's verdict on the tag.
func (v TagVerdict) Beats(o TagVerdict) bool { return v.value().beats(o.value()) }

// Same reports whether v and o are one verdict, whenever each was made.
func (v TagVerdict) Same(o TagVerdict) bool { return v.value().same(o.value()) }

// Beats reports whether v wins over o, a value of the same person's verdict on the tag.
func (v PersonVerdict) Beats(o PersonVerdict) bool { return v.value().beats(o.value()) }

// Same reports whether v and o are one verdict, whenever each was made.
func (v PersonVerdict) Same(o PersonVerdict) bool { return v.value().same(o.value()) }

// Retag is what a retag of tag from to tag to ("" deleting it) writes over the verdicts
// mv holds: each room's and person's moved as Retagged and RetaggedPeople move them,
// and the entry under from cleared, at at or just after the entry, so the clearing
// wins over it.
func (mv MeshVerdicts) Retag(from, to string, at time.Time) MeshVerdicts {
	return MeshVerdicts{Tags: retagRooms(mv.Tags, from, to, at), People: retagPeople(mv.People, from, to, at)}
}

// retagRooms is what a retag writes over the rooms' verdicts tags (MeshVerdicts.Retag).
func retagRooms(tags []TagVerdict, from, to string, at time.Time) []TagVerdict {
	var out []TagVerdict
	rooms := map[RoomID][]TagVerdict{}
	for _, v := range tags {
		if v.In() || v.Released {
			rooms[v.Room] = append(rooms[v.Room], v)
		}
	}
	for _, room := range slices.Sorted(maps.Keys(rooms)) {
		set := rooms[room]
		retagged, changed := Retagged(set, from, to)
		if !changed {
			continue
		}
		for _, v := range set {
			if SameTag(v.Tag, from) {
				out = append(out, TagVerdict{Room: room, Tag: v.Tag, At: clearingAt(at, v.At)})
			}
		}
		for _, v := range retagged {
			i := slices.IndexFunc(set, func(o TagVerdict) bool { return SameTag(o.Tag, v.Tag) })
			if i < 0 || !set[i].Same(v) {
				v.Tag = strings.ToLower(strings.TrimSpace(v.Tag))
				out = append(out, v)
			}
		}
	}
	return out
}

// retagPeople is what a retag writes over the people's verdicts (MeshVerdicts.Retag).
func retagPeople(people []PersonVerdict, from, to string, at time.Time) []PersonVerdict {
	var out, held []PersonVerdict
	for _, v := range people {
		if v.In() || v.Released {
			held = append(held, v)
		}
	}
	retagged, _ := RetaggedPeople(held, from, to)
	for _, v := range held {
		if SameTag(v.Tag, from) {
			out = append(out, PersonVerdict{Person: v.Person, Tag: v.Tag, At: clearingAt(at, v.At)})
		}
	}
	for _, v := range retagged {
		i := slices.IndexFunc(held, func(o PersonVerdict) bool { return o.Person == v.Person && SameTag(o.Tag, v.Tag) })
		if i < 0 || !held[i].Same(v) {
			v.Tag = strings.ToLower(strings.TrimSpace(v.Tag))
			out = append(out, v)
		}
	}
	return out
}

// clearingAt is when a clearing of an entry made at made is dated: at, or the
// millisecond after made when made is later (a clock ahead), so the clearing wins.
func clearingAt(at, made time.Time) time.Time {
	if after := made.Add(time.Millisecond); !at.After(made) {
		return after
	}
	return at
}
