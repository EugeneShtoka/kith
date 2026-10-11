package phonebook

import (
	"slices"
	"strings"
)

// Index is cards held in memory, found by identifier without reading the folder.
type Index struct {
	cards []Card
	// by is the first card, by file name, holding each identifier (identifier).
	by map[string]int
}

// NewIndex indexes cards, in the order given (Book.Cards: by file name).
func NewIndex(cards []Card) Index {
	x := Index{cards: cards, by: map[string]int{}}
	for i := range cards {
		for key, values := range cards[i].IDs {
			for _, v := range values {
				if id := identifier(key, v); id != "" {
					if _, taken := x.by[id]; !taken {
						x.by[id] = i
					}
				}
			}
		}
	}
	return x
}

// Cards is every card indexed.
func (x Index) Cards() []Card { return x.cards }

// Find returns the first card sharing an identifier with ids: a phone number by its
// digits, an email in any case, the rest as written.
func (x Index) Find(ids Identifiers) (Card, bool) {
	first := -1
	for key, values := range ids {
		for _, v := range values {
			if i, ok := x.by[identifier(key, v)]; ok && (first < 0 || i < first) {
				first = i
			}
		}
	}
	if first < 0 {
		return Card{}, false
	}
	return x.cards[first], true
}

// Keys is every field key the cards use, sorted, identifiers left out.
func (x Index) Keys() []string {
	var keys []string
	for i := range x.cards {
		for _, f := range x.cards[i].Fields {
			if !slices.Contains(keys, f.Key) {
				keys = append(keys, f.Key)
			}
		}
	}
	slices.Sort(keys)
	return keys
}

// Field is the card's values of key, its case ignored; identifier keys count too.
func (c Card) Field(key string) []string {
	for k, values := range c.IDs {
		if strings.EqualFold(k, key) {
			return values
		}
	}
	for _, f := range c.Fields {
		if strings.EqualFold(f.Key, key) {
			return f.Values
		}
	}
	return nil
}
