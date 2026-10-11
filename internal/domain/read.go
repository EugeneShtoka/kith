package domain

import "time"

// Read policy: when a room counts as read, and whether a receipt is sent.

// ReadRule is one place's read policy; nil fields inherit.
type ReadRule struct {
	// Match is a room ID or a home entry (NamesHome); Sender is an MXID.
	Match  string
	Sender string
	// Send is whether a read receipt goes to the homeserver at all.
	Send *bool
	// Delay is how long the cursor must rest on an unopened room before it counts as read.
	Delay *time.Duration
}

// ReadPolicy is the resolved read policy for one place.
type ReadPolicy struct {
	// Send is whether other people are told.
	Send bool
	// OnFocus is whether selecting a room without opening it can read it at all.
	OnFocus bool
	// FocusAfter is how long the cursor must rest there first.
	FocusAfter time.Duration
}

// FocusIsImmediate reports whether selecting the room is enough on its own.
func (p ReadPolicy) FocusIsImmediate() bool { return p.OnFocus && p.FocusAfter <= 0 }

// ResolveRead applies the narrowest matching rule to the defaults; a negative delay
// disables reading on focus.
func ResolveRead(send bool, delay time.Duration, rules []ReadRule, place DownloadPlace) ReadPolicy {
	if rule, ok := narrowestReadRule(rules, place); ok {
		if rule.Send != nil {
			send = *rule.Send
		}
		if rule.Delay != nil {
			delay = *rule.Delay
		}
	}
	if delay < 0 {
		return ReadPolicy{Send: send}
	}
	return ReadPolicy{Send: send, OnFocus: true, FocusAfter: delay}
}

// narrowestReadRule is the most specific rule naming this place, if any.
func narrowestReadRule(rules []ReadRule, place DownloadPlace) (ReadRule, bool) {
	at, ok := Narrowest(Matches(rules, func(r ReadRule) Match {
		return Match{Place: r.Match, Sender: r.Sender}
	}), place.scope())
	if !ok {
		return ReadRule{}, false
	}
	return rules[at], true
}
