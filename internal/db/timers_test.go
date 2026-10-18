package db

import (
	"context"
	"fmt"
	"math/rand/v2"
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

// A timer set at a version is taken unless an older version than the one kept says it:
// over any sequence of changes, from any number of writers (the other side, the
// account's other devices, a message carrying the timer it was sent with), the cache
// holds what a model keeping the newest version's last word holds. Turning the timer
// off keeps its version; version 0 (none) is taken only while none is kept.
func TestATimerTakesNoOlderVersion(t *testing.T) {
	t.Parallel()
	c, ctx := openTemp(t), context.Background()
	rng := rand.New(rand.NewPCG(18, 1))
	type said struct {
		timer   time.Duration
		version uint32
	}
	timers := []time.Duration{0, time.Hour, 24 * time.Hour, 7 * 24 * time.Hour}
	for run := range 40 {
		room := domain.RoomID(fmt.Sprintf("signal:a/chat-%d", run))
		var model said
		for step := range 12 {
			s := said{timers[rng.IntN(len(timers))], uint32(rng.IntN(5))}
			kept, err := c.KeepTimer(ctx, room, s.timer, s.version)
			if err != nil {
				t.Fatal(err)
			}
			want := s.version >= model.version
			if want {
				model = said{s.timer, max(model.version, s.version)}
			}
			if kept != want {
				t.Fatalf("run %d step %d: %+v kept = %v over %+v", run, step, s, kept, model)
			}
			timer, version, err := c.TimerVersion(ctx, room)
			if err != nil {
				t.Fatal(err)
			}
			if timer != model.timer || version != model.version {
				t.Fatalf("run %d step %d after %+v: have %v v%d, want %+v", run, step, s, timer, version, model)
			}
		}
	}
}
