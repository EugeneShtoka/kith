package signal

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A Signal poll's answers are named by their place (kith's IDs "0", "1", …). A vote is
// its own message, numbered among its voter's votes in that poll, and the highest
// number stands: an older vote arriving late, or a poll's copy arriving again, never
// undoes a newer one. The poll keeps each voter's answers and their latest number.

var errNoPoll = errors.New("signal: that message asks no poll kith holds")

// pollOf is a wire poll as kith keeps it, tallied for self.
func pollOf(self string, p *signalv1.Poll) *domain.Poll {
	poll := &domain.Poll{Question: p.GetQuestion(), Multiple: p.GetMultiple(), Closed: p.GetClosed()}
	for i, o := range p.GetOptions() {
		poll.Options = append(poll.Options, domain.PollOption{ID: strconv.Itoa(i), Text: o})
	}
	for _, b := range p.GetBallots() {
		cast(poll, person(b.GetVoter()), answers(b.GetOptions()), b.GetRound())
	}
	tally(self, poll)
	return poll
}

// answers is a vote's answers as kith names them.
func answers(indexes []uint32) []string {
	out := make([]string, 0, len(indexes))
	for _, i := range indexes {
		out = append(out, strconv.FormatUint(uint64(i), 10))
	}
	return out
}

// cast records voter's vote numbered round, unless a vote numbered as high is held;
// no answers takes the vote back. It reports whether the poll changed.
func cast(poll *domain.Poll, voter string, chosen []string, round uint32) bool {
	if held, ok := poll.Rounds[voter]; ok && round <= held {
		return false
	}
	ballots, rounds := maps.Clone(poll.Ballots), maps.Clone(poll.Rounds)
	if ballots == nil {
		ballots = map[string][]string{}
	}
	if rounds == nil {
		rounds = map[string]uint32{}
	}
	rounds[voter] = round
	if len(chosen) == 0 {
		delete(ballots, voter)
	} else {
		ballots[voter] = chosen
	}
	poll.Ballots, poll.Rounds = ballots, rounds
	return true
}

// tally recounts poll's votes, the account self's among them.
func tally(self string, poll *domain.Poll) {
	me := person(self)
	poll.Retally(func(voter string) bool { return voter == me })
}

// folded is a poll's new copy with the votes the held one has that are newer, so a
// copy arriving again (redelivered, or from the phone's history) keeps what is known.
func folded(self string, held, fresh *domain.Poll) *domain.Poll {
	out := *fresh
	out.Closed = held.Closed || fresh.Closed
	for _, voter := range slices.Sorted(maps.Keys(held.Rounds)) {
		cast(&out, voter, held.Ballots[voter], held.Rounds[voter])
	}
	tally(self, &out)
	return &out
}

// withHeldVotes folds into each poll in msgs the votes kith already holds for it.
// The caller holds pollsMu.
func (a *Adapter) withHeldVotes(ctx context.Context, room domain.RoomID, msgs []domain.Message) error {
	self, _ := selfOf(room)
	for i := range msgs {
		if msgs[i].Poll == nil {
			continue
		}
		held, ok, err := a.cache.MessageByID(ctx, room, msgs[i].ID)
		if err != nil {
			return fmt.Errorf("read a poll's votes: %w", err)
		}
		if ok && held.Poll != nil {
			msgs[i].Poll = folded(self, held.Poll, msgs[i].Poll)
		}
	}
	return nil
}

// changePoll applies change to a held poll and keeps it when it changed; a poll kith
// does not hold is left (its votes come with it from the phone's history, or not).
func (a *Adapter) changePoll(ctx context.Context, room domain.RoomID, target domain.EventID, change func(*domain.Poll) bool) error {
	a.pollsMu.Lock()
	defer a.pollsMu.Unlock()
	msg, ok, err := a.cache.MessageByID(ctx, room, target)
	if err != nil {
		return fmt.Errorf("read a poll: %w", err)
	}
	if !ok || msg.Poll == nil {
		return errNoPoll
	}
	poll := *msg.Poll
	if !change(&poll) {
		return nil
	}
	if err := a.cache.SetPoll(ctx, room, target, poll); err != nil {
		return fmt.Errorf("keep a poll's votes: %w", err)
	}
	if a.onChanged != nil {
		a.onChanged(room)
	}
	return nil
}

// takeVote counts someone's vote in a poll.
func (a *Adapter) takeVote(ctx context.Context, self string, v *signalv1.Vote) error {
	room := roomID(self, v.GetChat(), v.GetGroup())
	target := eventID(self, v.GetChat(), v.GetGroup(), v.GetTargetAuthor(), v.GetTargetSent())
	err := a.changePoll(ctx, room, target, func(poll *domain.Poll) bool {
		if !cast(poll, person(v.GetSender()), answers(v.GetOptions()), v.GetRound()) {
			return false
		}
		tally(self, poll)
		return true
	})
	if errors.Is(err, errNoPoll) {
		return nil
	}
	return err
}

// takePollEnd closes a poll: its author's own, as the ID it names is theirs.
func (a *Adapter) takePollEnd(ctx context.Context, self string, e *signalv1.PollEnd) error {
	room := roomID(self, e.GetChat(), e.GetGroup())
	target := eventID(self, e.GetChat(), e.GetGroup(), e.GetSender(), e.GetTargetSent())
	err := a.changePoll(ctx, room, target, func(poll *domain.Poll) bool {
		closed := poll.Closed
		poll.Closed = true
		return !closed
	})
	if errors.Is(err, errNoPoll) {
		return nil
	}
	return err
}

// VotePoll votes by answer ID ("0", "1", …); no answers takes the vote back. Signal does
// not echo it to this device, so it is counted here.
func (a *Adapter) VotePoll(ctx context.Context, room domain.RoomID, target domain.EventID, options []string) error {
	self, chat, group, err := a.chatTo(room)
	if err != nil {
		return err
	}
	author, sent, ok := authorAndSent(room, target)
	if !ok {
		return fmt.Errorf("signal: %s is not a Signal message", target)
	}
	h, err := a.sender(self)
	if err != nil {
		return err
	}
	msg, held, err := a.cache.MessageByID(ctx, room, target)
	if err != nil {
		return fmt.Errorf("signal: read the poll of %s: %w", target, err)
	}
	if !held || msg.Poll == nil {
		return errNoPoll
	}
	poll := msg.Poll
	if poll.Closed {
		return fmt.Errorf("%w: the poll has ended", api.ErrNotOnNetwork)
	}
	if len(options) > 1 && !poll.Multiple {
		return fmt.Errorf("%w: the poll takes one answer", api.ErrNotOnNetwork)
	}
	indexes := make([]uint32, 0, len(options))
	for _, id := range options {
		i, err := strconv.ParseUint(id, 10, 32)
		if err != nil || int(i) >= len(poll.Options) {
			return fmt.Errorf("signal: the poll has no answer %q", id)
		}
		indexes = append(indexes, uint32(i))
	}
	vote := &signalv1.Vote{
		Chat: chat, Group: group, Sender: self, TargetAuthor: author, TargetSent: sent,
		Options: indexes, Round: poll.Rounds[person(self)] + 1,
	}
	req := &signalv1.SendRequest{Account: self, Chat: chat, Group: group, Sent: a.sentAt(""), Vote: vote}
	if _, err := h.client.Send(ctx, connect.NewRequest(req)); err != nil {
		return fmt.Errorf("signal: vote in %s: %w", room, err)
	}
	return a.takeVote(ctx, self, vote)
}

// PollVoters is who chose what, from the votes kith holds: Signal's polls name their
// voters.
func (a *Adapter) PollVoters(ctx context.Context, room domain.RoomID, target domain.EventID) (domain.PollVoters, error) {
	msg, held, err := a.cache.MessageByID(ctx, room, target)
	if err != nil {
		return domain.PollVoters{}, fmt.Errorf("signal: read the poll of %s: %w", target, err)
	}
	if !held || msg.Poll == nil {
		return domain.PollVoters{}, errNoPoll
	}
	out := domain.PollVoters{}
	for _, voter := range slices.Sorted(maps.Keys(msg.Poll.Ballots)) {
		out.Voters = append(out.Voters, domain.PollVoter{ID: voter, Options: msg.Poll.Ballots[voter]})
	}
	return out, nil
}
