package domain

import (
	"slices"
	"testing"
)

// A tag is a place: tag:<name> matches the rooms the tag holds, judged on the room
// alone (a state word matches nothing there), wherever a place entry is read.
func TestATagIsAPlace(t *testing.T) {
	t.Parallel()
	set := mustTags(t,
		Tag{Name: "Family", Rule: []string{"dm"}},
		Tag{Name: "Busy", Rule: []string{"unread"}},
		Tag{Name: "Quiet", Rule: []string{"not unread", "not dm"}},
	)
	places := Places{Tags: set}
	dm := places.Facts(Room{ID: "!mom:x", Name: "Mom", IsDirect: true}, nil)
	group := places.Facts(Room{ID: "!g:x", Name: "Group"}, nil)
	if !dm.Names("tag:Family") || !dm.Names("TAG:family") || group.Names("tag:Family") {
		t.Errorf("tag:Family on a DM = %t, on a group = %t", dm.Names("tag:Family"), group.Names("tag:Family"))
	}
	if dm.Names("tag:Busy") || group.Names("tag:Busy") {
		t.Error("a state word matched as a place")
	}
	if !group.Names("tag:Quiet") || dm.Names("tag:Quiet") {
		t.Error("`not` a state word does not match everything not otherwise excluded")
	}
	if kind, ok := ParseEntry("tag:Family"); !ok || kind != EntryClass {
		t.Errorf("ParseEntry(tag:Family) = %v, %t; want a class of rooms", kind, ok)
	}
}

// A room's homes are its spaces and tags in the person's priority; what it does not
// name follows, spaces before tags; a tag and a space of one name stay apart; a tag
// reads by its name.
func TestHomesFollowThePriority(t *testing.T) {
	t.Parallel()
	got := Homes([]string{"Work", "Family"}, []string{"Family", "Busy"}, []string{"tag:Family", "Work"})
	if want := []string{"tag:Family", "Work", "Family", "tag:Busy"}; !slices.Equal(got, want) {
		t.Errorf("Homes = %v, want %v", got, want)
	}
	if got := Homes([]string{"Work"}, []string{"DMs", "All"}, nil); !slices.Equal(got, []string{"Work", "tag:DMs", "tag:All"}) {
		t.Errorf("Homes = %v unranked, want spaces, then tags in config order", got)
	}
	if HomeLabel("tag:Family") != "Family" || HomeLabel("Work") != "Work" {
		t.Error("HomeLabel does not read a tag by its name")
	}
	if got := expandDownloadTemplate("{space}/{name}{ext}", DownloadPlace{Space: "tag:Family"}, "a.jpg"); got != "Family/a.jpg" {
		t.Errorf("a download's {space} = %q, want the tag's name", got)
	}
}
