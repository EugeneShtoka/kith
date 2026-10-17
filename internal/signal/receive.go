package signal

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// relisten is the wait before listening for the helper's events again after the
// stream broke or an event could not be kept.
const relisten = time.Second

// receive takes h's events until life ends: each written into the cache, and only then
// acked, so the helper tells Signal it arrived. An event that cannot be kept is not
// acked; the stream is opened again, and the helper sends what is still waiting first.
func (a *Adapter) receive(life context.Context, h *helper) {
	connected := false
	for life.Err() == nil {
		err := a.listen(life, h, func() {
			if !connected {
				connected = true
				a.connectAll(life, h)
			}
		})
		if life.Err() != nil {
			return
		}
		a.log.Warn("Signal's events stopped; listening again", "err", err)
		select {
		case <-life.Done():
		case <-time.After(relisten):
		}
	}
}

// listen takes events from one stream until it ends or one cannot be kept; opened runs
// once the stream is open.
func (a *Adapter) listen(life context.Context, h *helper, opened func()) error {
	stream, err := h.client.Events(life, connect.NewRequest(&signalv1.EventsRequest{}))
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = stream.Close() }() // a new listener takes over in the helper
	opened()
	for stream.Receive() {
		ev := stream.Msg()
		if !a.isConfigured(ev.GetAccount()) {
			a.hold(h, ev)
			continue
		}
		if err := a.keep(life, h, ev); err != nil {
			return err
		}
	}
	return stream.Err() //nolint:wrapcheck // the stream's own end, logged by receive
}

// keep writes ev into the cache, then acks it.
func (a *Adapter) keep(ctx context.Context, h *helper, ev *signalv1.EventsResponse) error {
	if err := a.take(ctx, h, ev); err != nil {
		return fmt.Errorf("keep an event: %w", err)
	}
	if ev.GetToken() == 0 {
		return nil
	}
	if _, err := h.client.Ack(ctx, connect.NewRequest(&signalv1.AckRequest{Token: ev.GetToken()})); err != nil {
		return fmt.Errorf("ack: %w", err)
	}
	return nil
}

// hold keeps an event of a Signal account no configured account is linked to, unacked:
// the account was renamed or taken out of the config, and Signal keeps the event until
// it is back (release), or until the helper ends and Signal sends it again.
func (a *Adapter) hold(h *helper, ev *signalv1.EventsResponse) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.helper != h {
		return
	}
	a.held = append(a.held, ev)
}

// release keeps the held events of accounts configured again.
func (a *Adapter) release() {
	a.mu.Lock()
	h, ctx := a.helper, a.run
	var now []*signalv1.EventsResponse
	still := a.held[:0]
	for _, ev := range a.held {
		if a.configuredLocked(ev.GetAccount()) {
			now = append(now, ev)
		} else {
			still = append(still, ev)
		}
	}
	a.held = still
	a.mu.Unlock()
	if h == nil || len(now) == 0 {
		return
	}
	for _, ev := range now {
		if err := a.keep(ctx, h, ev); err != nil {
			// Unacked: the helper sends it again to the next listener.
			a.log.Warn("keeping a held event failed", "err", err)
		}
	}
}

// connectAll connects every configured account linked in h's store.
func (a *Adapter) connectAll(ctx context.Context, h *helper) {
	for _, account := range a.accountsNow() {
		aci, ok, err := a.linkedTo(account.Name)
		if err == nil && ok && h.has(aci) {
			a.connectOne(ctx, h, account, aci)
		}
	}
}

func (a *Adapter) connectOne(ctx context.Context, h *helper, account Account, aci string) {
	if _, err := h.client.Connect(ctx, connect.NewRequest(&signalv1.ConnectRequest{Aci: aci})); err != nil {
		a.log.Warn("connecting failed", "account", account.Name, "err", err)
		h.setState(aci, signalv1.Connection_STATE_DISCONNECTED, "connecting failed: "+err.Error())
		a.announceAll()
	}
}

// take writes what ev carries into the cache.
func (a *Adapter) take(ctx context.Context, h *helper, ev *signalv1.EventsResponse) error {
	self := ev.GetAccount()
	switch e := ev.GetEvent().(type) {
	case *signalv1.EventsResponse_Connection:
		h.setState(self, e.Connection.GetState(), e.Connection.GetDetail())
		a.announceAll()
		return nil
	case *signalv1.EventsResponse_Message:
		return a.takeMessage(ctx, self, e.Message)
	case *signalv1.EventsResponse_Group:
		return a.takeGroup(ctx, self, e.Group)
	case *signalv1.EventsResponse_Contacts:
		return a.takeContacts(ctx, self, e.Contacts)
	}
	return nil
}

// isConfigured is whether a configured account is linked to aci.
func (a *Adapter) isConfigured(aci string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.configuredLocked(aci)
}

// configuredLocked is isConfigured with a.mu held.
func (a *Adapter) configuredLocked(aci string) bool {
	for _, account := range a.accounts {
		if linked, ok, err := a.linkedTo(account.Name); err == nil && ok && linked == aci {
			return true
		}
	}
	return false
}

// book is what kithd knows of one account's chats: its rooms, the names of the people
// in them, and its groups.
type book struct {
	rooms  map[domain.RoomID]domain.Room
	people map[string]string // person ID → name
	groups map[string]*signalv1.Group
}

// bookOf is self's book, read from the cache the first time.
func (a *Adapter) bookOf(ctx context.Context, self string) (*book, error) {
	a.booksMu.Lock()
	defer a.booksMu.Unlock()
	if b := a.books[self]; b != nil {
		return b, nil
	}
	b := &book{rooms: map[domain.RoomID]domain.Room{}, people: map[string]string{}, groups: map[string]*signalv1.Group{}}
	rooms, err := a.cache.Rooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the cached chats: %w", err)
	}
	for i := range rooms {
		if !owner(self).Owns(rooms[i].ID) {
			continue
		}
		b.rooms[rooms[i].ID] = rooms[i]
		members, err := a.cache.Members(ctx, rooms[i].ID, 0)
		if err != nil {
			return nil, fmt.Errorf("read a chat's people: %w", err)
		}
		for _, m := range members {
			if m.DisplayName != "" {
				b.people[m.UserID] = m.DisplayName
			}
		}
	}
	a.books[self] = b
	return b, nil
}

// learn keeps who's name, reporting whether it is new.
func (b *book) learn(who, name string) bool {
	if name == "" || b.people[who] == name {
		return false
	}
	b.people[who] = name
	return true
}

// takeMessage caches m, its chat and its people, then announces it.
func (a *Adapter) takeMessage(ctx context.Context, self string, m *signalv1.Message) error {
	b, err := a.bookOf(ctx, self)
	if err != nil {
		return err
	}
	a.booksMu.Lock()
	learned := b.learn(person(m.GetSender()), m.GetSenderName())
	if !m.GetGroup() {
		learned = b.learn(person(m.GetChat()), m.GetChatName()) || learned
	}
	room, changed := b.chat(self, m.GetChat(), m.GetGroup())
	members := b.members(self, m.GetChat(), m.GetGroup())
	names := b.names()
	a.booksMu.Unlock()
	if changed || learned {
		if err := a.keepChat(ctx, self, room, members); err != nil {
			return err
		}
	}
	if learned {
		if err := a.cache.SetPeople(ctx, "signal:"+self, names, nil); err != nil {
			return fmt.Errorf("keep who people are: %w", err)
		}
	}
	msg := message(self, m)
	if err := a.cache.SaveMessages(ctx, room.ID, []domain.Message{msg}); err != nil {
		return fmt.Errorf("cache a message: %w", err)
	}
	if m.GetSender() == self {
		// Written on another of the account's devices: read up to here.
		if err := a.cache.SaveUnread(ctx, domain.Unread{RoomID: room.ID}, msg.Timestamp.UnixMilli()); err != nil {
			return fmt.Errorf("save the read position: %w", err)
		}
	}
	if a.onCached != nil {
		a.onCached(msg)
	}
	emit(a, a.messages, msg)
	a.recount(ctx, room.ID)
	return nil
}

// chat is the room for a chat, made or renamed from what the book knows; changed is
// whether it differs from the cached one. Caller holds booksMu.
func (b *book) chat(self, chat string, group bool) (domain.Room, bool) {
	id := roomID(self, chat, group)
	old, had := b.rooms[id]
	r := old
	if !had {
		r = domain.Room{ID: id, IsDirect: !group, Membership: domain.MembershipJoin, Network: domain.ProtocolSignal}
	}
	r.Members = nil
	for _, m := range b.members(self, chat, group) {
		r.Members = append(r.Members, m.Name())
	}
	switch {
	case group && b.groups[chat] != nil:
		r.Name = b.groups[chat].GetTitle()
	case !group:
		r.Name = b.people[person(chat)]
	}
	b.rooms[id] = r
	return r, !had || r.Name != old.Name || !slices.Equal(r.Members, old.Members)
}

// members is a chat's people but self: the other side, or the group's members. Caller
// holds booksMu.
func (b *book) members(self, chat string, group bool) []domain.Member {
	var acis []string
	if !group {
		acis = []string{chat}
	} else if g := b.groups[chat]; g != nil {
		acis = g.GetMembers()
	}
	var out []domain.Member
	for _, aci := range acis {
		if aci != self {
			out = append(out, domain.Member{UserID: person(aci), DisplayName: b.people[person(aci)]})
		}
	}
	return out
}

// names is every name the book knows, as the directory takes them. Caller holds
// booksMu.
func (b *book) names() []domain.PersonName {
	out := make([]domain.PersonName, 0, len(b.people))
	for id, name := range b.people {
		out = append(out, domain.PersonName{ID: id, Name: name, Rank: domain.RankChosen})
	}
	return out
}

// keepChat caches a chat's room and members, and says the rooms changed.
func (a *Adapter) keepChat(ctx context.Context, self string, room domain.Room, members []domain.Member) error {
	if err := a.cache.AddRooms(ctx, owner(self), []domain.Room{room}); err != nil {
		return fmt.Errorf("cache a chat: %w", err)
	}
	if err := a.cache.SaveMembers(ctx, room.ID, members); err != nil {
		return fmt.Errorf("cache a chat's people: %w", err)
	}
	a.roomsChanged()
	return nil
}

// takeGroup keeps a group's title and members, renaming its room if kith has it.
func (a *Adapter) takeGroup(ctx context.Context, self string, g *signalv1.Group) error {
	b, err := a.bookOf(ctx, self)
	if err != nil {
		return err
	}
	a.booksMu.Lock()
	b.groups[g.GetId()] = g
	_, had := b.rooms[roomID(self, g.GetId(), true)]
	var room domain.Room
	var changed bool
	if had {
		room, changed = b.chat(self, g.GetId(), true)
	}
	members := b.members(self, g.GetId(), true)
	a.booksMu.Unlock()
	if !changed {
		return nil
	}
	return a.keepChat(ctx, self, room, members)
}

// takeContacts keeps the names the phone syncs, renaming the chats they change.
func (a *Adapter) takeContacts(ctx context.Context, self string, c *signalv1.Contacts) error {
	b, err := a.bookOf(ctx, self)
	if err != nil {
		return err
	}
	type kept struct {
		room    domain.Room
		members []domain.Member
	}
	var renamed []kept
	a.booksMu.Lock()
	learned := false
	for _, contact := range c.GetContacts() {
		learned = b.learn(person(contact.GetAci()), contact.GetName()) || learned
	}
	if learned {
		for id := range b.rooms {
			chat, group, ok := chatOf(self, id)
			if !ok {
				continue
			}
			if room, changed := b.chat(self, chat, group); changed {
				renamed = append(renamed, kept{room, b.members(self, chat, group)})
			}
		}
	}
	names := b.names()
	a.booksMu.Unlock()
	if !learned {
		return nil
	}
	if err := a.cache.SetPeople(ctx, "signal:"+self, names, nil); err != nil {
		return fmt.Errorf("keep who people are: %w", err)
	}
	for i := range renamed {
		if err := a.keepChat(ctx, self, renamed[i].room, renamed[i].members); err != nil {
			return err
		}
	}
	return nil
}

// chatOf is the chat a room of self's is, as the helper names it (a group's ID back in
// std base64), and whether it is a group.
func chatOf(self string, room domain.RoomID) (chat string, group bool, ok bool) {
	part, ok := strings.CutPrefix(string(room), string(owner(self)))
	if !ok || part == "" || strings.Contains(part, "/") {
		return "", false, false
	}
	if isACI(part) {
		return part, false, true
	}
	raw, err := decodeGroup(part)
	if err != nil {
		return "", false, false
	}
	return raw, true, true
}

// message is m as kith keeps it.
func message(self string, m *signalv1.Message) domain.Message {
	msg := domain.Message{
		ID:         eventID(self, m.GetChat(), m.GetGroup(), m.GetSender(), m.GetSent()),
		RoomID:     roomID(self, m.GetChat(), m.GetGroup()),
		Sender:     person(m.GetSender()),
		SenderName: m.GetSenderName(),
		Timestamp:  time.UnixMilli(int64(m.GetSent())), //nolint:gosec // a Signal timestamp fits
	}
	msg.Body, msg.Mentioned = mentioned(self, m.GetBody(), m.GetMentions())
	if q := m.GetQuote(); q != nil && q.GetAuthor() != "" {
		msg.ReplyTo = eventID(self, m.GetChat(), m.GetGroup(), q.GetAuthor(), q.GetSent())
	}
	if files := m.GetAttachments(); len(files) > 0 {
		msg.Media = media(files[0])
		if more := len(files) - 1; more > 0 {
			msg.Body = strings.TrimSpace(msg.Body + fmt.Sprintf("\n(+%d more attachments)", more))
		}
	}
	return msg
}

// mentioned writes each mention's name where Signal left U+FFFC, in order, and reports
// whether self is among them.
func mentioned(self, body string, mentions []*signalv1.Mention) (string, bool) {
	if len(mentions) == 0 {
		return body, false
	}
	ordered := slices.Clone(mentions)
	slices.SortFunc(ordered, func(x, y *signalv1.Mention) int { return int(x.GetStart()) - int(y.GetStart()) })
	var out strings.Builder
	next, me := 0, false
	for _, r := range body {
		if r == '￼' && next < len(ordered) {
			m := ordered[next]
			next++
			name := m.GetName()
			if name == "" {
				name = "someone"
			}
			out.WriteString("@" + name)
			me = me || m.GetAci() == self
			continue
		}
		out.WriteRune(r)
	}
	return out.String(), me
}

// media is an attachment as kith describes it (fetching it comes later).
func media(f *signalv1.Attachment) *domain.Media {
	m := &domain.Media{Name: f.GetFileName(), Mime: f.GetContentType(), Size: int(f.GetSize())} //nolint:gosec // an attachment's size fits
	switch kind, _, _ := strings.Cut(f.GetContentType(), "/"); kind {
	case "image":
		m.Type = domain.MediaImage
	case "video":
		m.Type = domain.MediaVideo
	case "audio":
		m.Type = domain.MediaAudio
	default:
		m.Type = domain.MediaFile
	}
	return m
}

// recount streams a room's locally counted unread state.
func (a *Adapter) recount(ctx context.Context, room domain.RoomID) {
	messages, mentions, counted, err := a.cache.CountUnread(ctx, a.Me(), room)
	if err != nil {
		a.log.Warn("count unread failed", "room", room, "err", err)
		return
	}
	emit(a, a.unread, domain.Unread{RoomID: room, Messages: messages, Mentions: mentions, Counted: counted})
}

// emit sends v without blocking; a client not reading misses it and catches up from the
// cache, as with the other adapters.
func emit[T any](a *Adapter, ch chan T, v T) {
	a.streamMu.RLock()
	defer a.streamMu.RUnlock()
	if a.closed {
		return
	}
	select {
	case ch <- v:
	default:
	}
}

// connState is an account's connection, as the helper last said it.
type connState struct {
	state  signalv1.Connection_State
	detail string
}

func (h *helper) setState(aci string, state signalv1.Connection_State, detail string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.states[aci] = connState{state, detail}
}

func (h *helper) stateOf(aci string) connState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.states[aci]
}
