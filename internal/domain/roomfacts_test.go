package domain_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// SpaceEntry yields a class entry that matches the space's rooms and round-trips via SpaceOf.
func TestSpaceEntryRoundTrips(t *testing.T) {
	t.Parallel()

	entry := domain.SpaceEntry("Work Stuff")
	if kind, ok := domain.ParseEntry(entry); !ok || kind != domain.EntryClass {
		t.Errorf("ParseEntry(%q) = %v, %v — want a class of rooms", entry, kind, ok)
	}
	if !(domain.RoomFacts{ID: "!a:x", Spaces: []string{"Work Stuff"}}).Names(entry) {
		t.Errorf("%q should name a room in that space", entry)
	}
	if name, ok := domain.SpaceOf(entry); !ok || name != "Work Stuff" {
		t.Errorf("SpaceOf(%q) = %q, %v", entry, name, ok)
	}
	for _, other := range []string{"!a:x", "room:Standup", "Work", "dm"} {
		if _, ok := domain.SpaceOf(other); ok {
			t.Errorf("SpaceOf(%q) should not name a space", other)
		}
	}
}
