package domain

import "time"

// The default context a completion quotes.
const (
	// DefaultContextRecent is how many of the last messages a completion sees.
	DefaultContextRecent = 4
	// DefaultContextGap is the silence that ends a conversation.
	DefaultContextGap = 90 * time.Minute
	// DefaultContextBefore is how much of what a reply's target was itself answering
	// comes along.
	DefaultContextBefore = 2
)

// DefaultContextPick is that shape as a value.
func DefaultContextPick() ContextPick {
	return ContextPick{Recent: DefaultContextRecent, Gap: DefaultContextGap, Before: DefaultContextBefore}
}

// ContextPick is how much a completion may quote, and what counts as still going.
type ContextPick struct {
	// Recent is how many of the last messages to take.
	Recent int
	// Gap is the silence that ends a conversation.
	Gap time.Duration
	// Before is how many messages to take before a reply's target, for what that
	// message was itself answering.
	Before int
}

// CompletionContext is the messages a completion may quote, oldest first.
func CompletionContext(msgs []Message, anchor EventID, pick ContextPick) []Message {
	// A zero pick is the default shape, not silence. Nothing is ever improved by a
	// caller that forgot to configure this quoting no conversation at all, and the
	// failure would look exactly like a model that has stopped being useful.
	if pick.Recent <= 0 && pick.Before <= 0 {
		pick = DefaultContextPick()
	}
	keep := make(map[EventID]bool, pick.Recent+pick.Before+1)
	// Indexed rather than ranged by value: a Message is 280 bytes and these loops only
	// want its ID.
	recent := recentRun(msgs, pick)
	for i := range recent {
		keep[recent[i].ID] = true
	}
	around := aroundAnchor(msgs, anchor, pick.Before)
	for i := range around {
		keep[around[i].ID] = true
	}
	out := make([]Message, 0, len(keep))
	for i := range msgs {
		if keep[msgs[i].ID] {
			out = append(out, msgs[i])
		}
	}
	return out
}

// recentRun is the tail of the conversation that is still going: at most Recent
// messages, stopping at the first silence longer than Gap.
func recentRun(msgs []Message, pick ContextPick) []Message {
	if pick.Recent <= 0 || len(msgs) == 0 {
		return nil
	}
	first := len(msgs) - 1
	for ; first > 0 && len(msgs)-first < pick.Recent; first-- {
		if pick.Gap > 0 && msgs[first].Timestamp.Sub(msgs[first-1].Timestamp) > pick.Gap {
			break
		}
	}
	return msgs[first:]
}

// aroundAnchor is the message a draft replies to, plus the few before it.
func aroundAnchor(msgs []Message, anchor EventID, before int) []Message {
	if anchor == "" {
		return nil
	}
	for i := range msgs {
		if msgs[i].ID != anchor {
			continue
		}
		first := max(i-before, 0)
		return msgs[first : i+1]
	}
	return nil
}
