package main

import "testing"

func TestShortcodes(t *testing.T) {
	for in, want := range map[string]string{
		"Idk why :neutral_face:":              "Idk why 😐",
		":+1: and :thumbsup:":                 "👍 and 👍",
		"at 10:30:00 sharp":                   "at 10:30:00 sharp",
		":no_such_emoji_name: stays":          ":no_such_emoji_name: stays",
		"https://x.io/a:b:c":                  "https://x.io/a:b:c",
		"is aligned:pray:":                    "is aligned🙏",
		"done:+1: thanks":                     "done👍 thanks",
		":cold_face::face_with_rolling_eyes:": "🥶🙄",
	} {
		if got := shortcodes(in); got != want {
			t.Errorf("shortcodes(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestColorSlotsSkipTaken(t *testing.T) {
	tl := timeline{slots: map[string]int{"kithd-a": 0, "kithd-b": 2}}
	first, second := tl.color("new-1"), tl.color("new-2")
	if tl.slots["new-1"] != 1 || tl.slots["new-2"] != 3 {
		t.Fatalf("slots = %v, want new-1 in 1 and new-2 in 3", tl.slots)
	}
	if first == second || tl.color("kithd-b") != senderColors[2] || tl.color("new-1") != first {
		t.Fatal("a sender's color is not its slot's, or not stable")
	}
}
