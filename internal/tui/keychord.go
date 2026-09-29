package tui

import "strings"

// Chord resolution, in order: a complete binding acts immediately; an incomplete one
// is held (and shown on the status line); anything else ends the sequence and is
// judged on its own, so a half-typed chord is harmless. Only resolved where keys are
// commands, never while typing.

// advanceChord resolves one press against the sequence in progress. It returns the
// binding to act on and whether the press was swallowed as an incomplete step.
func (m Model) advanceChord(press string) (Model, string, bool) {
	return m.advanceChordIn(press, m.activeScopes()...)
}

// advanceChordIn is advanceChord against scopes: an overlay that owns the keyboard
// (help) resolves its own sequences ("gg") before any pane sees the press.
func (m Model) advanceChordIn(press string, scopes ...scope) (Model, string, bool) {
	seq := press
	if len(m.chord) > 0 {
		seq = strings.Join(m.chord, " ") + " " + press
	}
	switch {
	case m.keys.boundIn(seq, scopes...):
		m.chord = nil
		return m, seq, false
	case m.keys.incomplete(seq, scopes...):
		m.chord = append(append([]string(nil), m.chord...), press)
		return m, "", true
	default:
		m.chord = nil
		return m, press, false
	}
}

// activeScopes is the chain the focused pane will resolve a press against. It mirrors
// the pane handlers because the chord resolver runs before the press reaches a pane.
func (m Model) activeScopes() []scope {
	pane := scopeTimeline
	switch m.focus {
	case paneRail:
		pane = scopeRail
	case paneRooms:
		pane = scopeRooms
	case paneTimeline, paneCount:
	}
	return append([]scope{scopeGlobal}, m.withPlayerScope(pane, scopeNav, scopeCommand)...)
}

// typing reports whether keys are text rather than commands right now; chords are
// suspended then, since holding a key back would drop a character.
func (m Model) typing() bool {
	return m.compose.insertMode || m.compose.reacting || m.prompt.active() || m.picker.active() ||
		m.completion.active || m.search.active
}

// chordPending is the sequence so far for the status line, or "".
func (m Model) chordPending() string {
	if len(m.chord) == 0 {
		return ""
	}
	return spellSequence(strings.Join(m.chord, " ")) + "…"
}
