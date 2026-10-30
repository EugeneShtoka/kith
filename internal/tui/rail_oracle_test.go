package tui

import (
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The oracle: the rail as the TUI built it before it moved into domain (BuildRail,
// ShownRows), kept as it was, each row admitting rooms by a closure judged per call.
// rail_diff_test.go compares the two.

type oracleGroup struct {
	key, label                                 string
	admits                                     func(unreadView, domain.Room) bool
	sepAfter                                   bool
	sticky, first, countInLabel, hideWhenEmpty bool
	home                                       domain.Protocol
	owner                                      domain.RoomOwner
}

// oracleTags is what the tags make of a room, judged as computeTags did.
type oracleTags struct {
	in       []bool
	silenced bool
	owner    int
	leaves   domain.Exclusive
}

func oracleTagsOf(v unreadView, room domain.Room) oracleTags {
	if v.tags.Len() == 0 {
		return oracleTags{owner: -1}
	}
	facts := v.factsOf(room)
	raw := v.rawState(room)
	n := v.tags.Len()
	out := oracleTags{in: make([]bool, n), owner: -1}
	for i := range n {
		if v.tags.At(i).Silent && v.tags.HasAt(i, facts, raw) {
			out.silenced = true
			break
		}
	}
	seen := raw
	if out.silenced {
		seen.Unread, seen.Mention = false, false
	}
	for i := range n {
		tag, state := v.tags.At(i), seen
		if tag.Silent {
			state = raw
		}
		if out.in[i] = v.tags.HasAt(i, facts, state); !out.in[i] {
			continue
		}
		if tag.Exclusive.Tags && out.owner < 0 {
			out.owner = i
		}
		out.leaves.Spaces = out.leaves.Spaces || tag.Exclusive.Spaces
		out.leaves.Origin = out.leaves.Origin || tag.Exclusive.Origin
	}
	return out
}

func oracleShowsInTag(v unreadView, i int, room domain.Room) bool {
	t := oracleTagsOf(v, room)
	return t.in[i] && (t.owner < 0 || t.owner == i)
}

func oracleLeavesSpace(v unreadView, room domain.Room, origin bool) bool {
	leaves := oracleTagsOf(v, room).leaves
	if origin {
		return leaves.Origin
	}
	return leaves.Spaces
}

func oracleRailGroups(spaces []domain.Space, cfg config.Rail, names []config.DisplayName, view unreadView, rooms []domain.Room,
	parents map[domain.RoomID]domain.SpaceID,
) []oracleGroup {
	groups := oracleTagGroups(view)
	for _, s := range spaces {
		children := make(map[domain.RoomID]bool, len(s.Children))
		for _, id := range s.Children {
			children[id] = true
		}
		name, spaceID := s.DisplayName(), s.ID
		belongs := s.Keeper != "" || s.Bridge.IsBridged()
		var home domain.Protocol
		if s.Bridge != "" && s.Bridge != domain.ProtocolMatrix {
			home = s.Bridge
		}
		var owner domain.RoomOwner
		if s.Leaving == domain.LeftBySigningOut {
			owner = domain.OwnerOf(domain.RoomID(spaceID))
		}
		groups = append(groups, oracleGroup{
			key: name, label: name, home: home, owner: owner,
			admits: func(v unreadView, r domain.Room) bool {
				if !children[r.ID] || r.IsInvite() {
					return false
				}
				return !oracleLeavesSpace(v, r, belongs || parents[r.ID] == spaceID)
			},
		})
	}
	if len(groups) == 0 {
		groups = []oracleGroup{{key: fallbackGroupKey, label: "All", admits: func(_ unreadView, r domain.Room) bool { return !r.IsInvite() }}}
	}
	groups, cfg.Order = oracleApplyRailConfig(groups, cfg, names, view, rooms)
	groups = oraclePromoteFirst(groups, cfg)
	if n := len(groups); n > 0 && groups[n-1].sepAfter {
		groups[n-1].sepAfter = false
	}
	return groups
}

func oracleTagGroups(view unreadView) []oracleGroup {
	groups := make([]oracleGroup, 0, view.tags.Len())
	for i := range view.tags.Len() {
		t := view.tags.At(i)
		if t.Hidden {
			continue
		}
		groups = append(groups, oracleGroup{
			key: tagGroupKey(t.Name), label: t.Name,
			admits: func(v unreadView, r domain.Room) bool { return oracleShowsInTag(v, i, r) },
			sticky: t.Sticky, first: t.First, countInLabel: t.CountInLabel, hideWhenEmpty: t.HideWhenEmpty,
		})
	}
	return groups
}

func oraclePromoteFirst(groups []oracleGroup, cfg config.Rail) []oracleGroup {
	var front, rest []oracleGroup
	for _, g := range groups {
		if g.first && !slices.Contains(cfg.Order, g.key) {
			front = append(front, g)
		} else {
			rest = append(rest, g)
		}
	}
	if len(front) == 0 {
		return groups
	}
	return append(front, rest...)
}

func oracleApplyRailConfig(groups []oracleGroup, cfg config.Rail, names []config.DisplayName, view unreadView, rooms []domain.Room) ([]oracleGroup, []string) {
	display := config.Display{Names: names}
	for i := range groups {
		if label := nameFor(display, config.GroupTarget(groups[i].key)); label != "" {
			groups[i].label = label
		}
	}
	namedBy := func(g oracleGroup, entry string) bool { return domain.NamesHome(entry, g.key, g.label) }
	named := func(entry string) []string {
		var keys []string
		for _, g := range groups {
			if namedBy(g, entry) {
				keys = append(keys, g.key)
			}
		}
		return keys
	}
	anyRoomIn := func(g oracleGroup) bool {
		return slices.ContainsFunc(rooms, func(r domain.Room) bool { return g.admits(view, r) })
	}
	hidden := make(map[string]bool, len(cfg.Hidden))
	for _, entry := range cfg.Hidden {
		for _, key := range named(entry) {
			hidden[key] = true
		}
	}
	for _, g := range groups {
		if g.hideWhenEmpty && !hidden[g.key] && !anyRoomIn(g) {
			hidden[g.key] = true
		}
	}
	for _, g := range groups {
		listed := slices.ContainsFunc(cfg.HideWhenEmpty, func(entry string) bool { return namedBy(g, entry) })
		if listed && !hidden[g.key] && !anyRoomIn(g) {
			hidden[g.key] = true
		}
	}
	byKey := make(map[string]oracleGroup, len(groups))
	kept := make([]oracleGroup, 0, len(groups))
	for _, g := range groups {
		if hidden[g.key] {
			continue
		}
		byKey[g.key] = g
		kept = append(kept, g)
	}
	order := make([]string, 0, len(cfg.Order))
	for _, tok := range cfg.Order {
		if oracleSeparator(tok) || tok == "*" {
			order = append(order, tok)
			continue
		}
		order = append(order, named(tok)...)
	}
	if len(kept) == 0 {
		return groups[:1], order
	}
	if len(order) == 0 {
		return kept, order
	}
	return oracleOrderGroups(kept, byKey, order), order
}

func oracleOrderGroups(kept []oracleGroup, byKey map[string]oracleGroup, order []string) []oracleGroup {
	named := make(map[string]bool, len(order))
	for _, tok := range order {
		if !oracleSeparator(tok) && tok != "*" {
			named[tok] = true
		}
	}
	placed := make(map[string]bool, len(kept))
	out := make([]oracleGroup, 0, len(kept))
	for _, tok := range order {
		switch {
		case oracleSeparator(tok):
			if len(out) > 0 {
				out[len(out)-1].sepAfter = true
			}
		case tok == "*":
			for _, g := range kept {
				if !named[g.key] && !placed[g.key] {
					out, placed[g.key] = append(out, g), true
				}
			}
		default:
			if g, ok := byKey[tok]; ok && !placed[tok] {
				out, placed[tok] = append(out, g), true
			}
		}
	}
	for _, g := range kept {
		if !placed[g.key] {
			out = append(out, g)
		}
	}
	return out
}

func oracleSeparator(tok string) bool { return tok != "" && strings.Trim(tok, "-") == "" }

// nameFor is the name [[display.name]] gives one target, or empty: the last entry for it.
func nameFor(d config.Display, target string) string {
	name := ""
	for _, entry := range d.Names {
		if entry.Target == target {
			name = entry.Name
		}
	}
	return name
}
