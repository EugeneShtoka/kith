package daemon_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// A scoped, timed mute survives the wire with its scope, its name and its deadline
// intact, and it silences exactly what it named.
func TestSetDNDRoundTrip(t *testing.T) {
	t.Parallel()

	client, h := attach(t, &fakeBackend{Nop: nopWithChannels()})
	until := time.Now().Add(2 * time.Hour).Truncate(time.Second)

	rule := hideRule("!standup:x", "")
	rule.Name, rule.Until = "Standup", until
	got, err := client.SetDND(context.Background(), rule)
	if err != nil {
		t.Fatalf("SetDND: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("mutes = %+v, want one", got)
	}
	mute := got[0]
	if mute.Match != "!standup:x" || mute.Sender != "" || mute.Name != "Standup" {
		t.Errorf("rule = %+v, want the room it named", mute)
	}
	if mute.Show == nil || *mute.Show != notify.LevelNone {
		t.Errorf("Show = %v, want the axis to survive the wire — unset would read as \"changes nothing\"", mute.Show)
	}
	if !mute.Temp {
		t.Error("everything crossing this wire is a temporary rule, and one that came back standing would outlive the restart meant to clear it")
	}
	if !mute.Until.Equal(until) {
		t.Errorf("Until = %v, want %v", mute.Until, until)
	}
	// A deadline that crossed the wire has to read in local time, or a countdown
	// drawn from it is hours out.
	if _, offset := mute.Until.Zone(); offset != zoneOffset(until) {
		t.Errorf("Until came back in the wrong zone: %v", mute.Until)
	}
	// And the daemon acts on it: this is not merely stored state.
	if _, ok := h.notifications.Deliver(context.Background(), domain.Message{
		ID: "$1", RoomID: "!standup:x", Sender: "@alice:x", Body: "hi",
	}); ok {
		t.Error("a muted room should not notify, even at level all")
	}
	if _, ok := h.notifications.Deliver(context.Background(), domain.Message{
		ID: "$2", RoomID: "!other:x", Sender: "@alice:x", Body: "hi",
	}); !ok {
		t.Error("a room the mute did not name should still notify")
	}
}

// zoneOffset is the local zone's offset at t, for comparing what came back.
func zoneOffset(t time.Time) int {
	_, offset := t.Local().Zone()
	return offset
}

// An untimed mute travels with no deadline rather than with the Unix epoch, which
// would read as a mute that lapsed in 1970.
func TestUntimedDNDRoundTrip(t *testing.T) {
	t.Parallel()

	client, _ := attach(t, &fakeBackend{Nop: nopWithChannels()})
	got, err := client.SetDND(context.Background(), hideRule("", ""))
	if err != nil {
		t.Fatalf("SetDND: %v", err)
	}
	if len(got) != 1 || !got[0].Until.IsZero() {
		t.Fatalf("rules = %+v, want one entry with no deadline", got)
	}
	if !got[0].Live(time.Now().Add(365 * 24 * time.Hour)) {
		t.Error("an untimed mute should still be in force a year later")
	}
}

// Every method answers with the resulting set, so a client that changed one thing
// learns the state in the same round trip.
func TestDNDReadsAndClears(t *testing.T) {
	t.Parallel()

	client, _ := attach(t, &fakeBackend{Nop: nopWithChannels()})
	ctx := context.Background()
	if _, err := client.SetDND(ctx, named(hideRule("!a:x", ""), "A")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SetDND(ctx, named(hideRule("", "@bob:x"), "Bob")); err != nil {
		t.Fatal(err)
	}
	got, err := client.DND(ctx)
	if err != nil {
		t.Fatalf("DND: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("mutes = %+v, want both", got)
	}

	left, err := client.ClearDND(ctx, "!a:x", "")
	if err != nil {
		t.Fatalf("ClearDND: %v", err)
	}
	if len(left) != 1 || left[0].Sender != "@bob:x" {
		t.Errorf("mutes = %+v, want only the person left", left)
	}

	left, err = client.ClearAllDND(ctx)
	if err != nil {
		t.Fatalf("ClearAllDND: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("mutes = %+v, want nothing left", left)
	}
}

// The daemon refuses a mute that names nothing (it would read as the whole account) and
// one whose deadline has already passed (it would silently do nothing).
func TestSetDNDRefusesBadRules(t *testing.T) {
	t.Parallel()

	client, _ := attach(t, &fakeBackend{Nop: nopWithChannels()})
	for name, rule := range map[string]notify.Rule{
		"unnamed scope": {Match: "!a:x", Temp: true},
		"past deadline": withUntil(hideRule("", ""), time.Now().Add(-time.Minute)),
	} {
		if _, err := client.SetDND(context.Background(), rule); err == nil {
			t.Errorf("%s: SetDND() = nil, want a refusal", name)
		}
	}
}

// ReloadConfig reaches the daemon, and a config it refuses comes back with the reason.
func TestReloadConfigRoundTrip(t *testing.T) {
	t.Parallel()

	client, h := attach(t, &fakeBackend{Nop: nopWithChannels()})
	if err := client.ReloadConfig(context.Background()); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	if h.reload.count() != 1 {
		t.Errorf("the daemon re-read %d times, want 1", h.reload.count())
	}

	h.reload.fail(errors.New(`notifications.on: unknown level "mentions"`))
	err := client.ReloadConfig(context.Background())
	if err == nil || !strings.Contains(err.Error(), `unknown level "mentions"`) {
		t.Errorf("ReloadConfig() = %v, want the daemon's parse failure", err)
	}
}
