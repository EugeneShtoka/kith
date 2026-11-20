package main

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The prompt offers the choices holding what is typed; Enter takes the one under the
// arrows, else the words typed, trimmed.
func TestAPromptTakesAChoiceOrTheWords(t *testing.T) {
	u := &ui{}
	p := u.newPrompt("Same person as", "", " Ann ", nil)
	p.choices = []string{"Anna", "Joanne", "Bob"}
	if got := p.offered(); !slices.Equal(got, []string{"Anna", "Joanne"}) {
		t.Errorf("offered %q", got)
	}
	if got := p.taken(); got != "Ann" {
		t.Errorf("no choice: took %q, want the words", got)
	}
	p.at = 1
	if got := p.taken(); got != "Joanne" {
		t.Errorf("second choice: took %q", got)
	}
}

// A filing asks only for the places ticked otherwise than they hold the room, and says
// what came of it: tags moved, spaces as kithd filed them.
func TestAFilingAsksForWhatChanged(t *testing.T) {
	f := &filingView{
		room: domain.Room{ID: "!r"}, name: "Room",
		places: []domain.FilingPlace{
			{Space: "!s1", Label: "Work", In: true},
			{Space: "!s2", Label: "Home"},
			{Tag: "archive", Label: "Archived"},
			{Tag: "fav", Label: "Starred", In: true},
		},
		ticked: []bool{true, true, true, false},
	}
	tags, spaces := f.asked()
	if fmt.Sprint(tags) != "map[archive:true fav:false]" || fmt.Sprint(spaces) != "map[!s2:true]" {
		t.Errorf("asked tags %v spaces %v", tags, spaces)
	}
	r := &v1.FileRoomResponse{Spaces: []*v1.SpaceFiled{{SpaceId: "!s2", Outcome: v1.SpaceFiledOutcome_SPACE_FILED_OUTCOME_FILED}}}
	if got, want := f.told(tags, spaces, r), "Room is in Archived, and out of Starred · filed into Home"; got != want {
		t.Errorf("told %q, want %q", got, want)
	}
	r = &v1.FileRoomResponse{Spaces: []*v1.SpaceFiled{{SpaceId: "!s2", Outcome: v1.SpaceFiledOutcome_SPACE_FILED_OUTCOME_REFUSED, Error: "no power"}}}
	if got, want := f.told(nil, spaces, r), "refused by Home (no power)"; got != want {
		t.Errorf("told %q, want %q", got, want)
	}
}

// A room shows the name you gave it, else its own.
func TestARoomShowsTheNameYouGaveIt(t *testing.T) {
	u := &ui{rail: rail{served: map[domain.RoomID]domain.ServedRoom{"!a": {Name: "Mine"}, "!b": {}}}}
	for _, tc := range []struct {
		room domain.Room
		want string
	}{
		{domain.Room{ID: "!a", Name: "Theirs"}, "Mine"},
		{domain.Room{ID: "!b", Name: "Theirs"}, "Theirs"},
		{domain.Room{ID: "!c"}, "!c"},
	} {
		if got := u.roomName(tc.room); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.room.ID, got, tc.want)
		}
	}
}

// Filing with nothing changed closes the card and says so, under the UI's lock as the
// keys and clicks call it, sending nothing.
func TestFilingNothingChangedClosesTheCard(t *testing.T) {
	u := &ui{}
	u.filing = &filingView{places: []domain.FilingPlace{{Tag: "a", In: true}}, ticked: []bool{true}}
	done := make(chan struct{})
	go func() {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.applyFiling(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("applyFiling did not return under the UI's lock")
	}
	if u.filing != nil || u.note != "No change." {
		t.Errorf("card %v, note %q; want closed and No change.", u.filing, u.note)
	}
}
