package domain

import (
	"strings"
)

// People names a person the way everything kith shows does, from what this person
// has told it: their own alias for someone first, then a name the network gave
// (a member's, a sender's, the words a mention was written in), then what the phone
// book calls their number, then the number. A surface applies its own shaping after
// (the timeline's first-name rule); People never shortens.
type People struct {
	// Alias is the person's own name for someone ([[display.identity]]), "" for none.
	Alias func(userID string) string
	// Book names a person a network shows only by number.
	Book PhoneBook
}

// Name is userID's name, from known: names the network gave, best first. A known name
// that is only a number is looked up in the phone book, and kept only when nothing
// better is known.
func (p People) Name(userID string, known ...string) string {
	if p.Alias != nil {
		if alias := strings.TrimSpace(p.Alias(userID)); alias != "" {
			return alias
		}
	}
	number := ""
	for _, k := range append(known, ShortName(userID)) {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if name, ok := p.Book.Named(k); ok {
			return name
		}
		if _, isNumber := PhoneIn(k); isNumber || digitsOnly(k) {
			if number == "" && !digitsOnly(k) {
				number = k // a number written as one, not an ID's bare digits
			}
			continue
		}
		return k
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

// ResolveMentions is body with each person a mention names written as name says,
// keeping the "@" a mention was written with, in one pass: a name containing another
// mention's words cannot be rewritten twice. name is given the mention (its Known
// name among them) and the words it was written in, without the "@" (nothing when
// they are only an ID's digits, as WhatsApp writes them). The mentions returned name
// what body now says. A room mention, or one whose words body does not hold, is left
// as it was.
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

// digitsOnly reports whether s is digits only, and some.
func digitsOnly(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}
