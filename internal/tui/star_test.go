package tui

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// `*` toggles both ways, and a successful write does not undo the mark.
func TestStarTogglesBothWays(t *testing.T) {
	t.Parallel()

	m := dayRoom(t)
	id := m.timeline.selected
	on, cmd := m.toggleStar()
	if !on.isStarred(id) {
		t.Fatal("the first press did not star the message")
	}
	if got := deliver(t, on, cmd); !got.isStarred(id) {
		t.Error("the star was undone by its own write succeeding")
	}
	if off, _ := on.toggleStar(); off.isStarred(id) {
		t.Error("the second press did not unstar it")
	}
}

// The mark is optimistic, so a refusal puts the row back and says so.
func TestStarUndoneWhenTheServerRefuses(t *testing.T) {
	t.Parallel()

	m := dayRoom(t)
	id := m.timeline.selected
	starred, _ := m.toggleStar()
	after, _ := starred.handleStarred(starredMsg{eventID: id, on: true, err: errRefused{}})
	if after.isStarred(id) {
		t.Error("the star survived a refusal")
	}
	if !strings.Contains(after.status(), "could not star") {
		t.Errorf("status = %q, want it to say the star failed", after.status())
	}
}

// A late star set for a room no longer open is dropped.
func TestStarSetIgnoredForAnotherRoom(t *testing.T) {
	t.Parallel()

	m := dayRoom(t)
	got, _ := m.handleStarredIn(starredInMsg{
		roomID: "!elsewhere:x",
		ids:    []domain.EventID{"$d0m0"},
	})
	if got.isStarred("$d0m0") {
		t.Error("another room's stars were adopted into this one")
	}
}

// The starred list is a search whose clause alone is a query.
func TestStarredListNarrowsToStars(t *testing.T) {
	t.Parallel()

	var f domain.SearchFilter
	(starredList{}).narrow(&f)
	if !f.Starred {
		t.Error("the starred list does not narrow to starred messages")
	}
	if f.Empty() {
		t.Error("the clause alone must be a query, or the list needs terms typed first")
	}
}

// errRefused stands in for a server that said no.
type errRefused struct{}

func (errRefused) Error() string { return "refused" }
