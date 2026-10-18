package db

import (
	"context"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A chat's timer is kept, replaced, survives a listing rewrite, applies to a room no
// listing has brought yet, and zero removes it.
func TestAChatsTimerIsKeptBesideIt(t *testing.T) {
	t.Parallel()
	c, ctx := openTemp(t), context.Background()
	owner := domain.AccountRooms(domain.ProtocolWhatsApp, "42")
	rooms := []domain.Room{{ID: "whatsapp:42/7@s.whatsapp.net", Name: "Dana"}}
	if err := c.SaveRooms(ctx, owner, rooms); err != nil {
		t.Fatal(err)
	}
	timer := func(room domain.RoomID) time.Duration {
		got, err := c.Timer(ctx, room)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	const dana, late domain.RoomID = "whatsapp:42/7@s.whatsapp.net", "whatsapp:42/9@s.whatsapp.net"
	week := 7 * 24 * time.Hour
	if err := c.SetTimers(ctx, map[domain.RoomID]time.Duration{dana: 24 * time.Hour, late: week}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetTimers(ctx, map[domain.RoomID]time.Duration{dana: week}); err != nil {
		t.Fatal(err)
	}
	if err := c.SaveRooms(ctx, owner, rooms); err != nil { // a listing rewrite
		t.Fatal(err)
	}
	if timer(dana) != week || timer(late) != week {
		t.Errorf("timers = %v, %v, want a week each", timer(dana), timer(late))
	}
	if err := c.SetTimers(ctx, map[domain.RoomID]time.Duration{dana: 0}); err != nil {
		t.Fatal(err)
	}
	if got := timer(dana); got != 0 {
		t.Errorf("after turning it off: %v", got)
	}
}
