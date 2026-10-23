package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// pollCreatedMsg is the outcome of a /poll create. On success it names the start
// event ID; on failure the error is spoken.
type pollCreatedMsg struct {
	target string // room written in, for the status line
	err    error
}

// runCreatePoll is `/poll <question> | <answer1> | <answer2> [| …] [| any]`: creates a
// Matrix poll in the open room. A trailing `| any` makes it multi-select; a bare "|"
// (no trailing pipe) is single-choice. At least two answers are required.
func runCreatePoll(m Model, arg string, room domain.Room) (Model, tea.Cmd) {
	question, options, multiple, ok := parsePollArg(arg)
	if !ok {
		return m.say("/poll <question> | <answer1> | <answer2> [| …] [| any]"), nil
	}
	m.compose.input, m.compose.drafted = "", nil
	return m, m.createPollCmd(room.ID, question, options, multiple)
}

// parsePollArg splits a /poll argument into its parts. The pipes separate; a trailing
// "any" answer sets multiple. ok is false for a question missing or fewer than two
// answers.
func parsePollArg(arg string) (question string, options []string, multiple, ok bool) {
	parts := strings.Split(arg, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	if len(parts) < 3 {
		return "", nil, false, false
	}
	question = parts[0]
	options = parts[1:]
	if last := strings.ToLower(options[len(options)-1]); last == "any" {
		multiple = true
		options = options[:len(options)-1]
	}
	if question == "" || len(options) < 2 || slices.Contains(options, "") {
		return "", nil, false, false
	}
	return question, options, multiple, true
}

// createPollCmd asks the daemon to create a poll, off the event loop.
func (m Model) createPollCmd(roomID domain.RoomID, question string, options []string, multiple bool) tea.Cmd {
	backend, ctx := m.backend, m.ctx
	target := string(roomID)
	return func() tea.Msg {
		_, err := backend.CreatePoll(ctx, roomID, question, options, multiple)
		return pollCreatedMsg{target: target, err: err}
	}
}

// handlePollCreated reports the daemon's answer.
func (m Model) handlePollCreated(msg pollCreatedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("create poll", msg.err), nil
	}
	return m.say("poll created"), nil
}
