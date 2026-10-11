package domain

import "time"

// The default context a completion quotes.
const (
	// DefaultContextRecent is how many of the last messages a completion sees.
	DefaultContextRecent = 4
	// DefaultContextGap is the silence that ends a conversation.
	DefaultContextGap = 90 * time.Minute
	// DefaultContextBefore is how many messages before a reply's target come along.
	DefaultContextBefore = 2
)

// DefaultContextPick is the default ContextPick.
func DefaultContextPick() ContextPick {
	return ContextPick{Recent: DefaultContextRecent, Gap: DefaultContextGap, Before: DefaultContextBefore}
}

// ContextPick is how much a completion may quote, and what counts as still going.
type ContextPick struct {
	// Recent is how many of the last messages to take.
	Recent int
	// Gap is the silence that ends a conversation.
	Gap time.Duration
	// Before is how many messages to take before a reply's target.
	Before int
}

// CompletionContext is the messages a completion may quote, oldest first.
func CompletionContext(msgs []Message, anchor EventID, pick ContextPick) []Message {
	// A zero pick means the default, not no context: quoting nothing would look like a
	// useless model.
	if pick.Recent <= 0 && pick.Before <= 0 {
		pick = DefaultContextPick()
	}
	keep := make(map[EventID]bool, pick.Recent+pick.Before+1)
	// Indexed, not ranged by value: a Message is large.
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

// recentRun is the last Recent messages, cut at the latest silence longer than Gap.
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
