package domain

// Unattended capture of a verification code: the decision, with none of the doing.

// AutoCopy is the configured shape of unattended capture: whether it happens at all,
// where it may happen, and what a code looks like there.
type AutoCopy struct {
	// Enabled is the switch, and it is separate from code *detection* on purpose:
	// finding codes so the copy key can offer them is not the same act as writing one
	// into the clipboard unasked, and turning the second one on should be a deliberate
	// thing to do.
	Enabled bool
	// Scope is where codes are worth looking for.
	Scope CodeScope
	// Rules is what counts as code-shaped.
	Rules CodeRules
}

// Arrival is what the daemon knows about a message beyond its content: whether it
// came in live, whose it is, and where it landed.
type Arrival struct {
	// CaughtUp reports that the daemon had finished its first sync response when this
	// message arrived — so it is a message that happened *now*, not one of the batch
	// catching up on everything missed while the daemon was down.
	CaughtUp bool
	// Mine marks our own message.
	Mine bool
	// Room places the message for the scope: where it landed, what network it is on,
	// and whether it is a direct message.
	Room RoomFacts
}

// Decide returns the code to copy from msg, and whether there is one to copy.
func (a AutoCopy) Decide(msg Message, in Arrival) (Code, bool) {
	if !a.Enabled || in.Mine {
		return Code{}, false
	}
	if !in.CaughtUp {
		return Code{}, false
	}
	if a.Scope.Everywhere() || !a.Scope.Admits(in.Room) {
		return Code{}, false
	}
	found := Codes(msg.Body, a.Rules)
	if len(found) == 0 {
		return Code{}, false
	}
	return found[0], true
}
