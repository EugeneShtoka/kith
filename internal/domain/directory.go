package domain

import (
	"cmp"
	"slices"
	"strings"
)

// The directory groups identifiers (a PhoneID, network IDs) into people. Sources name
// identifiers and link them; an ID that is a number (PhoneOf) is that number too. A
// person's name is the best any source gives any of their identifiers.

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

// numberOf is id's phone identifier, "" for none.
func numberOf(id string) string {
	if strings.HasPrefix(id, phonePrefix) {
		return ""
	}
	if digits := PhoneOf(id); digits != "" {
		return PhoneID(digits)
	}
	return ""
}

// Directory is the people a set of names and links describes. Immutable once built,
// so safe to share.
type Directory struct {
	names []PersonName
	links []PersonLink
	// person maps each identifier to its person's root identifier; members maps a
	// root to its identifiers, sorted.
	person  map[string]string
	members map[string][]string
	best    map[string]PersonName
}

// NewDirectory builds a directory; row order does not matter.
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

// identities is a union-find over identifiers.
type identities map[string]string

// find returns id's root, adding id as its own root when new.
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

// known adds id, joined with its number (numberOf).
func (g identities) known(id string) {
	g.find(id)
	if number := numberOf(id); number != "" {
		g.union(id, number)
	}
}

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

// comparePersonNames orders names best first, deterministically.
func comparePersonNames(a, b PersonName) int {
	return cmp.Or(cmp.Compare(a.Rank, b.Rank), cmp.Compare(a.Source, b.Source),
		cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
}

// naming reports whether name is a real name: not empty, not a number.
func naming(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || digitsOnly(name) {
		return false
	}
	_, isNumber := PhoneIn(name)
	return !isNumber
}

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

// Name is the best name for id's person, and its rank.
func (d Directory) Name(id string) (string, NameRank, bool) {
	root, ok := d.personOf(id)
	if !ok {
		return "", 0, false
	}
	best, ok := d.best[root]
	return best.Name, best.Rank, ok
}

// Named is the name for a label that is only a phone number ("+359881234567 (WA)");
// false otherwise or when unnamed.
func (d Directory) Named(label string) (string, bool) {
	digits, ok := PhoneIn(label)
	if !ok {
		return "", false
	}
	name, _, ok := d.Name(PhoneID(digits))
	return name, ok
}

// Identifiers is every identifier of id's person, including id.
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

// Names is the names the directory was built from, sorted; they and Links carry it
// between processes.
func (d Directory) Names() []PersonName { return d.names }

// Links is the links the directory was built from, sorted.
func (d Directory) Links() []PersonLink { return d.links }

// Equal reports whether two directories were built from the same rows.
func (d Directory) Equal(o Directory) bool {
	return slices.Equal(d.names, o.names) && slices.Equal(d.links, o.links)
}
