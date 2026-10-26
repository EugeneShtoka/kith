package daemon_test

import (
	"context"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// noticeServer serves a notifier over cfg, past the catch-up batch, which a test
// reloads and delivers to.
func noticeServer(t *testing.T, cfg config.Config) (*harness, *daemon.Notifications) {
	t.Helper()
	n, err := daemon.NewNotifications(cfg, scopeOnly{nopWithChannels()},
		func(config.Notifications) notify.Notifier { return notify.Nop{} })
	if err != nil {
		t.Fatal(err)
	}
	n.Synced(mx, time.Now())
	n.Synced(mx, time.Now())
	return serve(t, &fakeBackend{Nop: nopWithChannels()}, func(d *daemon.Daemon) { d.Notifications = n }), n
}

// next is the next notice on notes, or fails.
func next(t *testing.T, notes <-chan daemon.Notice, what string) daemon.Notice {
	t.Helper()
	select {
	case v := <-notes:
		return v
	case <-time.After(settle):
		t.Fatalf("no notification %s", what)
		return daemon.Notice{}
	}
}

// none fails if anything arrives on notes for a while.
func none(t *testing.T, notes <-chan daemon.Notice, what string) {
	t.Helper()
	select {
	case v := <-notes:
		t.Errorf("%s: got %q", what, v.Alert.Body)
	case <-time.After(100 * time.Millisecond):
	}
}

// A profile a reload removes gets nothing on its open stream, rather than another
// profile's notifications; written back, it gets its own again.
func TestARemovedProfilesStreamGetsNothing(t *testing.T) {
	t.Parallel()
	room := domain.RoomID(mx + "!a:x")
	cfg := notifsOn("all")
	cfg.Profiles = []config.Profile{{Name: "first", Include: []string{"*"}}, {Name: "p", Include: []string{string(room)}}}
	h, n := noticeServer(t, cfg)
	client := h.client()
	client.UseProfile("p")
	notes := listen(t, client)

	n.Deliver(t.Context(), msg(room, alice, "while p holds it"))
	if v := next(t, notes, "while p holds the room"); v.Alert.Body != "while p holds it" {
		t.Fatalf("got %q", v.Alert.Body)
	}
	without := cfg
	without.Profiles = cfg.Profiles[:1]
	if err := n.Reload(without); err != nil {
		t.Fatal(err)
	}
	n.Deliver(t.Context(), msg(room, alice, "with p gone"))
	none(t, notes, "p's stream after p was removed")

	if err := n.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	n.Deliver(t.Context(), msg(room, alice, "with p back"))
	if v := next(t, notes, "with p back"); v.Alert.Body != "with p back" {
		t.Errorf("got %q", v.Alert.Body)
	}
}

// A notification arrives as kithd made it: a burst past the room's limit is one silent
// popup replacing itself per room, under an ID of its own.
func TestANotificationArrivesAsKithdMadeIt(t *testing.T) {
	t.Parallel()
	room := domain.RoomID(mx + "!a:x")
	cfg := asStarted(notifsOn("all"))
	cfg.Notifications.MaxPerRoom = 1
	h, n := noticeServer(t, cfg)
	notes := listen(t, h.client())

	n.Deliver(context.Background(), msg(room, alice, "hello"))
	first := next(t, notes, "for the first message")
	n.Deliver(context.Background(), msg(room, alice, "again"))
	burst := next(t, notes, "for the burst")
	if first.Alert.Body != "hello" || first.Alert.Silent || first.Alert.Replaces != "" || first.Alert.Sent.IsZero() {
		t.Errorf("first = %+v, want the message, with a sound and its time", first.Alert)
	}
	if !burst.Alert.Silent || burst.Alert.Replaces != string(room) || burst.Alert.Body != "1 new message" {
		t.Errorf("burst = %+v, want a silent count replacing the room's popup", burst.Alert)
	}
	if first.ID == "" || first.ID == burst.ID {
		t.Errorf("IDs %q and %q, want two of their own", first.ID, burst.ID)
	}
}
