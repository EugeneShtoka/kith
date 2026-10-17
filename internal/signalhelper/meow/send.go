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

// Send sends a message from a connected account, at the sent time kithd chose.
func (n *Network) Send(ctx context.Context, req *signalv1.SendRequest) error {
	a, err := n.connected(req.GetAccount())
	if err != nil {
		return err
	}
	sent := req.GetSent()
	data := &signalpb.DataMessage{Body: new(req.GetBody()), Timestamp: &sent}
	if q := req.GetQuote(); q != nil {
		data.Quote = &signalpb.DataMessage_Quote{
			Id: new(q.GetSent()), AuthorAci: new(q.GetAuthor()), Text: new(q.GetText()),
			Type: signalpb.DataMessage_Quote_NORMAL.Enum(),
		}
	}
	return a.send(ctx, req.GetChat(), req.GetGroup(), &signalpb.Content{Content: &signalpb.Content_DataMessage{DataMessage: data}})
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
