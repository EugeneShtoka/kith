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
	if due, settled := a.outbox.begin(tmp, time.Now()); !due {
		return settled
	}
	ctx, cancel := context.WithTimeout(ctx, sendSpan)
	defer cancel()
	var resp *gmproto.SendMessageResponse
	err = a.guard("sending a message", func() (err error) {
		resp, err = client.SendMessage(ctx, req)
		return err //nolint:wrapcheck // end says what it was
	})
	return a.outbox.end(tmp, resp, err, time.Now())
}

// request is draft as the phone takes it, for a conversation the inbox knows.
func (c *conn) request(conversation, tmp string, draft domain.Draft) (*gmproto.SendMessageRequest, error) {
	c.applying.Lock()
	cv := c.inbox.convs[conversation]
	var forceRCS bool
	var outgoing string
	var sim *gmproto.SIMCard
	if cv != nil {
		forceRCS, outgoing, sim = cv.forceRCS(), cv.outgoing, cv.sim
	}
	c.applying.Unlock()
	if cv == nil {
		return nil, errNotListed
	}
	text := draft.Body
	if draft.Emote {
		text = "/me " + text
	}
	req := &gmproto.SendMessageRequest{
		ConversationID: conversation,
		MessagePayload: &gmproto.MessagePayload{
			TmpID: tmp, TmpID2: tmp, ConversationID: conversation, ParticipantID: outgoing,
			MessageInfo: []*gmproto.MessageInfo{{Data: &gmproto.MessageInfo_MessageContent{
				MessageContent: &gmproto.MessageContent{Content: text},
			}}},
		},
		SIMPayload: c.simOf(sim, outgoing).GetSIMData().GetSIMPayload(),
		TmpID:      tmp,
		ForceRCS:   forceRCS,
	}
	if draft.ReplyTo != "" {
		if target, ok := messageOf(draft.ReplyTo); ok {
			req.Reply = &gmproto.ReplyPayload{MessageID: target}
		}
	}
	return req, nil
}
