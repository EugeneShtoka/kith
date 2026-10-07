package whatsapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A WhatsApp poll is a message asking it; each vote is a message of its own, encrypted
// to the poll, naming the answers chosen by their SHA-256 and replacing the voter's
// last. So the poll keeps every voter's ballot and counts the votes from them. An
// answer's ID is its words, which a vote is built from.

// pollOf is a poll creation message as kith keeps the poll; nil for any other.
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

// chosenOptions is a vote's chosen hashes as the poll's answers.
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

// onPollVote counts a vote heard: its voter's ballot replaced by what they chose now.
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

// cast keeps one voter's ballot in a cached poll, chosen as hashes or as answers, and
// tells the clients the room changed.
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
	for v, chosen := range poll.Ballots {
		ballots[v] = chosen
	}
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

// VotePoll votes in a poll on WhatsApp, the answers by their words; none takes the
// vote back. WhatsApp does not echo a vote to the device that sent it: it is counted
// here.
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
