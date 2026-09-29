package domain

import "time"

// When a room counts as read, and whether anyone else is told.

// ReadRule is one place's read policy, in the pure layer's own terms. Each field is
// "unset means inherit", so a rule states only what it changes.
type ReadRule struct {
	// Match is a room ID or a space's name; Sender is an MXID.
	Match  string
	Sender string
	// Send is whether a read receipt goes to the homeserver at all.
	Send *bool
	// Delay is how long the cursor must rest on the room before it counts as read
	// without being opened.
	Delay *time.Duration
}

// ReadPolicy is what actually applies somewhere.
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

// ResolveRead works out what applies in one place.
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
