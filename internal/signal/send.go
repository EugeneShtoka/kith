package signal

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// errNoEdit: editing a sent message comes with edits received.
var errNoEdit = errors.New("signal: editing a sent message is not supported yet")

// Send sends draft to a chat from its account, then caches it: a message sent from this
// device does not come back to it. A send repeated with the same TxnID goes at the same
// sent time, which is the message's ID, so Signal's clients take it once.
func (a *Adapter) Send(ctx context.Context, room domain.RoomID, draft domain.Draft) error {
	if draft.Edits != "" {
		return errNoEdit
	}
	self, chat, group, err := a.chatTo(room)
	if err != nil {
		return err
	}
	h, err := a.sender(self)
	if err != nil {
		return err
	}
	sent := a.sentAt(draft.TxnID)
	req := &signalv1.SendRequest{Account: self, Chat: chat, Group: group, Body: draft.Body, Sent: sent}
	if draft.ReplyTo != "" {
		req.Quote = a.quote(ctx, room, draft.ReplyTo)
	}
	if _, err := h.client.Send(ctx, connect.NewRequest(req)); err != nil {
		return fmt.Errorf("signal: send to %s: %w", room, err)
	}
	a.forgetTxn(draft.TxnID)
	return a.takeMessage(ctx, self, &signalv1.Message{
		Chat: chat, Group: group, Sender: self, Sent: sent, Received: sent, Body: draft.Body, Quote: req.GetQuote(),
	})
}

// SendTyping says the account is typing in a chat, or stopped (Signal's typing lapses
// on its own; timeout is not sent).
func (a *Adapter) SendTyping(ctx context.Context, room domain.RoomID, typing bool, _ time.Duration) error {
	self, chat, group, err := a.chatTo(room)
	if err != nil {
		return err
	}
	h, err := a.sender(self)
	if err != nil {
		return err
	}
	req := &signalv1.TypingRequest{Account: self, Chat: chat, Group: group, Typing: typing}
	if _, err := h.client.Typing(ctx, connect.NewRequest(req)); err != nil {
		return fmt.Errorf("signal: typing in %s: %w", room, err)
	}
	return nil
}

// chatTo is a room's account and chat.
func (a *Adapter) chatTo(room domain.RoomID) (self, chat string, group bool, err error) {
	self, ok := selfOf(room)
	if !ok {
		return "", "", false, fmt.Errorf("signal: %s is not a Signal chat", room)
	}
	chat, group, ok = chatOf(self, room)
	if !ok {
		return "", "", false, fmt.Errorf("signal: %s is not a Signal chat", room)
	}
	return self, chat, group, nil
}

// sender is the helper, when the account self is connected through it.
func (a *Adapter) sender(self string) (*helper, error) {
	a.mu.Lock()
	h := a.helper
	a.mu.Unlock()
	if h == nil || !a.isConfigured(self) {
		return nil, fmt.Errorf("%w: Signal is not connected", api.ErrNetworkOff)
	}
	if h.stateOf(self).state != signalv1.Connection_STATE_CONNECTED {
		return nil, fmt.Errorf("%w: this Signal account is not connected", api.ErrNetworkOff)
	}
	return h, nil
}

// sentAt is a send's sent time: the one an earlier try with txn chose, else now.
func (a *Adapter) sentAt(txn string) uint64 {
	now := uint64(time.Now().UnixMilli())
	if txn == "" {
		return now
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if at, ok := a.txns[txn]; ok {
		return at
	}
	a.txns[txn] = now
	return now
}

func (a *Adapter) forgetTxn(txn string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.txns, txn)
}

// quote is the message a reply quotes: its author and sent time (from its ID), and its
// text when cached.
func (a *Adapter) quote(ctx context.Context, room domain.RoomID, target domain.EventID) *signalv1.Quote {
	part, ok := strings.CutPrefix(string(target), string(room)+"/")
	if !ok {
		return nil
	}
	author, at, ok := strings.Cut(part, ".")
	sent, err := strconv.ParseUint(at, 10, 64)
	if !ok || err != nil {
		return nil
	}
	q := &signalv1.Quote{Author: author, Sent: sent}
	if msg, cached, err := a.cache.MessageByID(ctx, room, target); err == nil && cached {
		q.Text = msg.Body
	}
	return q
}
