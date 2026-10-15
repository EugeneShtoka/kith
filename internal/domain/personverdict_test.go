package domain

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// A mark or a release replaces the person's entry for that tag only; a filter's mark
// never overwrites a release, and a release can overwrite a mark.
func TestAPersonVerdictReplacesOnlyItsOwnEntry(t *testing.T) {
	t.Parallel()

	at := time.Unix(1_700_000_000, 0)
	set := []PersonVerdict{
		{Person: "@ann:x", Tag: "spam", Rule: MoveAlways, Filter: "crypto", At: at},
		{Person: "@ann:x", Tag: "work", Rule: MoveAlways, At: at},
		{Person: "@bob:x", Tag: "spam", Released: true, At: at},
	}
	if _, err := WithPersonVerdict(set, PersonVerdict{Person: "@bob:x", Tag: "Spam", Rule: MoveAlways}); !errors.Is(err, ErrPersonReleased) {
		t.Fatalf("a mark over a release = %v, want ErrPersonReleased", err)
	}
	got, err := WithPersonVerdict(set, PersonVerdict{Person: "@ann:x", Tag: "SPAM", Released: true, At: at})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !slices.ContainsFunc(got, func(v PersonVerdict) bool { return v.Person == "@ann:x" && v.Released }) ||
		!slices.ContainsFunc(got, func(v PersonVerdict) bool { return v.Person == "@ann:x" && v.Tag == "work" && v.In() }) {
		t.Errorf("releasing Ann from Spam = %+v, want her Work mark kept and Spam released", got)
	}
	if got, err := WithPersonVerdict(set, PersonVerdict{Person: "@ann:x", Tag: "work"}); err != nil || len(got) != 2 {
		t.Errorf("an empty verdict = %+v, %v; want the entry dropped", got, err)
	}
}

// Renaming a tag moves its people; deleting it drops them; combining two keeps a
// release over a mark.
func TestRetaggedPeopleFollowTheTag(t *testing.T) {
	t.Parallel()

	set := []PersonVerdict{
		{Person: "@ann:x", Tag: "junk", Rule: MoveAlways},
		{Person: "@bob:x", Tag: "junk", Released: true},
		{Person: "@bob:x", Tag: "spam", Rule: MoveAlways},
		{Person: "@cat:x", Tag: "work", Rule: MoveAlways},
	}
	got, changed := RetaggedPeople(set, "Junk", "Spam")
	want := map[string]bool{"@ann:x spam in": true, "@bob:x spam released": true, "@cat:x work in": true}
	if !changed || len(got) != len(want) {
		t.Fatalf("junk into spam = %+v, want %v", got, want)
	}
	for _, v := range got {
		state := "in"
		if v.Released {
			state = "released"
		}
		if !want[v.Person+" "+strings.ToLower(v.Tag)+" "+state] {
			t.Errorf("junk into spam kept %+v", v)
		}
	}
	if got, changed := RetaggedPeople(set, "junk", ""); !changed || len(got) != 2 {
		t.Errorf("deleting junk = %+v, want two entries left", got)
	}
	if _, changed := RetaggedPeople(set, "none", "spam"); changed {
		t.Error("a tag nobody is in changed something")
	}
}

// Only marks in a muted tag hide; a release never does.
func TestOnlyMarksInAMutedTagHide(t *testing.T) {
	t.Parallel()

	verdicts := []PersonVerdict{
		{Person: "@ann:x", Tag: "spam", Rule: MoveAlways},
		{Person: "@bob:x", Tag: "spam", Released: true},
		{Person: "@cat:x", Tag: "work", Rule: MoveAlways},
		{Person: "@ann:x", Tag: "Spam", Rule: MoveAlways},
	}
	if got := HiddenByVerdict(verdicts, []string{"Spam"}); !slices.Equal(got, []string{"@ann:x"}) {
		t.Errorf("hidden = %v, want Ann once", got)
	}
}
