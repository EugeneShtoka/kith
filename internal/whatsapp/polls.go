package whatsapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"maps"
	"slices"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A WhatsApp vote is its own message, encrypted to the poll, naming chosen answers by
// SHA-256 and replacing the voter's last. The poll keeps each voter's ballot; an
// answer's ID is its text.

// pollOf converts a poll creation message; nil for any other.
func pollOf(msg *waE2E.Message) *domain.Poll {
	var created *waE2E.PollCreationMessage
	switch {
	case msg.GetPollCreationMessage() != nil:
		created = msg.GetPollCreationMessage()
	case msg.GetPollCreationMessageV2() != nil:
		created = msg.GetPollCreationMessageV2()
	case msg.GetPollCreationMessageV3() != nil:
		created = msg.GetPollCreationMessageV3()
	default:
		return nil
	}
	p := &domain.Poll{Question: created.GetName(), Multiple: created.GetSelectableOptionsCount() != 1}
	for _, o := range created.GetOptions() {
		p.Options = append(p.Options, domain.PollOption{ID: o.GetOptionName(), Text: o.GetOptionName()})
	}
	return p
}

// chosenOptions maps a vote's hashes to the poll's answers.
func chosenOptions(p *domain.Poll, hashes [][]byte) []string {
	var out []string
	for _, o := range p.Options {
		sum := sha256.Sum256([]byte(o.ID))
		for _, h := range hashes {
			if bytes.Equal(h, sum[:]) {
				out = append(out, o.ID)
				break
			}
		}
	}
	return out
}

// onPollVote replaces the voter's ballot.
func (a *Adapter) onPollVote(ctx context.Context, account Account, client *whatsmeow.Client, e *events.Message, update *waE2E.PollUpdateMessage) {
	if a.cache == nil {
		return
	}
	vote, err := client.DecryptPollVote(ctx, e)
	if err != nil {
		a.log.Warn("read a poll vote failed", "account", account.Name, "err", err)
		return
	}
	room, voter, target := a.change(ctx, account, client, e, update.GetPollCreationMessageKey().GetID())
	a.cast(ctx, client, room, target, voter, vote.GetSelectedOptions(), nil)
}

// cast stores a voter's ballot (as hashes or answers) in a cached poll and reports
// the room changed.
func (a *Adapter) cast(ctx context.Context, client *whatsmeow.Client, room domain.RoomID, target domain.EventID, voter string, hashes [][]byte, answers []string) {
	msg, held, err := a.cache.MessageByID(ctx, room, target)
	if err != nil || !held || msg.Poll == nil {
		return // a poll not cached: nothing to count on
	}
	poll := *msg.Poll
	if hashes != nil {
		answers = chosenOptions(&poll, hashes)
	}
	ballots := make(map[string][]string, len(poll.Ballots)+1)
	maps.Copy(ballots, poll.Ballots)
	if len(answers) == 0 {
		delete(ballots, voter)
	} else {
		ballots[voter] = answers
	}
	poll.Ballots = ballots
	own := selfOf(client)
	poll.Retally(func(v string) bool {
		jid, err := types.ParseJID(domain.ParseID(v).Native)
		return err == nil && own.is(jid)
	})
	if err := a.cache.SetPoll(ctx, room, target, poll); err != nil {
		a.log.Warn("keep a poll's votes failed", "room", room, "err", err)
		return
	}
	if a.onChanged != nil {
		a.onChanged(room)
	}
}

// VotePoll votes by answer text; no options retracts. WhatsApp does not echo the
// vote, so it is counted here.
func (a *Adapter) VotePoll(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, options []string) error {
	chat, id, sender, _, client, err := a.target(ctx, roomID, eventID)
	if err != nil {
		return err
	}
	own := selfOf(client)
	info := &types.MessageInfo{
		MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsFromMe: own.is(sender), IsGroup: chat.Server == types.GroupServer},
		ID:            id,
	}
	vote, err := client.BuildPollVote(ctx, info, options)
	if err != nil {
		return fmt.Errorf("whatsapp: vote in %s: %w", roomID, err)
	}
	if _, err := client.SendMessage(ctx, chat, vote); err != nil {
		return fmt.Errorf("whatsapp: vote in %s: %w", roomID, err)
	}
	if a.cache != nil {
		a.cast(ctx, client, roomID, eventID, domain.NativePerson(domain.ProtocolWhatsApp, own.pn.String()), nil, options)
	}
	return nil
}

// PollVoters returns who chose each answer, from the kept ballots.
func (a *Adapter) PollVoters(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.PollVoters, error) {
	if a.cache == nil {
		return domain.PollVoters{}, nil
	}
	msg, held, err := a.cache.MessageByID(ctx, roomID, eventID)
	switch {
	case err != nil:
		return domain.PollVoters{}, fmt.Errorf("whatsapp: read the poll of %s: %w", eventID, err)
	case !held || msg.Poll == nil:
		return domain.PollVoters{}, fmt.Errorf("whatsapp: %s asks no poll kith holds", eventID)
	}
	out := domain.PollVoters{}
	// A retracted vote deletes its ballot, so every ballot is non-empty.
	for _, voter := range slices.Sorted(maps.Keys(msg.Poll.Ballots)) {
		out.Voters = append(out.Voters, domain.PollVoter{ID: voter, Options: msg.Poll.Ballots[voter]})
	}
	return out, nil
}
