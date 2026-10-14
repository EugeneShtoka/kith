package linkedin

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"go.mau.fi/mautrix-linkedin/pkg/linkedingo"
	"go.mau.fi/util/jsontime"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Talking goes to LinkedIn first and is kept here when LinkedIn has taken it: a sent
// message comes back whole (its ID and time), so it shows at once, and its echo on the
// realtime stream merges into it; an edit, a deletion and a reaction are kept as their
// realtime counterparts are.

// sendSpan bounds one request to LinkedIn.
const sendSpan = 30 * time.Second

var (
	errEmptyMessage  = errors.New("linkedin: an empty message")
	errNotAMessage   = errors.New("linkedin: not a LinkedIn message")
	errUnknownTarget = errors.New("linkedin: the message replied to is not in the cache")
)

// client is the connected client for room's account, with its thread.
func (a *Adapter) client(room domain.RoomID) (*conn, *linkedingo.Client, string, error) {
	self, thread, ok := threadOf(room)
	if !ok {
		return nil, nil, "", fmt.Errorf("%w: %s", errNotAConversation, room)
	}
	c := a.connOf(self)
	if c == nil {
		return nil, nil, "", errNotConnected
	}
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return nil, nil, "", errNotConnected
	}
	return c, client, thread, nil
}

// ask runs one request to LinkedIn under sendSpan, a panic in the library an error.
func (a *Adapter) ask(ctx context.Context, what string, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(a.withLog(ctx), sendSpan)
	defer cancel()
	return a.guard(what, func() error { return fn(ctx) })
}

// Send sends a draft: a new message (a reply when it is one) or, with Edits, an edit.
func (a *Adapter) Send(ctx context.Context, room domain.RoomID, draft domain.Draft) error {
	c, client, thread, err := a.client(room)
	if err != nil {
		return err
	}
	if strings.TrimSpace(draft.Body) == "" {
		return errEmptyMessage
	}
	body := linkedingo.SendMessageBody{Text: draft.Body, Attributes: mentionAttributes(draft.Body, draft.LiveMentions())}
	if draft.Edits != "" {
		return a.edit(ctx, c, client, room, draft.Edits, body)
	}
	var render []linkedingo.SendRenderContent
	if draft.ReplyTo != "" {
		reply, rerr := a.replyTo(ctx, room, draft.ReplyTo)
		if rerr != nil {
			return rerr
		}
		render = append(render, linkedingo.SendRenderContent{RepliedMessageContent: reply})
	}
	token := draft.TxnID
	if token == "" {
		token = newToken()
	}
	var sent *linkedingo.MessageSentResponse
	err = a.ask(ctx, "sending a message", func(ctx context.Context) (err error) {
		sent, err = client.SendMessage(ctx, conversationURN(c.profile, thread), body, render, token)
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return fmt.Errorf("linkedin: send: %w", err)
	}
	c.applying.Lock()
	defer c.applying.Unlock()
	if msg, ok := c.inbox.message(thread, &sent.Data); ok {
		a.store(ctx, c, change{live: []domain.Message{msg}})
	}
	return nil
}

// replyTo is a cached message as LinkedIn quotes it in a reply.
func (a *Adapter) replyTo(ctx context.Context, room domain.RoomID, event domain.EventID) (*linkedingo.SendRepliedMessage, error) {
	_, _, message, ok := messageOf(event)
	if !ok {
		return nil, fmt.Errorf("%w: %s", errNotAMessage, event)
	}
	if a.cache == nil {
		return nil, fmt.Errorf("%w: %s", errUnknownTarget, event)
	}
	orig, found, err := a.cache.MessageByID(ctx, room, event)
	if err != nil || !found {
		return nil, fmt.Errorf("%w: %s", errUnknownTarget, event)
	}
	return &linkedingo.SendRepliedMessage{
		OriginalSenderURN:  linkedingo.NewURN("urn:li:msg_messagingParticipant:urn:li:fsd_profile:" + domain.ParseID(orig.Sender).Native),
		OriginalSendAt:     jsontime.UnixMilli{Time: orig.Timestamp},
		OriginalMessageURN: messageURN(message),
		MessageBody:        linkedingo.AttributedText{Text: orig.Body},
	}, nil
}

// edit replaces a message's text on LinkedIn, then here.
func (a *Adapter) edit(ctx context.Context, c *conn, client *linkedingo.Client, room domain.RoomID, event domain.EventID, body linkedingo.SendMessageBody) error {
	_, _, message, ok := messageOf(event)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAMessage, event)
	}
	err := a.ask(ctx, "editing a message", func(ctx context.Context) error {
		return client.EditMessage(ctx, messageURN(message), body)
	})
	if err != nil {
		return fmt.Errorf("linkedin: edit: %w", err)
	}
	c.applying.Lock()
	defer c.applying.Unlock()
	now := time.Now()
	a.edited(ctx, c, domain.Message{
		ID: event, RoomID: room, Sender: personID(c.profile), Body: body.Text, Timestamp: now, Edited: true, EditedAt: now,
	})
	return nil
}

// Redact deletes one of your messages for everyone, as LinkedIn's own "Delete" does.
func (a *Adapter) Redact(ctx context.Context, room domain.RoomID, event domain.EventID, _ string) error {
	c, client, _, err := a.client(room)
	if err != nil {
		return err
	}
	_, _, message, ok := messageOf(event)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAMessage, event)
	}
	err = a.ask(ctx, "deleting a message", func(ctx context.Context) error {
		return client.RecallMessage(ctx, messageURN(message))
	})
	if err != nil {
		return fmt.Errorf("linkedin: delete: %w", err)
	}
	c.applying.Lock()
	defer c.applying.Unlock()
	a.unsent(ctx, c, domain.Message{ID: event, RoomID: room, Sender: personID(c.profile)}, time.Now())
	return nil
}

// SendReaction reacts to a message with key, or takes your reaction back when you
// reacted with it already.
func (a *Adapter) SendReaction(ctx context.Context, room domain.RoomID, target domain.EventID, key string) error {
	c, client, thread, err := a.client(room)
	if err != nil {
		return err
	}
	_, _, message, ok := messageOf(target)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAMessage, target)
	}
	r := domain.Reaction{
		ID: reactionID(c.profile, thread, message, c.profile, key), RoomID: room, Target: target,
		Sender: personID(c.profile), Key: key,
	}
	had := a.haveReaction(ctx, room, r.ID)
	err = a.ask(ctx, "reacting", func(ctx context.Context) error {
		if had {
			return client.RemoveReaction(ctx, messageURN(message), key)
		}
		return client.SendReaction(ctx, messageURN(message), key)
	})
	if err != nil {
		return fmt.Errorf("linkedin: react with %s: %w", key, err)
	}
	a.applyReaction(ctx, r, !had)
	return nil
}

// SendTyping says you are typing; LinkedIn has no stop, which its own timeout does.
func (a *Adapter) SendTyping(ctx context.Context, room domain.RoomID, typing bool, _ time.Duration) error {
	if !typing {
		return nil
	}
	c, client, thread, err := a.client(room)
	if err != nil {
		return err
	}
	return a.ask(ctx, "typing", func(ctx context.Context) error {
		return client.StartTyping(ctx, conversationURN(c.profile, thread))
	})
}

// haveReaction reports whether a reaction is cached.
func (a *Adapter) haveReaction(ctx context.Context, room domain.RoomID, id domain.EventID) bool {
	if a.cache == nil {
		return false
	}
	rs, err := a.cache.Reactions(ctx, room)
	if err != nil {
		return false
	}
	for i := range rs {
		if rs[i].ID == id {
			return true
		}
	}
	return false
}

// applyReaction keeps a reaction added or taken back, and streams it.
func (a *Adapter) applyReaction(ctx context.Context, r domain.Reaction, added bool) {
	if a.cache != nil {
		var err error
		if added {
			err = a.cache.SaveReactions(ctx, []domain.Reaction{r})
		} else {
			_, _, err = a.cache.DeleteReaction(ctx, r.ID)
		}
		if err != nil {
			a.log.Warn("cache a reaction failed", "room", r.RoomID, "err", err)
		}
	}
	emit(a, a.reactions, domain.ReactionUpdate{Reaction: r, Removed: !added})
}

// mentionAttributes are a draft's mentions of LinkedIn people as LinkedIn marks them:
// the mention's text in the body, by rune offsets, naming the person's profile.
func mentionAttributes(body string, mentions []domain.Mention) []linkedingo.SendMessageAttribute {
	var out []linkedingo.SendMessageAttribute
	from := 0
	for _, m := range mentions {
		id := domain.ParseID(m.UserID)
		if id.Network != domain.ProtocolLinkedIn || id.Account != "" || m.Name == "" {
			continue
		}
		at := strings.Index(body[from:], m.Name)
		if at < 0 {
			continue
		}
		at += from
		out = append(out, linkedingo.SendMessageAttribute{
			Start: utf8.RuneCountInString(body[:at]), Length: utf8.RuneCountInString(m.Name),
			AttributeKindUnion: linkedingo.AttributeKind{Entity: &linkedingo.Entity{URN: linkedingo.NewURN("urn:li:fsd_profile:" + id.Native)}},
		})
		from = at + len(m.Name)
	}
	return out
}

// newToken is a message's origin token: a random UUID, as LinkedIn's web app sends.
func newToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // never fails (crypto/rand)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
