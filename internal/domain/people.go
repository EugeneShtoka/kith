package domain

import (
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
