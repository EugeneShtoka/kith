package whatsapp

import (
	"context"
	"fmt"
	"maps"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A WhatsApp channel (newsletter) is a room only its admins post in. Followed channels
// come with the group listing; one with nothing cached gets its latest posts fetched
// once, as read history.

// channelHistory is how many latest posts a channel's first fetch takes.
const channelHistory = 50

// errChannelReadOnly refuses posting in a channel the account only follows.
var errChannelReadOnly = fmt.Errorf("%w: only the channel's admins post in it", api.ErrNoPower)

// errChannelReaction: channel reactions need the post's server ID, which kith does
// not keep.
var errChannelReaction = fmt.Errorf("%w: reacting in WhatsApp channels is not supported yet", api.ErrNotOnNetwork)

// channel is the listing's info on one followed channel.
type channel struct {
	name string
	role types.NewsletterRole
}

// isChannel reports whether a WhatsApp room is a channel.
func isChannel(roomID domain.RoomID) bool {
	jid, err := types.ParseJID(domain.ParseID(string(roomID)).Native)
	return err == nil && jid.Server == types.NewsletterServer
}

// channelRooms converts followed channels into rooms plus their info.
func channelRooms(account string, metas []*types.NewsletterMetadata) ([]domain.Room, map[domain.RoomID]channel) {
	rooms := make([]domain.Room, 0, len(metas))
	known := make(map[domain.RoomID]channel, len(metas))
	for _, m := range metas {
		if m == nil {
			continue
		}
		id := roomID(account, m.ID)
		ch := channel{name: m.ThreadMeta.Name.Text, role: types.NewsletterRoleSubscriber}
		if m.ViewerMeta != nil && m.ViewerMeta.Role != "" {
			ch.role = m.ViewerMeta.Role
		}
		rooms = append(rooms, domain.Room{ID: id, Name: ch.name, Topic: m.ThreadMeta.Description.Text})
		known[id] = ch
	}
	return rooms, known
}

// useChannels replaces an account's channel info with the latest listing's.
func (a *Adapter) useChannels(account Account, known map[domain.RoomID]channel) {
	owner := domain.AccountRooms(domain.ProtocolWhatsApp, account.Digits)
	a.mu.Lock()
	defer a.mu.Unlock()
	for id := range a.channels {
		if owner.Owns(id) {
			delete(a.channels, id)
		}
	}
	maps.Copy(a.channels, known)
}

// channelOf is the last listing's info on a channel; false when none.
func (a *Adapter) channelOf(roomID domain.RoomID) (channel, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ch, ok := a.channels[roomID]
	return ch, ok
}

// mayPost refuses posting in a channel unless the account is an admin. A channel not
// yet listed is refused too, since follower and admin are indistinguishable then.
func (a *Adapter) mayPost(roomID domain.RoomID) error {
	if !isChannel(roomID) {
		return nil
	}
	if ch, ok := a.channelOf(roomID); ok && (ch.role == types.NewsletterRoleAdmin || ch.role == types.NewsletterRoleOwner) {
		return nil
	}
	return errChannelReadOnly
}

// channelEvents converts fetched channel posts into live-style message events.
func channelEvents(jid types.JID, posts []*types.NewsletterMessage) []*events.Message {
	out := make([]*events.Message, 0, len(posts))
	for _, p := range posts {
		if p == nil || p.Message == nil {
			continue
		}
		out = append(out, &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: jid, Sender: jid},
				ID:            p.MessageID, ServerID: p.MessageServerID, Timestamp: p.Timestamp,
			},
			Message: p.Message,
		})
	}
	return out
}

// fetchChannelHistory caches latest posts, as read history, for channels with nothing
// cached. A failed channel is retried on the next listing.
func (a *Adapter) fetchChannelHistory(ctx context.Context, account Account, client *whatsmeow.Client, rooms []domain.Room) {
	if a.cache == nil || len(rooms) == 0 {
		return
	}
	ids := make([]domain.RoomID, len(rooms))
	for i := range rooms {
		ids[i] = rooms[i].ID
	}
	latest, err := a.cache.LatestEvents(ctx, ids)
	if err != nil {
		a.log.Warn("read which channels have history failed", "account", account.Name, "err", err)
		return
	}
	for _, room := range ids {
		if latest[room] != "" {
			continue
		}
		jid, err := types.ParseJID(domain.ParseID(string(room)).Native)
		if err != nil {
			continue
		}
		posts, err := client.GetNewsletterMessages(ctx, jid, &whatsmeow.GetNewsletterMessagesParams{Count: channelHistory})
		if err != nil {
			a.log.Warn("fetch a channel's posts failed", "account", account.Name, "room", room, "err", err)
			continue
		}
		var msgs []arrived
		for _, evt := range channelEvents(jid, posts) {
			if in, ok := a.convert(ctx, account, client, evt); ok {
				msgs = append(msgs, in)
			}
		}
		if len(msgs) > 0 {
			a.recordHistory(ctx, account, client, room, msgs, "", 0)
		}
	}
}
