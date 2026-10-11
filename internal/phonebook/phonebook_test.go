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
	if _, err := b.Set(path, "pets", []string{"Rex", "Mia"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Set(path, "birthday", nil, false); err != nil {
		t.Fatal(err)
	}
	card, err := b.Set(path, "origin", []string{"Haifa"}, false)
	if err != nil {
		t.Fatal(err)
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
	card, err := b.Create("Dana Levi", Identifiers{"phone": {"+15550100009"}, "telegram": {"77"}})
	if err != nil {
		t.Fatal(err)
	}
	if card.Name != "Dana Levi 2" || card.IDs["telegram"][0] != "77" || card.IDs["phone"][0] != "+15550100009" {
		t.Errorf("card = %+v", card)
	}
	odd, err := b.Create(" a/b: c? ", nil)
	if err != nil || odd.Name != "ab c" {
		t.Errorf("odd name = %q, %v", odd.Name, err)
	}
	if _, err := b.Set(card.Path, "spouse", []string{"[[Eli Levi]]"}, false); err != nil {
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
