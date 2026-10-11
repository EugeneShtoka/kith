package domain

import (
	"strings"
	"testing"
	"time"
)

// said builds messages at the given minute offsets.
func spoken(minutes ...int) []Message {
	base := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	out := make([]Message, 0, len(minutes))
	for i, at := range minutes {
		out = append(out, Message{
			ID:        EventID(string(rune('a' + i))),
			Sender:    "@dana:x",
			Body:      string(rune('a' + i)),
			Timestamp: base.Add(time.Duration(at) * time.Minute),
		})
	}
	return out
}

// run joins the chosen messages' bodies into one string.
func run(msgs []Message) string { return strings.Join(bodies(msgs), "") }

// The recent window stops at the last long silence, not at a calendar day.
func TestTheRecentWindowStopsAtASilence(t *testing.T) {
	t.Parallel()

	pick := ContextPick{Recent: 4, Gap: 90 * time.Minute}
	// Four messages in a row, then a three-hour silence, then three more.
	msgs := spoken(0, 5, 10, 15, 195, 200, 205)

	if got := run(CompletionContext(msgs, "", pick)); got != "efg" {
		t.Errorf("context = %q, want only the exchange after the silence", got)
	}
	// With no silence in the way, the window is the count.
	if got := run(CompletionContext(spoken(0, 5, 10, 15, 20, 25), "", pick)); got != "cdef" {
		t.Errorf("context = %q, want the last four", got)
	}
	// A conversation across midnight is one conversation.
	late := spoken(0, 5, 10)
	for i := range late {
		late[i].Timestamp = time.Date(2026, 9, 20, 23, 50+i*10, 0, 0, time.UTC)
	}
	if got := run(CompletionContext(late, "", pick)); got != "abc" {
		t.Errorf("context = %q, want the whole exchange across midnight", got)
	}
}

// The reply target is quoted however old it is.
func TestTheReplyTargetIsQuotedHoweverOldItIs(t *testing.T) {
	t.Parallel()

	pick := ContextPick{Recent: 2, Gap: 30 * time.Minute, Before: 2}
	msgs := spoken(0, 5, 10, 15, 300, 305)

	got := run(CompletionContext(msgs, "b", pick))
	// a, b — the target and what it was answering — and the recent pair at the end.
	if got != "abef" {
		t.Errorf("context = %q, want the reply target with its lead-in and the recent pair", got)
	}
	// A target older than the cache is nothing rather than a guess.
	if got := run(CompletionContext(msgs, "$gone", pick)); got != "ef" {
		t.Errorf("context = %q, want just the recent window", got)
	}
	// A target inside the recent window is quoted once, with its lead-in.
	if got := run(CompletionContext(msgs, "f", pick)); got != "def" {
		t.Errorf("context = %q, want the window widened by the lead-in and nothing repeated", got)
	}
}

// A zero pick means the defaults, not quoting nothing.
func TestAZeroPickIsTheDefaultShape(t *testing.T) {
	t.Parallel()

	msgs := spoken(0, 5, 10, 15, 20, 25)
	if got := run(CompletionContext(msgs, "", ContextPick{})); got != "cdef" {
		t.Errorf("context = %q, want the default window rather than nothing", got)
	}
}
