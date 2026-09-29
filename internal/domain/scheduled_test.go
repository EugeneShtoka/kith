package domain

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

func at(base time.Time, d time.Duration) time.Time { return base.Add(d) }

// The three outcomes a queue has at any moment, and the cutoff is what separates the
// second from the third.
func TestPlanScheduleSortsIntoSendHeldAndNext(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	queue := []ScheduledMessage{
		{ID: "future", At: at(now, 2*time.Hour)},
		{ID: "just-missed", At: at(now, -5*time.Minute)},
		{ID: "ancient", At: at(now, -72*time.Hour)},
		{ID: "sooner", At: at(now, 30*time.Minute)},
		{ID: "yesterday-early", At: at(now, -20*time.Hour)},
	}

	plan := PlanSchedule(queue, now, 24*time.Hour)

	// Due and inside the cutoff, oldest first: the order they were meant to arrive.
	if len(plan.Send) != 2 || plan.Send[0].ID != "yesterday-early" || plan.Send[1].ID != "just-missed" {
		t.Errorf("Send = %v, want yesterday-early then just-missed", ids(plan.Send))
	}
	// Past the cutoff: nothing happens without a person saying so.
	if len(plan.Held) != 1 || plan.Held[0].ID != "ancient" {
		t.Errorf("Held = %v, want ancient", ids(plan.Held))
	}
	// The wake-up is the *earliest* thing still to come, not the last one seen.
	if !plan.Next.Equal(at(now, 30*time.Minute)) {
		t.Errorf("Next = %v, want the sooner of the two future entries", plan.Next)
	}
}

// A queue with nothing left to wait for asks for no timer at all — which is what
// keeps an idle daemon idle.
func TestPlanScheduleWantsNoWakeupWithNothingPending(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	plan := PlanSchedule(nil, now, 24*time.Hour)
	if !plan.Next.IsZero() {
		t.Errorf("Next = %v on an empty queue, want the zero time", plan.Next)
	}

	// Same when everything is due: they go now, so there is nothing to wake for.
	plan = PlanSchedule([]ScheduledMessage{{ID: "a", At: at(now, -time.Minute)}}, now, 24*time.Hour)
	if !plan.Next.IsZero() {
		t.Errorf("Next = %v with only due messages, want the zero time", plan.Next)
	}
}

// A cutoff of zero means "however late is fine" — for someone who would rather a
// message arrive very late than not at all.
func TestACutoffOfZeroHoldsNothing(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	queue := []ScheduledMessage{{ID: "ancient", At: at(now, -1000*time.Hour)}}

	for _, cutoff := range []time.Duration{0, -time.Hour} {
		plan := PlanSchedule(queue, now, cutoff)
		if len(plan.Held) != 0 {
			t.Errorf("cutoff %v held %v, want nothing held", cutoff, ids(plan.Held))
		}
		if len(plan.Send) != 1 {
			t.Errorf("cutoff %v sent %v, want the message sent", cutoff, ids(plan.Send))
		}
	}
}

// Overdue and Stale at the boundaries: exactly at the cutoff is still sent, and a
// future message is neither, whatever the cutoff.
func TestOverdueAndStale(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	for _, tc := range []struct {
		name           string
		offset, cutoff time.Duration
		overdue, stale bool
	}{
		{"exactly at the cutoff", -day, day, true, false},
		{"past the cutoff", -day - time.Second, day, true, true},
		{"future, tiny cutoff", time.Hour, time.Minute, false, false},
	} {
		msg := ScheduledMessage{At: at(now, tc.offset)}
		if got := msg.Overdue(now); got != tc.overdue {
			t.Errorf("%s: Overdue = %v, want %v", tc.name, got, tc.overdue)
		}
		if got := msg.Stale(now, tc.cutoff); got != tc.stale {
			t.Errorf("%s: Stale = %v, want %v", tc.name, got, tc.stale)
		}
	}
}

func ids(msgs []ScheduledMessage) []string {
	out := make([]string, 0, len(msgs))
	for i := range msgs {
		out = append(out, msgs[i].ID)
	}
	return out
}

// A summary is cut by character, never by byte.
func TestSummaryCutsWholeCharacters(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	for name, body := range map[string]string{
		"hebrew": strings.Repeat("שלום ", 20),
		// Odd-length ASCII before the Hebrew puts byte 47 inside a letter.
		"mixed": "a" + strings.Repeat("עולם ", 20),
		// A family emoji is one character of seven runes; cutting it apart draws four.
		"emoji":  strings.Repeat("👨‍👩‍👧 ", 30),
		"arabic": strings.Repeat("مرحبا ", 20),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := ScheduledMessage{Body: body, At: now.Add(time.Hour), Written: now}.Summary(now)
			if !utf8.ValidString(got) {
				t.Fatalf("Summary = %q, which is not valid UTF-8", got)
			}
			_, text, _ := strings.Cut(got, " — ")
			if !strings.HasSuffix(text, "…") {
				t.Fatalf("Summary = %q, want the long body shortened with an ellipsis", got)
			}
			if !strings.HasPrefix(body, strings.TrimSuffix(text, "…")) {
				t.Errorf("Summary text %q is not a prefix of the body", text)
			}
			if n := uniseg.GraphemeClusterCount(text); n != summaryLength {
				t.Errorf("Summary keeps %d characters, want %d", n, summaryLength)
			}
		})
	}
	short := ScheduledMessage{Body: "שלום", At: now.Add(time.Hour), Written: now}.Summary(now)
	if !strings.HasSuffix(short, "— שלום") {
		t.Errorf("a short body should be kept whole, got %q", short)
	}
}
