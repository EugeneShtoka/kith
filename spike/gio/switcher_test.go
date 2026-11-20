package main

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Typing in the filter finds rooms by each word in their name, members or ID, those
// with unread first, and the rail rows it names after them.
func TestFilterFindsRoomsByEveryWordAndPlaces(t *testing.T) {
	u := &ui{
		local: true,
		rooms: []domain.Room{
			{ID: "matrix:!a:x", Name: "Climbing", Members: []string{"Ann Lee"}},
			{ID: "matrix:!b:x", Name: "Ann Lee", Members: []string{"Ann Lee"}},
			{ID: "matrix:!c:x", Name: "Lee family", Members: []string{"Bo Lee"}},
		},
		unread: map[domain.RoomID]domain.Unread{"matrix:!c:x": {Messages: 2, Counted: true}},
		rail:   rail{rows: []domain.RailRow{{Key: "tag:family", Label: "Family"}, {Key: "Work", Label: "Work"}}},
	}
	for _, tc := range []struct {
		typed  string
		rooms  []int
		places []int
	}{
		{"lee", []int{2, 0, 1}, nil},   // members match; the unread room first
		{"ann climb", []int{0}, nil},   // every word, in any field
		{"!b", []int{1}, nil},          // the ID
		{"FAMILY", []int{2}, []int{0}}, // a place by its label, after the room
		{"work", nil, []int{1}},        // only a place
		{"  ", []int{0, 1, 2}, nil},    // nothing typed: the row's rooms, every room before the rail holds any
	} {
		u.filter.SetText(tc.typed)
		if tc.typed == "  " {
			u.rail.rows = nil
		}
		if got := u.filtered(); !slices.Equal(got, tc.rooms) {
			t.Errorf("%q: rooms %v, want %v", tc.typed, got, tc.rooms)
		}
		if got := u.places(); !slices.Equal(got, tc.places) {
			t.Errorf("%q: places %v, want %v", tc.typed, got, tc.places)
		}
	}
}
