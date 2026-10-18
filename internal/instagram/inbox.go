package instagram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mau.fi/mautrix-meta/pkg/instameow"
	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// How the inbox is listed after connecting: page by page with a pause between pages
// (Instagram's apps page slowly too), and a thread's newest messages fetched when it
// has news the cache lacks.
const (
	pagePause  = time.Second
	maxPages   = 200
	fetchPause = time.Second
)

// inbox is what one connection knows of its threads and people. A thread is kept by its
// IG chat ID (what deltas, sending and history name), with its long ID (what read marks,
// unsending and typing name).
type inbox struct {
	mu      sync.Mutex
	threads map[string]*thread
	byLong  map[string]string // long thread ID → chat ID
	people  map[string]string // person ID → name
	byIGID  map[string]string // IG user ID → person ID (typing names the former)
	// reactions are reactions kept by Instagram's log ID (a removal may name only it).
	reactions map[string]domain.Reaction
}

type thread struct {
	id, long string
	group    bool
	title    string
	users    []*slidetypes.User // all but the account
}

func newInbox() *inbox {
	return &inbox{
		threads: map[string]*thread{}, byLong: map[string]string{}, people: map[string]string{},
		byIGID: map[string]string{}, reactions: map[string]domain.Reaction{},
	}
}

// seed starts the inbox's names from what the account told the directory before: each
// write of its names replaces all of them, so one made before the inbox is listed again
// must not drop the rest.
func (a *Adapter) seed(ctx context.Context, c *conn) {
	if a.cache == nil {
		return
	}
	dir, err := a.cache.Directory(ctx)
	if err != nil {
		a.log.Warn("read who people are failed", "account", c.account.Name, "err", err)
		return
	}
	source := "instagram:" + c.self
	c.inbox.mu.Lock()
	defer c.inbox.mu.Unlock()
	for _, n := range dir.Names() {
		if n.Source == source {
			if _, known := c.inbox.people[n.ID]; !known {
				c.inbox.people[n.ID] = n.Name
			}
		}
	}
}

// learn keeps a thread and its people; it reports whether the people's names changed.
func (in *inbox) learn(t *slidetypes.ThreadInfo, self string) (*thread, bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	th := &thread{id: t.ID, long: t.ThreadID, group: t.IsGroup || len(t.Users) > 1, title: t.ThreadTitle}
	met := false
	for _, u := range t.Users {
		if u == nil || fbid(u.InteropMessagingUserFBID) == self {
			continue
		}
		th.users = append(th.users, u)
		met = in.meet(u) || met
	}
	if t.Viewer != nil {
		met = in.meet(t.Viewer) || met
	}
	in.threads[t.ID] = th
	if t.ThreadID != "" {
		in.byLong[t.ThreadID] = t.ID
	}
	return th, met
}

// meet keeps a user's name and IG ID; it reports whether the name is new. Caller holds
// in.mu.
func (in *inbox) meet(u *slidetypes.User) bool {
	id := person(fbid(u.InteropMessagingUserFBID))
	if u.ID != "" {
		in.byIGID[u.ID] = id
	}
	name := nameOf(u)
	if name == "" || in.people[id] == name {
		return false
	}
	in.people[id] = name
	return true
}

func (in *inbox) thread(chat string) *thread {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.threads[chat]
}

// room is a thread's room: a group by its title (else its people), one to one by the
// other side's name.
func (in *inbox) room(self string, th *thread) (domain.Room, []domain.Member) {
	in.mu.Lock()
	defer in.mu.Unlock()
	r := domain.Room{ID: roomID(self, th.id), IsDirect: !th.group, Membership: domain.MembershipJoin, Network: domain.ProtocolInstagram}
	var members []domain.Member
	for _, u := range th.users {
		id := person(fbid(u.InteropMessagingUserFBID))
		name := in.people[id]
		members = append(members, domain.Member{UserID: id, DisplayName: name})
		if name != "" {
			r.Members = append(r.Members, name)
		}
	}
	switch {
	case th.group && th.title != "":
		r.Name = th.title
	case th.group:
		r.Name = strings.Join(r.Members, ", ")
	case len(r.Members) > 0:
		r.Name = r.Members[0]
	}
	return r, members
}

// names is every name the inbox knows, as the directory takes them.
func (in *inbox) names() []domain.PersonName {
	in.mu.Lock()
	defer in.mu.Unlock()
	out := make([]domain.PersonName, 0, len(in.people))
	for id, name := range in.people {
		out = append(out, domain.PersonName{ID: id, Name: name, Rank: domain.RankChosen})
	}
	return out
}

// keepThread caches a thread's room, members and, when they changed, its people.
func (a *Adapter) keepThread(ctx context.Context, c *conn, th *thread, met bool) {
	if a.cache == nil {
		return
	}
	room, members := c.inbox.room(c.self, th)
	if err := a.cache.AddRooms(ctx, owner(c.self), []domain.Room{room}); err != nil {
		a.log.Warn("cache a thread failed", "account", c.account.Name, "err", err)
		return
	}
	if err := a.cache.SaveMembers(ctx, room.ID, members); err != nil {
		a.log.Warn("cache a thread's people failed", "account", c.account.Name, "err", err)
	}
	if met {
		if err := a.cache.SetPeople(ctx, "instagram:"+c.self, c.inbox.names(), nil); err != nil {
			a.log.Warn("keep who people are failed", "account", c.account.Name, "err", err)
		}
	}
}

// takeThread keeps a listed thread: its room and people, the account's read position
// (Instagram's read watermark for it, or none when marked unread), and, when it has
// news the cache lacks, its newest messages.
func (a *Adapter) takeThread(ctx context.Context, c *conn, client *instameow.Client, t *slidetypes.ThreadInfo) {
	th, met := c.inbox.learn(t, c.self)
	a.keepThread(ctx, c, th, met)
	if a.cache == nil {
		return
	}
	room := roomID(c.self, th.id)
	for _, r := range t.SlideReadReceipts {
		if fbid(r.ParticipantFBID) == c.self && !t.MarkedAsUnread {
			if err := a.cache.SaveUnread(ctx, domain.Unread{RoomID: room}, r.WatermarkTimestampMS.UnixMilli()); err != nil {
				a.log.Warn("save a thread's read position failed", "account", c.account.Name, "err", err)
			}
		}
	}
	if t.MarkedAsUnread {
		if err := a.cache.SaveUnread(ctx, domain.Unread{RoomID: room, Notifications: 1}, 0); err != nil {
			a.log.Warn("mark a thread unread failed", "account", c.account.Name, "err", err)
		}
	}
	if a.lacksNews(ctx, room, t.LastActivityTimestampMS.Time) {
		a.fetchRecent(ctx, c, client, th.id)
		time.Sleep(fetchPause)
	}
	a.recount(ctx, room)
}

// lacksNews is whether a room's newest cached message is older than its last activity.
func (a *Adapter) lacksNews(ctx context.Context, room domain.RoomID, active time.Time) bool {
	latest, err := a.cache.LatestEvents(ctx, []domain.RoomID{room})
	if err != nil || active.IsZero() {
		return false
	}
	event, ok := latest[room]
	if !ok {
		return true
	}
	msg, cached, err := a.cache.MessageByID(ctx, room, event)
	return err == nil && (!cached || msg.Timestamp.Before(active))
}

// fetchRecent caches a thread's newest messages (Instagram's first page of it), quietly:
// they are history, not news.
func (a *Adapter) fetchRecent(ctx context.Context, c *conn, client *instameow.Client, chat string) {
	if client == nil {
		return
	}
	resp, err := client.GetThread(ctx, slidetypes.MakeGetThreadInfoRequest(chat))
	if err != nil {
		a.log.Warn("fetch a thread's messages failed", "account", c.account.Name, "err", err)
		return
	}
	t := resp.ThreadInfo.AsIGDirectThread
	if t == nil || t.SlideMessages == nil {
		return
	}
	a.cachePage(ctx, c, chat, t.SlideMessages.Edges)
}

// cachePage caches a page of a thread's messages, with their reactions and where their
// attachments are.
func (a *Adapter) cachePage(ctx context.Context, c *conn, chat string, edges []slidetypes.Node[*slidetypes.Message]) []domain.Message {
	var msgs []domain.Message
	var sources []mediaSource
	var reactions []domain.Reaction
	for _, e := range edges {
		m, source, ok := message(c.self, chat, e.Node)
		if !ok {
			continue
		}
		msgs, sources = append(msgs, m), append(sources, source)
		for _, r := range e.Node.Reactions {
			if r != nil && r.Reaction != "" {
				reactions = append(reactions, c.reaction(m.ID, r))
			}
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	room := roomID(c.self, chat)
	if err := a.save(ctx, room, msgs...); err != nil {
		a.log.Warn("cache a thread's messages failed", "account", c.account.Name, "err", err)
		return nil
	}
	for i := range msgs {
		a.keepSource(ctx, msgs[i], sources[i])
	}
	if len(reactions) > 0 {
		if err := a.cache.SaveReactions(ctx, reactions); err != nil {
			a.log.Warn("cache reactions failed", "account", c.account.Name, "err", err)
		}
	}
	slices.SortFunc(msgs, func(x, y domain.Message) int { return x.Timestamp.Compare(y.Timestamp) })
	return msgs
}

// reaction is r on the message target, one per sender; its log ID is kept, which a
// removal may name alone.
func (c *conn) reaction(target domain.EventID, r *slidetypes.Reaction) domain.Reaction {
	sender := person(fbid(r.SenderFBID))
	out := domain.Reaction{
		ID: domain.EventID(string(target) + ":reaction:" + fbid(r.SenderFBID)), RoomID: roomOfEvent(target),
		Target: target, Sender: sender, Key: r.Reaction,
	}
	if r.LogMessageID != "" {
		c.inbox.mu.Lock()
		c.inbox.reactions[r.LogMessageID] = out
		c.inbox.mu.Unlock()
	}
	return out
}

// roomOfEvent is the room a message's ID names.
func roomOfEvent(event domain.EventID) domain.RoomID {
	id := string(event)
	if i := strings.LastIndex(id, "/"); i > 0 {
		return domain.RoomID(id[:i])
	}
	return ""
}

// syncInbox lists the inbox page by page, from first (the page connecting loaded),
// keeping each thread.
func (a *Adapter) syncInbox(ctx context.Context, c *conn, client *instameow.Client, first *slidetypes.Mailbox) {
	if first == nil {
		return
	}
	self, err := strconv.ParseInt(c.self, 10, 64)
	if err != nil {
		return
	}
	page := first.ThreadsByFolder
	for i := 0; ; i++ {
		for _, e := range page.Edges {
			if t := e.Node.AsIGDirectThread; t != nil {
				a.takeThread(ctx, c, client, t)
			}
		}
		a.roomsChanged()
		if !page.PageInfo.HasNextPage || page.PageInfo.EndCursor == "" || i >= maxPages || ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pagePause):
		}
		resp, err := client.PaginateMailbox(ctx, slidetypes.MakePaginateMailboxRequest(self, page.PageInfo.EndCursor, "INBOX", nil))
		if err != nil || resp.Mailbox == nil {
			a.log.Warn("list the rest of the inbox failed", "account", c.account.Name, "err", err)
			return
		}
		page = resp.Mailbox.ThreadsByFolder
	}
}

// errNotConnected is a thread to fetch while the account is not connected.
var errNotConnected = errors.New("instagram: not connected")

// ensureThread fetches and keeps a thread the inbox does not know yet.
func (a *Adapter) ensureThread(ctx context.Context, c *conn, client *instameow.Client, chat string) error {
	if c.inbox.thread(chat) != nil {
		return nil
	}
	if client == nil {
		return errNotConnected
	}
	resp, err := client.GetThread(ctx, slidetypes.MakeGetThreadInfoRequest(chat))
	if err != nil {
		return fmt.Errorf("fetch the thread: %w", err)
	}
	t := resp.ThreadInfo.AsIGDirectThread
	if t == nil {
		return fmt.Errorf("instagram has no thread %s", chat)
	}
	th, met := c.inbox.learn(t, c.self)
	a.keepThread(ctx, c, th, met)
	a.roomsChanged()
	return nil
}
