package tui

// The read-and-dismiss overlays: full-frame, one question, closed by any key.
// (Not overlayTarget, which is what an open prompt is aimed at.)

// reader names which overlay is up.
type reader uint8

const (
	readerNone    reader = iota
	readerHelp           // the keybinding list
	readerWhy            // which rules are silencing the open room
	readerTopic          // the room's full m.room.topic
	readerAsk            // the model layer's dry run (see assist.go)
	readerSummary        // `/summary`
	readerTodo           // what people are waiting on you for
	readerScript         // a script's output with `output = "pager"`
)

// readerState is the overlay up and its first visible row. The help overlay can be
// narrowed: filter is the text its rows must contain, and filtering that it is being
// typed (keys are text then, see fieldHelpFilter).
type readerState struct {
	kind      reader
	scroll    int
	filter    string
	filtering bool
}

// up reports whether an overlay has the frame.
func (r readerState) up() bool { return r.kind != readerNone }

// boxed reports whether readerBox draws this overlay (title, body, footer).
func (r readerState) boxed() bool {
	switch r.kind {
	case readerSummary, readerTopic, readerTodo, readerAsk, readerScript:
		return true
	case readerNone, readerHelp, readerWhy:
		return false
	}
	return false
}

// showing reports whether this particular overlay is the one up.
func (r readerState) showing(kind reader) bool { return r.kind == kind }

// opening puts one up, scrolled to the top.
func (r readerState) opening(kind reader) readerState {
	return readerState{kind: kind, scroll: 0}
}

// closed dismisses whatever is up.
func (r readerState) closed() readerState { return readerState{} }

// scrolledTo moves the offset, clamped to [0, limit]; limit is the largest first row
// that still fills the overlay.
func (r readerState) scrolledTo(want, limit int) readerState {
	r.scroll = max(min(want, limit), 0)
	return r
}
