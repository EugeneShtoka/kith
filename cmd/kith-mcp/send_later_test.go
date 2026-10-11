package main

import (
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/agent"
)

// With at, a send-list room gets the message queued for that time, recorded as
// scheduled, dashes shortened, and the room rests as after a send.
func TestALaterSendIsQueuedForItsTime(t *testing.T) {
	t.Parallel()
	f := twoRooms()
	s := newWriter(t, f, shareAll, []string{"space:Work"})
	at := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "morning — all", "at": at.Format(time.RFC3339), "thread": "$1"})
	if out["action"] != agent.Scheduled {
		t.Fatalf("action = %v, want scheduled", out["action"])
	}
	if len(f.sent) != 0 || len(f.queued) != 1 {
		t.Fatalf("sent %d, queued %d; want it queued alone", len(f.sent), len(f.queued))
	}
	q := f.queued[0]
	if !q.At.Equal(at) || q.Body != "morning - all" || q.TxnID == "" || q.ThreadRoot != "$1" {
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
	if msg := callErr(t, s, "send_message", map[string]any{"room": "Standup", "text": "hi", "at": at}); !strings.Contains(msg, "sent later only where you may send") {
		t.Errorf("drafted room: %q", msg)
	}
	if len(f.queued) != 0 || len(f.drafts) != 0 {
		t.Errorf("queued %d, drafts %d; want nothing written", len(f.queued), len(f.drafts))
	}
	if ledger := entries(t, s); len(ledger) != 1 || ledger[0].Outcome != agent.Refused {
		t.Errorf("ledger = %+v, want the refusal recorded", ledger)
	}
	sender := newWriter(t, twoRooms(), shareAll, []string{"space:Work"})
	for _, bad := range []string{"tomorrow 9am", time.Now().Add(-time.Minute).Format(time.RFC3339)} {
		if msg := callErr(t, sender, "send_message", map[string]any{"room": "Standup", "text": "hi", "at": bad}); !strings.Contains(msg, "at ") {
			t.Errorf("at %q: %q", bad, msg)
		}
	}
}
