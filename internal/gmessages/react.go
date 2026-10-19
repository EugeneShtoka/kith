package gmessages

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Reactions come whole: the phone sends a message again with every reaction it has, one
// per person, whenever one changes. kith keeps a message's reactions as the newest copy
// says, so a listing asked for before a change it already knows of is not taken. A
// reaction sent from kith is kept at once; the phone's copy of the message follows.

// errReactionRefused: the phone answered that it did not take a reaction.
var errReactionRefused = errors.New("gmessages: the phone did not take the reaction")

// customReaction stands for a reaction drawn as a picture of Google's own.
const customReaction = ":custom:"

// reactionKey is the emoji a reaction shows, as mautrix-gmessages reads it; "" for one
// it cannot show.
func reactionKey(data *gmproto.ReactionData) string {
	switch data.GetType() {
	case gmproto.EmojiType_EMOTIFY:
		return customReaction
	case gmproto.EmojiType_CUSTOM:
		return data.GetUnicode()
	default:
		return data.GetType().Unicode()
	}
}

// reactions is every reaction on m, as kith keeps them.
func (in *inbox) reactions(m *gmproto.Message) []domain.Reaction {
	conversation := m.GetConversationID()
	room, target := roomID(in.self, conversation), eventID(in.self, conversation, m.GetMessageID())
	cv := in.convs[conversation]
	var out []domain.Reaction
	for _, entry := range m.GetReactions() {
		key := reactionKey(entry.GetData())
		if key == "" {
			continue
		}
		for _, pid := range entry.GetParticipantIDs() {
			out = append(out, domain.Reaction{
				ID: reactionID(in.self, conversation, m.GetMessageID(), pid), RoomID: room, Target: target,
				Sender: in.participant(cv, pid), Key: key,
			})
		}
	}
	return out
}

// participant is a conversation's participant as a kith ID.
func (in *inbox) participant(cv *conv, pid string) string {
	if cv != nil {
		if person := cv.members[pid]; person != "" {
			return person
		}
	}
	return domain.NativePerson(domain.ProtocolGMessages, in.self+"."+pid)
}

// takeReactions keeps the reactions msgs carry, as the phone had them at at; a message
// whose reactions changed later is left as it is. Caller holds c.applying.
func (a *Adapter) takeReactions(ctx context.Context, c *conn, msgs []*gmproto.Message, live bool, at time.Time) {
	if a.cache == nil || len(msgs) == 0 {
		return
	}
	for target, changed := range c.reacted {
		if time.Since(changed) > syncSpan {
			delete(c.reacted, target) // no listing taken now was asked for before it
		}
	}
	cached := map[domain.RoomID][]domain.Reaction{}
	for _, m := range msgs {
		room, target := roomID(c.self, m.GetConversationID()), eventID(c.self, m.GetConversationID(), m.GetMessageID())
		if changed := c.reacted[target]; changed.After(at) {
			continue
		}
		have, ok := cached[room]
		if !ok {
			var err error
			if have, err = a.cache.Reactions(ctx, room); err != nil {
				a.log.Warn("read a conversation's reactions failed", "account", c.account.Name, "err", err)
				continue
			}
			cached[room] = have
		}
		if live {
			c.reacted[target] = at
		}
		cached[room] = a.replaceReactions(ctx, c, target, have, c.inbox.reactions(m), live)
	}
}

// replaceReactions makes target's cached reactions want, announcing what changed when
// live, and returns the room's reactions after.
func (a *Adapter) replaceReactions(ctx context.Context, c *conn, target domain.EventID, have, want []domain.Reaction, live bool) []domain.Reaction {
	kept := have[:0:0]
	for _, r := range have {
		if r.Target != target {
			kept = append(kept, r)
			continue
		}
		if slices.Contains(want, r) {
			continue // still there; kept below
		}
		if _, _, err := a.cache.DeleteReaction(ctx, r.ID); err != nil {
			a.log.Warn("take a reaction off failed", "account", c.account.Name, "err", err)
			kept = append(kept, r)
			continue
		}
		if live {
			emit(a, a.reactions, domain.ReactionUpdate{Reaction: r, Removed: true})
		}
	}
	var added []domain.Reaction
	for _, r := range want {
		if !slices.Contains(have, r) {
			added = append(added, r)
		}
	}
	if err := a.cache.SaveReactions(ctx, added); err != nil {
		a.log.Warn("cache reactions failed", "account", c.account.Name, "err", err)
		added = nil
	}
	for _, r := range added {
		if live {
			emit(a, a.reactions, domain.ReactionUpdate{Reaction: r})
		}
	}
	for _, r := range want {
		if slices.Contains(have, r) || slices.Contains(added, r) {
			kept = append(kept, r)
		}
	}
	return kept
}

// SendReaction puts key on a message as the account's reaction, replacing the one it
// had (one each); the same key again takes it off. It is kept at once.
func (a *Adapter) SendReaction(ctx context.Context, room domain.RoomID, target domain.EventID, key string) error {
	self, conversation, ok := conversationOf(room)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAConversation, room)
	}
	message, ok := messageOf(target)
	if !ok {
		return fmt.Errorf("gmessages: %s is not a Google Messages message", target)
	}
	c, client, err := a.connected(self)
	if err != nil {
		return err
	}
	via, ok := c.route(conversation)
	if !ok {
		return errNotListed
	}
	mine := reactionID(self, conversation, message, via.outgoing)
	had, err := a.reactionOf(ctx, room, mine)
	if err != nil {
		return err
	}
	action, data := gmproto.SendReactionRequest_ADD, gmproto.MakeReactionData(key)
	switch {
	case had == key:
		action = gmproto.SendReactionRequest_REMOVE
	case had != "":
		action = gmproto.SendReactionRequest_SWITCH
	}
	var resp *gmproto.SendReactionResponse
	err = a.guard("sending a reaction", func() (err error) {
		resp, err = client.SendReaction(ctx, &gmproto.SendReactionRequest{
			MessageID: message, ReactionData: data, Action: action,
			SIMPayload: via.sim,
		})
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return fmt.Errorf("gmessages: react: %w", err)
	}
	if !resp.GetSuccess() {
		return errReactionRefused
	}
	a.keepOwnReaction(ctx, c, conversation, message, via.outgoing, key, action)
	return nil
}

// reactionOf is the key of a cached reaction, "" when there is none.
func (a *Adapter) reactionOf(ctx context.Context, room domain.RoomID, id domain.EventID) (string, error) {
	if a.cache == nil {
		return "", nil
	}
	rs, err := a.cache.Reactions(ctx, room)
	if err != nil {
		return "", fmt.Errorf("gmessages: read the reactions of %s: %w", room, err)
	}
	for _, r := range rs {
		if r.ID == id {
			return r.Key, nil
		}
	}
	return "", nil
}

// keepOwnReaction keeps the account's reaction (as outgoing) to a message as sent,
// before the phone's copy comes.
func (a *Adapter) keepOwnReaction(ctx context.Context, c *conn, conversation, message, outgoing, key string, action gmproto.SendReactionRequest_Action) {
	if a.cache == nil {
		return
	}
	room, target := roomID(c.self, conversation), eventID(c.self, conversation, message)
	mine := reactionID(c.self, conversation, message, outgoing)
	c.applying.Lock()
	defer c.applying.Unlock()
	c.reacted[target] = time.Now()
	have, err := a.cache.Reactions(ctx, room)
	if err != nil {
		a.log.Warn("read a conversation's reactions failed", "account", c.account.Name, "err", err)
		return
	}
	var want []domain.Reaction
	for _, r := range have {
		if r.Target == target && r.ID != mine {
			want = append(want, r)
		}
	}
	if action != gmproto.SendReactionRequest_REMOVE {
		sender := c.inbox.participant(c.inbox.convs[conversation], outgoing)
		want = append(want, domain.Reaction{ID: mine, RoomID: room, Target: target, Sender: sender, Key: key})
	}
	a.replaceReactions(ctx, c, target, have, want, true)
}
