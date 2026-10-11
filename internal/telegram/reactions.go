package telegram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Telegram sends a message's reactions whole: counts per emoji, plus reactors where
// it names them (DMs, small groups). kith keeps one row per person per emoji; unnamed
// counts (channels) become anonymous rows. Each update replaces the whole set.

// errCustomEmoji: kith cannot send custom emoji.
var errCustomEmoji = errors.New("telegram: a custom emoji cannot be sent from kith")

// customEmoji is the key for any custom emoji, which has no Unicode form.
const customEmoji = ":custom_emoji:"

// reactionKey is a reaction's emoji; false for ones kith doesn't show (paid stars).
func reactionKey(r tg.ReactionClass) (string, bool) {
	switch r := r.(type) {
	case *tg.ReactionEmoji:
		return r.Emoticon, r.Emoticon != ""
	case *tg.ReactionCustomEmoji:
		return customEmoji, true
	}
	return "", false
}

// reactionID identifies one person's key on a message; anonymous ones are numbered.
func reactionID(target domain.EventID, who, key string) domain.EventID {
	return domain.EventID(string(target) + "/reaction/" + who + "/" + key)
}

// messageReactions expands a message's reactions: one per named person (including
// the account), the rest of each count anonymous.
func messageReactions(room domain.RoomID, msgID int, rs tg.MessageReactions) []domain.Reaction {
	target := inRoom(room, msgID)
	self, _ := strconv.ParseInt(domain.ParseID(string(room)).Account, 10, 64) // whose room it is
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

// reactionsChanged applies a live reaction set and notes the time so an older history
// page can't restore the old set (pageReactions).
func (a *Adapter) reactionsChanged(ctx context.Context, room domain.RoomID, target domain.EventID, rs []domain.Reaction) {
	a.mu.Lock()
	a.reacted[target] = time.Now()
	a.mu.Unlock()
	a.replaceReactions(ctx, room, target, rs, true)
}

// replaceReactions writes rs as the message's reactions, streaming changes when live.
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

// pageReactions caches a history page's reactions, except where a live change since
// fetched is newer.
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
		room := chatRoom(self, chat, topicOf(msg))
		target := inRoom(room, msg.ID)
		a.mu.Lock()
		changed := a.reacted[target]
		a.mu.Unlock()
		if !changed.IsZero() && !changed.Before(read) {
			continue
		}
		// A page carries all reactions, so none listed means none.
		a.replaceReactions(ctx, room, target, messageReactions(room, msg.ID, msg.Reactions), false)
	}
}

// SendReaction toggles our key on target, keeping our other reactions (Premium allows
// several; otherwise Telegram replaces it).
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
			rs := messageReactions(roomID, id, r.Reactions)
			a.sentReactions(target, rs)
			a.reactionsChanged(ctx, roomID, target, rs)
		}
	}
	return nil
}

// heardReactions applies an update's reactions unless it echoes our own send (ownEcho).
func (a *Adapter) heardReactions(ctx context.Context, room domain.RoomID, target domain.EventID, rs []domain.Reaction) {
	if a.ownEcho(target, rs) {
		return // applied when it was sent
	}
	a.reactionsChanged(ctx, room, target, rs)
}

// echoFor is how long a sent reaction's echo is awaited.
const echoFor = time.Minute

// sentReactions remembers reactions applied from a send's answer. The same updates
// arrive again, possibly after a later send's, and must not undo it.
func (a *Adapter) sentReactions(target domain.EventID, rs []domain.Reaction) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, at := range a.echoes {
		if now.Sub(at) > echoFor {
			delete(a.echoes, k)
		}
	}
	a.echoes[reactionState(target, rs)] = now
}

// ownEcho reports and consumes a matching sentReactions entry; a second hearing is real.
func (a *Adapter) ownEcho(target domain.EventID, rs []domain.Reaction) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	k := reactionState(target, rs)
	at, ok := a.echoes[k]
	delete(a.echoes, k)
	return ok && time.Since(at) <= echoFor
}

// reactionState is a key for a message's full reaction set.
func reactionState(target domain.EventID, rs []domain.Reaction) string {
	ids := make([]string, len(rs))
	for i := range rs {
		ids[i] = string(rs[i].ID)
	}
	slices.Sort(ids)
	return string(target) + "\x00" + strings.Join(ids, "\x00")
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

// myReactions is me's cached reaction keys on target.
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
