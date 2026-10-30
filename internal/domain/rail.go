package domain

import (
	"slices"
	"strings"
)

// The rail: a row per tag, then one per space, renamed, hidden and ordered as the
// config says, each holding the rooms it lists. BuildRail judges every row for every
// room in the state the room is in now; ShownRows is the rows drawn.

// RailSpec is what a rail is built from besides the spaces and the rooms: the tags as
// the profile has them, [display.rail]'s lists, and the labels [[display.name]] gives
// rows.
type RailSpec struct {
	Tags TagSet
	// Order, Hidden and HideWhenEmpty are home entries; Order also holds dividers (one
	// or more dashes) and the wildcard ("*", every row it does not name).
	Order, Hidden, HideWhenEmpty []string
	// Labels is a renamed row's label, by its key.
	Labels map[string]string
}

// RailRoom is one room as the rail judges it.
type RailRoom struct {
	ID    RoomID
	Facts RoomFacts
	State RoomState
	// Parent is its canonical parent space; "" while unknown.
	Parent SpaceID
}

// RailRow is one rail row and the rooms it holds.
type RailRow struct {
	// Key is `tag:<name>` for a tag's row, the space's name for a space's, and
	// RailFallbackKey for the one row of a rail with nothing else; Label is what it
	// shows.
	Key, Label string
	// Space is the space a space's row is; "" for any other.
	Space SpaceID
	// Home is the network whose own space this is (an account's, a bridge's); "" for a
	// space you made and for a tag.
	Home Protocol
	// Owner is the account an account's space is, by its rooms' owner; "" for any other.
	Owner RoomOwner
	// The tag's flags (Tag): Sticky, First, CountInLabel, HideWhenEmpty.
	Sticky, First, CountInLabel, HideWhenEmpty bool
	// SepAfter draws a divider beneath the row (ShownRows).
	SepAfter bool
	// Rooms is the rooms it holds, in the order they were given.
	Rooms []RoomID
}

// Rail is every row the config and the spaces make, in default order: tags, then
// spaces; and what the tags make of each room, by room.
type Rail struct {
	Rows   []RailRow
	Judged map[RoomID]RoomTags
}

// RailFallbackKey is the key of the single row a rail with no spaces and no shown tags
// gets: every room but invitations.
const RailFallbackKey = "*all"

// railWildcard is the rail-order token standing for every row the order does not name.
const railWildcard = "*"

// BuildRail is the rail's rows, each holding the rooms it lists given their state now.
func BuildRail(spec RailSpec, spaces []Space, rooms []RailRoom) Rail {
	rows, holds := railRows(spec, spaces)
	all := make(map[RoomID]RoomTags, len(rooms))
	for i := range rooms {
		room := &rooms[i]
		judged := spec.Tags.Judge(room.Facts, room.State)
		all[room.ID] = judged
		for j := range rows {
			if holds[j](room, judged) {
				rows[j].Rooms = append(rows[j].Rooms, room.ID)
			}
		}
	}
	return Rail{Rows: rows, Judged: all}
}

// ServedRail is the rail kithd serves a profile (Rail): the rows it draws, in order
// (ShownRows), each holding its rooms, and every room the profile holds as the rail
// judged it. Revision grows with every rail kithd serves, so a client keeps the latest.
type ServedRail struct {
	Revision uint64
	Rows     []RailRow
	Rooms    []ServedRoom
}

// ServedRoom is one room of a served rail: its facts, and what the tags made of it in
// its state then.
type ServedRoom struct {
	Facts RoomFacts
	// In is the tags holding it, by name; Owner is the exclusive tag it shows under alone
	// ("" for none); Silenced says a silent tag holds it.
	In       []string
	Owner    string
	Silenced bool
}

// Served is the rail as kithd serves it: the rows ShownRows draws, and each of rooms
// with what the tags made of it.
func (r Rail) Served(spec RailSpec, rooms []RailRoom) ServedRail {
	out := ServedRail{Rows: ShownRows(r, spec), Rooms: make([]ServedRoom, 0, len(rooms))}
	for i := range rooms {
		judged := r.Judged[rooms[i].ID]
		room := ServedRoom{Facts: rooms[i].Facts, Silenced: judged.Silenced}
		for j, in := range judged.In {
			if in {
				room.In = append(room.In, spec.Tags.At(j).Name)
			}
		}
		if judged.Owner >= 0 {
			room.Owner = spec.Tags.At(judged.Owner).Name
		}
		out.Rooms = append(out.Rooms, room)
	}
	return out
}

// NeedsParent reports whether a room's canonical parent space decides which rows hold
// it: the tags holding it take it out of the spaces it does not belong to but not its
// origin, or the other way round.
func (r Rail) NeedsParent(id RoomID) bool {
	leaves := r.Judged[id].Leaves
	return leaves.Spaces != leaves.Origin
}

// Tags is what the tags make of the room by tags' indexes, as served: a tag the
// served rail names that tags lacks is left out.
func (r ServedRoom) Tags(tags TagSet) RoomTags {
	out := RoomTags{In: make([]bool, tags.Len()), Silenced: r.Silenced, Owner: -1}
	for _, name := range r.In {
		if i, ok := tags.Index(name); ok {
			out.In[i] = true
		}
	}
	if i, ok := tags.Index(r.Owner); ok && r.Owner != "" {
		out.Owner = i
	}
	return out
}

// railHolds reports whether a row holds a room, given what the tags make of it.
type railHolds func(*RailRoom, RoomTags) bool

// railRows is the rows, renamed, and what each holds: a row per shown tag, then one per
// space; one row of every room but invitations when there are neither.
func railRows(spec RailSpec, spaces []Space) ([]RailRow, []railHolds) {
	var rows []RailRow
	var holds []railHolds
	for i := range spec.Tags.Len() {
		t := spec.Tags.At(i)
		if t.Hidden {
			continue
		}
		rows = append(rows, RailRow{
			Key: TagEntry(t.Name), Label: t.Name,
			Sticky: t.Sticky, First: t.First, CountInLabel: t.CountInLabel, HideWhenEmpty: t.HideWhenEmpty,
		})
		holds = append(holds, func(_ *RailRoom, judged RoomTags) bool { return judged.shows(i) })
	}
	for _, s := range spaces {
		rows = append(rows, spaceRow(s))
		holds = append(holds, spaceHolds(s))
	}
	if len(rows) == 0 {
		rows = []RailRow{{Key: RailFallbackKey, Label: "All"}}
		holds = []railHolds{func(r *RailRoom, _ RoomTags) bool { return !r.State.Invite }}
	}
	for i := range rows {
		if label := spec.Labels[rows[i].Key]; label != "" {
			rows[i].Label = label
		}
	}
	return rows, holds
}

// spaceRow is a space's row, without its rooms.
func spaceRow(s Space) RailRow {
	name := s.DisplayName()
	row := RailRow{Key: name, Label: name, Space: s.ID}
	if s.Bridge != "" && s.Bridge != ProtocolMatrix {
		row.Home = s.Bridge
	}
	if s.Leaving == LeftBySigningOut {
		row.Owner = OwnerOf(RoomID(s.ID))
	}
	return row
}

// spaceHolds is what a space's row holds: its children, but invitations and the rooms a
// tag takes out of it.
func spaceHolds(s Space) railHolds {
	children := make(map[RoomID]bool, len(s.Children))
	for _, id := range s.Children {
		children[id] = true
	}
	// A bridge-kept space (a network's grouping, a community) is where its rooms belong; a
	// person-made space is not.
	belongs := s.Keeper != "" || s.Bridge.IsBridged()
	return func(r *RailRoom, judged RoomTags) bool {
		// An invitation is not in any space until dealt with.
		if !children[r.ID] || r.State.Invite {
			return false
		}
		// Its origin is where it belongs: a bridge's space or its canonical home.
		return !judged.leavesSpace(belongs || r.Parent == s.ID)
	}
}

// ShownRows is the rows the rail draws: hidden rows and empty ones that hide while empty
// dropped, then ordered, with a divider after the row a divider follows. Order: named
// rows first, the wildcard standing for every unnamed row, the rest in default order at
// the end; a row marked first goes to the top unless the order places it. Hiding every
// row keeps the first.
func ShownRows(rail Rail, spec RailSpec) []RailRow {
	rows := slices.Clone(rail.Rows)
	if len(rows) == 0 {
		return nil
	}
	hidden := make(map[string]bool, len(spec.Hidden))
	for _, entry := range spec.Hidden {
		for _, key := range rowsNamed(rows, entry) {
			hidden[key] = true
		}
	}
	for _, r := range rows {
		if (r.HideWhenEmpty || slices.ContainsFunc(spec.HideWhenEmpty, r.namedBy)) && len(r.Rooms) == 0 {
			hidden[r.Key] = true
		}
	}
	byKey := make(map[string]RailRow, len(rows))
	kept := make([]RailRow, 0, len(rows))
	for _, r := range rows {
		if hidden[r.Key] {
			continue
		}
		byKey[r.Key] = r
		kept = append(kept, r)
	}
	order := resolveOrder(rows, spec.Order)
	var shown []RailRow
	switch {
	case len(kept) == 0: // never leave the rail empty
		shown = rows[:1]
	case len(order) == 0:
		shown = kept
	default:
		shown = orderRows(kept, byKey, order)
	}
	return withoutTrailingSeparator(promoteFirst(shown, order))
}

// resolveOrder maps each rail-order entry to the keys of the rows it names, in rail order
// (a place:<name> can name several). Dividers and the wildcard stay as written; an entry
// naming no row drops out.
func resolveOrder(rows []RailRow, order []string) []string {
	out := make([]string, 0, len(order))
	for _, tok := range order {
		if isSeparator(tok) || isWildcard(tok) {
			out = append(out, tok)
			continue
		}
		out = append(out, rowsNamed(rows, tok)...)
	}
	return out
}

// rowsNamed is the keys of the rows a home entry names, in rail order.
func rowsNamed(rows []RailRow, entry string) []string {
	var keys []string
	for _, r := range rows {
		if r.namedBy(entry) {
			keys = append(keys, r.Key)
		}
	}
	return keys
}

// namedBy reports whether a home entry names the row: space:<its name>, tag:<its name>, or
// place: with its label or name.
func (r RailRow) namedBy(entry string) bool { return NamesHome(entry, r.Key, r.Label) }

// orderRows emits the kept rows in configured order, unplaced ones last.
func orderRows(kept []RailRow, byKey map[string]RailRow, order []string) []RailRow {
	// The wildcard expands against the whole order, so a row named after it (`["*", "archived"]`)
	// is not swallowed.
	named := make(map[string]bool, len(order))
	for _, tok := range order {
		if !isSeparator(tok) && !isWildcard(tok) {
			named[tok] = true
		}
	}

	placed := make(map[string]bool, len(kept))
	out := make([]RailRow, 0, len(kept))
	for _, tok := range order {
		switch {
		case isSeparator(tok):
			if len(out) > 0 {
				out[len(out)-1].SepAfter = true
			}
		case isWildcard(tok):
			out = placeUnnamed(out, kept, named, placed)
		default:
			if r, ok := byKey[tok]; ok && !placed[tok] {
				out, placed[tok] = append(out, r), true
			}
		}
	}
	for _, r := range kept {
		if !placed[r.Key] {
			out = append(out, r)
		}
	}
	return out
}

// placeUnnamed appends every row the order neither names nor has placed — the wildcard's share.
func placeUnnamed(out, kept []RailRow, named, placed map[string]bool) []RailRow {
	for _, r := range kept {
		if !named[r.Key] && !placed[r.Key] {
			out, placed[r.Key] = append(out, r), true
		}
	}
	return out
}

// promoteFirst moves rows marked first (a tag with `first`, as Invites) to the top unless the
// resolved order places them: unnamed rows go last, which would bury invitations.
func promoteFirst(rows []RailRow, order []string) []RailRow {
	var front, rest []RailRow
	for _, r := range rows {
		if r.First && !slices.Contains(order, r.Key) {
			front = append(front, r)
		} else {
			rest = append(rest, r)
		}
	}
	if len(front) == 0 {
		return rows
	}
	return append(front, rest...)
}

// withoutTrailingSeparator drops a divider under the last row, e.g. from
// `["*", "-", "tag:Archived"]` on a day nothing is archived.
func withoutTrailingSeparator(rows []RailRow) []RailRow {
	if n := len(rows); n > 0 && rows[n-1].SepAfter {
		rows[n-1].SepAfter = false
	}
	return rows
}

// isWildcard reports whether a rail-order token is the wildcard.
func isWildcard(tok string) bool { return tok == railWildcard }

// isSeparator reports whether a rail-order token is one or more dashes (a divider).
func isSeparator(tok string) bool { return tok != "" && strings.Trim(tok, "-") == "" }
