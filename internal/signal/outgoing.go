package signal

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// errNotYours: Signal edits and deletes for everyone only one's own messages.
var errNotYours = errors.New("signal: only your own messages can be edited or deleted for everyone")

// authorAndSent is the author and sent time a message's ID names (…/<author>.<sent>).
func authorAndSent(room domain.RoomID, id domain.EventID) (string, uint64, bool) {
	part, ok := strings.CutPrefix(string(id), string(room)+"/")
	if !ok {
		return "", 0, false
	}
	author, at, ok := strings.Cut(part, ".")
	sent, err := strconv.ParseUint(at, 10, 64)
	return author, sent, ok && err == nil && author != ""
}

// version is the message's author and the sent time of its latest version: its shown
// edit's, else its own. An edit and a reaction name that version.
func (a *Adapter) version(ctx context.Context, room domain.RoomID, id domain.EventID) (string, uint64, error) {
	author, sent, ok := authorAndSent(room, id)
	if !ok {
		return "", 0, fmt.Errorf("signal: %s is not a Signal message", id)
	}
	msg, cached, err := a.cache.MessageByID(ctx, room, id)
	if err != nil {
		return "", 0, fmt.Errorf("signal: read %s: %w", id, err)
	}
	if cached && msg.Edited && msg.RevisionID != "" {
		if _, edited, ok := authorAndSent(room, msg.RevisionID); ok {
			sent = edited
		}
	}
	return author, sent, nil
}

// editOwn sends a new text for one of the account's messages, then caches it: an edit
// sent from this device does not come back to it.
func (a *Adapter) editOwn(ctx context.Context, room domain.RoomID, self, chat string, group bool, h *helper, draft domain.Draft) error {
	author, of, err := a.version(ctx, room, draft.Edits)
	if err != nil {
		return err
	}
	if author != self {
		return errNotYours
	}
	sent := a.sentAt(draft.TxnID)
	body, mentions := mentionsOf(draft)
	req := &signalv1.SendRequest{Account: self, Chat: chat, Group: group, Body: body, Sent: sent, EditOf: of, Mentions: mentions}
	if _, err := h.client.Send(ctx, connect.NewRequest(req)); err != nil {
		return fmt.Errorf("signal: edit in %s: %w", room, err)
	}
	a.forgetTxn(draft.TxnID)
	return a.takeEdit(ctx, self, &signalv1.Edit{Chat: chat, Group: group, Sender: self, Target: of, Sent: sent, Body: body, Mentions: mentions})
}

// SendReaction puts the account's reaction key on a message (naming its latest version,
// as Signal's apps do), replacing the one it had, then caches it.
func (a *Adapter) SendReaction(ctx context.Context, room domain.RoomID, target domain.EventID, key string) error {
	self, chat, group, err := a.chatTo(room)
	if err != nil {
		return err
	}
	h, err := a.sender(self)
	if err != nil {
		return err
	}
	author, version, err := a.version(ctx, room, target)
	if err != nil {
		return err
	}
	r := &signalv1.Reaction{Chat: chat, Group: group, Sender: self, Emoji: key, TargetAuthor: author, TargetSent: version}
	return a.sendReaction(ctx, h, self, r)
}

func (a *Adapter) sendReaction(ctx context.Context, h *helper, self string, r *signalv1.Reaction) error {
	req := &signalv1.SendRequest{Account: self, Chat: r.GetChat(), Group: r.GetGroup(), Sent: a.sentAt(""), Reaction: r}
	if _, err := h.client.Send(ctx, connect.NewRequest(req)); err != nil {
		return fmt.Errorf("signal: react: %w", err)
	}
	return a.takeReaction(ctx, self, r)
}

// Redact takes the account's reaction off, or deletes one of its messages for
// everyone, then caches that.
func (a *Adapter) Redact(ctx context.Context, room domain.RoomID, event domain.EventID, _ string) error {
	self, chat, group, err := a.chatTo(room)
	if err != nil {
		return err
	}
	h, err := a.sender(self)
	if err != nil {
		return err
	}
	if target, sender, ok := strings.Cut(string(event), reactionMark); ok {
		if sender != self {
			return errNotYours
		}
		return a.unreact(ctx, h, room, self, chat, group, domain.EventID(target), event)
	}
	author, sent, ok := authorAndSent(room, event)
	if !ok {
		return fmt.Errorf("signal: %s is not a Signal message", event)
	}
	if author != self {
		return errNotYours
	}
	now := a.sentAt("")
	req := &signalv1.SendRequest{Account: self, Chat: chat, Group: group, Sent: now, DeleteOf: sent}
	if _, err := h.client.Send(ctx, connect.NewRequest(req)); err != nil {
		return fmt.Errorf("signal: delete in %s: %w", room, err)
	}
	return a.takeDelete(ctx, self, &signalv1.Delete{Chat: chat, Group: group, Sender: self, Target: sent, Sent: now})
}

// unreact takes the account's reaction (its ID reaction) off target: Signal names the
// emoji taken off, and the target's latest version.
func (a *Adapter) unreact(ctx context.Context, h *helper, room domain.RoomID, self, chat string, group bool, target, reaction domain.EventID) error {
	rs, err := a.cache.Reactions(ctx, room)
	if err != nil {
		return fmt.Errorf("signal: read reactions: %w", err)
	}
	key := ""
	for _, r := range rs {
		if r.ID == reaction {
			key = r.Key
		}
	}
	if key == "" {
		return nil // not on it: nothing to take off
	}
	author, version, err := a.version(ctx, room, target)
	if err != nil {
		return err
	}
	return a.sendReaction(ctx, h, self, &signalv1.Reaction{
		Chat: chat, Group: group, Sender: self, Emoji: key, Remove: true, TargetAuthor: author, TargetSent: version,
	})
}

// readMarks is how many of a room's newest messages a read tells Signal of.
const readMarks = 50

// tellRead tells the account's devices (the phone), and unless private each author,
// that the room was read up to at. The read is kith's already: a failure is logged.
func (a *Adapter) tellRead(ctx context.Context, room domain.RoomID, at time.Time, private bool) {
	self, ok := selfOf(room)
	if !ok {
		return
	}
	h, err := a.sender(self)
	if err != nil {
		return // the phone hears of it from the next read once connected
	}
	msgs, err := a.cache.Messages(ctx, room, readMarks)
	if err != nil {
		a.log.Warn("reading what to mark read failed", "room", room, "err", err)
		return
	}
	var read []*signalv1.ReadMessage
	for i := range msgs {
		author, sent, ok := authorAndSent(room, msgs[i].ID)
		if ok && author != self && !msgs[i].Redacted && !msgs[i].Timestamp.After(at) {
			read = append(read, &signalv1.ReadMessage{Author: author, Sent: sent})
		}
	}
	if len(read) == 0 {
		return
	}
	req := &signalv1.MarkReadRequest{Account: self, Receipts: !private, Messages: read}
	if _, err := h.client.MarkRead(ctx, connect.NewRequest(req)); err != nil {
		a.log.Warn("telling the phone a chat was read failed", "room", room, "err", err)
	}
}
