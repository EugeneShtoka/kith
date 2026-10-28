package daemon_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// autoCopyNotifier finds codes in the SMS bridge room, after syncs sync responses.
func autoCopyNotifier(t *testing.T, autoCopy bool, syncs int) (*daemon.Notifications, *recorder) {
	t.Helper()
	cfg := notifsOn("all")
	cfg.Clipboard.AutoCopy = autoCopy
	cfg.Codes.Include = []string{smsRoom}

	n, err := daemon.NewNotifications(asStarted(cfg), codeWorld())
	if err != nil {
		t.Fatalf("NewNotifications: %v", err)
	}
	rec := record(t, n)
	n.UseSelves(func() []string { return []string{me} })
	for range syncs {
		n.Synced(mx, time.Now())
	}
	return n, rec
}

// capturingDaemon is autoCopyNotifier past the catch-up batch.
func capturingDaemon(t *testing.T) (*daemon.Notifications, *recorder) {
	t.Helper()
	return autoCopyNotifier(t, true, 2)
}

// codes are the codes published to copy.
func codes(rec *recorder) []string {
	var out []string
	published := rec.published()
	for i := range published {
		if published[i].Code != "" {
			out = append(out, published[i].Code)
		}
	}
	return out
}

const smsRoom = mx + "!sms:example.org"

func codeWorld() *scopeBackend {
	return &scopeBackend{rooms: []domain.Room{
		{ID: smsRoom, Name: "SMS", IsDirect: true},
		{ID: chatRm, Name: chatNam},
	}}
}

func codeMsg(room domain.RoomID, body string) domain.Message {
	return domain.Message{ID: mx + "$1", RoomID: room, Sender: "matrix:@bridge:example.org", Body: body, Timestamp: time.Now()}
}

// A code arriving with nothing attached is published to copy, announced by the code
// alone: kith-notify says what came of copying it.
func TestAutoCopyPublishesTheCode(t *testing.T) {
	t.Parallel()
	n, rec := capturingDaemon(t)

	n.Deliver(context.Background(), codeMsg(smsRoom, "Your code is 482910"))

	var announced bool
	published := rec.published()
	for i := range published {
		if p := &published[i]; p.Code == "482910" {
			announced = p.Alert.Body == "482910" && p.Alert.Room == "SMS" && slices.Equal(p.Topics, []string{"default"})
		}
	}
	if !announced {
		t.Errorf("published %+v, want the code to copy, announced by itself in the room, to the profile holding it", rec.published())
	}
}

// An unnamed room is not copied from, however code-shaped the message.
func TestAutoCopyStaysWhereItWasTold(t *testing.T) {
	t.Parallel()
	n, rec := capturingDaemon(t)
	n.Deliver(context.Background(), codeMsg(chatRm, "the build failed with code 482910"))
	if got := codes(rec); len(got) != 0 {
		t.Errorf("codes %v from a room outside the include list", got)
	}
}

// Nothing is copied before catch-up: a week-old code must not overwrite the clipboard.
func TestAutoCopyWaitsUntilCaughtUp(t *testing.T) {
	t.Parallel()
	n, rec := autoCopyNotifier(t, true, 1) // the catch-up response only
	n.Deliver(context.Background(), codeMsg(smsRoom, "Your code is 482910"))
	if got := codes(rec); len(got) != 0 {
		t.Errorf("codes %v from the catch-up batch", got)
	}
}

// Auto-copy ignores do-not-disturb: copying interrupts nobody.
func TestAutoCopyIgnoresDND(t *testing.T) {
	t.Parallel()
	n, rec := capturingDaemon(t)
	n.SetDND(hideRule("", ""))

	msg := codeMsg(smsRoom, "Your code is 482910")
	if _, notified := n.Deliver(context.Background(), msg); notified {
		t.Error("do-not-disturb should still hold the ordinary notification back")
	}
	if got := codes(rec); !slices.Equal(got, []string{"482910"}) {
		t.Errorf("codes = %v, want the code published anyway", got)
	}
}

// With auto-copy off (the default) nothing is copied, even from an included room.
func TestAutoCopyOffCopiesNothing(t *testing.T) {
	t.Parallel()
	n, rec := autoCopyNotifier(t, false, 2)
	n.Deliver(context.Background(), codeMsg(smsRoom, "Your code is 482910"))
	if got := codes(rec); len(got) != 0 {
		t.Errorf("codes %v with the switch off", got)
	}
}
