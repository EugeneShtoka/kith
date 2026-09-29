package domain

import (
	"testing"
	"time"
)

// The ladder is notify.Rule's, to the letter: more constraints beat fewer, and
// among equals the narrower place wins.
func TestResolveReadPrecedence(t *testing.T) {
	place := DownloadPlace{RoomID: "!a:x", Space: "Work", Sender: "@dana:x"}
	rules := []ReadRule{
		{Match: "Work", Send: new(false)},                               // the space
		{Sender: "@dana:x", Send: new(true)},                            // the person anywhere
		{Match: "!a:x", Send: new(false)},                               // this room
		{Match: "Work", Sender: "@dana:x", Send: new(true)},             // person in space
		{Match: "!a:x", Sender: "@dana:x", Delay: new(9 * time.Second)}, // person in room — narrowest
	}
	got := ResolveRead(true, 0, rules, place)
	if !got.OnFocus || got.FocusAfter != 9*time.Second {
		t.Errorf("policy = %+v, want the narrowest rule's 9s", got)
	}
	// The narrowest rule says nothing about Send, so the setting above it stands rather
	// than the next-narrowest rule winning that field.
	if !got.Send {
		t.Error("send = false; an unset field inherits the outer setting, not another rule")
	}
}

// A rule constrained by nothing is not a match: these lists sit under settings that
// already say what happens by default.
func TestResolveReadIgnoresUnconstrainedRules(t *testing.T) {
	got := ResolveRead(true, 0, []ReadRule{{Send: new(false)}}, DownloadPlace{RoomID: "!a:x"})
	if !got.Send {
		t.Error("a rule naming no place overrode the global setting")
	}
}

func TestResolveReadDefaults(t *testing.T) {
	got := ResolveRead(true, 15*time.Second, nil, DownloadPlace{RoomID: "!a:x"})
	if !got.Send || !got.OnFocus || got.FocusAfter != 15*time.Second {
		t.Errorf("with no rules = %+v, want the outer settings", got)
	}
	if got.FocusIsImmediate() {
		t.Error("a 15s delay is not immediate")
	}
	// Zero reads a room as soon as the cursor lands on it.
	zero := ResolveRead(true, 0, nil, DownloadPlace{RoomID: "!a:x"})
	if !zero.FocusIsImmediate() {
		t.Errorf("zero = %+v, want reading on focus at once", zero)
	}
}

// Negative is "never on focus", and it must not collapse into zero — those are opposite
// answers, and collapsing them would make the most conservative setting the most
// aggressive one.
func TestResolveReadNeverOnFocus(t *testing.T) {
	never := ResolveRead(true, -time.Second, nil, DownloadPlace{RoomID: "!a:x"})
	if never.OnFocus || never.FocusIsImmediate() {
		t.Errorf("negative delay = %+v, want never reading on focus", never)
	}
	if !never.Send {
		t.Error("never-on-focus should not change whether receipts are announced")
	}
	// And a rule can say it for one place.
	scoped := ResolveRead(true, 0, []ReadRule{{Match: "!a:x", Delay: new(-time.Second)}},
		DownloadPlace{RoomID: "!a:x"})
	if scoped.OnFocus {
		t.Errorf("a rule saying never = %+v", scoped)
	}
}

// A sender rule with the wrong sender must not match, even when its room does.
func TestResolveReadSenderMustMatch(t *testing.T) {
	rules := []ReadRule{{Match: "!a:x", Sender: "@dana:x", Send: new(false)}}
	got := ResolveRead(true, 0, rules, DownloadPlace{RoomID: "!a:x", Sender: "@bob:x"})
	if !got.Send {
		t.Error("a rule naming Dana applied to a message from Bob")
	}
}
