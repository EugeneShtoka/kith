package main

import (
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/agent"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// With at, a send-list room gets the message queued for that time, recorded as
// scheduled, dashes shortened, and the room rests as after a send.
func TestALaterSendIsQueuedForItsTime(t *testing.T) {
	t.Parallel()
	f := twoRooms()
	s := newWriter(t, f, shareAll, []string{"space:Work"})
	at := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "morning — all", "at": at.Format(time.RFC3339), "thread": mx + "$1"})
	if out["action"] != agent.Scheduled {
		t.Fatalf("action = %v, want scheduled", out["action"])
	}
	if len(f.sent) != 0 || len(f.queued) != 1 {
		t.Fatalf("sent %d, queued %d; want it queued alone", len(f.sent), len(f.queued))
	}
	q := f.queued[0]
	if !q.At.Equal(at) || q.Body != "morning - all" || q.TxnID == "" || q.ThreadRoot != mx+"$1" {
		t.Errorf("queued %+v", q)
	}
	if ledger := entries(t, s); len(ledger) != 1 || ledger[0].Outcome != agent.Scheduled {
		t.Errorf("ledger = %+v", ledger)
	}
	again := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "and again"})
	if again["action"] != agent.Drafted || len(f.sent) != 0 {
		t.Errorf("right after a scheduled message: %v, sent %d; want the room resting", again["action"], len(f.sent))
	}
}

// A draft-only room refuses a later send (recorded); a non-RFC 3339 or past time is
// refused first.
func TestALaterSendIsRefusedWhereItWouldBeDrafted(t *testing.T) {
	t.Parallel()
	f := twoRooms()
	s := newWriter(t, f, shareAll, nil)
	at := time.Now().Add(time.Hour).Format(time.RFC3339)
	callErr(t, s, "send_message", map[string]any{"room": "Standup", "text": "hi", "at": at})
	if len(f.queued) != 0 || len(f.drafts) != 0 {
		t.Errorf("queued %d, drafts %d; want nothing written", len(f.queued), len(f.drafts))
	}
	if ledger := entries(t, s); len(ledger) != 1 || ledger[0].Outcome != agent.Refused {
		t.Errorf("ledger = %+v, want the refusal recorded", ledger)
	}
	sender := newWriter(t, twoRooms(), shareAll, []string{"space:Work"})
	for _, bad := range []string{"tomorrow 9am", time.Now().Add(-time.Minute).Format(time.RFC3339)} {
		callErr(t, sender, "send_message", map[string]any{"room": "Standup", "text": "hi", "at": bad})
	}
}

// Outside the room's working hours a send, and a later send, waits for them to open,
// saying why; urgent sends it now; inside them it goes now.
func TestASendOutsideWorkingHoursWaitsForThem(t *testing.T) {
	t.Parallel()
	now := time.Now()
	y, m, d := now.Date()
	tomorrow := time.Date(y, m, d+1, 0, 0, 0, 0, time.Local)
	closedToday := domain.WorkingHours{Match: "space:Work", End: 24 * time.Hour, Days: []time.Weekday{tomorrow.Weekday()}}
	f := twoRooms()
	s := newWriter(t, f, shareAll, []string{"space:Work"})
	s.cooldown = 0
	s.hours = []domain.WorkingHours{{End: 24 * time.Hour}, closedToday}

	out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "now please"})
	if out["action"] != agent.Scheduled || len(f.sent) != 0 || len(f.queued) != 1 || !f.queued[0].At.Equal(tomorrow) {
		t.Fatalf("outside hours: %v, sent %d, queued %+v; want it scheduled for %s", out["action"], len(f.sent), f.queued, tomorrow)
	}
	if why, _ := out["reason"].(string); !strings.Contains(why, "working hours") {
		t.Errorf("reason = %q, want it to name the working hours", why)
	}
	call(t, s, "send_message", map[string]any{"room": "Standup", "text": "in an hour", "at": now.Add(time.Hour).Format(time.RFC3339)})
	if len(f.queued) != 2 || !f.queued[1].At.Equal(tomorrow) {
		t.Errorf("a later send inside today: queued %+v, want it moved to %s", f.queued, tomorrow)
	}
	if out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "fire", "urgent": true}); out["action"] != agent.Sent || len(f.sent) != 1 {
		t.Errorf("urgent: %v, sent %d; want it sent now", out["action"], len(f.sent))
	}
	s.hours = s.hours[:1] // open all day everywhere
	if out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "hi"}); out["action"] != agent.Sent || len(f.sent) != 2 {
		t.Errorf("inside hours: %v, sent %d; want it sent now", out["action"], len(f.sent))
	}
}
