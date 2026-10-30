package domain

import (
	"testing"
	"time"
)

// extends needs a word boundary: a longer word is not an extension ("a10" of "a1"),
// nor is a draft starting mid-word.
func TestExtendsNeedsAWordBoundary(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		text, base string
		want       bool
	}{
		{"hi", "hi", true},
		{"hi there", "hi", true},
		{"hi\n\nthe report", "hi", true},
		{"anything", "", true},
		{"", "", true},
		{"a10", "a1", false},
		{"hid", "hi", false},
		{"h", "hi", false},
		{"", "hi", false},
		{"say hi", "hi", false},
	} {
		if got := extends(c.text, c.base); got != c.want {
			t.Errorf("extends(%q, %q) = %v, want %v", c.text, c.base, got, c.want)
		}
	}
}

// RebaseDraft for each way the stored draft can move under a write (assistant append, or
// deleted with its room elsewhere): nothing of the window's is lost.
func TestRebaseDraftEveryWayTheStoreMoved(t *testing.T) {
	t.Parallel()
	body := func(b string) StoredDraft { return StoredDraft{Body: b} }
	editing := func(correction, saved string) StoredDraft {
		return StoredDraft{Body: correction, Editing: mx + "$e", EditSaved: saved}
	}
	for _, c := range []struct {
		name          string
		writing       StoredDraft
		base, current StoredDraft
		want          StoredDraft
		theirs        string
	}{
		{"they appended", body("hi there"), body("hi"), body("hi\n\nreport"), body("hi there\n\nreport"), "report"},
		{"nothing moved but the stamp", body("hi there"), body("hi"), body("hi"), body("hi there"), ""},
		{"they wrote the same", body("hi there"), body("hi"), body("hi there"), body("hi there"), ""},
		{"a draft begun on both sides", body("Hi"), StoredDraft{}, body("Hi Bob, the report"), body("Hi\n\nHi Bob, the report"), "Hi Bob, the report"},
		{"a longer word is not an append", body("a1 w11"), body("a1"), body("a10"), body("a1 w11\n\na10"), "a10"},
		{"deleted with its room", body("hi there"), body("hi"), StoredDraft{}, body("hi there"), ""},
		{"deleted and begun again", body("hi there"), body("hi"), body("new"), body("hi there\n\nnew"), "new"},
		{"sent while they appended: theirs stays", StoredDraft{}, body("hi"), body("hi\n\nreport"), body("report"), "report"},
		{"their words wait for the edit to end", editing("fixed", "hi"), body("hi"), body("hi\n\nreport"), editing("fixed", "hi\n\nreport"), "report"},
		{"a deleted draft leaves the correction", editing("fixed", "hi w6"), editing("fixed", "hi"), StoredDraft{}, editing("fixed", "hi w6"), ""},
		{"an edit ended here after the store saw it", body("hi w6"), editing("fixed", "hi"), StoredDraft{}, body("hi w6"), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, theirs := RebaseDraft(c.writing, c.base, c.current, nil)
			if !sameVersion(got, c.want) || theirs != c.theirs {
				t.Errorf("RebaseDraft = %+v, theirs %q\nwant          %+v, theirs %q", got, theirs, c.want, c.theirs)
			}
		})
	}
}

// What current descends from: one of the window's unanswered writes it extends, the
// longest (a tie goes to the window's), else the base.
func TestDescentOf(t *testing.T) {
	t.Parallel()
	at := func(body string, ms int64) StoredDraft { return StoredDraft{Body: body, Updated: time.UnixMilli(ms)} }
	none := StoredDraft{}
	cases := []struct {
		name    string
		current StoredDraft
		base    StoredDraft
		own     []StoredDraft
		want    StoredDraft
	}{
		{"nothing of ours: the base", at("hi\n\na1", 9), at("hi", 1), nil, at("hi", 1)},
		{"our write, stored as it was", at("hi there", 5), at("hi", 1), []StoredDraft{at("hi there", 5)}, at("hi there", 5)},
		{"our write, and the assistant after it", at("hi there\n\na1", 9), at("hi", 1),
			[]StoredDraft{at("hi there", 5)}, at("hi there", 5)},
		{"the longest of ours that it extends", at("hi there you\n\na1", 9), at("hi", 1),
			[]StoredDraft{at("hi there", 5), at("hi there you", 6)}, at("hi there you", 6)},
		{"ours that it does not extend: the base", at("hi\n\na1", 9), at("hi", 1),
			[]StoredDraft{at("hi there", 5)}, at("hi", 1)},
		{"a base it does not hold does not outrank ours", at("w1\n\na6", 9), at("hi", 1),
			[]StoredDraft{at("w1", 5)}, at("w1", 5)},
		{"a tie: ours, which came after the base", at("hi\n\na1", 9), at("hi", 1),
			[]StoredDraft{at("hi", 5)}, at("hi", 5)},
		{"a reply chosen here, landed unheard, then the assistant: ours", StoredDraft{
			Body: "hi\n\na1", ReplyTo: mx + "$t", Updated: time.UnixMilli(9),
		}, at("hi", 1), []StoredDraft{{Body: "hi", ReplyTo: mx + "$t", Updated: time.UnixMilli(5)}},
			StoredDraft{Body: "hi", ReplyTo: mx + "$t", Updated: time.UnixMilli(5)}},
		{"a word boundary: a10 does not extend a1", at("a10", 9), none, []StoredDraft{at("a1", 5)}, none},
		{"words alike by chance, the edit state not: not ours", at("a2", 9), none,
			[]StoredDraft{{Body: "fixed", EditSaved: "a2", Editing: mx + "$e", Updated: time.UnixMilli(5)}}, none},
		{"words alike by chance, the reply target not: not ours", at("hi\n\na1", 9), at("hi", 1),
			[]StoredDraft{{Body: "hi\n\na1", ReplyTo: mx + "$t", Updated: time.UnixMilli(5)}}, at("hi", 1)},
		{"words alike by chance, the thread not: not ours", at("hi\n\na1", 9), at("hi", 1),
			[]StoredDraft{{Body: "hi\n\na1", ThreadRoot: mx + "$t", Updated: time.UnixMilli(5)}}, at("hi", 1)},
	}
	for _, c := range cases {
		if got := descentOf(c.current, c.base, c.own); !sameVersion(got, c.want) {
			t.Errorf("%s: descentOf = %+v, want %+v", c.name, got, c.want)
		}
	}
}
