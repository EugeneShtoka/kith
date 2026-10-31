package domain

import (
	"slices"
	"strings"
)

// Filing a room: the places the filing picker offers it (its spaces and the tags filed
// by hand), ticked where they hold it now and in one order for every client, and what
// filing it into or out of a tag asks of its network's archive.

// FilingPlace is one place the filing picker offers: a space (Space set) or a tag (Tag,
// its name as configured), labeled as the rail labels it, In where it holds the room now.
type FilingPlace struct {
	Space SpaceID
	Tag   string
	Label string
	In    bool
}

// FilingRail is what the filing picker is built from: the rail's rows in order, the
// spaces of the room's profile, its tags and which of them hold the room (by index), the
// homes' order (a filing_spaces entry may name a space by its rail label), [display]
// priority with the spaces' labels, and [display] filing_spaces.
type FilingRail struct {
	Rows        []RailRow
	Spaces      []Space
	Tags        TagSet
	Held        []bool
	Homes       HomeOrder
	Priority    []string
	SpaceLabels map[string]string
	Named       []string
}

// FilingPlaces is the filing picker for room: the spaces then the tags, in [display]
// priority then rail order, the tags the rail does not show last. A [display]
// filing_spaces list keeps the spaces it names in its written order before the tags;
// false when it names none.
func FilingPlaces(room RoomID, r FilingRail) ([]FilingPlace, bool) {
	spaces := r.spacePlaces(room)
	tags := r.tagPlaces()
	if len(r.Named) > 0 {
		if len(spaces) == 0 {
			return nil, false
		}
		return append(spaces, r.byPriority(tags)...), true
	}
	places := slices.Concat(spaces, tags)
	position := func(p FilingPlace) int {
		key := r.key(p)
		if at := slices.IndexFunc(r.Rows, func(row RailRow) bool { return row.Key == key }); at >= 0 {
			return at
		}
		return len(r.Rows)
	}
	slices.SortStableFunc(places, func(a, b FilingPlace) int { return position(a) - position(b) })
	return r.byPriority(places), true
}

// key names a place as [display] priority and the rail do: a space by its name, a tag
// as tag:<name>.
func (r FilingRail) key(p FilingPlace) string {
	if p.Space == "" {
		return TagEntry(p.Tag)
	}
	if at := slices.IndexFunc(r.Spaces, func(s Space) bool { return s.ID == p.Space }); at >= 0 {
		return r.Spaces[at].DisplayName()
	}
	return string(p.Space)
}

// byPriority puts the places [display] priority names first, keeping the rest in order.
func (r FilingRail) byPriority(places []FilingPlace) []FilingPlace {
	keys := make([]string, len(places))
	for i := range places {
		keys[i] = r.key(places[i])
	}
	out := make([]FilingPlace, 0, len(places))
	used := make([]bool, len(places))
	for _, key := range NewHomeOrder(r.Priority, nil, TagSet{}, nil, r.SpaceLabels).ByPriority(keys) {
		for i := range places {
			if !used[i] && keys[i] == key {
				used[i] = true
				out = append(out, places[i])
				break
			}
		}
	}
	return out
}

// spacePlaces is a place per space on the rail, in its order and labels. A managed space
// (a bridge's, or an origin) is left out: filing into it is meaningless. A hidden space
// is absent, so never unfiled. Named by filing_spaces, the spaces it names instead.
func (r FilingRail) spacePlaces(room RoomID) []FilingPlace {
	if len(r.Named) > 0 {
		return r.namedSpaces(room)
	}
	out := make([]FilingPlace, 0, len(r.Rows))
	for _, row := range r.Rows {
		space, ok := r.spaceOfRow(row)
		if !ok || space.Managed() {
			continue
		}
		out = append(out, spacePlace(space, row.Label, room))
	}
	return out
}

// spaceOfRow is the space a rail row (keyed by its name) is; false for any other row,
// or for a name two spaces share, since filing into a guess is worse.
func (r FilingRail) spaceOfRow(row RailRow) (Space, bool) {
	var found Space
	hits := 0
	for i := range r.Spaces {
		if r.Spaces[i].DisplayName() == row.Key {
			found, hits = r.Spaces[i], hits+1
		}
	}
	return found, hits == 1
}

// namedSpaces is the spaces filing_spaces names, in its order, managed ones too; an
// entry naming none is skipped.
func (r FilingRail) namedSpaces(room RoomID) []FilingPlace {
	out := make([]FilingPlace, 0, len(r.Named))
	seen := make(map[SpaceID]bool, len(r.Named))
	for _, entry := range r.Named {
		for _, space := range r.spacesByEntry(entry) {
			if !seen[space.ID] {
				seen[space.ID] = true
				out = append(out, spacePlace(space, r.spaceLabel(space), room))
			}
		}
	}
	return out
}

// spacesByEntry resolves a filing_spaces entry: a space's ID, or every space a space: or
// place: entry names.
func (r FilingRail) spacesByEntry(entry string) []Space {
	entry = strings.TrimSpace(entry)
	if at := slices.IndexFunc(r.Spaces, func(s Space) bool { return s.ID == SpaceID(entry) }); at >= 0 {
		return []Space{r.Spaces[at]}
	}
	var found []Space
	for i := range r.Spaces {
		if name := r.Spaces[i].DisplayName(); NamesHome(entry, name, r.Homes.Label(name)) {
			found = append(found, r.Spaces[i])
		}
	}
	return found
}

// spaceLabel is what the rail calls a space: its row's label, else its name.
func (r FilingRail) spaceLabel(space Space) string {
	if at := slices.IndexFunc(r.Rows, func(row RailRow) bool { return row.Key == space.DisplayName() }); at >= 0 {
		return r.Rows[at].Label
	}
	return space.DisplayName()
}

func spacePlace(space Space, label string, room RoomID) FilingPlace {
	return FilingPlace{Space: space.ID, Label: label, In: slices.Contains(space.Children, room)}
}

// tagPlaces is a place per tag filed by hand (not one its rule fills, see Automatic):
// the rail's tags in its order and labels, then the ones it does not show by name.
func (r FilingRail) tagPlaces() []FilingPlace {
	var out []FilingPlace
	listed := make([]bool, r.Tags.Len())
	add := func(i int, label string) {
		listed[i] = true
		if r.Tags.Automatic(i) {
			return // filled by its rule (Unread, DMs, All…): not a place to file a room
		}
		out = append(out, FilingPlace{Tag: r.Tags.At(i).Name, Label: label, In: i < len(r.Held) && r.Held[i]})
	}
	for _, row := range r.Rows {
		if name, ok := TagOf(row.Key); ok {
			if i, ok := r.Tags.Index(name); ok && !listed[i] {
				add(i, row.Label)
			}
		}
	}
	for i := range r.Tags.Len() {
		if !listed[i] {
			add(i, r.Tags.At(i).Name)
		}
	}
	return out
}

// TagFiling files a room into the tag at index Tag of a tag set (In), or out of it.
type TagFiling struct {
	Tag int
	In  bool
}

// ArchiveFiling is the filing among filings that crosses network's mirrored archive tag
// (mirrored, by network: setup.Mirrored): filing the room in archives it on its network,
// filing it out unarchives it. followed says kith follows that archive (archives:
// setup.Archives), so the network decides whether the tag holds the room. False when no
// filing crosses one.
func ArchiveFiling(tags TagSet, mirrored, archives map[Protocol]string, network Protocol, filings []TagFiling) (f TagFiling, followed, ok bool) {
	tag := mirrored[network]
	if tag == "" {
		return TagFiling{}, false, false
	}
	for _, f := range filings {
		if strings.EqualFold(tags.At(f.Tag).Name, tag) {
			return f, strings.EqualFold(archives[network], tag), true
		}
	}
	return TagFiling{}, false, false
}

// StateIn is the state tag i judges a room in: its own (raw) for a silent tag, read for
// any other while a silent tag holds the room (silenced).
func (s TagSet) StateIn(i int, raw RoomState, silenced bool) RoomState {
	if !s.At(i).Silent && silenced {
		raw.Unread, raw.Mention = false, false
	}
	return raw
}
