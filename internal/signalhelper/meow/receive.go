//go:build signal

package meow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
	a := &account{aci: aci, sink: sink, groups: map[string]bool{}, log: n.log}
	a.cli = signalmeow.NewClient(device, n.zerolog(), func(evt events.SignalEvent) bool {
		a.handle(ctx, evt)
		return true // kept, or not kept at all on purpose
	})
	a.cli.SyncContactsOnConnect = true // the phone's contacts name people kith has no chat with yet
	status, err := a.cli.StartReceiveLoops(ctx)
	if err != nil {
		return fmt.Errorf("signal: connect: %w", err)
	}
	n.mu.Lock()
	n.accounts[aci] = a
	n.mu.Unlock()
	go func() {
		var history sync.Once
		for s := range status {
			sink.Tell(&signalv1.EventsResponse{Account: aci, Event: &signalv1.EventsResponse_Connection{Connection: connection(s)}})
			if s.Event == signalmeow.SignalConnectionEventConnected {
				history.Do(func() { go a.syncHistory(ctx) })
			}
		}
	}()
	return nil
}

// account is one connected account.
type account struct {
	aci  string
	sink signalhelper.Sink
	cli  *signalmeow.Client
	// log is the helper's own log, which kithd's journal keeps at Info (signalmeow's
	// keeps only warnings).
	log *slog.Logger

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

// handle hands kithd what evt carries and returns once kept; typing waits for nothing.
// What kith does not keep returns at once.
func (a *account) handle(ctx context.Context, evt events.SignalEvent) {
	switch e := evt.(type) {
	case *events.ChatEvent:
		a.chatEvent(ctx, e)
	case *events.Receipt:
		r := &signalv1.Receipt{Sender: e.Sender.String(), Sent: e.Content.GetTimestamp()}
		switch e.Content.GetType() {
		case signalpb.ReceiptMessage_DELIVERY:
			r.Kind = signalv1.Receipt_KIND_DELIVERED
		case signalpb.ReceiptMessage_READ:
			r.Kind = signalv1.Receipt_KIND_READ
		case signalpb.ReceiptMessage_VIEWED:
			r.Kind = signalv1.Receipt_KIND_VIEWED
		}
		a.deliver(&signalv1.EventsResponse{Event: &signalv1.EventsResponse_Receipt{Receipt: r}})
	case *events.ReadSelf:
		read := &signalv1.Read{}
		for _, m := range e.Messages {
			read.Messages = append(read.Messages, &signalv1.ReadMessage{Author: m.GetSenderAci(), Sent: m.GetTimestamp()})
		}
		a.deliver(&signalv1.EventsResponse{Event: &signalv1.EventsResponse_Read{Read: read}})
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

// deliver hands kithd ev as the account's and waits until kept.
func (a *account) deliver(ev *signalv1.EventsResponse) {
	ev.Account = a.aci
	a.sink.Deliver(ev)
}

// chatEvent hands kithd a message, an edit, a deletion, a reaction or typing.
func (a *account) chatEvent(ctx context.Context, e *events.ChatEvent) {
	chat, sender := e.Info.ChatID, e.Info.Sender.String()
	switch c := e.Event.(type) {
	case *signalpb.DataMessage:
		group := c.GetGroupV2() != nil
		switch {
		case c.GetReaction() != nil:
			r := c.GetReaction()
			a.deliver(&signalv1.EventsResponse{Event: &signalv1.EventsResponse_Reaction{Reaction: &signalv1.Reaction{
				Chat: chat, Group: group, Sender: sender, Emoji: r.GetEmoji(), Remove: r.GetRemove(),
				TargetAuthor: r.GetTargetAuthorAci(), TargetSent: r.GetTargetSentTimestamp(), SenderName: a.nameOf(ctx, e.Info.Sender),
			}}})
		case c.GetDelete() != nil:
			a.deliver(&signalv1.EventsResponse{Event: &signalv1.EventsResponse_Delete{Delete: &signalv1.Delete{
				Chat: chat, Group: group, Sender: sender, Target: c.GetDelete().GetTargetSentTimestamp(), Sent: c.GetTimestamp(),
			}}})
		case c.GetFlags()&uint32(signalpb.DataMessage_EXPIRATION_TIMER_UPDATE) != 0:
			a.deliver(&signalv1.EventsResponse{Event: &signalv1.EventsResponse_Timer{Timer: &signalv1.Timer{
				Chat: chat, Group: group, Sender: sender, Seconds: c.GetExpireTimer(), Version: c.GetExpireTimerVersion(),
			}}})
		case c.GetGroupV2().GetGroupChange() != nil:
			a.groupChange(ctx, e.Info, c)
		case c.GetPollVote() != nil:
			a.deliver(&signalv1.EventsResponse{Event: &signalv1.EventsResponse_Vote{Vote: vote(chat, group, sender, c.GetPollVote())}})
		case c.GetPollTerminate() != nil:
			a.deliver(&signalv1.EventsResponse{Event: &signalv1.EventsResponse_PollEnd{PollEnd: &signalv1.PollEnd{
				Chat: chat, Group: group, Sender: sender, TargetSent: c.GetPollTerminate().GetTargetSentTimestamp(),
			}}})
		default:
			m := a.message(ctx, e.Info, c)
			if m.GetBody() == "" && len(m.GetAttachments()) == 0 && m.GetPoll() == nil {
				return // a profile key: nothing to read
			}
			if m.GetGroup() {
				a.describe(ctx, m.GetChat(), e.Info.GroupRevision)
			}
			a.deliver(&signalv1.EventsResponse{Event: &signalv1.EventsResponse_Message{Message: m}})
		}
	case *signalpb.EditMessage:
		data := c.GetDataMessage()
		a.deliver(&signalv1.EventsResponse{Event: &signalv1.EventsResponse_Edit{Edit: &signalv1.Edit{
			Chat: chat, Group: data.GetGroupV2() != nil, Sender: sender, Target: c.GetTargetSentTimestamp(),
			Sent: data.GetTimestamp(), Body: data.GetBody(), Mentions: a.mentions(ctx, data.GetBodyRanges()),
			Styles: styles(data.GetBodyRanges()),
		}}})
	case *signalpb.TypingMessage:
		if sender == a.aci {
			return
		}
		a.sink.Tell(&signalv1.EventsResponse{Account: a.aci, Event: &signalv1.EventsResponse_Typing{Typing: &signalv1.Typing{
			Chat: chat, Group: len(c.GetGroupId()) > 0, Sender: sender,
			Started: c.GetAction() == signalpb.TypingMessage_STARTED,
		}}})
	}
}

// mentions are a body's mentions of people (not its styles).
func (a *account) mentions(ctx context.Context, ranges []*signalpb.BodyRange) []*signalv1.Mention {
	var out []*signalv1.Mention
	for _, r := range ranges {
		mentioned := r.GetMentionAci()
		if mentioned == "" {
			continue // a style (bold, spoiler), not a person
		}
		who, _ := uuid.Parse(mentioned)
		out = append(out, &signalv1.Mention{Start: r.GetStart(), Length: r.GetLength(), Aci: mentioned, Name: a.nameOf(ctx, who)})
	}
	return out
}

// vote is a poll vote as the wire carries it; a target author not an ACI is left out.
func vote(chat string, group bool, sender string, v *signalpb.DataMessage_PollVote) *signalv1.Vote {
	out := &signalv1.Vote{
		Chat: chat, Group: group, Sender: sender, TargetSent: v.GetTargetSentTimestamp(),
		Options: v.GetOptionIndexes(), Round: v.GetVoteCount(),
	}
	if author := v.GetTargetAuthorAciBinary(); len(author) == 16 {
		out.TargetAuthor = uuid.UUID(author).String()
	}
	return out
}

// styles is how a body is formatted: its ranges that are styles, not people. Signal's
// styles are numbered as the wire's kinds.
func styles(ranges []*signalpb.BodyRange) []*signalv1.Style {
	var out []*signalv1.Style
	for _, r := range ranges {
		if style := r.GetStyle(); style != signalpb.BodyRange_NONE {
			out = append(out, &signalv1.Style{Start: r.GetStart(), Length: r.GetLength(), Kind: signalv1.Style_Kind(style)})
		}
	}
	return out
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
		Timer:    data.GetExpireTimer(),
	}
	m.TimerVersion = data.GetExpireTimerVersion()
	m.SenderName, m.SenderNumber = a.whoIs(ctx, info.Sender)
	if !m.GetGroup() {
		if other, err := uuid.Parse(info.ChatID); err == nil {
			m.ChatName, m.ChatNumber = a.whoIs(ctx, other)
		}
	}
	if q := data.GetQuote(); q != nil {
		m.Quote = &signalv1.Quote{Author: q.GetAuthorAci(), Sent: q.GetId(), Text: q.GetText()}
	}
	m.Mentions = a.mentions(ctx, data.GetBodyRanges())
	m.Styles = styles(data.GetBodyRanges())
	if p := data.GetPollCreate(); p != nil {
		m.Poll = &signalv1.Poll{Question: p.GetQuestion(), Multiple: p.GetAllowMultiple(), Options: p.GetOptions()}
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
	if s := data.GetSticker(); s != nil {
		m.Attachments, m.Body = sticker(s, m.GetAttachments(), m.GetBody())
	}
	return m
}

// sticker adds a sticker to a message as its picture, as other networks' show; one
// whose picture is not there reads as its emoji.
func sticker(s *signalpb.DataMessage_Sticker, files []*signalv1.Attachment, body string) ([]*signalv1.Attachment, string) {
	p := s.GetData()
	pointer, err := proto.Marshal(p)
	if p == nil || err != nil {
		return files, strings.TrimSpace(body + " [sticker] " + s.GetEmoji())
	}
	return append(files, &signalv1.Attachment{
		ContentType: cmp.Or(p.GetContentType(), "image/webp"), FileName: "sticker", Size: uint64(p.GetSize()), Pointer: pointer,
	}), body
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
	group := &signalv1.Group{Id: id, Title: g.Title, Timer: g.DisappearingMessagesDuration}
	for _, member := range g.Members {
		group.Members = append(group.Members, member.ACI.String())
	}
	a.sink.Tell(&signalv1.EventsResponse{Account: a.aci, Event: &signalv1.EventsResponse_Group{Group: group}})
}

// groupChange hands kithd what of a group's change kith keeps: its new timer.
func (a *account) groupChange(ctx context.Context, info events.MessageInfo, data *signalpb.DataMessage) {
	change, err := a.cli.DecryptGroupChange(ctx, data.GetGroupV2())
	if err != nil {
		a.cli.Log.Warn().Err(err).Msg("a group's change could not be read")
		return
	}
	if change == nil || change.ModifyDisappearingMessagesDuration == nil {
		return
	}
	a.deliver(&signalv1.EventsResponse{Event: &signalv1.EventsResponse_Timer{Timer: &signalv1.Timer{
		Chat: info.ChatID, Group: true, Sender: info.Sender.String(), Seconds: *change.ModifyDisappearingMessagesDuration,
	}}})
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
