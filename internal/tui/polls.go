package tui

import (
	"log/slog"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A message's poll draws as its question and an answer per line, with how many chose
// each and which this person did. The vote key (P) opens its answers, ticked as this
// person chose them, to vote (several where the poll takes them) or take the vote
// back, and says under them who chose each, as far as the network names them. The
// network's new results come back as the message's poll changing.

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

// votersState is who chose what in the poll the vote picker is open on, read when it
// opens.
type votersState struct {
	event  domain.EventID
	read   bool
	voters domain.PollVoters
	err    error
}

// pollVotersMsg is who chose what in a poll, as its network names them.
type pollVotersMsg struct {
	event  domain.EventID
	voters domain.PollVoters
	err    error
}

// readVotersCmd asks who chose what in msg's poll.
func (m Model) readVotersCmd(msg domain.Message) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		voters, err := backend.PollVoters(ctx, msg.RoomID, msg.ID)
		return pollVotersMsg{event: msg.ID, voters: voters, err: err}
	}
}

// handlePollVoters keeps the voters of the poll the picker is still open on.
func (m Model) handlePollVoters(msg pollVotersMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelWarn, "read a poll's voters", msg.err)
	if !m.votePickerOpen() || msg.event != m.voters.event {
		return m, nil
	}
	m.voters = votersState{event: msg.event, read: true, voters: msg.voters, err: msg.err}
	return m, nil
}

// votePickerOpen reports whether the picker open is a poll's.
func (m Model) votePickerOpen() bool {
	return m.picker.active() && (m.picker.kind == pickerVote || m.picker.kind == pickerVoteMulti)
}

// voterLines is who chose each answer of the poll the picker is open on, a paragraph
// an answer wrapped to width, under a blank line; why there are none when no one is
// named.
func (m Model) voterLines(width int) []string {
	if !m.votePickerOpen() || m.aimedAt.vote.Poll == nil {
		return nil
	}
	p, room := m.aimedAt.vote.Poll, m.aimedAt.vote.RoomID
	var paras []string
	switch v := m.voters; {
	case !v.read:
		paras = []string{"reading who voted…"}
	case v.err != nil:
		paras = []string{"could not read who voted: " + v.err.Error()}
	case v.voters.Hidden == domain.PollAnonymous:
		paras = []string{"an anonymous poll: no one who voted is named"}
	case v.voters.Hidden == domain.PollVoteFirst:
		paras = []string{"who voted is shown once you have voted"}
	default:
		for _, o := range p.Options {
			who := chosenBy(v.voters.Voters, o.ID, m.voterName(room))
			if len(who) == 0 && o.Votes == 0 {
				continue
			}
			line := o.Text + ": " + strings.Join(who, ", ")
			if rest := o.Votes - len(who); rest > 0 {
				if len(who) > 0 {
					line += ","
				}
				line += " " + strconv.Itoa(rest) + " more"
			}
			paras = append(paras, line)
		}
		if len(paras) == 0 {
			paras = []string{"no one has voted yet"}
		}
	}
	lines := []string{""}
	for _, para := range paras {
		lines = append(lines, drawBlock(para, blockSpec{width: width})...)
	}
	return lines
}

// chosenBy is who chose an answer, by name: "you" first, then sorted.
func chosenBy(voters []domain.PollVoter, option string, name func(domain.PollVoter) string) []string {
	var who []string
	for _, voter := range voters {
		if slices.Contains(voter.Options, option) {
			who = append(who, name(voter))
		}
	}
	slices.SortFunc(who, func(a, b string) int {
		switch {
		case a == b:
			return 0
		case a == voterYou:
			return -1
		case b == voterYou:
			return 1
		}
		return strings.Compare(a, b)
	})
	return who
}

// voterYou is how a poll names this person among its voters.
const voterYou = "you"

// voterName is how a poll in room names a voter: "you"; as the sender column would
// when kith knows them; else as the network called them.
func (m Model) voterName(room domain.RoomID) func(domain.PollVoter) string {
	return func(voter domain.PollVoter) string {
		if m.isMe(voter.ID) {
			return voterYou
		}
		if voter.Name != "" && !m.knowsName(voter.ID) {
			return isolate(m.processedName(domain.Message{RoomID: room, Sender: voter.ID, SenderName: voter.Name}))
		}
		return isolate(m.knownName(voter.ID, room))
	}
}

// knowsName reports whether the open room names id: as a member, or by a message.
func (m Model) knowsName(id string) bool {
	for i := range m.timeline.members {
		if m.timeline.members[i].UserID == id && m.timeline.members[i].DisplayName != "" {
			return true
		}
	}
	for i := range m.timeline.messages {
		if m.timeline.messages[i].Sender == id && m.timeline.messages[i].SenderName != "" {
			return true
		}
	}
	return false
}

// votedMsg is the outcome of a vote.
type votedMsg struct{ err error }

// openVote offers the selected message's poll's answers, the chosen ones ticked.
func (m Model) openVote() (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	if !ok || msg.Poll == nil || msg.Redacted {
		return m.say("this message asks no poll"), nil
	}
	p := msg.Poll
	items := make([]pickerItem, 0, len(p.Options)+1)
	checked := map[string]bool{}
	for _, o := range p.Options {
		items = append(items, pickerItem{label: o.Text, detail: p.Tally(o), value: o.ID})
		checked[o.ID] = o.Mine
	}
	m.aimedAt.vote = msg
	m.voters = votersState{event: msg.ID}
	if p.Multiple {
		spec := pickerSpecs[pickerVoteMulti]
		spec.title = p.Question + " — tick any, then choose"
		if p.Closed {
			spec.title = p.Question + " (closed)"
		}
		m.picker = newPickerWith(pickerVoteMulti, spec, items)
		m.picker.checked = checked
		return m, m.readVotersCmd(msg)
	}
	if p.Voted() && !p.Closed {
		items = append(items, pickerItem{label: "Take my vote back", value: voteTakeBack})
	}
	spec := pickerSpecs[pickerVote]
	spec.title = p.Question
	if p.Closed {
		spec.title += " (closed)"
	}
	m.picker = newPickerWith(pickerVote, spec, items)
	return m, m.readVotersCmd(msg)
}

// castVote sends a vote, the answers by ID; none takes it back.
func (m Model) castVote(options []string) (Model, tea.Cmd) {
	m = m.closePicker()
	msg := m.aimedAt.vote
	switch {
	case msg.ID == "":
		return m, nil
	case msg.Poll != nil && msg.Poll.Closed:
		return m.say("this poll is closed"), nil
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
