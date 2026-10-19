package gmessages

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Sending goes through the phone, which sends as the Messages app would: an SMS chat by
// SMS, an RCS chat by RCS, falling back to SMS as the phone's own settings say, except
// in a chat that switched to end-to-end encryption, which is held to RCS. The message
// shows when the phone sends it back (see outbox.go).

// sendSpan bounds handing one message to the phone.
const sendSpan = time.Minute

// errNotListed: a conversation is sent to as the phone listed it, with its SIM and the
// participant you are there.
var errNotListed = errors.New("gmessages: that conversation is not listed yet; try again in a moment")

// errNoEdit: Google Messages does not edit a sent message.
var errNoEdit = errors.New("gmessages: a sent message cannot be edited")

// connected is the connection of the account self and its client, when connected.
func (a *Adapter) connected(self string) (*conn, *libgm.Client, error) {
	c := a.connOf(self)
	if c == nil {
		return nil, nil, fmt.Errorf("%w: this Google Messages account is not connected", api.ErrNetworkOff)
	}
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return nil, nil, fmt.Errorf("%w: this Google Messages account is not connected", api.ErrNetworkOff)
	}
	return c, client, nil
}

// Send hands draft to the phone to send in a conversation. A retry of one the phone
// already has is not handed over again.
func (a *Adapter) Send(ctx context.Context, room domain.RoomID, draft domain.Draft) error {
	self, conversation, ok := conversationOf(room)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAConversation, room)
	}
	if draft.Edits != "" {
		return errNoEdit
	}
	c, client, err := a.connected(self)
	if err != nil {
		return err
	}
	tmp := tmpFor(draft.TxnID)
	req, err := c.request(conversation, tmp, draft)
	if err != nil {
		return err
	}
	return a.hand(ctx, client, tmp, req)
}

// hand gives the phone req, sent as tmp, unless the phone already has it.
func (a *Adapter) hand(ctx context.Context, client *libgm.Client, tmp string, req *gmproto.SendMessageRequest) error {
	if due, settled := a.outbox.begin(tmp, time.Now()); !due {
		return settled
	}
	ctx, cancel := context.WithTimeout(ctx, sendSpan)
	defer cancel()
	var resp *gmproto.SendMessageResponse
	err := a.guard("sending a message", func() (err error) {
		resp, err = client.SendMessage(ctx, req)
		return err //nolint:wrapcheck // end says what it was
	})
	return a.outbox.end(tmp, resp, err, time.Now())
}

// route is how a message goes to a conversation: as which participant, from which SIM
// (nil: the phone picks), and whether it is held to RCS.
type route struct {
	outgoing string
	sim      *gmproto.SIMPayload
	forceRCS bool
}

// route is how messages go to a conversation the inbox knows; false when it does not.
func (c *conn) route(conversation string) (route, bool) {
	c.applying.Lock()
	cv := c.inbox.convs[conversation]
	var r route
	var card *gmproto.SIMCard
	if cv != nil {
		r.outgoing, r.forceRCS, card = cv.outgoing, cv.forceRCS(), cv.sim
	}
	c.applying.Unlock()
	if cv == nil {
		return route{}, false
	}
	r.sim = c.simOf(card, r.outgoing).GetSIMData().GetSIMPayload()
	return r, true
}

// request is draft as the phone takes it, for a conversation the inbox knows.
func (c *conn) request(conversation, tmp string, draft domain.Draft) (*gmproto.SendMessageRequest, error) {
	via, ok := c.route(conversation)
	if !ok {
		return nil, errNotListed
	}
	text := draft.Body
	if draft.Emote {
		text = "/me " + text
	}
	req := &gmproto.SendMessageRequest{
		ConversationID: conversation,
		MessagePayload: &gmproto.MessagePayload{
			TmpID: tmp, TmpID2: tmp, ConversationID: conversation, ParticipantID: via.outgoing,
			MessageInfo: []*gmproto.MessageInfo{{Data: &gmproto.MessageInfo_MessageContent{
				MessageContent: &gmproto.MessageContent{Content: text},
			}}},
		},
		SIMPayload: via.sim,
		TmpID:      tmp,
		ForceRCS:   via.forceRCS,
	}
	if draft.ReplyTo != "" {
		if target, ok := messageOf(draft.ReplyTo); ok {
			req.Reply = &gmproto.ReplyPayload{MessageID: target}
		}
	}
	return req, nil
}

// SendTyping tells the phone the account is typing in a conversation. Google Messages
// has no "stopped": that, and timeout, are not sent.
func (a *Adapter) SendTyping(ctx context.Context, room domain.RoomID, typing bool, _ time.Duration) error {
	if !typing {
		return nil
	}
	self, conversation, ok := conversationOf(room)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAConversation, room)
	}
	c, client, err := a.connected(self)
	if err != nil {
		return err
	}
	via, _ := c.route(conversation) // unlisted: the phone picks the SIM
	err = a.guard("sending typing", func() error {
		return client.SetTyping(ctx, conversation, via.sim)
	})
	if err != nil {
		return fmt.Errorf("gmessages: typing in %s: %w", room, err)
	}
	return nil
}
