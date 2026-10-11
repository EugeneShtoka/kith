package phonebook

import "strings"

// Query is search terms over cards. A field term is key:value: the key is matched against
// a card's keys in any case (identifier keys count too), the value as part of any of the
// key's values in any case. Every other word is a plain word, which each place reading a
// query gives its own meaning; Card.Matches looks for it in the card's name and aliases.
type Query struct {
	Terms []Term
	Words []string
}

// Term is one key:value field term.
type Term struct {
	Key, Value string
}

// String is the term as typed.
func (t Term) String() string { return t.Key + ":" + t.Value }

// ParseQuery splits text into field terms and plain words. A word with nothing before or
// after its first colon is a plain word.
func ParseQuery(text string) Query {
	var q Query
	for word := range strings.FieldsSeq(text) {
		if term, ok := ParseTerm(word); ok {
			q.Terms = append(q.Terms, term)
			continue
		}
		q.Words = append(q.Words, word)
	}
	return q
}

// ParseTerm reads one key:value field term.
func ParseTerm(word string) (Term, bool) {
	key, value, ok := strings.Cut(word, ":")
	if !ok || key == "" || value == "" {
		return Term{}, false
	}
	return Term{Key: key, Value: value}, true
}

// HasTerms reports whether the query holds a field term.
func (q Query) HasTerms() bool { return len(q.Terms) > 0 }

// Fields is the query's field terms alone.
func (q Query) Fields() Query { return Query{Terms: q.Terms} }

// Matches reports whether the card meets every term and holds every word in its name or
// aliases.
func (c Card) Matches(q Query) bool {
	for _, t := range q.Terms {
		if !containsFold(c.Field(t.Key), t.Value) {
			return false
		}
	}
	names := append([]string{c.Name}, c.Field("aliases")...)
	for _, w := range q.Words {
		if !containsFold(names, w) {
			return false
		}
	}
	return true
}

// containsFold reports whether any value holds part in any case.
func containsFold(values []string, part string) bool {
	part = strings.ToLower(part)
	for _, v := range values {
		if strings.Contains(strings.ToLower(v), part) {
			return true
		}
	}
	return false
}
