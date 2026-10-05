package telegram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Telegram tells a message's reactions whole — how many of each, and who reacted
// where it may say (a private chat, a small group) — and kith keeps one reaction per
// person per emoji, as every network does. A reaction counted with no one named
// (a channel's) is kept as one of no one. Each change replaces the message's
// reactions, so the cache drops those the new set lacks.

// errCustomEmoji is a custom emoji sent: kith has no way to name one.
var errCustomEmoji = errors.New("telegram: a custom emoji cannot be sent from kith")

// customEmoji is the key a custom emoji reacts as: it has no Unicode to show.
const customEmoji = ":custom_emoji:"

// reactionKey is the emoji a Telegram reaction is; false for one kith does not show
// (a paid star).
func reactionKey(r tg.ReactionClass) (string, bool) {
	switch r := r.(type) {
	case *tg.ReactionEmoji:
		return r.Emoticon, r.Emoticon != ""
	case *tg.ReactionCustomEmoji:
		return customEmoji, true
	}
	return "", false
}

// reactionID is one person's reaction of key on a message; anonymous ones are
// numbered instead.
func reactionID(target domain.EventID, who, key string) domain.EventID {
	return domain.EventID(string(target) + "/reaction/" + who + "/" + key)
}

// messageReactions are the reactions a message carries, as kith keeps them: one per
// person named, the account's own among them, and the rest of each count as no one.
func messageReactions(self, chat int64, msgID int, rs tg.MessageReactions) []domain.Reaction {
	room, target := roomID(self, chat), messageID(self, chat, msgID)
	var out []domain.Reaction
	named := map[string]int{}
	add := func(who int64, key string) {
		id := reactionID(target, strconv.FormatInt(who, 10), key)
		if slices.ContainsFunc(out, func(r domain.Reaction) bool { return r.ID == id }) {
			return
		}
		out = append(out, domain.Reaction{ID: id, RoomID: room, Target: target, Sender: personID(who), Key: key})
		named[key]++
	}
	for _, r := range rs.RecentReactions {
		key, ok := reactionKey(r.Reaction)
		who, known := markedPeer(r.PeerID)
		if ok && known {
			add(who, key)
		}
	}
	for _, c := range rs.Results {
		key, ok := reactionKey(c.Reaction)
		if !ok {
			continue
		}
		if _, mine := c.GetChosenOrder(); mine {
			add(self, key)
		}
		for n := named[key]; n < c.Count; n++ {
			out = append(out, domain.Reaction{ID: reactionID(target, "~"+strconv.Itoa(n), key), RoomID: room, Target: target, Key: key})
		}
	}
	return out
}

// reactionsChanged replaces a message's reactions with rs, heard live, and streams
// what changed. The message is noted, so a history page read before the change does
// not bring the old ones back (pageReactions).
func (a *Adapter) reactionsChanged(ctx context.Context, room domain.RoomID, target domain.EventID, rs []domain.Reaction) {
	a.mu.Lock()
	a.reacted[target] = time.Now()
	a.mu.Unlock()
	a.replaceReactions(ctx, room, target, rs, true)
}

// replaceReactions makes rs a message's reactions in the cache, streaming each change
// when live.
func (a *Adapter) replaceReactions(ctx context.Context, room domain.RoomID, target domain.EventID, rs []domain.Reaction, live bool) {
	var had []domain.Reaction
	if a.cache != nil {
		cached, err := a.cache.Reactions(ctx, room)
		if err != nil {
			a.log.Warn("read reactions failed", "room", room, "err", err)
			return
		}
		had = slices.DeleteFunc(cached, func(r domain.Reaction) bool { return r.Target != target })
	}
	for _, r := range had {
		if slices.ContainsFunc(rs, func(n domain.Reaction) bool { return n.ID == r.ID }) {
			continue
		}
		if a.cache != nil {
			if _, _, err := a.cache.DeleteReaction(ctx, r.ID); err != nil {
				a.log.Warn("drop a reaction failed", "room", room, "err", err)
			}
		}
		if live {
			emit(a, a.reactions, domain.ReactionUpdate{Reaction: r, Removed: true})
		}
	}
	added := slices.DeleteFunc(slices.Clone(rs), func(r domain.Reaction) bool {
		return slices.ContainsFunc(had, func(h domain.Reaction) bool { return h.ID == r.ID })
	})
	if a.cache != nil && len(added) > 0 {
		if err := a.cache.SaveReactions(ctx, added); err != nil {
			a.log.Warn("cache reactions failed", "room", room, "err", err)
		}
	}
	if live {
		for _, r := range added {
			emit(a, a.reactions, domain.ReactionUpdate{Reaction: r})
		}
	}
}

// pageReactions caches the reactions of messages a history page read then carried,
// but not over a change heard live since: that is newer.
func (a *Adapter) pageReactions(ctx context.Context, self int64, raw []tg.MessageClass, read time.Time) {
	for _, m := range raw {
		msg, ok := m.(*tg.Message)
		if !ok {
			continue
		}
		chat, ok := markedPeer(msg.PeerID)
		if !ok {
			continue
		}
		target := messageID(self, chat, msg.ID)
		a.mu.Lock()
		changed := a.reacted[target]
		a.mu.Unlock()
		if !changed.IsZero() && !changed.Before(read) {
			continue
		}
		// None said is none: a page carries every reaction its messages have.
		a.replaceReactions(ctx, roomID(self, chat), target, messageReactions(self, chat, msg.ID, msg.Reactions), false)
	}
}

// SendReaction puts our reaction of key on target, or takes it back when we have it
// there already. Our other reactions stay (Premium keeps several); an account that
// may keep only one has it replaced.
func (a *Adapter) SendReaction(ctx context.Context, roomID domain.RoomID, target domain.EventID, key string) error {
	ch, err := a.chatOf(ctx, roomID)
	if err != nil {
		return err
	}
	id, ok := messageNumber(roomID, target)
	if !ok {
		return fmt.Errorf("telegram: %s is no message of %s", target, roomID)
	}
	if key == customEmoji {
		return errCustomEmoji
	}
	mine := a.myReactions(ctx, roomID, target, personID(ch.conn.user))
	keys := slices.DeleteFunc(slices.Clone(mine), func(k string) bool { return k == key })
	if len(keys) == len(mine) {
		keys = append(keys, key)
	}
	res, err := a.react(ctx, ch, id, keys)
	if tgerr.Is(err, "REACTIONS_TOO_MANY") && len(keys) > 1 {
		res, err = a.react(ctx, ch, id, []string{key})
	}
	if err != nil {
		return fmt.Errorf("telegram: react in %s: %w", roomID, err)
	}
	if ch.conn.live != nil {
		_ = ch.conn.live.manager.Handle(ctx, res)
	}
	for _, u := range updatesIn(res) {
		if r, ok := u.(*tg.UpdateMessageReactions); ok && r.MsgID == id && samePeer(r.Peer, ch.id) {
			a.reactionsChanged(ctx, roomID, target, messageReactions(ch.conn.user, ch.id, id, r.Reactions))
		}
	}
	return nil
}

// react sets our reactions on message id to keys.
func (a *Adapter) react(ctx context.Context, ch chat, id int, keys []string) (tg.UpdatesClass, error) {
	req := &tg.MessagesSendReactionRequest{Peer: ch.peer, MsgID: id}
	for _, k := range keys {
		req.Reaction = append(req.Reaction, &tg.ReactionEmoji{Emoticon: k})
	}
	req.SetReaction(req.Reaction)
	res, err := ch.conn.client.API().MessagesSendReaction(ctx, req)
	if err != nil {
		return nil, err //nolint:wrapcheck // SendReaction wraps it, naming the room
	}
	return res, nil
}

// myReactions is the keys of me's reactions on target, as the cache holds them.
func (a *Adapter) myReactions(ctx context.Context, room domain.RoomID, target domain.EventID, me string) []string {
	if a.cache == nil {
		return nil
	}
	rs, err := a.cache.Reactions(ctx, room)
	if err != nil {
		return nil
	}
	var keys []string
	for _, r := range rs {
		if r.Target == target && r.Sender == me && r.Key != customEmoji {
			keys = append(keys, r.Key)
		}
	}
	return keys
}

// updatesIn are an answer's updates.
func updatesIn(res tg.UpdatesClass) []tg.UpdateClass {
	switch r := res.(type) {
	case *tg.Updates:
		return r.Updates
	case *tg.UpdatesCombined:
		return r.Updates
	case *tg.UpdateShort:
		return []tg.UpdateClass{r.Update}
	}
	return nil
}
