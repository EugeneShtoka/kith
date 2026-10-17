//go:build signal

package meow

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/events"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"
	"google.golang.org/protobuf/proto"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/signalhelper"
)

// Connect connects the linked account aci, handing sink its events until the helper
// ends. signalmeow tells Signal an event arrived when its handler returns, so the
// handler returns only once kithd has kept what it carries (sink.Deliver waits).
func (n *Network) Connect(ctx context.Context, aci string, sink signalhelper.Sink) error {
	id, err := uuid.Parse(aci)
	if err != nil {
		return fmt.Errorf("signal: %q is no ACI: %w", aci, err)
	}
	n.mu.Lock()
	st := n.store
	n.mu.Unlock()
	if st == nil {
		return errors.New("signal: the key store is not open")
	}
	device, err := st.DeviceByACI(ctx, id)
	if err != nil {
		return fmt.Errorf("signal: read the linked device: %w", err)
	}
	if device == nil {
		return fmt.Errorf("signal: no device linked to %s", aci)
	}
	a := &account{aci: aci, sink: sink, groups: map[string]bool{}}
	a.cli = signalmeow.NewClient(device, n.zerolog(), func(evt events.SignalEvent) bool {
		a.handle(ctx, evt)
		return true // kept, or not kept at all on purpose
	})
	status, err := a.cli.StartReceiveLoops(ctx)
	if err != nil {
		return fmt.Errorf("signal: connect: %w", err)
	}
	go func() {
		for s := range status {
			sink.Tell(&signalv1.EventsResponse{Account: aci, Event: &signalv1.EventsResponse_Connection{Connection: connection(s)}})
		}
	}()
	return nil
}

// account is one connected account.
type account struct {
	aci  string
	sink signalhelper.Sink
	cli  *signalmeow.Client

	mu sync.Mutex
	// groups are the groups kithd has been described this run.
	groups map[string]bool
}

func connection(s signalmeow.SignalConnectionStatus) *signalv1.Connection {
	c := &signalv1.Connection{}
	if s.Err != nil {
		c.Detail = s.Err.Error()
	}
	switch s.Event {
	case signalmeow.SignalConnectionEventConnected:
		c.State = signalv1.Connection_STATE_CONNECTED
	case signalmeow.SignalConnectionEventLoggedOut:
		c.State = signalv1.Connection_STATE_LOGGED_OUT
	default:
		c.State = signalv1.Connection_STATE_DISCONNECTED
	}
	return c
}

// handle hands kithd what evt carries and returns once kept. What kith does not keep
// yet (reactions, edits, deletions, receipts, typing) returns at once.
func (a *account) handle(ctx context.Context, evt events.SignalEvent) {
	switch e := evt.(type) {
	case *events.ChatEvent:
		data, ok := e.Event.(*signalpb.DataMessage)
		if !ok || data.GetReaction() != nil || data.GetDelete() != nil {
			return
		}
		m := a.message(ctx, e.Info, data)
		if m.GetBody() == "" && len(m.GetAttachments()) == 0 {
			return // a profile key, a timer change: nothing to read
		}
		if m.GetGroup() {
			a.describe(ctx, m.GetChat(), e.Info.GroupRevision)
		}
		a.sink.Deliver(&signalv1.EventsResponse{Account: a.aci, Event: &signalv1.EventsResponse_Message{Message: m}})
	case *events.ContactList:
		contacts := &signalv1.Contacts{}
		for _, r := range e.Contacts {
			if r.ACI == uuid.Nil {
				continue
			}
			contacts.Contacts = append(contacts.Contacts, &signalv1.Contact{Aci: r.ACI.String(), Name: name(r), Number: r.E164})
		}
		a.sink.Deliver(&signalv1.EventsResponse{Account: a.aci, Event: &signalv1.EventsResponse_Contacts{Contacts: contacts}})
	}
}

// message is data as a wire message.
func (a *account) message(ctx context.Context, info events.MessageInfo, data *signalpb.DataMessage) *signalv1.Message {
	m := &signalv1.Message{
		Chat:     info.ChatID,
		Group:    data.GetGroupV2() != nil,
		Sender:   info.Sender.String(),
		Sent:     data.GetTimestamp(),
		Received: info.ServerTimestamp,
		Body:     data.GetBody(),
	}
	m.SenderName, m.SenderNumber = a.whoIs(ctx, info.Sender)
	if !m.GetGroup() {
		if other, err := uuid.Parse(info.ChatID); err == nil {
			m.ChatName, m.ChatNumber = a.whoIs(ctx, other)
		}
	}
	if q := data.GetQuote(); q != nil {
		m.Quote = &signalv1.Quote{Author: q.GetAuthorAci(), Sent: q.GetId(), Text: q.GetText()}
	}
	for _, r := range data.GetBodyRanges() {
		mentioned := r.GetMentionAci()
		if mentioned == "" {
			continue // a style (bold, spoiler), not a person
		}
		who, _ := uuid.Parse(mentioned)
		m.Mentions = append(m.Mentions, &signalv1.Mention{
			Start: r.GetStart(), Length: r.GetLength(), Aci: mentioned, Name: a.nameOf(ctx, who),
		})
	}
	for _, p := range data.GetAttachments() {
		pointer, err := proto.Marshal(p)
		if err != nil {
			continue
		}
		m.Attachments = append(m.Attachments, &signalv1.Attachment{
			ContentType: p.GetContentType(), FileName: p.GetFileName(), Size: uint64(p.GetSize()), Pointer: pointer,
		})
	}
	return m
}

// describe hands kithd a group's title and members before its first message this run.
func (a *account) describe(ctx context.Context, id string, revision uint32) {
	a.mu.Lock()
	known := a.groups[id]
	a.groups[id] = true
	a.mu.Unlock()
	if known {
		return
	}
	g, _, err := a.cli.RetrieveGroupByID(ctx, types.GroupIdentifier(id), revision)
	if err != nil || g == nil {
		a.cli.Log.Warn().Err(err).Msg("the group's details could not be had")
		return
	}
	group := &signalv1.Group{Id: id, Title: g.Title}
	for _, member := range g.Members {
		group.Members = append(group.Members, member.ACI.String())
	}
	a.sink.Tell(&signalv1.EventsResponse{Account: a.aci, Event: &signalv1.EventsResponse_Group{Group: group}})
}

// nameOf is who's name as the account knows them, or "". A profile name needs their
// profile key, which a new device has only once they wrote.
func (a *account) nameOf(ctx context.Context, who uuid.UUID) string {
	name, _ := a.whoIs(ctx, who)
	return name
}

// whoIs is who's name and phone number (E.164) as the account knows them, either "".
func (a *account) whoIs(ctx context.Context, who uuid.UUID) (string, string) {
	if who == uuid.Nil {
		return "", ""
	}
	r, err := a.cli.ContactByACI(ctx, who)
	if err != nil || r == nil {
		return "", ""
	}
	return name(r), r.E164
}

// name is a recipient's name: the nickname the account gave them, their contact name,
// else their own profile name.
func name(r *types.Recipient) string {
	for _, n := range []string{r.Nickname, r.ContactName, r.Profile.Name} {
		if n != "" {
			return n
		}
	}
	return ""
}
