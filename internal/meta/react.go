package meta

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/methods"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Messenger keeps one reaction per person on a message: a new one replaces it and an
// empty one removes it. A reaction goes the way its message came, since a thread moved
// to encryption keeps its plain messages: a plain message's as a Messenger task, an
// encrypted one's over the encrypted connection. Yours is applied here at once; Meta
// echoes a plain one, which changes nothing, and the encrypted connection does not
// echo to this device.

// reactionID is actor's reaction on message mid in a thread.
func reactionID(self, thread int64, mid string, actor int64) domain.EventID {
	return eventID(self, thread, "reaction:"+mid+":"+itoa(actor))
}

// plainMessage reports a plain thread's message ID; an encrypted chat's are numbers.
func plainMessage(mid string) bool { return strings.HasPrefix(mid, "mid.") }

// reaction is actor's reaction (empty: none) on message mid of Meta's thread metaKey.
func (in *inbox) reaction(metaKey int64, mid string, actor int64, key string) (domain.Reaction, bool) {
	if metaKey == 0 || mid == "" || actor == 0 {
		return domain.Reaction{}, false
	}
	k := in.key(metaKey)
	return domain.Reaction{
		ID: reactionID(in.self, k, mid, actor), RoomID: roomID(in.self, k), Target: eventID(in.self, k, mid),
		Sender: personID(actor), Key: key,
	}, true
}

// takeReactions applies a table's reactions. Under c.applying.
func (a *Adapter) takeReactions(ctx context.Context, c *conn, tbl *table.LSTable) {
	for _, r := range tbl.LSUpsertReaction {
		if reaction, ok := c.inbox.reaction(r.ThreadKey, r.MessageId, r.ActorId, r.Reaction); ok {
			a.applyReaction(ctx, reaction)
		}
	}
	for _, r := range tbl.LSDeleteReaction {
		if reaction, ok := c.inbox.reaction(r.ThreadKey, r.MessageId, r.ActorId, ""); ok {
			a.applyReaction(ctx, reaction)
		}
	}
}

// applyReaction replaces the reaction with reaction's ID (removes it for an empty
// key), streaming a removal and an add.
func (a *Adapter) applyReaction(ctx context.Context, reaction domain.Reaction) {
	if a.cache != nil {
		old, had, err := a.cache.DeleteReaction(ctx, reaction.ID)
		if err != nil {
			a.log.Warn("drop an earlier reaction failed", "room", reaction.RoomID, "err", err)
		}
		if had {
			emit(a, a.reactions, domain.ReactionUpdate{Reaction: old, Removed: true})
		}
		if reaction.Key == "" {
			return
		}
		if err := a.cache.SaveReactions(ctx, []domain.Reaction{reaction}); err != nil {
			a.log.Warn("cache a reaction failed", "room", reaction.RoomID, "err", err)
		}
	}
	if reaction.Key != "" {
		emit(a, a.reactions, domain.ReactionUpdate{Reaction: reaction})
	}
}

// SendReaction reacts to a message with key, replacing your earlier reaction on it;
// reacting again with the one you have takes it back.
func (a *Adapter) SendReaction(ctx context.Context, room domain.RoomID, target domain.EventID, key string) error {
	self, thread, ok := threadOf(room)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAThread, room)
	}
	c := a.connOf(self)
	if c == nil {
		return errNotConnected
	}
	if a.cache == nil {
		return errNotCached
	}
	msg, found, err := a.cache.MessageByID(ctx, room, target)
	if err != nil {
		return fmt.Errorf("meta: read %s: %w", target, err)
	}
	if !found {
		return fmt.Errorf("%w: %s", errNotCached, target)
	}
	mid, author, err := replyParts(&msg)
	if err != nil {
		return err
	}
	mine := domain.Reaction{ID: reactionID(self, thread, mid, self), RoomID: room, Target: target, Sender: personID(self), Key: key}
	cached, err := a.cache.Reactions(ctx, room)
	if err != nil {
		return fmt.Errorf("meta: read the reactions in %s: %w", room, err)
	}
	for i := range cached {
		if cached[i].ID == mine.ID && cached[i].Key == key {
			mine.Key = "" // the same again takes it back
		}
	}
	if plainMessage(mid) {
		err = a.reactPlain(ctx, c, thread, mid, mine.Key)
	} else {
		c.applying.Lock()
		t := c.inbox.threads[thread]
		group := t != nil && !t.direct()
		c.applying.Unlock()
		err = a.reactEncrypted(ctx, c, thread, group, mid, author, mine.Key)
	}
	if err != nil {
		return err
	}
	a.applyReaction(ctx, mine)
	return nil
}

// reactPlain sends a reaction (empty: none) on a plain thread's message.
func (a *Adapter) reactPlain(ctx context.Context, c *conn, thread int64, mid, key string) error {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return errNotConnected
	}
	err := a.guard("sending a reaction", func() error {
		if err := client.WaitUntilCanSendMessages(ctx, sendWait); err != nil {
			return fmt.Errorf("%w: %w", errNotConnected, err)
		}
		_, err := client.ExecuteTasks(ctx, &socket.SendReactionTask{
			ThreadKey: thread, TimestampMs: time.Now().UnixMilli(), MessageID: mid, Reaction: key,
			ActorID: c.metaID, SyncGroup: 1, SendAttribution: table.MESSENGER_INBOX_IN_THREAD,
		})
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return fmt.Errorf("meta: react: %w", err)
	}
	return nil
}

// reactEncrypted sends a reaction (empty: none) on an encrypted chat's message by
// author.
func (a *Adapter) reactEncrypted(ctx context.Context, c *conn, thread int64, group bool, mid string, author int64, key string) error {
	c.mu.Lock()
	wa := c.e2ee
	c.mu.Unlock()
	if wa == nil {
		return errNoEncrypted
	}
	chat := types.JID{User: itoa(thread), Server: types.MessengerServer}
	if group {
		chat.Server = types.GroupServer
	}
	remote := chat.String()
	msgKey := &waCommon.MessageKey{RemoteJID: &remote, ID: &mid}
	if author == c.metaID {
		fromMe := true
		msgKey.FromMe = &fromMe
	}
	if group {
		participant := types.JID{User: itoa(author), Server: types.MessengerServer}.String()
		msgKey.Participant = &participant
	}
	at := time.Now().UnixMilli()
	content := &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
		Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: &waConsumerApplication.ConsumerApplication_Content{
			Content: &waConsumerApplication.ConsumerApplication_Content_ReactionMessage{
				ReactionMessage: &waConsumerApplication.ConsumerApplication_ReactionMessage{Key: msgKey, Text: &key, SenderTimestampMS: &at},
			},
		}},
	}}
	id := strconv.FormatInt(methods.GenerateEpochID(), 10)
	err := a.guard("sending an encrypted reaction", func() error {
		_, err := wa.SendFBMessage(ctx, chat, content, nil, whatsmeow.SendRequestExtra{ID: id})
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return fmt.Errorf("meta: react in an encrypted chat: %w", err)
	}
	return nil
}

// SendTyping says you are typing in a room, or have stopped.
func (a *Adapter) SendTyping(ctx context.Context, room domain.RoomID, typing bool, _ time.Duration) error {
	self, thread, ok := threadOf(room)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAThread, room)
	}
	c := a.connOf(self)
	if c == nil {
		return errNotConnected
	}
	c.applying.Lock()
	t := c.inbox.threads[thread]
	var kind table.ThreadType
	group := false
	if t != nil {
		kind, group = t.kind, !t.direct()
	}
	c.applying.Unlock()
	if t == nil {
		return nil // a thread not heard of yet: nobody to tell
	}
	if kind.IsWhatsApp() {
		return a.typingEncrypted(ctx, c, thread, group, typing)
	}
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return errNotConnected
	}
	err := a.guard("sending typing", func() error {
		return client.ExecuteStatelessTask(ctx, &socket.UpdatePresenceTask{
			ThreadKey: thread, IsGroupThread: flag(group), IsTyping: flag(typing), SyncGroup: 1, ThreadType: int64(kind),
		})
	})
	if err != nil {
		return fmt.Errorf("meta: typing: %w", err)
	}
	return nil
}

// typingEncrypted says you are typing in an encrypted chat, or have stopped.
func (a *Adapter) typingEncrypted(ctx context.Context, c *conn, thread int64, group, typing bool) error {
	c.mu.Lock()
	wa := c.e2ee
	c.mu.Unlock()
	if wa == nil {
		return errNoEncrypted
	}
	chat := types.JID{User: itoa(thread), Server: types.MessengerServer}
	if group {
		chat.Server = types.GroupServer
	}
	presence := types.ChatPresencePaused
	if typing {
		presence = types.ChatPresenceComposing
	}
	err := a.guard("sending typing to an encrypted chat", func() error {
		return wa.SendChatPresence(ctx, chat, presence, types.ChatPresenceMediaText)
	})
	if err != nil {
		return fmt.Errorf("meta: typing in an encrypted chat: %w", err)
	}
	return nil
}

// flag is Meta's 0 or 1 for b.
func flag(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
