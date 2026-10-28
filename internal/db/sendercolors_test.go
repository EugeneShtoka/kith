package db

import (
	"context"
	"testing"
)

// A given hue is never taken back: a client with a colder cache must not renumber a room.
func TestSenderSlotsDoNotMove(t *testing.T) {
	ctx := context.Background()
	c := openTemp(t)

	if err := c.SaveSenderSlots(ctx, mx+"!a:x", map[string]int{"matrix:@bob:x": 3}); err != nil {
		t.Fatal(err)
	}
	// A second client, having seen fewer people, gives Bob the first color.
	if err := c.SaveSenderSlots(ctx, mx+"!a:x", map[string]int{"matrix:@bob:x": 0, "matrix:@dave:x": 1}); err != nil {
		t.Fatal(err)
	}
	got, err := c.SenderSlots(ctx, mx+"!a:x")
	if err != nil {
		t.Fatal(err)
	}
	if got["matrix:@bob:x"] != 3 {
		t.Errorf("bob moved to slot %d; a hue once given is never taken back", got["matrix:@bob:x"])
	}
	if got["matrix:@dave:x"] != 1 {
		t.Errorf("dave = %d, want the new slot 1 to be recorded", got["matrix:@dave:x"])
	}
}

// Slots are per room: the same person is not promised the same hue everywhere.
func TestSenderSlotsAreScopedToTheRoom(t *testing.T) {
	ctx := context.Background()
	c := openTemp(t)

	if err := c.SaveSenderSlots(ctx, mx+"!a:x", map[string]int{"matrix:@bob:x": 0}); err != nil {
		t.Fatal(err)
	}
	got, err := c.SenderSlots(ctx, mx+"!b:x")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("room B inherited %v from room A", got)
	}
}
