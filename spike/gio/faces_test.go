package main

import "testing"

// The desktop's own cases (faces.test.ts), so both windows draw a face alike.
func TestInitialsAsTheDesktop(t *testing.T) {
	for name, want := range map[string]string{
		"Ada Lovelace": "AL", "ada byron lovelace": "AL", "Ada": "A", "@ada:example.org": "A",
		"  (Work) chat ": "WC", "Ölga Ünal": "ÖÜ", "🎉 Party": "P", "Лев Толстой": "ЛТ",
		"+1 555 0100": "#", "🎉": "#", "": "?", "   ": "?",
	} {
		if got := initials(name); got != want {
			t.Errorf("initials(%q) = %q, want %q", name, got, want)
		}
	}
}

// Values from the desktop's hueOf, run in node: UTF-16 units, astral ones as pairs.
func TestHueOfAsTheDesktop(t *testing.T) {
	for id, want := range map[string]int{"@ada:example.org": 5, "slack:T0X/C1": 6, "Лев": 5, "🎉 party": 6} {
		if got := hueOf(id, 8); got != want {
			t.Errorf("hueOf(%q) = %d, want %d", id, got, want)
		}
	}
}
