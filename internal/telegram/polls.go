package telegram

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A Telegram poll is a message's media: its question and answers, and its results
// (votes per answer, which this account chose). Results change as people vote, and
// Telegram says so in an update naming the poll's message; a vote is sent by naming
// the answers' options. An answer's option is bytes: kith names it in base64.

// optionID is an answer's option as kith names it.
func optionID(option []byte) string { return base64.RawURLEncoding.EncodeToString(option) }

// pollOf is a Telegram poll and its results as kith keeps them.
func pollOf(p *tg.Poll, r *tg.PollResults) *domain.Poll {
	out := &domain.Poll{
		ID: strconv.FormatInt(p.ID, 10), Question: p.Question.Text,
		Multiple: p.MultipleChoice, Closed: p.Closed, Quiz: p.Quiz,
	}
	for _, a := range p.Answers {
		if answer, ok := a.(*tg.PollAnswer); ok {
			out.Options = append(out.Options, domain.PollOption{ID: optionID(answer.Option), Text: answer.Text.Text})
		}
	}
	withResults(out, r, nil)
	return out
}

// withResults applies results to a poll. Results that are min say nothing of this
// account's choice: what before said of it is kept.
func withResults(p *domain.Poll, r *tg.PollResults, before *domain.Poll) {
	if r == nil {
		return
	}
	p.Voters = r.TotalVoters
	chose := map[string]bool{}
	if before != nil {
		for _, o := range before.Options {
			chose[o.ID] = o.Mine
		}
	}
	for i := range p.Options {
		p.Options[i].Votes = 0
		if r.Min {
			p.Options[i].Mine = chose[p.Options[i].ID]
		}
	}
	for _, v := range r.Results {
		id := optionID(v.Option)
		for i := range p.Options {
			if p.Options[i].ID != id {
				continue
			}
			p.Options[i].Votes = v.Voters
			if !r.Min {
				p.Options[i].Mine = v.Chosen
			}
		}
	}
}

// pollUpdated keeps a poll's results as an update says, and tells the clients its
// room changed.
func (a *Adapter) pollUpdated(ctx context.Context, self int64, u *tg.UpdateMessagePoll) {
	if a.cache == nil {
		return
	}
	var targets []domain.Message
	if chat, ok := markedPeer(u.Peer); ok && u.MsgID != 0 {
		room := chatRoom(self, chat, topicNumber(u.TopMsgID))
		if msg, held, err := a.cache.MessageByID(ctx, room, inRoom(room, u.MsgID)); err == nil && held && msg.Poll != nil {
			targets = append(targets, msg)
		}
	}
	if len(targets) == 0 { // an older update, naming the poll alone
		found, err := a.cache.PollsByID(ctx, domain.AccountRooms(domain.ProtocolTelegram, strconv.FormatInt(self, 10)), strconv.FormatInt(u.PollID, 10))
		if err != nil {
			a.log.Warn("find a poll's message failed", "err", err)
			return
		}
		targets = found
	}
	for i := range targets {
		msg := &targets[i]
		poll := *msg.Poll
		poll.Options = append([]domain.PollOption(nil), msg.Poll.Options...)
		if p, ok := u.GetPoll(); ok {
			poll = *pollOf(&p, nil)
		}
		withResults(&poll, &u.Results, msg.Poll)
		if err := a.cache.SetPoll(ctx, msg.RoomID, msg.ID, poll); err != nil {
			a.log.Warn("keep a poll's results failed", "room", msg.RoomID, "err", err)
			continue
		}
		if a.onChanged != nil {
			a.onChanged(msg.RoomID)
		}
	}
}

// VotePoll votes in a message's poll on Telegram: the answers by their options; none
// takes the vote back. The results arrive as an update.
func (a *Adapter) VotePoll(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, options []string) error {
	ch, err := a.chatOf(ctx, roomID)
	if err != nil {
		return err
	}
	id, ok := messageNumber(roomID, eventID)
	if !ok {
		return fmt.Errorf("telegram: %s is no message of %s", eventID, roomID)
	}
	chosen := make([][]byte, 0, len(options))
	for _, o := range options {
		option, derr := base64.RawURLEncoding.DecodeString(o)
		if derr != nil {
			return fmt.Errorf("telegram: %q is no answer of the poll", o)
		}
		chosen = append(chosen, option)
	}
	res, err := ch.conn.client.API().MessagesSendVote(ctx, &tg.MessagesSendVoteRequest{Peer: ch.peer, MsgID: id, Options: chosen})
	if err != nil {
		return fmt.Errorf("telegram: vote in %s: %w", roomID, err)
	}
	if ch.conn.live != nil {
		_ = ch.conn.live.manager.Handle(ctx, res) // the results come back as an update
	}
	return nil
}

// Who chose what is asked of Telegram page by page, and only of a poll made public:
// an anonymous one names no one, and a public one may name its voters only to those
// who have voted. At most pollVotersPages pages are read; the counts still tell the
// rest.
const (
	pollVotersPage  = 50
	pollVotersPages = 20
)

// PollVoters is who chose each answer of a message's poll, as Telegram names them.
func (a *Adapter) PollVoters(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.PollVoters, error) {
	ch, err := a.chatOf(ctx, roomID)
	if err != nil {
		return domain.PollVoters{}, err
	}
	id, ok := messageNumber(roomID, eventID)
	if !ok {
		return domain.PollVoters{}, fmt.Errorf("telegram: %s is no message of %s", eventID, roomID)
	}
	raw, _, err := a.fetchRaw(ctx, ch, id)
	if err != nil {
		return domain.PollVoters{}, fmt.Errorf("telegram: read the poll of %s: %w", eventID, err)
	}
	var media *tg.MessageMediaPoll
	if msg, isMsg := raw.(*tg.Message); isMsg {
		media, _ = msg.Media.(*tg.MessageMediaPoll)
	}
	if media == nil {
		return domain.PollVoters{}, fmt.Errorf("telegram: %s asks no poll", eventID)
	}
	if !media.Poll.PublicVoters {
		return domain.PollVoters{Hidden: domain.PollAnonymous}, nil
	}
	return readVotes(ctx, ch, id)
}

// readVotes asks who chose what in message id's public poll, page by page.
func readVotes(ctx context.Context, ch chat, id int) (domain.PollVoters, error) {
	ballots := map[string][]string{}
	names := map[string]string{}
	var order []string
	offset := ""
	for range pollVotersPages {
		req := &tg.MessagesGetPollVotesRequest{Peer: ch.peer, ID: id, Limit: pollVotersPage}
		if offset != "" {
			req.SetOffset(offset)
		}
		list, err := ch.conn.client.API().MessagesGetPollVotes(ctx, req)
		switch {
		case tgerr.Is(err, "POLL_VOTE_REQUIRED"):
			return domain.PollVoters{Hidden: domain.PollVoteFirst}, nil
		case tgerr.Is(err, "BROADCAST_FORBIDDEN"):
			return domain.PollVoters{Hidden: domain.PollAnonymous}, nil
		case err != nil:
			return domain.PollVoters{}, fmt.Errorf("telegram: read who voted in %s: %w", ch.room(), err)
		}
		ent := peer.EntitiesFromResult(list)
		for _, v := range list.Votes {
			voter, name, options, ok := peerVote(v, ent)
			if !ok {
				continue
			}
			if _, seen := ballots[voter]; !seen {
				order = append(order, voter)
			}
			ballots[voter] = append(ballots[voter], options...)
			names[voter] = name
		}
		offset = list.NextOffset
		if offset == "" {
			break
		}
	}
	out := domain.PollVoters{Voters: make([]domain.PollVoter, 0, len(order))}
	for _, voter := range order {
		out.Voters = append(out.Voters, domain.PollVoter{ID: voter, Name: names[voter], Options: ballots[voter]})
	}
	return out, nil
}

// peerVote is one vote as kith names it: the voter's person ID and name, and the
// answers chosen. A vote with an answer of the voter's own words names none.
func peerVote(v tg.MessagePeerVoteClass, ent peer.Entities) (voter, name string, options []string, ok bool) {
	var from tg.PeerClass
	switch v := v.(type) {
	case *tg.MessagePeerVote:
		from, options = v.Peer, []string{optionID(v.Option)}
	case *tg.MessagePeerVoteMultiple:
		from = v.Peer
		for _, o := range v.Options {
			options = append(options, optionID(o))
		}
	default:
		return "", "", nil, false
	}
	marked, ok := markedPeer(from)
	if !ok {
		return "", "", nil, false
	}
	switch from := from.(type) {
	case *tg.PeerUser:
		if u, found := ent.User(from.UserID); found {
			name = personName(u)
		}
	case *tg.PeerChannel:
		if c, found := ent.Channel(from.ChannelID); found {
			name = c.Title
		}
	case *tg.PeerChat:
		if c, found := ent.Chat(from.ChatID); found {
			name = c.Title
		}
	}
	return personID(marked), name, options, true
}
