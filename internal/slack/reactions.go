package slack

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/emoji"
)

// Slack names a reaction ("thumbsup", "+1::skin-tone-2"); kith keys one by its emoji,
// as every network does. A name with no emoji kith knows (a workspace's own) keys as
// ":name:". Each person may put several different reactions on a message, so a
// reaction is the message, the person and the name.

// skinTones are Slack's tone suffixes and the modifiers they are.
var skinTones = map[string]string{
	"skin-tone-2": "\U0001F3FB", "skin-tone-3": "\U0001F3FC", "skin-tone-4": "\U0001F3FD",
	"skin-tone-5": "\U0001F3FE", "skin-tone-6": "\U0001F3FF",
}

// presentation is U+FE0F, which a toned emoji drops before its modifier.
const presentation = "️"

// reactionKey is the emoji a Slack reaction name stands for, toned as it says; ":name:"
// for one kith has no emoji for.
func reactionKey(name string) string {
	base, tone, _ := strings.Cut(name, "::")
	e, ok := emoji.ByName(base)
	if !ok {
		return ":" + name + ":"
	}
	if mod, toned := skinTones[tone]; toned {
		e = strings.TrimSuffix(e, presentation) + mod
	}
	return e
}

// namesByEmoji is each curated emoji's Slack name — the shortest of its names, which
// is Slack's own where they differ ("+1" before "thumbsup") — then Unicode's names
// for the rest.
var namesByEmoji = func() map[string]string {
	out := map[string]string{}
	for _, set := range []map[string]string{emoji.Curated, emoji.Standard} {
		names := make([]string, 0, len(set))
		for name := range set {
			names = append(names, name)
		}
		slices.SortFunc(names, func(a, b string) int {
			if len(a) != len(b) {
				return len(a) - len(b)
			}
			return strings.Compare(a, b)
		})
		for _, name := range names {
			e := strings.TrimSuffix(set[name], presentation)
			if _, taken := out[e]; !taken {
				out[e] = name
			}
		}
	}
	return out
}()

// reactionName is the Slack name for a reaction key: ":name:" as written, a toned emoji
// as name::skin-tone-n; false for an emoji kith has no name for.
func reactionName(key string) (string, bool) {
	if len(key) > 2 && strings.HasPrefix(key, ":") && strings.HasSuffix(key, ":") {
		return key[1 : len(key)-1], true
	}
	for tone, mod := range skinTones {
		if base, toned := strings.CutSuffix(key, mod); toned {
			if name, ok := namesByEmoji[strings.TrimSuffix(base, presentation)]; ok {
				return name + "::" + tone, true
			}
		}
	}
	name, ok := namesByEmoji[strings.TrimSuffix(key, presentation)]
	return name, ok
}

// reactionID is one person's reaction of one name on a message.
func reactionID(team, channel, ts, user, name string) domain.EventID {
	return domain.EventID(domain.NativeID(domain.ProtocolSlack, team, channel+"/"+ts+"/reaction/"+user+"/"+name))
}

// messageReactions are the reactions a message carries, as history gives them.
func messageReactions(team, channel string, m *slackgo.Msg) []domain.Reaction {
	var out []domain.Reaction
	for _, r := range m.Reactions {
		for _, user := range r.Users {
			out = append(out, domain.Reaction{
				ID:     reactionID(team, channel, m.Timestamp, user, r.Name),
				RoomID: roomID(team, channel), Target: messageID(team, channel, m.Timestamp),
				Sender: personID(team, user), Key: reactionKey(r.Name),
			})
		}
	}
	return out
}

// onReaction applies a reaction added or taken back live.
func (a *Adapter) onReaction(ctx context.Context, w *workspace, e slackgo.ReactionEvent, added bool) {
	if e.Item.Type != "message" || e.Item.Channel == "" || e.Item.Timestamp == "" {
		return
	}
	team, channel, ts := w.creds.Team, e.Item.Channel, e.Item.Timestamp
	r := domain.Reaction{
		ID:     reactionID(team, channel, ts, e.User, e.Reaction),
		RoomID: roomID(team, channel), Target: messageID(team, channel, ts),
		Sender: personID(team, e.User), Key: reactionKey(e.Reaction),
	}
	a.applyReaction(ctx, r, added)
}

// applyReaction caches a reaction added or drops one taken back, and streams it. A
// message whose reactions changed live is noted, so a history page read before the
// change does not bring the old ones back (cachePage).
func (a *Adapter) applyReaction(ctx context.Context, r domain.Reaction, added bool) {
	a.mu.Lock()
	a.reacted[r.Target] = time.Now()
	a.mu.Unlock()
	if !added {
		if a.cache != nil {
			if _, _, err := a.cache.DeleteReaction(ctx, r.ID); err != nil {
				a.log.Warn("drop a reaction failed", "room", r.RoomID, "err", err)
			}
		}
		emit(a, a.reactions, domain.ReactionUpdate{Reaction: r, Removed: true})
		return
	}
	if a.cache != nil {
		if err := a.cache.SaveReactions(ctx, []domain.Reaction{r}); err != nil {
			a.log.Warn("cache a reaction failed", "room", r.RoomID, "err", err)
		}
	}
	emit(a, a.reactions, domain.ReactionUpdate{Reaction: r})
}

// reactedSince reports whether target's reactions changed live after at.
func (a *Adapter) reactedSince(target domain.EventID, at time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.reacted[target].Before(at)
}

// SendReaction puts our reaction of key on target, or takes it back when we have it
// there already: Slack keeps one of each name per person, and refuses a second.
func (a *Adapter) SendReaction(ctx context.Context, roomID domain.RoomID, target domain.EventID, key string) error {
	w, channel, err := a.conversation(roomID)
	if err != nil {
		return err
	}
	in, ts, ok := cutLast(domain.ParseID(string(target)).Native)
	if !ok || in != channel {
		return fmt.Errorf("slack: %s is not a message of %s", target, roomID)
	}
	name, ok := reactionName(key)
	if !ok {
		return fmt.Errorf("slack: %s has no name Slack knows it by", key)
	}
	r := domain.Reaction{
		ID: reactionID(w.creds.Team, channel, ts, w.creds.User, name), RoomID: roomID, Target: target,
		Sender: personID(w.creds.Team, w.creds.User), Key: reactionKey(name),
	}
	ref := slackgo.NewRefToMessage(channel, ts)
	if a.haveReaction(ctx, roomID, r.ID) {
		if err := w.client.RemoveReactionContext(ctx, name, ref); err != nil {
			return fmt.Errorf("slack: take back %s: %w", key, err)
		}
		a.applyReaction(ctx, r, false)
		return nil
	}
	if err := w.client.AddReactionContext(ctx, name, ref); err != nil {
		return fmt.Errorf("slack: react with %s: %w", key, err)
	}
	a.applyReaction(ctx, r, true)
	return nil
}

// haveReaction reports whether the cache holds the reaction with id in room.
func (a *Adapter) haveReaction(ctx context.Context, room domain.RoomID, id domain.EventID) bool {
	if a.cache == nil {
		return false
	}
	rs, err := a.cache.Reactions(ctx, room)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(rs, func(r domain.Reaction) bool { return r.ID == id })
}
