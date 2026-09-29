package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Taking one thing (a link, a Matrix address, a code) out of the message under the
// cursor. The rule: none says so, one acts, several ask. Each kind keeps its own
// "none" sentence.

// contextKind is the thing being taken, not the purpose: three keys want a link and
// mean different things by it.
type contextKind int

const (
	ctxNone contextKind = iota
	// ctxLink is an http/https link — what `gx`, `gX` and `yu` work on.
	ctxLink
	// ctxPlace is a Matrix address: a room, a person, or one message in a room.
	ctxPlace
	// ctxCode is a verification code, as [codes] defines one.
	ctxCode
)

// contextSpec is one kind: how to find its candidates in a message, and what to say
// when there are none.
type contextSpec struct {
	// items builds the chooser's rows; each row's value is what a consumer receives.
	items func(m Model, msg domain.Message) []pickerItem
	// noun names one of these, for titling a chooser a script asked for.
	noun string
	// empty is the status line when the message holds none. The code one names the
	// rules it looked with, so a too-narrow rule set is distinguishable from no code.
	empty func(m Model) string
}

var contextSpecs = map[contextKind]contextSpec{
	ctxLink: {
		noun: "link",
		items: func(_ Model, msg domain.Message) []pickerItem {
			return linkItems(domain.Links(msg.Body))
		},
		empty: func(Model) string { return "no link in that message" },
	},
	ctxPlace: {
		noun: "place",
		items: func(m Model, msg domain.Message) []pickerItem {
			return placeItems(m, domain.PlaceLinks(msg.Body, m.messageHrefs(msg)))
		},
		empty: func(Model) string { return "nothing in that message points into Matrix" },
	},
	ctxCode: {
		noun: "code",
		items: func(m Model, msg domain.Message) []pickerItem {
			// CodeCandidates, not Codes: the key was pressed on this message, so the
			// bar is "most plausible", not "worth volunteering unprompted".
			return codeItems(domain.CodeCandidates(msg.Body, m.prefs.codes.rules))
		},
		empty: func(m Model) string {
			return "no code in that message — looking for " + m.prefs.codes.rules.Describe() + " ([codes])"
		},
	},
}

// contextFor is which kind each key asks for.
var contextFor = map[action]contextKind{
	actCopyURL:      ctxLink,
	actOpenURL:      ctxLink,
	actOpenURLFocus: ctxLink,
	actFollowLink:   ctxPlace,
	actCopyCode:     ctxCode,
}

// contextTitles is what the chooser asks, per action.
var contextTitles = map[action]string{
	actCopyURL:      "Copy which link?",
	actOpenURL:      "Open which link?",
	actOpenURLFocus: "Go to which link?",
	actFollowLink:   "Follow which link?",
	actCopyCode:     "Copy which code?",
}

// takeContext applies the one-or-ask rule. The action is stamped on the picker at
// press time, so accepting a row cannot drift from the key that opened it.
func (m Model) takeContext(then action) (Model, tea.Cmd) {
	kind := contextFor[then]
	if kind == ctxNone {
		return m, nil
	}
	msg, selected := m.selectedMessage()
	if !selected {
		return m, nil
	}
	spec := contextSpecs[kind]
	items := spec.items(m, msg)
	switch len(items) {
	case 0:
		return m.say(spec.empty(m)), nil
	case 1:
		return m.useContext(then, items[0].value)
	}
	m.picker = newContextPicker(then, items)
	return m, nil
}

// newContextPicker opens the chooser with the action already stamped on it.
func newContextPicker(then action, items []pickerItem) picker {
	p := newPickerWith(pickerContext, pickerSpec{title: contextTitles[then], modal: false}, items)
	p.then = then
	return p
}

// useContext hands a resolved value to the action, directly for a single candidate
// or through the chooser.
func (m Model) useContext(then action, value string) (Model, tea.Cmd) {
	switch then {
	case actCopyURL:
		return m.copy(value, describeCopy("link", value))
	case actOpenURL:
		return m.open(value)
	case actOpenURLFocus:
		return m.openFocused(value)
	case actFollowLink:
		place, ok := domain.ParsePlace(value)
		if !ok {
			return m, nil
		}
		return m.goToPlace(place)
	case actCopyCode:
		return m.copy(value, describeCopy("code", value))
	case actRunScript:
		return m.pickedScriptContext(value)
	}
	return m, nil
}

// acceptContext is what choosing a row does: close the chooser, then act on it with
// the intent the key stamped.
func (m Model) acceptContext(then action, value string) (Model, tea.Cmd) {
	m = m.closePicker()
	return m.useContext(then, value)
}
