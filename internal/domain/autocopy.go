package domain

// AutoCopy configures copying verification codes to the clipboard unattended.
type AutoCopy struct {
	// Enabled is separate from code detection on purpose: writing the clipboard
	// unasked must be opted into.
	Enabled bool
	Scope   CodeScope
	Rules   CodeRules
}

// Arrival is what the daemon knows about a message beyond its content.
type Arrival struct {
	// CaughtUp means the first sync response had finished: the message is live, not
	// catch-up after downtime.
	CaughtUp bool
	Mine     bool
	Room     RoomFacts
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
