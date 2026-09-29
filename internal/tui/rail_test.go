package tui

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// at() bounds-checks the cursor for every reader.
func TestRailCursorLookupIsBoundsChecked(t *testing.T) {
	t.Parallel()

	var empty railState
	if _, ok := empty.at(); ok {
		t.Error("a rail with no groups reported a group under the cursor")
	}
	if empty.key() != "" || empty.label() != "" {
		t.Errorf("empty rail: key=%q label=%q, want both empty", empty.key(), empty.label())
	}

	r := railState{groups: []group{{key: "home", label: "All"}, {key: "dms", label: "DMs"}}}
	for _, cursor := range []int{-1, 2, 99} {
		r.cursor = cursor
		if _, ok := r.at(); ok {
			t.Errorf("cursor %d is outside the groups and at() answered anyway", cursor)
		}
		if r.key() != "" {
			t.Errorf("cursor %d: key = %q, want empty", cursor, r.key())
		}
	}

	r.cursor = 1
	g, ok := r.at()
	if !ok || g.key != "dms" {
		t.Errorf("at() = %+v ok=%v, want the DMs group", g, ok)
	}
	if r.key() != "dms" || r.label() != "DMs" {
		t.Errorf("key=%q label=%q, want dms/DMs", r.key(), r.label())
	}
}

// The cursor stops at the ends rather than wrapping.
func TestRailCursorStopsAtTheEnds(t *testing.T) {
	t.Parallel()

	r := railState{groups: []group{{key: "a"}, {key: "b"}, {key: "c"}}}

	if _, moved := r.moved(-1); moved {
		t.Error("moving up from the top wrapped")
	}
	next, moved := r.moved(1)
	if !moved || next.cursor != 1 {
		t.Fatalf("moved(1) = cursor %d moved=%v, want 1/true", next.cursor, moved)
	}
	next.cursor = len(r.groups) - 1
	if _, moved := next.moved(1); moved {
		t.Error("moving down from the bottom wrapped")
	}
}

// A trailing divider before an absent group (e.g. empty Archived) is not drawn.
func TestNoDividerUnderTheLastGroup(t *testing.T) {
	t.Parallel()

	spaces := []domain.Space{{ID: "!w:x", Name: "Work"}}
	rooms := []domain.Room{{ID: "!a:x", Name: "Alpha"}}
	cfg := config.Rail{Order: []string{"*", "-", archivedGroupKey}}

	groups := railGroups(spaces, cfg, nil, unreadView{counts: map[domain.RoomID]domain.Unread{}}, 0, rooms, nil)
	last := groups[len(groups)-1]
	if last.key == archivedGroupKey {
		t.Fatal("nothing was archived, so the archived group should not exist")
	}
	if last.sepAfter {
		t.Errorf("a divider is drawn under %q, the last group", last.label)
	}
	// A divider in the middle is untouched: it has something under it.
	mid := config.Rail{Order: []string{"unread", "-", "*"}}
	groups = railGroups(spaces, mid, nil, unreadView{counts: map[domain.RoomID]domain.Unread{}}, 0, rooms, nil)
	if !groups[0].sepAfter {
		t.Error("the divider after the first group was dropped too")
	}
}
