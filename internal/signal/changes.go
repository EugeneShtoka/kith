package signal

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/readstate"
)

// typingLapse is how long someone shows as typing without saying so again: Signal's
// apps send it every few seconds while typing and forget it after 15 s.
const typingLapse = 15 * time.Second

// takeEdit replaces a message's text, keeping the version it replaced under
// [display.deleted] keep. An edit names the version it replaces: a second edit names the
// first edit's time, which leads back to the message.
func (a *Adapter) takeEdit(ctx context.Context, self string, e *signalv1.Edit) error {
	at := sentTime(e.GetSent())
	room := roomID(self, e.GetChat(), e.GetGroup())
	target := eventID(self, e.GetChat(), e.GetGroup(), e.GetSender(), e.GetTarget())
	if edited, ok, err := a.cache.EditShownBy(ctx, room, target); err != nil {
		return fmt.Errorf("find the message an edit is of: %w", err)
	} else if ok {
		target = edited
	}
	msg := domain.Message{
		ID:         target,
		RoomID:     room,
		Sender:     person(e.GetSender()),
		Timestamp:  at,
		Edited:     true,
		EditedAt:   at,
		RevisionID: eventID(self, e.GetChat(), e.GetGroup(), e.GetSender(), e.GetSent()),
	}
	msg.Body, msg.Format, msg.Mentioned = rendered(self, e.GetBody(), e.GetMentions(), e.GetStyles())
	if err := a.save(ctx, msg.RoomID, msg); err != nil {
		return fmt.Errorf("cache an edit: %w", err)
	}
	if a.onCached != nil {
		a.onCached(msg)
	}
	emit(a, a.messages, msg)
	a.recount(ctx, msg.RoomID)
	return nil
}

// takeDelete deletes a message for everyone, its text kept only under [display.deleted]
// keep. A deletion before the message arrived is kept as a tombstone.
func (a *Adapter) takeDelete(ctx context.Context, self string, d *signalv1.Delete) error {
	room := roomID(self, d.GetChat(), d.GetGroup())
	id := eventID(self, d.GetChat(), d.GetGroup(), d.GetSender(), d.GetTarget())
	at, by := sentTime(d.GetSent()), person(d.GetSender())
	if err := a.cache.MarkRedacted(ctx, room, id, by, "", at, a.keepsDeleted()); err != nil {
		return fmt.Errorf("mark a message deleted: %w", err)
	}
	stub := domain.Message{ID: id, RoomID: room, Redacted: true, RedactedBy: by, RedactedAt: at}
	if a.keepsDeleted() {
		if kept, ok, err := a.cache.MessageByID(ctx, room, id); err == nil && ok {
			stub.Body, stub.Format = kept.Body, kept.Format
		}
	}
	if a.onChanged != nil {
		a.onChanged(room)
	}
	emit(a, a.messages, stub)
	a.recount(ctx, room)
	return nil
}

// takeReaction puts a sender's reaction on a message, replacing the one they had, or
// takes it off. Signal names the message by its latest version's time, an edit's if it
// was edited, which leads back to the message.
func (a *Adapter) takeReaction(ctx context.Context, self string, r *signalv1.Reaction) error {
	room := roomID(self, r.GetChat(), r.GetGroup())
	target := eventID(self, r.GetChat(), r.GetGroup(), r.GetTargetAuthor(), r.GetTargetSent())
	if edited, ok, err := a.cache.EditShownBy(ctx, room, target); err != nil {
		return fmt.Errorf("find the message a reaction is on: %w", err)
	} else if ok {
		target = edited
	}
	id := reactionID(target, r.GetSender())
	old, had, err := a.cache.DeleteReaction(ctx, id)
	if err != nil {
		return fmt.Errorf("take a reaction off: %w", err)
	}
	if had {
		emit(a, a.reactions, domain.ReactionUpdate{Reaction: old, Removed: true})
	}
	if r.GetRemove() || r.GetEmoji() == "" {
		return nil
	}
	reaction := domain.Reaction{ID: id, RoomID: room, Target: target, Sender: person(r.GetSender()), Key: r.GetEmoji()}
	if err := a.cache.SaveReactions(ctx, []domain.Reaction{reaction}); err != nil {
		return fmt.Errorf("cache a reaction: %w", err)
	}
	emit(a, a.reactions, domain.ReactionUpdate{Reaction: reaction})
	return nil
}

// takeTyping shows who types in a room until they stop, or typingLapse passes.
func (a *Adapter) takeTyping(self string, t *signalv1.Typing) {
	room := roomID(self, t.GetChat(), t.GetGroup())
	who := person(t.GetSender())
	a.typingMu.Lock()
	defer a.typingMu.Unlock()
	typers := a.typing[room]
	if typers == nil {
		typers = map[string]*time.Timer{}
		a.typing[room] = typers
	}
	if timer := typers[who]; timer != nil {
		timer.Stop()
		delete(typers, who)
	}
	if t.GetStarted() {
		typers[who] = time.AfterFunc(typingLapse, func() {
			a.typingMu.Lock()
			defer a.typingMu.Unlock()
			if a.typing[room][who] != nil {
				delete(a.typing[room], who)
				a.sayTyping(room)
			}
		})
	}
	a.sayTyping(room)
}

// sayTyping emits who types in room now. Caller holds typingMu.
func (a *Adapter) sayTyping(room domain.RoomID) {
	who := make([]string, 0, len(a.typing[room]))
	for id := range a.typing[room] {
		who = append(who, id)
	}
	slices.Sort(who)
	emit(a, a.activity, domain.Activity{RoomID: room, Typing: who})
}

// takeRead reads each chat up to the messages read on another of the account's devices.
// Signal names them by author and time alone: the cache finds their chats.
func (a *Adapter) takeRead(ctx context.Context, self string, r *signalv1.Read) error {
	tails := make([]string, 0, len(r.GetMessages()))
	for _, m := range r.GetMessages() {
		tails = append(tails, m.GetAuthor()+"."+strconv.FormatUint(m.GetSent(), 10))
	}
	msgs, err := a.cache.MessagesEndingIn(ctx, owner(self), tails)
	if err != nil {
		return fmt.Errorf("find the messages read: %w", err)
	}
	for i := range msgs {
		at, err := readstate.TimeOf(ctx, a.cache, msgs[i].RoomID, msgs[i].ID, time.Now())
		if err != nil {
			return fmt.Errorf("signal: %w", err)
		}
		if err := a.cache.SaveUnread(ctx, domain.Unread{RoomID: msgs[i].RoomID, ReadEvent: msgs[i].ID}, at.UnixMilli()); err != nil {
			return fmt.Errorf("save the read position: %w", err)
		}
		a.recount(ctx, msgs[i].RoomID)
	}
	return nil
}

// save is the cache's save, keeping replaced versions under [display.deleted] keep.
func (a *Adapter) save(ctx context.Context, room domain.RoomID, msgs ...domain.Message) error {
	if a.keepsDeleted() {
		return a.cache.SaveMessagesWithRevisions(ctx, room, msgs) //nolint:wrapcheck // the caller says what failed
	}
	return a.cache.SaveMessages(ctx, room, msgs) //nolint:wrapcheck // the caller says what failed
}

func sentTime(ms uint64) time.Time {
	return time.UnixMilli(int64(ms)) //nolint:gosec // a Signal timestamp fits
}

// reactionID is sender's reaction on the message target: one per sender and message,
// whichever version of it the reaction names.
func reactionID(target domain.EventID, sender string) domain.EventID {
	return domain.EventID(string(target) + reactionMark + sender)
}

// reactionMark joins a reaction's message and sender in its ID.
const reactionMark = ":reaction:"
