package phonebook

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const dana = `---
aliases: [Dana Levi]
# her mobile
phone:
  - "+972 54-123-4567"
email: [Dana@Example.com]
birthday: 1990-05-04
pets: [Rex]
tags: [person]
---
Met at the [[Hiking club]].

---
A rule above is part of the body, not the front matter.
`

func book(t *testing.T, files map[string]string) *Book {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return New(dir)
}

// A card is found by any identifier it shares: a phone number by its digits, an email
// in any case; a file that is no card, and a card of someone else, are not found.
func TestACardIsFoundByAnIdentifier(t *testing.T) {
	t.Parallel()
	b := book(t, map[string]string{
		"Dana Levi.md": dana,
		"notes.md":     "# just notes, no front matter\n",
		"Eli.md":       "---\nphone: [\"+15550100002\"]\n---\n",
		"README.txt":   "---\nphone: [\"+972541234567\"]\n---\n",
	})
	for _, ids := range []Identifiers{
		{"phone": {"972541234567"}},
		{"email": {"dana@example.COM"}},
		{"matrix": {"@nobody:x"}, "phone": {"+972 (54) 123 4567"}},
	} {
		card, ok, err := b.Find(ids)
		if err != nil || !ok || card.Name != "Dana Levi" {
			t.Errorf("Find(%v) = %q, %v, %v; want Dana Levi", ids, card.Name, ok, err)
		}
	}
	if _, ok, _ := b.Find(Identifiers{"phone": {"+15550100003"}}); ok {
		t.Error("a number nobody has found someone")
	}
	card, _, _ := b.Find(Identifiers{"phone": {"972541234567"}})
	var keys []string
	for _, f := range card.Fields {
		keys = append(keys, f.Key+"="+strings.Join(f.Values, "|"))
	}
	if strings.Join(keys, " ") != "aliases=Dana Levi birthday=1990-05-04 pets=Rex tags=person" {
		t.Errorf("fields = %v, want the non-identifier keys in order", keys)
	}
	if got, err := New(filepath.Join(t.TempDir(), "absent")).Keys(); err != nil || len(got) != 0 {
		t.Errorf("a folder not made yet: %v, %v", got, err)
	}
}

// Setting a key changes it alone: the other keys keep their order and comments, and the
// body is kept byte for byte; no values removes the key; a new key goes last.
func TestSettingAKeyKeepsTheRest(t *testing.T) {
	t.Parallel()
	b := book(t, map[string]string{"Dana Levi.md": dana})
	path := filepath.Join(b.Dir(), "Dana Levi.md")
	card, err := b.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, set := range []Field{{Key: "pets", Values: []string{"Rex", "Mia"}, List: true}, {Key: "birthday"}, {Key: "origin", Values: []string{"Haifa"}}} {
		if card, err = b.set(card, set.Key, set.Values, set.List); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(path)
	text := string(raw)
	body := dana[strings.Index(dana, "\n---\n")+len("\n---\n"):]
	if !strings.HasSuffix(text, "---\n"+body) {
		t.Errorf("the body changed:\n%s", text)
	}
	if !strings.Contains(text, "# her mobile") || strings.Contains(text, "birthday") {
		t.Errorf("comment lost or removed key kept:\n%s", text)
	}
	var order []string
	for _, f := range card.Fields {
		order = append(order, f.Key)
	}
	if !slices.Equal(order, []string{"aliases", "pets", "tags", "origin"}) {
		t.Errorf("order = %v", order)
	}
	if card.Fields[1].Values[1] != "Mia" || !card.Fields[1].List || card.Fields[3].List {
		t.Errorf("fields = %+v", card.Fields)
	}
	if card.IDs["phone"][0] != "+972 54-123-4567" {
		t.Errorf("identifiers = %v", card.IDs)
	}
}

// A new card is named after the person, made safe and numbered when the name is taken,
// with their identifiers and the person tag; the keys of the whole folder are offered.
func TestANewCardIsMadeAndItsKeysOffered(t *testing.T) {
	t.Parallel()
	b := book(t, map[string]string{"Dana Levi.md": dana})
	card, err := b.create("Dana Levi", Identifiers{"phone": {"+15550100009"}, "telegram": {"77"}})
	if err != nil {
		t.Fatal(err)
	}
	if card.Name != "Dana Levi 2" || card.IDs["telegram"][0] != "77" || card.IDs["phone"][0] != "+15550100009" {
		t.Errorf("card = %+v", card)
	}
	odd, err := b.create(" a/b: c? ", nil)
	if err != nil || odd.Name != "ab c" {
		t.Errorf("odd name = %q, %v", odd.Name, err)
	}
	if _, err := b.set(card, "spouse", []string{"[[Eli Levi]]"}, false); err != nil {
		t.Fatal(err)
	}
	keys, _ := b.Keys()
	if !slices.Equal(keys, []string{"aliases", "birthday", "pets", "spouse", "tags"}) {
		t.Errorf("keys = %v", keys)
	}
	found, ok, _ := b.Find(Identifiers{"telegram": {"77"}})
	if !ok || found.Path != card.Path {
		t.Errorf("the new card is not found by its identifier: %+v", found)
	}
}

// A field term matches a key in any case, identifier keys too, by part of any value (any
// element of a list); several terms must all hold, and plain words look in the name and
// aliases. A word without both sides of its colon is a plain word.
func TestCardsAreSearchedByTheirFields(t *testing.T) {
	t.Parallel()
	b := book(t, map[string]string{
		"Dana Levi.md": dana,
		"Eli Stone.md": "---\nRole: Family Attorney\ncompany: [Acme, Meta]\nmatrix: [\"@eli:x\"]\n---\n",
		"Noa.md":       "---\nrole: architect\naliases: [Noa Stone]\n---\n",
	})
	cards, err := b.Cards()
	if err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]string{
		"role:attorney":             "Eli Stone",
		"ROLE:ATTORNEY":             "Eli Stone",
		"role:a":                    "Eli Stone,Noa",
		"company:meta":              "Eli Stone",
		"company:acme company:meta": "Eli Stone",
		"company:acme role:arch":    "",
		"pets:rex":                  "Dana Levi",
		"phone:123-4567":            "Dana Levi",
		"matrix:@eli":               "Eli Stone",
		"stone":                     "Eli Stone,Noa",
		"stone role:arch":           "Noa",
		"birthday:1990 dana":        "Dana Levi",
		"nickname:x":                "",
		"role: :attorney":           "",
		"":                          "Dana Levi,Eli Stone,Noa",
	} {
		var got []string
		for _, c := range cards {
			if c.Matches(ParseQuery(query)) {
				got = append(got, c.Name)
			}
		}
		if strings.Join(got, ",") != want {
			t.Errorf("%q matched %v, want %q", query, got, want)
		}
	}
	if q := ParseQuery("role: x:y :z dana"); len(q.Terms) != 1 || q.Terms[0].String() != "x:y" || strings.Join(q.Words, " ") != "role: :z dana" {
		t.Errorf("parsed %+v", q)
	}
}

// An index finds the card a person's identifiers name, the first by file name when two
// share one, as the folder does; an empty identifier names no one.
func TestAnIndexFindsCardsAsTheFolderDoes(t *testing.T) {
	t.Parallel()
	b := book(t, map[string]string{
		"Dana Levi.md": dana,
		"Dana Old.md":  "---\nemail: [dana@example.com]\n---\n",
		"Eli.md":       "---\nphone: [\"+15550100002\"]\nemail: [\"\"]\n---\n",
	})
	cards, _ := b.Cards()
	x := NewIndex(cards)
	for _, c := range []struct {
		ids  Identifiers
		want string
	}{
		{Identifiers{"email": {"DANA@example.com"}}, "Dana Levi"},
		{Identifiers{"phone": {"972 54 123 4567"}}, "Dana Levi"},
		{Identifiers{"phone": {"15550100002"}, "email": {"dana@example.com"}}, "Dana Levi"},
		{Identifiers{"phone": {"+1 555 010 0002"}}, "Eli"},
		{Identifiers{"email": {""}}, ""},
		{Identifiers{"phone": {"x"}}, ""},
		{Identifiers{"telegram": {"1"}}, ""},
	} {
		got, ok := x.Find(c.ids)
		if got.Name != c.want || ok != (c.want != "") {
			t.Errorf("%v found %q, %v; want %q", c.ids, got.Name, ok, c.want)
		}
	}
}

// A rule that hides people reads a term word for word: a value only part of a word in
// the card meets nothing, a phrase meets the same words, case aside, a word in any
// script counts as one, and an identifier key meets the identifier itself however it
// is written. Search's piece-wise matching (Matches) is left as it was.
func TestATermHoldsWordForWord(t *testing.T) {
	t.Parallel()
	b := book(t, map[string]string{
		"Dana Levi.md": dana,
		"Eli Stone.md": "---\nRole: Family Attorney\ncompany: [Acme, Meta-Platforms]\nmatrix: [\"@eli:x\"]\n---\n",
		"Noa.md":       "---\nrole: architect, former colleagues\nteam: [צוות אדום]\n---\n",
	})
	cards, err := b.Cards()
	if err != nil {
		t.Fatal(err)
	}
	for term, want := range map[string]string{
		"role:attorney":          "Eli Stone",
		"ROLE:ATTORNEY":          "Eli Stone",
		"role:family attorney":   "Eli Stone",
		"role:attorn":            "",
		"role:a":                 "",
		"role:colleagues":        "Noa",
		"role:colleague":         "",
		"company:meta":           "Eli Stone",
		"company:platforms":      "Eli Stone",
		"company:meta-platforms": "Eli Stone",
		"team:אדום":              "Noa",
		"team:אדו":               "",
		"phone:+972541234567":    "Dana Levi",
		"phone:972 54 123 4567":  "Dana Levi",
		"phone:123-4567":         "",
		"email:dana@example.com": "Dana Levi",
		"email:dana":             "",
		"matrix:@eli:x":          "Eli Stone",
		"matrix:@eli":            "",
		"nickname:x":             "",
	} {
		t.Run(term, func(t *testing.T) {
			t.Parallel()
			parsed, ok := ParseTerm(term)
			if !ok {
				t.Fatalf("%q is no term", term)
			}
			var got []string
			for _, c := range cards {
				if c.Holds(parsed) {
					got = append(got, c.Name)
				}
			}
			if strings.Join(got, ",") != want {
				t.Errorf("%q held for %v, want %q", term, got, want)
			}
		})
	}
}
