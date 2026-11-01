package phonebook

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// odd is a card written by hand: a comment on a line of its own and after a value,
// identifiers among the fields, the tags first.
const odd = `---
tags: [person] # kith's
# about her
pets: [Rex]
email: [dana@example.com]
aliases:
  - Dana
origin: Haifa
---
Met at the hiking club.
`

// folder is every file of dir, hidden ones too, by name.
func folder(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = string(raw)
	}
	return out
}

func edit(t *testing.T, path, old, replacement string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), old) {
		t.Fatalf("%s holds no %q", path, old)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), old, replacement, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// meddling is what another program does to the folder between a card's read and its
// write: whether the write must give way (moved), and whether the writer, reading again,
// writes its key again (as the desktop does when the key holds what it showed, on the
// same card).
type meddling struct {
	name   string
	do     func(t *testing.T, dir string)
	moved  bool
	again  bool
	holds  []string // in the card after, beside the key written
	broken bool     // the card cannot be read after
}

var meddlings = []meddling{
	{name: "nothing", do: func(*testing.T, string) {}},
	{name: "its time touched", do: func(t *testing.T, dir string) {
		at := time.Now().Add(time.Hour)
		if err := os.Chtimes(filepath.Join(dir, "Dana.md"), at, at); err != nil {
			t.Fatal(err)
		}
	}},
	{name: "rewritten as it was", do: func(t *testing.T, dir string) {
		if err := os.WriteFile(filepath.Join(dir, "Dana.md"), []byte(odd), 0o600); err != nil {
			t.Fatal(err)
		}
	}},
	{name: "the same key", moved: true, do: func(t *testing.T, dir string) {
		edit(t, filepath.Join(dir, "Dana.md"), "pets: [Rex]", "pets: [Bo]")
	}},
	{name: "another key", moved: true, again: true, holds: []string{"origin: Tel Aviv"}, do: func(t *testing.T, dir string) {
		edit(t, filepath.Join(dir, "Dana.md"), "origin: Haifa", "origin: Tel Aviv")
	}},
	{name: "a key added", moved: true, again: true, holds: []string{"origin: Haifa\nrole: architect\n"}, do: func(t *testing.T, dir string) {
		edit(t, filepath.Join(dir, "Dana.md"), "origin: Haifa\n", "origin: Haifa\nrole: architect\n")
	}},
	{name: "the body only", moved: true, again: true, holds: []string{"hiking club.\nAnd climbing.\n"}, do: func(t *testing.T, dir string) {
		edit(t, filepath.Join(dir, "Dana.md"), "hiking club.\n", "hiking club.\nAnd climbing.\n")
	}},
	{name: "a comment", moved: true, again: true, holds: []string{"# she moved\norigin"}, do: func(t *testing.T, dir string) {
		edit(t, filepath.Join(dir, "Dana.md"), "origin", "# she moved\norigin")
	}},
	{name: "renamed", moved: true, do: func(t *testing.T, dir string) {
		if err := os.Rename(filepath.Join(dir, "Dana.md"), filepath.Join(dir, "Dana Levi.md")); err != nil {
			t.Fatal(err)
		}
	}},
	{name: "removed", moved: true, do: func(t *testing.T, dir string) {
		if err := os.Remove(filepath.Join(dir, "Dana.md")); err != nil {
			t.Fatal(err)
		}
	}},
	{name: "its front matter broken by hand", moved: true, broken: true, do: func(t *testing.T, dir string) {
		edit(t, filepath.Join(dir, "Dana.md"), "pets: [Rex]", "pets: [Rex")
	}},
}

// A key is written only over the card as it was read. Whatever another program did
// meanwhile (the same key, another, the body, a comment, the name, the file gone, its
// front matter broken) refuses the write and leaves the folder as that program left it;
// reading the card again and writing the key onto it, when it still holds what was read,
// keeps both writers' changes. A change that is no change (the time, the same bytes)
// refuses nothing. Comments, the keys' order and the body are kept.
func TestAKeyIsWrittenOnlyOverTheCardAsRead(t *testing.T) {
	t.Parallel()
	ids := Identifiers{"email": {"dana@example.com"}}
	for _, m := range meddlings {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			b := book(t, map[string]string{"Dana.md": odd, "Eli.md": "---\nphone: [\"+15550100002\"]\n---\n"})
			read, _, err := b.Find(ids)
			if err != nil || read.Name != "Dana" {
				t.Fatalf("read %+v, %v", read, err)
			}
			m.do(t, b.Dir())
			left := folder(t, b.Dir())
			_, err = b.Write(read, "Dana", ids, "pets", []string{"Rex", "Mia"}, true)
			if got := errors.Is(err, ErrMoved); got != m.moved || (err != nil && !got) {
				t.Fatalf("write: %v; want moved %v", err, m.moved)
			}
			if m.moved {
				if after := folder(t, b.Dir()); !mapsEqual(after, left) {
					t.Fatalf("a refused write changed the folder:\n%v\nwant\n%v", after, left)
				}
				again := reapply(t, b, read, ids, m.again)
				if _, unreadable, _ := b.Scan(); m.broken != (len(unreadable) == 1 && errors.Is(unreadable[0].Err, ErrMalformed)) {
					t.Errorf("unreadable cards: %v", unreadable)
				}
				if !again {
					return
				}
			}
			now, found, err := b.Find(ids)
			if err != nil || !found {
				t.Fatalf("after: %+v %v %v", now, found, err)
			}
			text := folder(t, b.Dir())[filepath.Base(now.Path)]
			want := strings.Replace(odd, "pets: [Rex]", "pets: [Rex, Mia]", 1)
			if len(m.holds) == 0 && text != want {
				t.Errorf("card:\n%s\nwant\n%s", text, want)
			}
			for _, h := range append(m.holds, "pets: [Rex, Mia]") {
				if !strings.Contains(text, h) {
					t.Errorf("lost %q:\n%s", h, text)
				}
			}
		})
	}
}

// reapply reads the person's card again and writes the key onto it when it is the same
// card and still holds what was read, as the desktop does; it reports whether it wrote.
func reapply(t *testing.T, b *Book, read Card, ids Identifiers, want bool) bool {
	t.Helper()
	now, found, err := b.Find(ids)
	if err != nil {
		t.Fatal(err)
	}
	again := found && now.Name == read.Name && slices.Equal(now.Field("pets"), read.Field("pets"))
	if again != want {
		t.Fatalf("read again %+v (found %v): writes again %v, want %v", now, found, again, want)
	}
	if again {
		if _, err := b.Write(now, "Dana", ids, "pets", []string{"Rex", "Mia"}, true); err != nil {
			t.Fatal(err)
		}
	}
	return again
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// writeAsAWindow writes key = values on the person's card as a window does: over the
// card it read, and, refused, over the card read again while it is the same card (or
// still none) and the key holds what was read; it reports whether the key landed.
func writeAsAWindow(b *Book, ids Identifiers, key string, values []string) (bool, error) {
	shown, _, err := b.Find(ids)
	if err != nil {
		return false, err
	}
	read := shown
	for range 200 {
		_, err := b.Write(read, "Dana", ids, key, values, false)
		if !errors.Is(err, ErrMoved) {
			return err == nil, err
		}
		now, found, err := b.Find(ids)
		if err != nil {
			return false, err
		}
		if (shown.Path != "" && (!found || now.Name != shown.Name)) || !slices.Equal(now.Field(key), shown.Field(key)) {
			return false, nil
		}
		read = now
	}
	return false, errors.New("never landed")
}

// kith's writers of one folder, in one process or several (a terminal and a window),
// writing at once: a person with no card gets one card, holding every key each wrote;
// on a card there, every key lands; one key written by several holds one of their
// values, the others giving way to it.
func TestWritersAtOnceMakeOneCardAndLoseNoKey(t *testing.T) {
	t.Parallel()
	ids := Identifiers{"phone": {"+15550100001"}, "telegram": {"42"}}
	for _, start := range []struct {
		name  string
		files map[string]string
	}{
		{"no card yet", map[string]string{"Dana.md": "---\nphone: [\"+15550100002\"]\n---\nSomeone else.\n"}},
		{"a card there", map[string]string{"Dana Levi.md": "---\nphone: [\"+15550100001\"]\n---\n"}},
	} {
		t.Run(start.name, func(t *testing.T) {
			t.Parallel()
			for round := range 30 {
				b := book(t, start.files)
				const writers = 6
				var wg sync.WaitGroup
				landed := make([]bool, writers)
				errs := make([]error, writers)
				for w := range writers {
					// Two write the same key, the rest one key each; each its own Book, as
					// each process has.
					key := "k0"
					if w >= 2 {
						key = fmt.Sprintf("k%d", w)
					}
					wg.Go(func() {
						landed[w], errs[w] = writeAsAWindow(New(b.Dir()), ids, key, []string{fmt.Sprintf("v%d", w)})
					})
				}
				wg.Wait()
				cards, err := b.Cards()
				if err != nil {
					t.Fatal(err)
				}
				var theirs []Card
				for _, c := range cards {
					if _, ok := NewIndex([]Card{c}).Find(ids); ok {
						theirs = append(theirs, c)
					}
				}
				if len(theirs) != 1 {
					t.Fatalf("round %d: %d cards for one person: %v", round, len(theirs), names(theirs))
				}
				card := theirs[0]
				for w := range writers {
					if errs[w] != nil {
						t.Fatalf("round %d: writer %d: %v", round, w, errs[w])
					}
				}
				// The value it holds is a writer's that landed; the other landed only over it,
				// having read it, or gave way.
				got := strings.Join(card.Field("k0"), "")
				if at := slices.Index([]string{"v0", "v1"}, got); at < 0 || !landed[at] {
					t.Errorf("round %d: one key written twice at once: landed %v, holds %q", round, landed[:2], got)
				}
				for w := 2; w < writers; w++ {
					if got := strings.Join(card.Field(fmt.Sprintf("k%d", w)), ""); !landed[w] || got != fmt.Sprintf("v%d", w) {
						t.Errorf("round %d: writer %d's key: landed %v, holds %q", round, w, landed[w], got)
					}
				}
				for name := range folder(t, b.Dir()) {
					if strings.HasPrefix(name, ".") {
						t.Errorf("round %d: a staged file left: %s", round, name)
					}
				}
			}
		})
	}
}

func names(cards []Card) []string {
	out := make([]string, len(cards))
	for i, c := range cards {
		out[i] = c.Name
	}
	return out
}

// A new card's name comes from whoever names the person, so it is made a file of the
// folder's own whatever it holds: no separator or parent, no control or direction
// character, short enough for any file system, and numbered past every file there,
// none of which is touched; nothing is made outside the folder.
func TestANewCardsNameIsAlwaysTheFoldersOwn(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	dir := filepath.Join(parent, "phonebook")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	taken := map[string]string{"Dana.md": "---\nphone: [\"1\"]\n---\n", "Person.md": "# notes\n", "x.md": "---\nx: [\n"}
	for name, text := range taken {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	b := New(dir)
	for _, name := range []string{
		"../evil", "../../etc/passwd", "/etc/passwd", "..", ".", "...", "", "   ", "a\x00b", "a/../../b",
		"C:\\Windows\\x", "\u202eevil.exe", "Dana", "Dana", "dana", "x", "Person", ".hidden", "~", "nl\nname",
		strings.Repeat("é", 400), strings.Repeat("a", 254) + "/b", "\xff\xfe", "name\u2066x\u2069",
	} {
		card, err := b.Create(name, Identifiers{"phone": {"+15550100009"}})
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		base := filepath.Base(card.Path)
		if filepath.Dir(card.Path) != dir || len(base) > 255 || strings.HasPrefix(base, ".") ||
			strings.ContainsFunc(base, func(r rune) bool { return r < ' ' || r == '\u202e' || r == '\u2066' || r == '/' || r == '\\' }) {
			t.Errorf("%q made %q", name, card.Path)
		}
		if got, err := b.Read(card.Path); err != nil || got.IDs["phone"][0] != "+15550100009" {
			t.Errorf("%q: the card made reads %+v, %v", name, got, err)
		}
	}
	if left, _ := os.ReadDir(parent); len(left) != 1 {
		t.Errorf("made outside the folder: %v", left)
	}
	all := folder(t, dir)
	for name, text := range taken {
		if all[name] != text {
			t.Errorf("%s was written over: %q", name, all[name])
		}
	}
	for _, want := range []string{"Dana 2.md", "Dana 3.md", "x 2.md", "Person 2.md", "evil.md", "etcpasswd.md"} {
		if _, ok := all[want]; !ok {
			t.Errorf("no %s among %v", want, slices.Sorted(func(yield func(string) bool) {
				for k := range all {
					if !yield(k) {
						return
					}
				}
			}))
		}
	}
}

// A key or value a card cannot hold as one line of YAML is refused before anything is
// read or written.
func TestAFieldACardCannotHoldIsRefused(t *testing.T) {
	t.Parallel()
	b := book(t, map[string]string{"Dana.md": odd})
	card, err := b.Read(filepath.Join(b.Dir(), "Dana.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []Field{
		{Key: ""}, {Key: " pets"}, {Key: "a:b"}, {Key: "a#b"}, {Key: "-x"}, {Key: "a\nb"}, {Key: "a\x00b"},
		{Key: strings.Repeat("k", 129)}, {Key: "\xff"}, {Key: "pets", Values: []string{"a\nb"}},
		{Key: "pets", Values: []string{"a\x00"}}, {Key: "pets", Values: []string{"\xff"}},
	} {
		if _, err := b.Set(card, f.Key, f.Values, false); !errors.Is(err, ErrField) {
			t.Errorf("%q = %q: %v", f.Key, f.Values, err)
		}
		if _, err := b.Write(Card{}, "Noa", Identifiers{"telegram": {"7"}}, f.Key, append(f.Values, "x"), false); !errors.Is(err, ErrField) {
			t.Errorf("new card, %q = %q: %v", f.Key, f.Values, err)
		}
	}
	if got := folder(t, b.Dir()); len(got) != 1 || got["Dana.md"] != odd {
		t.Errorf("a refused field wrote: %v", got)
	}
	for _, key := range []string{"pets", "role", "Ünïcode key", "has space", strings.Repeat("k", 128), "[[x]]", "null"} {
		if err := CheckField(key, []string{"a, b", "[[Eli]]", "#tag", "true", "- dash"}); err != nil {
			t.Errorf("%q refused: %v", key, err)
		}
	}
}
