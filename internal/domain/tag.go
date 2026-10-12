package domain

import (
	"fmt"
	"slices"
	"strings"
)

// A tag is a local, cross-network grouping of rooms; a room may be in many. Its rule
// decides membership, overridden by picked (in) and excluded (out) rooms.

// Tag is one [[tag]].
type Tag struct {
	Name     string
	Rule     []string // terms; see TagSet.Has
	Picked   []string // room entries: a room ID, or room:<name>
	Excluded []string // room entries
	Hidden   bool     // no rail row; the tag still works wherever a place is named

	// Silent (counts_unread = false): its rooms count as read everywhere else, for
	// badges, other tags' rules and mark-all-read.
	Silent bool
	// Mute (notifies = false): nothing in its rooms notifies.
	Mute          bool
	Exclusive     Exclusive
	Sticky        bool // the open room stays listed until you move off it
	HideWhenEmpty bool // no rail row while it holds nothing
	First         bool // at the top of the rail, unless the rail order places it
	CountInLabel  bool // its row's label says how many rooms it holds
}

// Exclusive is where a tag takes its rooms from.
type Exclusive struct {
	Tags   bool // other tags' rows; of two such tags, the first configured keeps it
	Spaces bool // spaces it does not belong to
	Origin bool // the spaces it belongs to: a bridge's, a community, its canonical home
}

// RoomState is what a room is right now, for a rule's state words.
type RoomState struct {
	Unread  bool // something unread, or marked unread
	Mention bool // a mention or highlight among the unread
	Draft   bool // an unsent message is kept for it
	Invite  bool
}

// Rule terms beyond the place vocabulary (ParseEntry).
const (
	termEvery = "*"
	termNot   = "not "
	termTag   = "tag:"
)

// stateWords are the rule words that read RoomState.
var stateWords = map[string]func(RoomState) bool{
	"unread":  func(s RoomState) bool { return s.Unread },
	"mention": func(s RoomState) bool { return s.Mention },
	"draft":   func(s RoomState) bool { return s.Draft },
	"invite":  func(s RoomState) bool { return s.Invite },
}

// TagEntry spells a tag as a place: `tag:<name>`.
func TagEntry(name string) string { return termTag + name }

// TagOf is the inverse of TagEntry; false when the entry names something else.
func TagOf(entry string) (string, bool) {
	entry = strings.TrimSpace(entry)
	if !hasPrefixFold(entry, termTag) {
		return "", false
	}
	return strings.TrimSpace(entry[len(termTag):]), true
}

// term is one rule term, parsed: what it names and whether it is negated.
type term struct {
	not   bool
	every bool
	tag   string               // a tag's name, lower-cased
	state func(RoomState) bool // a state word
	place string               // a place entry (RoomFacts.Names)
	// shownAs is a place:<name> term's name: its spaces match on the room's facts, a
	// tag of that name (tag, then) through the set, so cycles are broken alike.
	shownAs string
}

// parseTerm reads one rule term; known is the tags that exist, lower-cased.
func parseTerm(raw string, known map[string]bool) (term, error) {
	t := term{}
	s := strings.TrimSpace(raw)
	if hasPrefixFold(s, termNot) {
		t.not, s = true, strings.TrimSpace(s[len(termNot):])
	}
	lower := strings.ToLower(s)
	switch {
	case s == termEvery:
		t.every = true
	case stateWords[lower] != nil:
		t.state = stateWords[lower]
	case hasPrefixFold(s, termTag):
		name, _ := TagOf(s)
		if !known[strings.ToLower(name)] {
			return term{}, fmt.Errorf("%q names no tag", raw)
		}
		t.tag = strings.ToLower(name)
	case hasPrefixFold(s, entryPlace):
		_, name, ok := ParseHomeEntry(s)
		if !ok {
			return term{}, fmt.Errorf("%q names no place", raw)
		}
		t.place, t.shownAs = s, name
		if known[strings.ToLower(name)] {
			t.tag = strings.ToLower(name)
		}
	default:
		if _, ok := ParseEntry(s); !ok {
			return term{}, fmt.Errorf("%q needs a prefix: %s (or *, a state word: %s, %s; "+
				"each may follow `not`)", raw, EntryPrefixes, strings.Join(stateWordList(), ", "), EntryWords)
		}
		t.place = s
	}
	return t, nil
}

func stateWordList() []string {
	words := make([]string, 0, len(stateWords))
	for w := range stateWords {
		words = append(words, w)
	}
	slices.Sort(words)
	return words
}

// compiled is one tag, ready to judge rooms.
type compiled struct {
	tag               Tag
	positive, negated []term
	picked, excluded  []string
}

// TagSet is the parsed tags with reference cycles broken. Build it with NewTagSet.
type TagSet struct {
	tags   []compiled
	byName map[string]int // lower-cased name → index
	// cyclic marks references inside a cycle (from → to, lower-cased); they match nothing.
	cyclic map[[2]string]bool
}

// NewTagSet parses tags. An invalid tag is an error; a reference cycle is a warning,
// and the references inside it match nothing.
func NewTagSet(tags []Tag) (TagSet, []string, error) {
	set := TagSet{byName: make(map[string]int, len(tags)), cyclic: map[[2]string]bool{}}
	known := make(map[string]bool, len(tags))
	for i, t := range tags {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			return TagSet{}, nil, fmt.Errorf("tag %d: name is empty", i+1)
		}
		lower := strings.ToLower(name)
		if known[lower] {
			return TagSet{}, nil, fmt.Errorf("tag %q is defined twice", name)
		}
		known[lower] = true
	}
	for i, t := range tags {
		c := compiled{tag: t}
		for _, raw := range t.Rule {
			parsed, err := parseTerm(raw, known)
			if err != nil {
				return TagSet{}, nil, fmt.Errorf("tag %q: rule: %w", t.Name, err)
			}
			if parsed.not {
				c.negated = append(c.negated, parsed)
			} else {
				c.positive = append(c.positive, parsed)
			}
		}
		for what, entries := range map[string][]string{"picked": t.Picked, "excluded": t.Excluded} {
			for _, entry := range entries {
				if kind, ok := ParseEntry(entry); !ok || kind != EntryRoom {
					return TagSet{}, nil, fmt.Errorf("tag %q: %s: %q is not a room — write its ID or room:<name>", t.Name, what, entry)
				}
			}
		}
		c.picked, c.excluded = t.Picked, t.Excluded
		set.byName[strings.ToLower(strings.TrimSpace(t.Name))] = i
		set.tags = append(set.tags, c)
	}
	return set, set.breakCycles(), nil
}

// breakCycles marks every reference inside each strongly connected component (a
// self-reference included) and returns one warning per component.
func (s TagSet) breakCycles() []string {
	refs := s.references()
	var warnings []string
	for _, component := range stronglyConnected(refs) {
		self := len(component) == 1 && slices.Contains(refs[component[0]], component[0])
		if len(component) < 2 && !self {
			continue
		}
		slices.Sort(component) // config order, for a stable message
		names := make([]string, 0, len(component))
		for _, i := range component {
			names = append(names, s.tags[i].tag.Name)
			for _, j := range refs[i] {
				if slices.Contains(component, j) {
					s.cyclic[[2]string{s.key(i), s.key(j)}] = true
				}
			}
		}
		warnings = append(warnings, "tags depend on each other: "+strings.Join(names, ", ")+
			"; their references to each other match nothing")
	}
	return warnings
}

// references is, for each tag, the tags its rule names.
func (s TagSet) references() [][]int {
	refs := make([][]int, len(s.tags))
	for i := range s.tags {
		for _, t := range slices.Concat(s.tags[i].positive, s.tags[i].negated) {
			if t.tag != "" {
				refs[i] = append(refs[i], s.byName[t.tag])
			}
		}
	}
	return refs
}

// stronglyConnected is the graph's strongly connected components (Tarjan).
func stronglyConnected(refs [][]int) [][]int {
	index, low := make([]int, len(refs)), make([]int, len(refs))
	onStack := make([]bool, len(refs))
	for i := range index {
		index[i] = -1
	}
	var stack []int
	var components [][]int
	next := 0
	var visit func(i int)
	visit = func(i int) {
		index[i], low[i] = next, next
		next++
		stack = append(stack, i)
		onStack[i] = true
		for _, j := range refs[i] {
			if index[j] < 0 {
				visit(j)
				low[i] = min(low[i], low[j])
			} else if onStack[j] {
				low[i] = min(low[i], index[j])
			}
		}
		if low[i] != index[i] {
			return
		}
		var component []int
		for k := -1; k != i; {
			k = stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[k] = false
			component = append(component, k)
		}
		components = append(components, component)
	}
	for i := range refs {
		if index[i] < 0 {
			visit(i)
		}
	}
	return components
}

// key is tag i's lower-cased name, as byName and cyclic hold it.
func (s TagSet) key(i int) string { return strings.ToLower(strings.TrimSpace(s.tags[i].tag.Name)) }

// Tags is the tags in the order they were configured.
func (s TagSet) Tags() []Tag {
	out := make([]Tag, len(s.tags))
	for i := range s.tags {
		out[i] = s.tags[i].tag
	}
	return out
}

// Of is the names of the tags holding a room judged as a place, without state (a
// state word matches nothing, its `not` everything).
func (s TagSet) Of(facts RoomFacts) []string {
	var names []string
	for i := range s.tags {
		if s.has(i, facts, RoomState{}, map[int]bool{}) {
			names = append(names, s.tags[i].tag.Name)
		}
	}
	return names
}

// Index is the position of the named tag (case-insensitive); false when none is.
func (s TagSet) Index(name string) (int, bool) {
	i, ok := s.byName[strings.ToLower(strings.TrimSpace(name))]
	return i, ok
}

// Automatic reports whether tag i has a rule naming no place (room, space, network,
// tag), only state, dm/group or "*" (Unread, DMs, All), so filing by hand makes no
// sense.
func (s TagSet) Automatic(i int) bool {
	c := s.tags[i]
	if len(c.positive) == 0 && len(c.negated) == 0 {
		return false
	}
	for _, t := range slices.Concat(c.positive, c.negated) {
		if t.tag != "" || (t.place != "" && !isKind(t.place)) {
			return false
		}
	}
	return true
}

// isKind reports whether a place entry is a kind of room (dm, group), not a place.
func isKind(entry string) bool {
	entry = strings.TrimSpace(entry)
	return strings.EqualFold(entry, entryDirect) || strings.EqualFold(entry, entryGroup)
}

// HoldsEvery reports whether tag i's rule takes every room ("*").
func (s TagSet) HoldsEvery(i int) bool {
	for _, t := range s.tags[i].positive {
		if t.every {
			return true
		}
	}
	return false
}

// Len is how many tags there are; At is the i-th, in configured order.
func (s TagSet) Len() int { return len(s.tags) }

// At is the i-th tag, in configured order.
func (s TagSet) At(i int) Tag { return s.tags[i].tag }

// HasAt is Has for the i-th tag.
func (s TagSet) HasAt(i int, facts RoomFacts, state RoomState) bool {
	return s.has(i, facts, state, map[int]bool{})
}

// Has reports whether the named tag holds a room. Excluded beats picked beats the
// rule; the rule needs no `not` term to match and some positive term to match
// (none if it has only `not` terms). An empty rule holds only picked rooms.
func (s TagSet) Has(name string, facts RoomFacts, state RoomState) bool {
	i, ok := s.byName[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return false
	}
	return s.has(i, facts, state, map[int]bool{})
}

// has judges tag i; asking guards against recursion that cycle breaking missed.
func (s TagSet) has(i int, facts RoomFacts, state RoomState, asking map[int]bool) bool {
	c := s.tags[i]
	if slices.ContainsFunc(c.excluded, facts.Names) {
		return false
	}
	if slices.ContainsFunc(c.picked, facts.Names) {
		return true
	}
	if facts.ArchivedIn != "" && strings.EqualFold(facts.ArchivedIn, strings.TrimSpace(c.tag.Name)) {
		return true // the network archived it, and this tag is that archive
	}
	if slices.ContainsFunc(facts.Caught, func(tag string) bool { return SameTag(tag, c.tag.Name) }) {
		return true // a filter moved it here
	}
	if len(c.positive) == 0 && len(c.negated) == 0 {
		return false
	}
	asking[i] = true
	defer delete(asking, i)
	from := s.key(i)
	matches := func(t term) bool {
		switch {
		case t.every:
			return true
		case t.state != nil:
			return t.state(state)
		case t.shownAs != "" && facts.inSpaceShownAs(t.shownAs):
			return true
		case t.tag != "":
			j := s.byName[t.tag]
			if s.cyclic[[2]string{from, t.tag}] || asking[j] {
				return false
			}
			return s.has(j, facts, state, asking)
		case t.shownAs != "":
			return false
		default:
			return facts.Names(t.place)
		}
	}
	if slices.ContainsFunc(c.negated, matches) {
		return false
	}
	return len(c.positive) == 0 || slices.ContainsFunc(c.positive, matches)
}

// Filed is tag i's picked and excluded lists after putting the room in (in) or out.
// Entries naming the room are dropped; its ID is added only where the rule disagrees.
// A dropped entry that also named other rooms (of others) is replaced by their IDs.
func (s TagSet) Filed(i int, facts RoomFacts, state RoomState, in bool, others []RoomFacts) (picked, excluded []string) {
	c := s.tags[i]
	picked = refiled(c.picked, facts, others)
	excluded = refiled(c.excluded, facts, others)
	bare := s
	bare.tags = slices.Clone(s.tags)
	bare.tags[i].picked, bare.tags[i].excluded = nil, nil
	switch rule := bare.has(i, facts, state, map[int]bool{}); {
	case in && !rule:
		picked = append(picked, facts.ID)
	case !in && rule:
		excluded = append(excluded, facts.ID)
	}
	return picked, excluded
}

// refiled drops entries naming the room, replacing each with the other rooms' IDs it named.
func refiled(entries []string, facts RoomFacts, others []RoomFacts) []string {
	var out []string
	for _, entry := range entries {
		if !facts.Names(entry) {
			out = append(out, entry)
			continue
		}
		for i := range others {
			if o := &others[i]; o.ID != facts.ID && o.Names(entry) && !slices.Contains(out, o.ID) {
				out = append(out, o.ID)
			}
		}
	}
	return out
}

// Excludes reports whether the named tag's excluded list names the room: no filter
// moves it there.
func (s TagSet) Excludes(name string, facts RoomFacts) bool {
	i, ok := s.Index(name)
	return ok && slices.ContainsFunc(s.tags[i].excluded, facts.Names)
}

// Mutes reports whether one of the named tags has notifies = false.
func (s TagSet) Mutes(names []string) bool {
	return slices.ContainsFunc(names, func(name string) bool {
		i, ok := s.Index(name)
		return ok && s.tags[i].tag.Mute
	})
}
