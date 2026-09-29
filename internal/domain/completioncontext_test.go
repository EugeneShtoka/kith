package domain

import (
	"strings"
	"testing"
	"time"
)

// said builds a run of messages at the given minute offsets, so a gap is written as a
// jump in the numbers rather than as a pile of timestamps.
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

// run is the chosen messages as one string, so an expectation reads as the letters that
// survived rather than as a slice.
func run(msgs []Message) string { return strings.Join(bodies(msgs), "") }

// The recent window is the exchange you are in, and it stops at the silence that ended
// the one before it — which is the whole reason it is a gap rather than a calendar day.
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
	// And a conversation that crosses midnight is one conversation: the gap is between
	// the messages, not against a date.
	late := spoken(0, 5, 10)
	for i := range late {
		late[i].Timestamp = time.Date(2026, 9, 20, 23, 50+i*10, 0, 0, time.UTC)
	}
	if got := run(CompletionContext(late, "", pick)); got != "abc" {
		t.Errorf("context = %q, want the whole exchange across midnight", got)
	}
}

// Whatever the draft is answering is quoted however old it is, which is the half a
// recency window cannot do — and is the answer to whether an old message is still
// relevant: it is, exactly when you are replying to it.
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
	// A target already inside the recent window is quoted once, with its lead-in added
	// around it rather than a second copy of it appended.
	if got := run(CompletionContext(msgs, "f", pick)); got != "def" {
		t.Errorf("context = %q, want the window widened by the lead-in and nothing repeated", got)
	}
}

// A zero pick is the default shape rather than silence: a caller that forgot to
// configure this must not quietly quote nothing, which looks exactly like a model that
// has stopped being useful.
func TestAZeroPickIsTheDefaultShape(t *testing.T) {
	t.Parallel()

	msgs := spoken(0, 5, 10, 15, 20, 25)
	if got := run(CompletionContext(msgs, "", ContextPick{})); got != "cdef" {
		t.Errorf("context = %q, want the default window rather than nothing", got)
	}
}
