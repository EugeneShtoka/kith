package tui

import (
	"github.com/EugeneShtoka/kith/internal/domain"
)

// composeState is the composer: what is being written, where the caret is, and what
// pressing enter will mean. The fields are reset together on send (see cleared).
// Model.selected is not here: it survives entering and leaving insert mode.
type composeState struct {
	input string
	caret caret
	// insertMode: keys compose text; otherwise the timeline is a message browser.
	insertMode bool
	// editing is the message being revised; editSaved is the composer text restored
	// if the edit is abandoned.
	editing   domain.EventID
	editSaved string
	replyTo   domain.EventID
	// reacting and reactInput drive the emoji prompt a react action opens.
	reacting   bool
	reactInput string
	// drafted are the mentions inserted into the composer. The body stays editable,
	// so what ships is filtered against the final text on send (Draft.LiveMentions).
	drafted []domain.Mention
}

// cleared is the composer after a message goes out. insertMode is kept: the next
// thing after one message is usually another.
func (c composeState) cleared() composeState {
	return composeState{insertMode: c.insertMode}
}

// isEditing reports whether enter replaces an existing message.
func (c composeState) isEditing() bool { return c.editing != "" }

// left is the composer put away: neither typing nor reacting.
func (c composeState) left() composeState {
	c.insertMode, c.reacting = false, false
	return c
}

// startReacting opens the emoji prompt empty.
func (c composeState) startReacting() composeState {
	c.reacting, c.reactInput = true, ""
	return c
}

// doneReacting closes the emoji prompt, answered or abandoned, and clears its input.
func (c composeState) doneReacting() composeState {
	c.reacting, c.reactInput = false, ""
	return c
}

// newTxnID is a fresh transaction ID for one send.
func newTxnID() string { return domain.NewTxnID() }
