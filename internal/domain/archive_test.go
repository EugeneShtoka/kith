package domain

import "testing"

// The toggle is exactly reversible, and emptying yields the zero Archive.
func TestArchiveWithIsReversible(t *testing.T) {
	t.Parallel()

	start := Archive{Entries: []string{"!keep:x"}}
	added := start.With("!room:x", true)
	if !added.Lists("!room:x") || !added.Lists("!keep:x") {
		t.Fatalf("With(add) = %v, want both entries", added.Entries)
	}
	back := added.With("!room:x", false)
	if len(back.Entries) != 1 || back.Entries[0] != "!keep:x" {
		t.Errorf("With(remove) = %v, want just the untouched entry", back.Entries)
	}
	// Adding twice must not duplicate: the entry is removed before it is re-added.
	if twice := added.With("!room:x", true); len(twice.Entries) != 2 {
		t.Errorf("With(add) twice = %v, want no duplicate", twice.Entries)
	}
	if empty := (Archive{Entries: []string{"!only:x"}}).With("!only:x", false); empty.Has() {
		t.Errorf("emptied archive = %v, want the zero value", empty.Entries)
	}
}

// Lists is exact where Archived is not: a room archived via its space is not listed.
func TestArchiveListsIsExactWhereArchivedIsNot(t *testing.T) {
	t.Parallel()

	archive := Archive{Entries: []string{"space:Bots"}}
	if !archive.Archived(RoomFacts{ID: "!alerts:x", Name: "Alerts", Spaces: []string{"Bots"}}) {
		t.Fatal("a room in an archived space is not archived")
	}
	if archive.Lists("!alerts:x") {
		t.Error("Lists() claimed the room itself is named, but only its space is")
	}
}
