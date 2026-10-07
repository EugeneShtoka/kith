package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A message's poll draws as its question and an answer per line, with how many chose
// each and which this person did; the vote key (P) picks answers, several where the
// poll takes them, or takes the vote back. The network's new results come back as the
// message's poll changing.

// voteTakeBack is the vote picker's row that takes a vote back.
const voteTakeBack = "\x00take back"

// pollBody is a poll as the lines of its message: the question, then each answer.
func pollBody(p *domain.Poll) string {
	var b strings.Builder
	b.WriteString("📊 " + p.Question)
	switch {
	case p.Closed:
		b.WriteString("  (closed)")
	case p.Multiple:
		b.WriteString("  (choose any)")
	}
	for _, o := range p.Options {
		mark := "○"
		if o.Mine {
			mark = "●"
		}
		b.WriteString("\n" + mark + " " + o.Text + " — " + p.Tally(o))
	}
	return b.String()
}

// votedMsg is the outcome of a vote.
type votedMsg struct{ err error }

// openVote offers the selected message's poll's answers, the chosen ones ticked.
func (m Model) openVote() (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	switch {
	case !ok || msg.Poll == nil || msg.Redacted:
		return m.say("this message asks no poll"), nil
	case msg.Poll.Closed:
		return m.say("this poll is closed"), nil
	}
	p := msg.Poll
	items := make([]pickerItem, 0, len(p.Options)+1)
	checked := map[string]bool{}
	for _, o := range p.Options {
		items = append(items, pickerItem{label: o.Text, detail: p.Tally(o), value: o.ID})
		checked[o.ID] = o.Mine
	}
	m.aimedAt.vote = msg
	if p.Multiple {
		spec := pickerSpecs[pickerVoteMulti]
		spec.title = p.Question + " — tick any, then choose"
		m.picker = newPickerWith(pickerVoteMulti, spec, items)
		m.picker.checked = checked
		return m, nil
	}
	if p.Voted() {
		items = append(items, pickerItem{label: "Take my vote back", value: voteTakeBack})
	}
	spec := pickerSpecs[pickerVote]
	spec.title = p.Question
	m.picker = newPickerWith(pickerVote, spec, items)
	return m, nil
}

// castVote sends a vote, the answers by ID; none takes it back.
func (m Model) castVote(options []string) (Model, tea.Cmd) {
	m = m.closePicker()
	msg := m.aimedAt.vote
	if msg.ID == "" {
		return m, nil
	}
	ctx, backend := m.ctx, m.backend
	saying := "voting…"
	if len(options) == 0 {
		saying = "taking the vote back…"
	}
	return m.doing(saying), func() tea.Msg {
		return votedMsg{err: backend.VotePoll(ctx, msg.RoomID, msg.ID, options)}
	}
}

// handleVoted says how a vote went; the results come with the poll's message.
func (m Model) handleVoted(msg votedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not vote", msg.err), nil
	}
	return m.say("voted"), nil
}
