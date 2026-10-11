package daemon_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// A scoped, timed mute keeps its scope, name and deadline across the wire and
// silences exactly what it named.
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
	// The deadline must read in local time, or a countdown is hours out.
	if _, offset := mute.Until.Zone(); offset != zoneOffset(until) {
		t.Errorf("Until came back in the wrong zone: %v", mute.Until)
	}
	// The daemon acts on it, not merely stores it.
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

// zoneOffset is the local zone's offset at t.
func zoneOffset(t time.Time) int {
	_, offset := t.Local().Zone()
	return offset
}

// An untimed mute travels with no deadline, not the Unix epoch.
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

// Every method answers with the resulting set.
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

// Refused: a mute naming nothing (it would read as the whole account) and one already
// lapsed.
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

// A config the daemon refuses comes back with the reason.
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

// A rescan's since, apply and rooms cross the wire, and the candidates come back whole.
func TestRescanSpamRoundTrip(t *testing.T) {
	t.Parallel()

	src := spamWorld()
	src.put(chatRm, alice, "go vote today", 48*time.Hour)
	src.put(chatRm, alice, "vote again", 30*time.Minute)
	n, _ := spamNotifier(t, spamConfig(config.Spam{Filters: []config.SpamFilter{
		{Name: "elections", Words: []string{"vote"}, Moves: []string{"always"}},
	}}), src)
	h := serve(t, &fakeBackend{Nop: nopWithChannels()}, func(d *daemon.Daemon) { d.Notifications = n })
	client := h.client()
	ctx := context.Background()

	found, err := client.RescanSpam(ctx, time.Now().Add(-time.Hour), false, nil)
	if err != nil {
		t.Fatalf("RescanSpam: %v", err)
	}
	if len(found) != 1 || found[0].Verdict.Room != chatRm || found[0].Verdict.Rule != domain.SpamAlways ||
		found[0].Verdict.Filter != "elections" || found[0].Sample.Body != "vote again" {
		t.Fatalf("candidates = %+v, want the room by its message within the hour", found)
	}
	if len(src.verdicts()) != 0 {
		t.Fatalf("a preview recorded %+v", src.verdicts())
	}
	if moved, err := client.RescanSpam(ctx, time.Time{}, true, []domain.RoomID{dmRoom}); err != nil || len(moved) != 0 {
		t.Fatalf("apply to another room = %+v (%v), want nothing moved", moved, err)
	}
	if moved, err := client.RescanSpam(ctx, time.Time{}, true, []domain.RoomID{chatRm}); err != nil || len(moved) != 1 {
		t.Fatalf("apply = %+v (%v), want the room moved", moved, err)
	}
	if marked := src.verdicts(); len(marked) != 1 || marked[0].Room != chatRm {
		t.Errorf("recorded %+v, want the room", marked)
	}
}
