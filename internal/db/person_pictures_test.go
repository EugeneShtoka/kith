package db

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// TestAPersonsPictureIsSeenOnlyInTheirRooms walks every way four people's pictures can
// be kept (none asked, one, found to have none) against two rooms: one person a member
// of the first, one a sender there, one a member of the second, one in no room. A room
// shows exactly the pictures of the people in it, and loads no one else's.
func TestAPersonsPictureIsSeenOnlyInTheirRooms(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	r1, r2 := domain.RoomID(mx+"!one:x"), domain.RoomID(mx+"!two:x")
	people := []string{"matrix:@member:x", "matrix:@sender:x", "matrix:@other:x", "matrix:@nobody:x"}
	in := map[domain.RoomID]map[string]bool{
		r1: {people[0]: true, people[1]: true},
		r2: {people[2]: true},
	}
	states := []string{"unasked", "kept", "none"}
	for n := range 81 { // 3^4
		if err := cache.Clear(ctx); err != nil {
			t.Fatal(err)
		}
		if err := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: r1}, {ID: r2}}); err != nil {
			t.Fatal(err)
		}
		if err := cache.SaveMembers(ctx, r1, []domain.Member{{UserID: people[0]}}); err != nil {
			t.Fatal(err)
		}
		if err := cache.SaveMembers(ctx, r2, []domain.Member{{UserID: people[2]}}); err != nil {
			t.Fatal(err)
		}
		if err := cache.SaveMessages(ctx, r1, []domain.Message{{ID: mx + "$1", RoomID: r1, Sender: people[1], Body: "hi"}}); err != nil {
			t.Fatal(err)
		}
		kept := map[string]string{}
		name := ""
		for i, p := range people {
			state := states[n/[]int{1, 3, 9, 27}[i]%3]
			name += p + "=" + state + " "
			if state == "unasked" {
				continue
			}
			id := map[string]string{"kept": "v-" + p, "none": ""}[state]
			if err := cache.PersonPictures().SetPicture(ctx, p, id, []byte(id), time.UnixMilli(1)); err != nil {
				t.Fatal(err)
			}
			if id != "" {
				kept[p] = id
			}
		}
		for room, there := range in {
			want := map[string]string{}
			for p, id := range kept {
				if there[p] {
					want[p] = id
				}
			}
			got, err := cache.SenderPictures(ctx, room)
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(got, want) {
				t.Errorf("%s: SenderPictures(%s) = %v, want %v", name, room, got, want)
			}
			for _, p := range people {
				data, err := cache.PersonPicture(ctx, room, p)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != want[p] {
					t.Errorf("%s: PersonPicture(%s, %s) = %q, want %q", name, room, p, data, want[p])
				}
			}
		}
	}
}

// TestAPersonsPictureIsCheckedAndCleared: a check is kept with its version, marked
// again by PictureUnchanged, and emptied by Clear.
func TestAPersonsPictureIsCheckedAndCleared(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	pics := cache.PersonPictures()
	if err := pics.SetPicture(ctx, "matrix:@a:x", "v1", []byte("one"), time.UnixMilli(1)); err != nil {
		t.Fatal(err)
	}
	if err := pics.SetPicture(ctx, "matrix:@b:x", "v2", nil, time.UnixMilli(1)); err != nil { // a version without bytes
		t.Fatal(err)
	}
	if err := pics.PictureUnchanged(ctx, "matrix:@a:x", time.UnixMilli(5)); err != nil {
		t.Fatal(err)
	}
	got, err := pics.PictureChecks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]domain.PictureCheck{
		"matrix:@a:x": {ID: "v1", Checked: time.UnixMilli(5)},
		"matrix:@b:x": {ID: "", Checked: time.UnixMilli(1)},
	}
	if !maps.EqualFunc(got, want, func(a, b domain.PictureCheck) bool { return a.ID == b.ID && a.Checked.Equal(b.Checked) }) {
		t.Errorf("PictureChecks() = %v, want %v", got, want)
	}
	if err := cache.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := pics.PictureChecks(ctx); len(got) != 0 {
		t.Errorf("PictureChecks() after Clear = %v, want none", got)
	}
}
