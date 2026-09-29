package matrix

import "maunium.net/go/mautrix/event"

// parse parses an event's raw content in place. Events from /messages and sync's
// ephemeral/account-data sections arrive unparsed, and forgetting this makes messages
// silently vanish. Errors are dropped: callers type-switch on Parsed, which stays nil.
func parse(evt *event.Event) {
	if evt == nil {
		return
	}
	_ = evt.Content.ParseRaw(evt.Type) // ignored: see above; Parsed nil is the signal
}
