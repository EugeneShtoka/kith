package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rivo/uniseg"
)

// ScheduledMessage is one message written now and sent later.
type ScheduledMessage struct {
	ID         string // for canceling
	RoomID     RoomID
	Body       string
	ThreadRoot EventID // keeps a scheduled reply in its thread
	ReplyTo    EventID
	Mentions   []Mention
	// TxnID makes a retry idempotent: the homeserver deduplicates by it.
	TxnID   string
	Emote   bool
	At      time.Time // when to send, UTC
	Written time.Time // when it was composed
}

// Overdue reports whether the send time has passed at now.
func (s ScheduledMessage) Overdue(now time.Time) bool { return now.After(s.At) }

// Stale reports whether it is so far overdue that sending it unasked would be worse
// than not sending it.
func (s ScheduledMessage) Stale(now time.Time, cutoff time.Duration) bool {
	if cutoff <= 0 {
		return false
	}
	return now.Sub(s.At) > cutoff
}

// SchedulePlan is what to do with a queue at one moment.
type SchedulePlan struct {
	Send []ScheduledMessage // due and within the cutoff, oldest first
	Held []ScheduledMessage // due but past the cutoff
	Next time.Time          // the earliest not-yet-due time, or zero
}

// PlanSchedule sorts a queue into what to send, what to hold, and when to wake up.
func PlanSchedule(queue []ScheduledMessage, now time.Time, cutoff time.Duration) SchedulePlan {
	var plan SchedulePlan
	for i := range queue {
		msg := &queue[i]
		switch {
		case !msg.Overdue(now):
			if plan.Next.IsZero() || msg.At.Before(plan.Next) {
				plan.Next = msg.At
			}
		case msg.Stale(now, cutoff):
			plan.Held = append(plan.Held, *msg)
		default:
			plan.Send = append(plan.Send, *msg)
		}
	}
	sort.Slice(plan.Send, func(i, j int) bool { return plan.Send[i].At.Before(plan.Send[j].At) })
	sort.Slice(plan.Held, func(i, j int) bool { return plan.Held[i].At.Before(plan.Held[j].At) })
	return plan
}

// SortScheduled orders a queue for display: soonest first, so the next thing to
// happen is the first thing read.
func SortScheduled(queue []ScheduledMessage) {
	sort.Slice(queue, func(i, j int) bool {
		if !queue[i].At.Equal(queue[j].At) {
			return queue[i].At.Before(queue[j].At)
		}
		return queue[i].ID < queue[j].ID
	})
}

// Immediate reports whether this was a send-now the homeserver refused, queued for
// retry, rather than one scheduled for a time.
func (s ScheduledMessage) Immediate() bool { return !s.At.After(s.Written) }

// summaryLength is how many characters of the body a Summary keeps.
const summaryLength = 48

// clipGraphemes shortens s to at most n characters — grapheme clusters, what a reader
// counts — ending a shortened one with an ellipsis.
func clipGraphemes(s string, n int) string {
	if uniseg.GraphemeClusterCount(s) <= n {
		return s
	}
	var b strings.Builder
	g := uniseg.NewGraphemes(s)
	for kept := 0; kept < n-1 && g.Next(); kept++ {
		b.WriteString(g.Str())
	}
	return b.String() + "…"
}

// Summary is a one-line description of a pending send, for a list or a status line.
func (s ScheduledMessage) Summary(now time.Time) string {
	return s.SummaryWith(now, func(body string) string { return body })
}

// SummaryWith is Summary with the shortened body passed through wrap, so a renderer can
// style it.
func (s ScheduledMessage) SummaryWith(now time.Time, wrap func(string) string) string {
	body := wrap(clipGraphemes(strings.TrimSpace(s.Body), summaryLength))
	when := s.At.Local().Format("Mon 15:04")
	switch {
	case s.Immediate():
		return fmt.Sprintf("unsent since %s — %s", when, body)
	case s.Overdue(now):
		return fmt.Sprintf("%s (overdue) — %s", when, body)
	}
	return fmt.Sprintf("%s — %s", when, body)
}
