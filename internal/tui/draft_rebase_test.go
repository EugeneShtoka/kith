package tui

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// extends is the one word-boundary rule every draft merge uses: a longer word is not
// an extension ("a10" of "a1"), and neither is a draft that starts mid-word.
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

// rebaseOnto for each way the stored draft can have moved under a save: the assistant
// appended, or the draft was deleted with its room (left elsewhere), and nothing of
// this client's is lost either way.
func TestRebaseOntoEveryWayTheStoreMoved(t *testing.T) {
	t.Parallel()
	body := func(b string) domain.StoredDraft { return domain.StoredDraft{Body: b} }
	editing := func(correction, saved string) domain.StoredDraft {
		return domain.StoredDraft{Body: correction, Editing: "$e", EditSaved: saved}
	}
	stamp := func(d domain.StoredDraft) draftStamp { d.RoomID = "!a:x"; return stampOf(d) }
	for _, c := range []struct {
		name          string
		writing       domain.StoredDraft
		base, current domain.StoredDraft
		want          domain.StoredDraft
		theirs        string
	}{
		{"they appended", body("hi there"), body("hi"), body("hi\n\nreport"), body("hi there\n\nreport"), "report"},
		{"nothing moved but the stamp", body("hi there"), body("hi"), body("hi"), body("hi there"), ""},
		{"they wrote the same", body("hi there"), body("hi"), body("hi there"), body("hi there"), ""},
		{"a draft begun on both sides", body("Hi"), domain.StoredDraft{}, body("Hi Bob, the report"), body("Hi\n\nHi Bob, the report"), "Hi Bob, the report"},
		{"a longer word is not an append", body("a1 w11"), body("a1"), body("a10"), body("a1 w11\n\na10"), "a10"},
		{"deleted with its room", body("hi there"), body("hi"), domain.StoredDraft{}, body("hi there"), ""},
		{"deleted and begun again", body("hi there"), body("hi"), body("new"), body("hi there\n\nnew"), "new"},
		{"their words wait for the edit to end", editing("fixed", "hi"), body("hi"), body("hi\n\nreport"), editing("fixed", "hi\n\nreport"), "report"},
		{"a deleted draft leaves the correction", editing("fixed", "hi w6"), editing("fixed", "hi"), domain.StoredDraft{}, editing("fixed", "hi w6"), ""},
		{"an edit ended here after the store saw it", body("hi w6"), editing("fixed", "hi"), domain.StoredDraft{}, body("hi w6"), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, theirs := rebaseOnto(c.writing, stamp(c.base), c.current)
			if !sameStored(got, c.want) || theirs != c.theirs {
				t.Errorf("rebaseOnto = %+v, theirs %q\nwant       %+v, theirs %q", got, theirs, c.want, c.theirs)
			}
		})
	}
}
