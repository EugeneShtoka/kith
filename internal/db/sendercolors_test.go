package db

import (
	"context"
	"testing"
)

func TestSenderSlotsRoundTrip(t *testing.T) {
	ctx := context.Background()
	c := openTemp(t)

	if got, err := c.SenderSlots(ctx, "!a:x"); err != nil || len(got) != 0 {
		t.Fatalf("a room nobody colored = %v, %v; want empty", got, err)
	}
	if err := c.SaveSenderSlots(ctx, "!a:x", map[string]int{"@bob:x": 0, "@carol:x": 1}); err != nil {
		t.Fatal(err)
	}
	got, err := c.SenderSlots(ctx, "!a:x")
	if err != nil {
		t.Fatal(err)
	}
	if got["@bob:x"] != 0 || got["@carol:x"] != 1 || len(got) != 2 {
		t.Errorf("slots = %v, want bob 0 and carol 1", got)
	}
}

// Once given, a hue is never taken back: a client that started from a colder cache
// must not renumber a room somebody else already learned.
func TestSenderSlotsDoNotMove(t *testing.T) {
	ctx := context.Background()
	c := openTemp(t)

	if err := c.SaveSenderSlots(ctx, "!a:x", map[string]int{"@bob:x": 3}); err != nil {
		t.Fatal(err)
	}
	// A second client, having seen fewer people, thinks Bob is the first color.
	if err := c.SaveSenderSlots(ctx, "!a:x", map[string]int{"@bob:x": 0, "@dave:x": 1}); err != nil {
		t.Fatal(err)
	}
	got, err := c.SenderSlots(ctx, "!a:x")
	if err != nil {
		t.Fatal(err)
	}
	if got["@bob:x"] != 3 {
		t.Errorf("bob moved to slot %d; a hue once given is never taken back", got["@bob:x"])
	}
	if got["@dave:x"] != 1 {
		t.Errorf("dave = %d, want the new slot 1 to be recorded", got["@dave:x"])
	}
}

// Slots are per room: the same person is not promised the same hue everywhere.
func TestSenderSlotsAreScopedToTheRoom(t *testing.T) {
	ctx := context.Background()
	c := openTemp(t)

	if err := c.SaveSenderSlots(ctx, "!a:x", map[string]int{"@bob:x": 0}); err != nil {
		t.Fatal(err)
	}
	got, err := c.SenderSlots(ctx, "!b:x")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("room B inherited %v from room A", got)
	}
}
