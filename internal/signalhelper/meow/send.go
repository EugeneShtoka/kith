//go:build signal

package meow

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
)

// connected is the connected account aci.
func (n *Network) connected(aci string) (*account, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	a := n.accounts[aci]
	if a == nil {
		return nil, fmt.Errorf("signal: %s is not connected", aci)
	}
	return a, nil
}

// Send sends from a connected account, at the sent time kithd chose: a message, an edit
// of one, a reaction, or a deletion for everyone.
func (n *Network) Send(ctx context.Context, req *signalv1.SendRequest) error {
	a, err := n.connected(req.GetAccount())
	if err != nil {
		return err
	}
	sent := req.GetSent()
	data := &signalpb.DataMessage{Timestamp: &sent}
	switch r := req.GetReaction(); {
	case r != nil:
		data.Reaction = &signalpb.DataMessage_Reaction{
			Emoji: new(r.GetEmoji()), Remove: new(r.GetRemove()),
			TargetAuthorAci: new(r.GetTargetAuthor()), TargetSentTimestamp: new(r.GetTargetSent()),
		}
	case req.GetDeleteOf() != 0:
		data.Delete = &signalpb.DataMessage_Delete{TargetSentTimestamp: new(req.GetDeleteOf())}
	default:
		data.Body = new(req.GetBody())
		if q := req.GetQuote(); q != nil {
			data.Quote = &signalpb.DataMessage_Quote{
				Id: new(q.GetSent()), AuthorAci: new(q.GetAuthor()), Text: new(q.GetText()),
				Type: signalpb.DataMessage_Quote_NORMAL.Enum(),
			}
		}
	}
	content := &signalpb.Content{Content: &signalpb.Content_DataMessage{DataMessage: data}}
	if of := req.GetEditOf(); of != 0 {
		content = &signalpb.Content{Content: &signalpb.Content_EditMessage{EditMessage: &signalpb.EditMessage{
			TargetSentTimestamp: new(of), DataMessage: data,
		}}}
	}
	return a.send(ctx, req.GetChat(), req.GetGroup(), content)
}

// MarkRead tells the account's other devices, and with receipts each author, that it
// read messages. A read receipt reaches the account's devices on its own (signalmeow
// sends them a copy); without receipts only they are told.
func (n *Network) MarkRead(ctx context.Context, req *signalv1.MarkReadRequest) error {
	a, err := n.connected(req.GetAccount())
	if err != nil {
		return err
	}
	byAuthor := map[string][]uint64{}
	var reads []*signalpb.SyncMessage_Read
	for _, m := range req.GetMessages() {
		byAuthor[m.GetAuthor()] = append(byAuthor[m.GetAuthor()], m.GetSent())
		reads = append(reads, &signalpb.SyncMessage_Read{SenderAci: new(m.GetAuthor()), Timestamp: new(m.GetSent())})
	}
	if !req.GetReceipts() {
		res := a.cli.SendMessage(ctx, a.cli.Store.ACIServiceID(), signalmeow.WrapSyncMessage(&signalpb.SyncMessage{Read: reads}))
		if !res.WasSuccessful {
			return fmt.Errorf("signal: tell the account's devices: %w", res.Error)
		}
		return nil
	}
	for author, sent := range byAuthor {
		to, err := uuid.Parse(author)
		if err != nil {
			continue
		}
		res := a.cli.SendMessage(ctx, libsignalgo.NewACIServiceID(to), signalmeow.ReadReceptMessageForTimestamps(sent))
		if !res.WasSuccessful {
			return fmt.Errorf("signal: send a read receipt: %w", res.Error)
		}
	}
	return nil
}

// Typing says a connected account is typing in a chat, or stopped.
func (n *Network) Typing(ctx context.Context, req *signalv1.TypingRequest) error {
	a, err := n.connected(req.GetAccount())
	if err != nil {
		return err
	}
	return a.send(ctx, req.GetChat(), req.GetGroup(), signalmeow.TypingMessage(req.GetTyping()))
}

// send sends content to a chat: one person, or a group (signalmeow adds the group's
// context). A group send counts as sent when any member got it.
func (a *account) send(ctx context.Context, chat string, group bool, content *signalpb.Content) error {
	if group {
		res, err := a.cli.SendGroupMessage(ctx, types.GroupIdentifier(chat), content)
		if err != nil {
			return fmt.Errorf("signal: send to the group: %w", err)
		}
		if len(res.SuccessfullySentTo) == 0 && len(res.FailedToSendTo) > 0 {
			return fmt.Errorf("signal: no member got it: %w", res.FailedToSendTo[0].Error)
		}
		return nil
	}
	to, err := uuid.Parse(chat)
	if err != nil {
		return fmt.Errorf("signal: %q is no ACI: %w", chat, err)
	}
	res := a.cli.SendMessage(ctx, libsignalgo.NewACIServiceID(to), content)
	if !res.WasSuccessful {
		if res.Error != nil {
			return fmt.Errorf("signal: send: %w", res.Error)
		}
		return errors.New("signal: send failed")
	}
	return nil
}
