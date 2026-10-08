package domain

import (
	"cmp"
	"slices"
	"strings"
)

// The directory is who people are, from everything kith has been told. A person is
// known by identifiers: their phone number (PhoneID) and their ID on each network. A
// source (an account, a bridge login) names identifiers, ranked as the phone book's
// names are, and may say two identifiers are one person (a Telegram user and their
// number). An ID that is a number (a WhatsApp ID, a bridge's puppet: PhoneOf) is that
// number too. A person's name is the best any source gives any of their identifiers,
// so a contact saved on one phone names them on every network and account.

// PersonName is what one source calls an identifier.
type PersonName struct {
	Source string
	ID     string
	Name   string
	Rank   NameRank
}

// PersonLink is one source saying two identifiers are one person.
type PersonLink struct {
	Source string
	ID     string
	Other  string
}

// phonePrefix begins a phone number's identifier.
const phonePrefix = "tel:"

// PhoneID is the identifier of a phone number, by its international digits.
func PhoneID(digits string) string { return phonePrefix + digits }

// numberOf is the phone identifier id also is, "" for none.
func numberOf(id string) string {
	if strings.HasPrefix(id, phonePrefix) {
		return ""
	}
	if digits := PhoneOf(id); digits != "" {
		return PhoneID(digits)
	}
	return ""
}

// Directory is the people a set of names and links describes. The zero Directory
// knows no one. Safe to share: it is never changed once built.
type Directory struct {
	names []PersonName
	links []PersonLink
	// person is each identifier's person (one of their identifiers stands for them;
	// which is never seen), and members each person's identifiers, sorted.
	person  map[string]string
	members map[string][]string
	// best is each person's name and its rank.
	best map[string]PersonName
}

// NewDirectory builds the people names and links describe. The same rows, in any
// order, give the same directory.
func NewDirectory(names []PersonName, links []PersonLink) Directory {
	d := Directory{
		names: slices.Clone(names), links: slices.Clone(links),
		person: map[string]string{}, members: map[string][]string{}, best: map[string]PersonName{},
	}
	slices.SortFunc(d.names, comparePersonNames)
	slices.SortFunc(d.links, func(a, b PersonLink) int {
		return cmp.Or(cmp.Compare(a.Source, b.Source), cmp.Compare(a.ID, b.ID), cmp.Compare(a.Other, b.Other))
	})
	groups := identities{}
	for _, n := range d.names {
		if n.ID != "" {
			groups.known(n.ID)
		}
	}
	for _, l := range d.links {
		if l.ID != "" && l.Other != "" {
			groups.join(l.ID, l.Other)
		}
	}
	for id := range groups {
		root := groups.find(id)
		d.person[id] = root
		d.members[root] = append(d.members[root], id)
	}
	for root := range d.members {
		slices.Sort(d.members[root])
	}
	// Names are sorted best first: the first a person gets is theirs.
	for _, n := range d.names {
		if !naming(n.Name) {
			continue
		}
		root := d.person[n.ID]
		if _, named := d.best[root]; !named {
			d.best[root] = n
		}
	}
	return d
}

// identities groups identifiers into people: each points towards another of its
// person's, the one standing for them pointing at itself (a union-find).
type identities map[string]string

// find is the identifier standing for id's person, id joining as a person of its own
// when new.
func (g identities) find(id string) string {
	parent, ok := g[id]
	if !ok {
		g[id] = id
		return id
	}
	if parent == id {
		return id
	}
	root := g.find(parent)
	g[id] = root
	return root
}

// known adds id, with the number it is (numberOf) as the same person.
func (g identities) known(id string) {
	g.find(id)
	if number := numberOf(id); number != "" {
		g.union(id, number)
	}
}

// join makes two identifiers one person.
func (g identities) join(a, b string) {
	g.known(a)
	g.known(b)
	g.union(a, b)
}

func (g identities) union(a, b string) {
	if ra, rb := g.find(a), g.find(b); ra != rb {
		g[rb] = ra
	}
}

// comparePersonNames orders names best first: by rank, then source, then name, then
// identifier, so ties fall the same way every time.
func comparePersonNames(a, b PersonName) int {
	return cmp.Or(cmp.Compare(a.Rank, b.Rank), cmp.Compare(a.Source, b.Source),
		cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
}

// naming reports whether name names someone: not empty, not only a number.
func naming(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || digitsOnly(name) {
		return false
	}
	_, isNumber := PhoneIn(name)
	return !isNumber
}

// personOf is the person id belongs to, by id or by the number it is.
func (d Directory) personOf(id string) (string, bool) {
	if root, ok := d.person[id]; ok {
		return root, true
	}
	if number := numberOf(id); number != "" {
		root, ok := d.person[number]
		return root, ok
	}
	return "", false
}

// Name is the best name any source gives the person id is, and its rank.
func (d Directory) Name(id string) (string, NameRank, bool) {
	root, ok := d.personOf(id)
	if !ok {
		return "", 0, false
	}
	best, ok := d.best[root]
	return best.Name, best.Rank, ok
}

// Named is the directory's name for a label that is only a number ("+972 54-123-4567",
// "+359881234567 (WA)"); false for any other label, or a number it has no name for.
func (d Directory) Named(label string) (string, bool) {
	digits, ok := PhoneIn(label)
	if !ok {
		return "", false
	}
	name, _, ok := d.Name(PhoneID(digits))
	return name, ok
}

// Identifiers is every identifier of the person id is, id among them.
func (d Directory) Identifiers(id string) []string {
	if root, ok := d.personOf(id); ok {
		ids := d.members[root]
		if slices.Contains(ids, id) {
			return ids
		}
		return append(slices.Clone(ids), id)
	}
	return []string{id}
}

// Names and Links are the rows the directory was built from, sorted: what carries it
// between processes.
func (d Directory) Names() []PersonName { return d.names }

// Links is the links the directory was built from, sorted.
func (d Directory) Links() []PersonLink { return d.links }

// Equal reports whether two directories were built from the same rows.
func (d Directory) Equal(o Directory) bool {
	return slices.Equal(d.names, o.names) && slices.Equal(d.links, o.links)
}
