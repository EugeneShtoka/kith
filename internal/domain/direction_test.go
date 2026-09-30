package domain

import "testing"

// A room's side set by hand: the narrowest entry naming it wins, and two lists naming
// it equally narrowly set nothing (the guess, or left to right, decides).
func TestTheNarrowestDirectionEntryWins(t *testing.T) {
	t.Parallel()
	room := RoomFacts{ID: "!fam:x", Name: "Family", Spaces: []string{"Friends", "Work"}}
	for _, c := range []struct {
		name string
		dirs Directions
		want Layout
	}{
		{"nothing set", Directions{}, LayoutUnset},
		{"its space", Directions{RTL: []string{"space:Friends"}}, LayoutRTL},
		{"the room over its space", Directions{RTL: []string{"space:Friends"}, LTR: []string{"!fam:x"}}, LayoutLTR},
		{"by its name", Directions{LTR: []string{"space:Friends"}, RTL: []string{"room:Family"}}, LayoutRTL},
		{"two of its spaces disagree", Directions{RTL: []string{"space:Friends"}, LTR: []string{"space:Work"}}, LayoutUnset},
		{"another room", Directions{RTL: []string{"!other:x"}}, LayoutUnset},
	} {
		if got := c.dirs.Of(room); got != c.want {
			t.Errorf("%s: Of = %v, want %v", c.name, got, c.want)
		}
	}
}

// Setting a room by hand moves its entry between the lists, and back out of both.
func TestDirectionsWithMovesOneEntry(t *testing.T) {
	t.Parallel()
	d := Directions{RTL: []string{"space:Friends"}}.With("!a:x", LayoutRTL)
	if d.Lists("!a:x") != LayoutRTL || d.Lists("space:Friends") != LayoutRTL {
		t.Fatalf("after RTL: %+v", d)
	}
	d = d.With("!a:x", LayoutLTR)
	if d.Lists("!a:x") != LayoutLTR || len(d.RTL) != 1 {
		t.Fatalf("after LTR: %+v", d)
	}
	if d = d.With("!a:x", LayoutUnset); d.Lists("!a:x") != LayoutUnset || len(d.LTR) != 0 {
		t.Fatalf("after unset: %+v", d)
	}
}

// One vote per message, by most of its letters, links left out; RTL takes more than
// half of the messages that have letters.
func TestGuessLayoutVotesPerMessage(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		bodies []string
		want   Layout
	}{
		{"nothing with letters", []string{"👍", "12:30", ""}, LayoutUnset},
		{"mostly Hebrew", []string{"שלום לכולם", "מה נשמע", "ok"}, LayoutRTL},
		{"a long English message is one vote", []string{"שלום", "מה קורה", "So far this session started rather uneventful, nothing compelling"}, LayoutRTL},
		{"half is not most", []string{"שלום", "hello"}, LayoutLTR},
		{"a link is not English", []string{"https://www.ynet.co.il/news/article/rk8rapu5gx החבר", "שלום"}, LayoutRTL},
		{"Arabic", []string{"مرحبا", "كيف حالك"}, LayoutRTL},
		{"Russian is left to right", []string{"Чтобы продать что-то", "привет"}, LayoutLTR},
	} {
		if got := GuessLayout(c.bodies); got != c.want {
			t.Errorf("%s: GuessLayout = %v, want %v", c.name, got, c.want)
		}
	}
}

// A bridge's header line is cut, and only its own: the words after it stay, and a
// sentence that merely starts alike is left alone.
func TestWithoutBridgeHeader(t *testing.T) {
	t.Parallel()
	for body, want := range map[string]string{
		"↷ Forwarded\n\n": "\n",
		"↷ Forwarded":     "",
		"↷ Forwarded\n\nשוב פעמיים יצא":             "\nשוב פעמיים יצא",
		"Forwarded message from Артём\n\n> Дорогие": "\n> Дорогие",
		"Sent an album with 4 images and 1 videos:": "",
		"Sent an album with 2 images:\nתמונות":      "תמונות",
		"Forwarded it to Dana already":              "Forwarded it to Dana already",
	} {
		if got := WithoutBridgeHeader(body); got != want {
			t.Errorf("WithoutBridgeHeader(%q) = %q, want %q", body, got, want)
		}
	}
}
