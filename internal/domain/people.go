package domain

import (
	"slices"
	"strings"
)

// People names a person. Precedence: own alias for any of their identifiers, a saved
// or bridged name, a name the network gives now, the person's chosen name, their
// number. People never shortens; surfaces shape names themselves.
type People struct {
	// Alias is the person's own name for someone ([[display.identity]]), "" for none.
	Alias func(userID string) string
	// Dir is who people are (Directory): their names, their numbers, their accounts.
	Dir Directory
}

// Name is userID's name; known are names the network gives now, best first. A known
// name that is only a number is looked up in the directory and used only as a last resort.
func (p People) Name(userID string, known ...string) string {
	if p.Alias != nil {
		for _, id := range p.Dir.Identifiers(userID) {
			if alias := strings.TrimSpace(p.Alias(id)); alias != "" {
				return alias
			}
		}
	}
	listed, rank, inDir := p.Dir.Name(userID)
	if inDir && rank <= RankBridged {
		return listed
	}
	number := ""
	pick := func(k string) (string, bool) {
		k = strings.TrimSpace(k)
		if k == "" {
			return "", false
		}
		if name, ok := p.Dir.Named(k); ok {
			return name, true
		}
		if _, isNumber := PhoneIn(k); isNumber || digitsOnly(k) {
			if number == "" && !digitsOnly(k) {
				number = k // a number written as one, not an ID's bare digits
			}
			return "", false
		}
		return k, true
	}
	for _, k := range known {
		if name, ok := pick(k); ok {
			return name
		}
	}
	if inDir {
		return listed
	}
	// The ID's own short form (a localpart, a number) only when nothing names them.
	if name, ok := pick(ShortName(userID)); ok {
		return name
	}
	if number != "" {
		return number
	}
	return userID
}

// FirstName is the first word of a name, for a place whose names are first names only.
func FirstName(name string) string {
	if i := strings.IndexAny(name, " \t"); i >= 0 {
		return name[:i]
	}
	return name
}

// ResolveMentions rewrites each user mention in body to name(m, words), keeping its
// "@", in one pass so a rewritten name is never rewritten again. words is the mention
// text without "@", empty when it is only an ID's digits (WhatsApp). The returned
// mentions match the new body. Room mentions and mentions not found in body stay.
func ResolveMentions(body string, mentions []Mention, name func(m Mention, words string) string) (string, []Mention) {
	if len(mentions) == 0 {
		return body, mentions
	}
	type place struct {
		start, end int
		text       string
		i          int
	}
	out := make([]Mention, len(mentions))
	copy(out, mentions)
	taken := make([]bool, len(body))
	var places []place
	for i, mn := range mentions {
		if mn.UserID == "" || mn.Name == "" {
			continue
		}
		start := freeIndex(body, mn.Name, taken)
		if start < 0 {
			continue
		}
		words, at := strings.CutPrefix(mn.Name, "@")
		if digitsOnly(words) {
			words = ""
		}
		drawn := strings.TrimSpace(name(mn, words))
		if drawn == "" {
			continue
		}
		if at && !strings.HasPrefix(drawn, "@") {
			drawn = "@" + drawn
		}
		end := start + len(mn.Name)
		for j := start; j < end; j++ {
			taken[j] = true
		}
		places = append(places, place{start, end, drawn, i})
	}
	if len(places) == 0 {
		return body, out
	}
	// Written in body order, each at its place in the original.
	for a := 1; a < len(places); a++ {
		for b := a; b > 0 && places[b].start < places[b-1].start; b-- {
			places[b], places[b-1] = places[b-1], places[b]
		}
	}
	var b strings.Builder
	last := 0
	for _, p := range places {
		b.WriteString(body[last:p.start])
		b.WriteString(p.text)
		last = p.end
		out[p.i].Name = p.text
	}
	b.WriteString(body[last:])
	return b.String(), out
}

// freeIndex is the first place word stands in s not already taken, -1 for none.
func freeIndex(s, word string, taken []bool) int {
	for from := 0; from <= len(s)-len(word); {
		i := strings.Index(s[from:], word)
		if i < 0 {
			return -1
		}
		i += from
		free := true
		for j := i; j < i+len(word); j++ {
			if taken[j] {
				free = false
				break
			}
		}
		if free {
			return i
		}
		from = i + 1
	}
	return -1
}

// digitsOnly reports whether s is non-empty and all digits.
func digitsOnly(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// MarkedPeople is a set of people, each given by one of their identifiers. Whoever kith
// knows to be the same person as one of them is in it too. same is an identifier's
// directly linked ones (the directory, identity groups); some links run one way (an ID
// the directory knows only by its number), so the links of every known identifier are
// followed and joined, and a link counts whichever end it is asked from.
type MarkedPeople struct {
	people identities
	marked map[string]bool // roots of the marked people
	same   func(id string) []string
}

// NewMarkedPeople is the people ids name, linked by same over ids and known.
func NewMarkedPeople(ids, known []string, same func(id string) []string) MarkedPeople {
	p := MarkedPeople{people: identities{}, marked: map[string]bool{}, same: same}
	for _, id := range slices.Concat(ids, known) {
		p.add(id)
	}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			p.marked[p.people.find(id)] = true
		}
	}
	return p
}

// add joins id with everything linked to it.
func (p MarkedPeople) add(id string) {
	if id = strings.TrimSpace(id); id == "" {
		return
	}
	p.people.find(id)
	if p.same == nil {
		return
	}
	for _, other := range p.same(id) {
		if other != "" {
			p.people.union(id, other)
		}
	}
}

// Empty reports whether nobody is marked.
func (p MarkedPeople) Empty() bool { return len(p.marked) == 0 }

// Has reports whether id is one of the marked people.
func (p MarkedPeople) Has(id string) bool {
	if p.Empty() || strings.TrimSpace(id) == "" {
		return false
	}
	ids := []string{strings.TrimSpace(id)}
	if p.same != nil {
		ids = append(ids, p.same(ids[0])...)
	}
	for _, x := range ids {
		if _, known := p.people[x]; known && p.marked[p.people.find(x)] {
			return true
		}
	}
	return false
}

// IDs is every identifier of the marked people, sorted: the ones Has finds them by. An
// ID Has finds only through a link that runs one way, to them (a number the directory
// never listed), is not among them.
func (p MarkedPeople) IDs() []string {
	var out []string
	for id := range p.people {
		if p.marked[p.people.find(id)] {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// HiddenPeople is who the muted tags hide: the people marked, linked by the directory
// and the identity groups ([[display.identity]]). You are never hidden: not your own
// IDs, nor anyone an identity group puts with them.
type HiddenPeople struct {
	marked MarkedPeople
	me     map[string]bool
}

// NewHiddenPeople is the people marked names, linked by dir and groups; selves are you.
func NewHiddenPeople(marked []string, groups [][]string, dir Directory, selves []string) HiddenPeople {
	byID := map[string][]string{}
	var known []string
	me := map[string]bool{}
	for _, id := range selves {
		me[id] = true
	}
	for _, group := range groups {
		mine := slices.ContainsFunc(group, func(id string) bool { return me[id] })
		for _, id := range group {
			byID[id] = slices.Concat(byID[id], group)
			known = append(known, id)
			if mine {
				me[id] = true
			}
		}
	}
	same := func(id string) []string {
		return slices.Concat(dir.Identifiers(id), byID[id])
	}
	return HiddenPeople{marked: NewMarkedPeople(marked, known, same), me: me}
}

// Empty reports whether nobody is hidden.
func (h HiddenPeople) Empty() bool { return h.marked.Empty() }

// Has reports whether sender is hidden.
func (h HiddenPeople) Has(sender string) bool {
	return !h.me[sender] && h.marked.Has(sender)
}
