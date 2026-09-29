package domain_test

import (
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// started is when the daemon came up; fresh is a message that arrived after it.
var (
	started = time.Date(2026, 8, 23, 9, 0, 0, 0, time.Local)
	fresh   = started.Add(time.Minute)
)

// capturing is auto-copy configured the way it has to be to do anything: on, and
// with somewhere named.
func capturing() domain.AutoCopy {
	return domain.AutoCopy{
		Enabled: true,
		Scope:   domain.CodeScope{Include: []string{"space:Bridges"}},
		Rules:   domain.DefaultCodeRules(),
	}
}

// arrived is a message with a code in it, landing in the SMS-bridge room.
func arrived(at time.Time) domain.Message {
	return domain.Message{
		ID: "$1", RoomID: "!sms:x", Sender: "@bridge:x",
		Body: "Your code is 482910", Timestamp: at,
	}
}

// here is the room that message landed in, in the space the scope names.
func here() domain.Arrival {
	return domain.Arrival{CaughtUp: true, Room: domain.RoomFacts{
		ID: "!sms:x", Name: "SMS", Spaces: []string{"Bridges"},
	}}
}

// The case the feature exists for: a code arrives while nothing is attached, in a
// place that was named, and it is captured.
func TestAutoCopyCapturesALiveCode(t *testing.T) {
	t.Parallel()

	code, ok := capturing().Decide(arrived(fresh), here())
	if !ok {
		t.Fatal("a live code in a named place should be captured")
	}
	if code.Value != "482910" {
		t.Errorf("code = %q, want the one in the message", code.Value)
	}
	if code.Label == "" {
		t.Error("the keyword that marked it should be kept — it is why this is a code")
	}
}

// refusal is one guard's case: the configuration, the message, and where it landed.
type refusal struct {
	rules domain.AutoCopy
	msg   domain.Message
	in    domain.Arrival
}

// mine, catchingUp and elsewhere are the arrival variations the guards turn on.
func mine() domain.Arrival       { in := here(); in.Mine = true; return in }
func catchingUp() domain.Arrival { in := here(); in.CaughtUp = false; return in }
func elsewhere() domain.Arrival {
	in := here()
	in.Room = domain.RoomFacts{ID: "!work:x", Name: "Standup", Spaces: []string{"Work"}}
	return in
}

// Every guard, one at a time. Each of these is the difference between a useful
// feature and a clipboard anyone who can message you controls.
func TestAutoCopyRefusals(t *testing.T) {
	t.Parallel()

	noCode := arrived(fresh)
	noCode.Body = "see you at the thing"

	tests := map[string]refusal{
		"off by default": {
			rules: domain.AutoCopy{Scope: capturing().Scope, Rules: capturing().Rules},
			msg:   arrived(fresh), in: here(),
		},
		"our own message":   {capturing(), arrived(fresh), mine()},
		"still catching up": {capturing(), arrived(fresh), catchingUp()},
		"nowhere named, so everywhere": {
			rules: domain.AutoCopy{Enabled: true, Rules: capturing().Rules},
			msg:   arrived(fresh), in: here(),
		},
		"somewhere else": {capturing(), arrived(fresh), elsewhere()},
		"excluded even though included": {
			rules: domain.AutoCopy{
				Enabled: true,
				Scope:   domain.CodeScope{Include: []string{"space:Bridges"}, Exclude: []string{"!sms:x"}},
				Rules:   capturing().Rules,
			},
			msg: arrived(fresh), in: here(),
		},
		"no code in the message": {capturing(), noCode, here()},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, ok := tc.rules.Decide(tc.msg, tc.in); ok {
				t.Error("this should not have been captured")
			}
		})
	}
}

// Nothing looks at the clock.
func TestAutoCopyDoesNotLookAtTheClock(t *testing.T) {
	t.Parallel()

	for name, at := range map[string]time.Time{
		"stamped now":       fresh,
		"stamped last week": started.Add(-7 * 24 * time.Hour),
		"unstamped":         {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, ok := capturing().Decide(arrived(at), here()); !ok {
				t.Error("a message that arrived after catch-up is live, whatever its stamp")
			}
		})
	}
}

// With several plausible codes the strongest is taken.
func TestAutoCopyTakesTheStrongestCode(t *testing.T) {
	t.Parallel()

	msg := arrived(fresh)
	msg.Body = "ticket 5512 — your code is 482910"
	code, ok := capturing().Decide(msg, here())
	if !ok {
		t.Fatal("a labeled code should still be found alongside a bare number")
	}
	if code.Value != "482910" {
		t.Errorf("code = %q, want the labeled one", code.Value)
	}
}
