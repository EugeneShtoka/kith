package main

import (
	"cmp"
	"context"
	"image"
	"slices"
	"strconv"
	"strings"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A message's poll as the desktop draws and works it (Poll.tsx): the question, each
// answer with its count and share, this person's own marked. Open, an answer is chosen
// by pressing it, or several ticked and voted; a vote of one answer may be taken back.
// Who voted lists who chose each answer, as far as the network names them.
// [keys.timeline] vote puts the keys on its buttons: [keys.nav] up and down move, open
// or [keys.picker] accept presses, back or close leaves. New results arrive as the
// message's poll changing: nothing here counts votes.

// The buttons after a poll's answers.
const (
	pollVote = -1 // vote the answers ticked, in a poll that takes several
	pollBack = -2 // take a vote of one answer back
	pollWho  = -3 // show who chose what, or stop
)

// pollState is a drawn poll's widgets, the answers ticked in one that takes several
// once the person ticks one (nil until then: their own), and who voted while shown.
type pollState struct {
	btns   []widget.Clickable
	ticks  map[string]bool
	voters *votersRead
}

// votersRead is who chose what in a poll, as read when shown: not yet, the voters, or
// the error.
type votersRead struct {
	read   bool
	voters domain.PollVoters
	err    error
}

// pollCursor is the keys on a poll's buttons: the message, the button.
type pollCursor struct {
	event domain.EventID
	at    int
}

// pollButtons are a poll's buttons in order: open, each answer by index, then Vote where
// several may be ticked, or Take my vote back where one answer was chosen; then Who voted.
func pollButtons(p *domain.Poll) []int {
	if p.Closed {
		return []int{pollWho}
	}
	btns := make([]int, len(p.Options), len(p.Options)+2)
	for i := range btns {
		btns[i] = i
	}
	switch {
	case p.Multiple:
		btns = append(btns, pollVote)
	case p.Voted():
		btns = append(btns, pollBack)
	}
	return append(btns, pollWho)
}

// ticked is whether answer id is ticked: as the person ticked it, else their own.
func (s *pollState) ticked(p *domain.Poll, id string) bool {
	if s.ticks != nil {
		return s.ticks[id]
	}
	i := slices.IndexFunc(p.Options, func(o domain.PollOption) bool { return o.ID == id })
	return i >= 0 && p.Options[i].Mine
}

// choice is the answers ticked, as a vote names them, in the poll's order.
func (s *pollState) choice(p *domain.Poll) []string {
	var ids []string
	for _, o := range p.Options {
		if s.ticked(p, o.ID) {
			ids = append(ids, o.ID)
		}
	}
	return ids
}

// press is what pressing button b does: the vote to send, if any (none ticked takes it
// back), or a tick changed.
func (s *pollState) press(p *domain.Poll, b int) (options []string, send bool) {
	switch {
	case b == pollBack:
		return nil, true
	case b == pollVote:
		options = s.choice(p)
		if len(options) == 0 && !p.Voted() {
			return nil, false
		}
		s.ticks = nil
		return options, true
	case p.Multiple:
		id := p.Options[b].ID
		ticks := map[string]bool{}
		for _, o := range p.Options {
			ticks[o.ID] = s.ticked(p, o.ID)
		}
		ticks[id] = !ticks[id]
		s.ticks = ticks
		return nil, false
	}
	return []string{p.Options[b].ID}, true
}

// pressPoll presses button b of m's poll, sending the vote it makes, or showing who
// voted (read now) or no longer.
func (u *ui) pressPoll(ctx context.Context, room domain.RoomID, m domain.Message, st *msgState, b int) {
	if b == pollWho {
		if st.poll.voters != nil {
			st.poll.voters = nil
			return
		}
		v := new(votersRead)
		st.poll.voters = v
		go func() {
			voters, err := u.backend.PollVoters(ctx, room, m.ID)
			u.mu.Lock()
			v.read, v.voters, v.err = true, voters, err
			u.mu.Unlock()
			u.w.Invalidate()
		}()
		return
	}
	options, send := st.poll.press(m.Poll, b)
	if !send {
		return
	}
	go func() {
		if err := u.backend.VotePoll(ctx, room, m.ID, options); err != nil {
			u.say("Not voted: " + err.Error())
		}
	}()
}

// choosePoll is a [keys] action while the keys are on a poll's buttons; false for one it
// does not take, or once the poll is gone from the timeline.
func (u *ui) choosePoll(ctx context.Context, path string) bool {
	t, v := &u.timeline, u.voting
	i := slices.IndexFunc(t.msgs, func(m domain.Message) bool { return m.ID == v.event })
	if i < 0 || t.msgs[i].Poll == nil || t.msgs[i].Redacted {
		return false
	}
	btns := pollButtons(t.msgs[i].Poll)
	switch path {
	case "nav.up":
		v.at = max(0, v.at-1)
	case "nav.down":
		v.at = min(len(btns)-1, v.at+1)
	case "nav.open", "picker.accept":
		u.pressPoll(ctx, t.room, t.msgs[i], t.stateOf(v.event), btns[min(v.at, len(btns)-1)])
	case "nav.back", "picker.close":
		u.voting = nil
	default:
		return false
	}
	return true
}

// layoutPoll draws m's poll, at most 440dp wide; open, its answers and buttons press.
func (u *ui) layoutPoll(ctx context.Context, gtx C, t *timeline, st *msgState, m domain.Message) D {
	p, s := m.Poll, &st.poll
	btns := pollButtons(p)
	if len(s.btns) != len(btns) {
		s.btns = make([]widget.Clickable, len(btns))
	}
	for k := range s.btns {
		if s.btns[k].Clicked(gtx) {
			u.pressPoll(ctx, t.room, m, st, btns[k])
		}
	}
	at := -1
	if u.voting != nil && u.voting.event == m.ID {
		at = min(u.voting.at, len(btns)-1)
	}
	gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(440))
	rows := []layout.FlexChild{layout.Rigid(func(gtx C) D {
		note := ""
		switch {
		case p.Closed:
			note = " (closed)"
		case p.Multiple:
			note = " (choose any)"
		}
		l := material.Body1(u.th, "📊 "+p.Question+note)
		l.Font.Weight = font.Bold
		return layout.Inset{Top: unit.Dp(2), Bottom: unit.Dp(4)}.Layout(gtx, l.Layout)
	})}
	for i, o := range p.Options {
		face := func(gtx C) D { return u.layoutAnswer(gtx, p, o, s.ticked(p, o.ID)) }
		rows = append(rows, layout.Rigid(func(gtx C) D {
			if p.Closed {
				return face(gtx)
			}
			return u.pollButton(gtx, &s.btns[i], at == i, face)
		}))
	}
	rows = append(rows, layout.Rigid(func(gtx C) D { return u.layoutPollActions(gtx, p, s, btns, at) }))
	if s.voters != nil {
		for _, line := range u.voterLines(p, s.voters) {
			rows = append(rows, layout.Rigid(func(gtx C) D {
				l := material.Body2(u.th, line)
				l.Color = u.pal.muted
				return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, l.Layout)
			}))
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
}

// layoutPollActions is the row of a poll's buttons after its answers, the one at under
// the keys outlined.
func (u *ui) layoutPollActions(gtx C, p *domain.Poll, s *pollState, btns []int, at int) D {
	first := slices.IndexFunc(btns, func(b int) bool { return b < 0 })
	actions := make([]layout.FlexChild, 0, len(btns)-first)
	for k := first; k < len(btns); k++ {
		label, enabled := s.label(p, btns[k])
		actions = append(actions, layout.Rigid(func(gtx C) D {
			return layout.Inset{Right: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
				dims := u.button(gtx, &s.btns[k], label, enabled, btns[k] == pollWho && s.voters != nil)
				if at == k {
					u.outline(gtx, image.Rectangle{Max: dims.Size})
				}
				return dims
			})
		}))
	}
	return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, func(gtx C) D { return layout.Flex{}.Layout(gtx, actions...) })
}

// label is what button b after the answers says, and whether it presses: Vote with
// nothing ticked takes a vote back, and with none to take back does nothing.
func (s *pollState) label(p *domain.Poll, b int) (string, bool) {
	switch b {
	case pollBack:
		return "Take my vote back", true
	case pollVote:
		ticked := len(s.choice(p)) > 0
		if !ticked && p.Voted() {
			return "Take my vote back", true
		}
		return "Vote", ticked
	}
	return "Who voted", true
}

// pollButton is an answer's row as a button, outlined under the keys.
func (u *ui) pollButton(gtx C, btn *widget.Clickable, cursor bool, face layout.Widget) D {
	return btn.Layout(gtx, func(gtx C) D {
		dims := face(gtx)
		if cursor {
			u.outline(gtx, image.Rectangle{Max: dims.Size})
		}
		return dims
	})
}

// layoutAnswer is one answer: its mark (● chosen, or ☑ ticked where several may be),
// its text and tally, and a bar of its share; this person's own on a raised ground.
func (u *ui) layoutAnswer(gtx C, p *domain.Poll, o domain.PollOption, ticked bool) D {
	mark := "○ "
	switch {
	case p.Multiple && !p.Closed && ticked:
		mark = "☑ "
	case p.Multiple && !p.Closed:
		mark = "☐ "
	case o.Mine:
		mark = "● "
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	rec := op.Record(gtx.Ops)
	dims := layout.UniformInset(unit.Dp(6)).Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
					layout.Flexed(1, material.Body2(u.th, mark+o.Text).Layout),
					layout.Rigid(func(gtx C) D {
						l := material.Caption(u.th, "  "+p.Tally(o))
						l.Color = u.pal.muted
						return l.Layout(gtx)
					}),
				)
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
					w, h := gtx.Constraints.Max.X, gtx.Dp(4)
					paint.FillShape(gtx.Ops, u.pal.border, clip.UniformRRect(image.Rect(0, 0, w, h), h/2).Op(gtx.Ops))
					paint.FillShape(gtx.Ops, u.pal.accent, clip.UniformRRect(image.Rect(0, 0, w*p.Share(o)/100, h), h/2).Op(gtx.Ops))
					return D{Size: image.Pt(w, h)}
				})
			}),
		)
	})
	call := rec.Stop()
	if o.Mine {
		paint.FillShape(gtx.Ops, u.pal.raised, clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(6)).Op(gtx.Ops))
	}
	call.Add(gtx.Ops)
	return dims
}

// voterLines lists who chose each answer, "you" first, an answer no one chose left out,
// with those the network counts but does not name as "n more"; or why no one is named
// (Poll.tsx voterLines).
func (u *ui) voterLines(p *domain.Poll, v *votersRead) []string {
	switch {
	case !v.read:
		return []string{"Reading who voted…"}
	case v.err != nil:
		return []string{"Could not read who voted: " + v.err.Error()}
	case v.voters.Hidden == domain.PollAnonymous:
		return []string{"An anonymous poll: no one who voted is named"}
	case v.voters.Hidden == domain.PollVoteFirst:
		return []string{"Who voted is shown once you have voted"}
	}
	var lines []string
	for _, o := range p.Options {
		var who []string
		for _, voter := range v.voters.Voters {
			if slices.Contains(voter.Options, o.ID) {
				who = append(who, u.voterName(voter))
			}
		}
		if len(who) == 0 && o.Votes == 0 {
			continue
		}
		slices.SortFunc(who, func(a, b string) int {
			switch {
			case a == b:
				return 0
			case a == "you":
				return -1
			case b == "you":
				return 1
			}
			return strings.Compare(a, b)
		})
		if rest := o.Votes - len(who); rest > 0 {
			who = append(who, strconv.Itoa(rest)+" more")
		}
		lines = append(lines, o.Text+": "+strings.Join(who, ", "))
	}
	if len(lines) == 0 {
		return []string{"No one has voted yet"}
	}
	return lines
}

// voterName names a voter: "you", else as kithd names them, else as the network does.
func (u *ui) voterName(v domain.PollVoter) string {
	if u.selves[v.ID] {
		return "you"
	}
	return cmp.Or(v.KnownAs, v.Name, v.ID)
}
