package telegram

import (
	"context"
	"fmt"
	"slices"

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Telegram folders can be copied into tags (domain.Grouping) as a snapshot: included
// chats plus chats of included kinds, minus excluded ones. Read/muted filters are
// dynamic, so they are skipped and reported. Archived is handled by [telegram.archive].

// chatKind is the chat properties folder filters test.
type chatKind struct {
	contact, nonContact, group, broadcast, bot, archived bool
}

// Groupings is an account's folders as a snapshot, with the account's rooms.
func (a *Adapter) Groupings(ctx context.Context, account string) (domain.RoomOwner, []domain.Grouping, error) {
	i := slices.IndexFunc(a.connected(), func(c conn) bool { return c.account.Name == account })
	if i < 0 {
		return "", nil, fmt.Errorf("telegram: account %s: %w", account, api.ErrNetworkOff)
	}
	c := a.connected()[i]
	res, err := c.client.API().MessagesGetDialogFilters(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("telegram: %s's folders: %w", account, err)
	}
	elems, err := readDialogs(ctx, c.client.API(), a.log)
	if err != nil {
		return "", nil, fmt.Errorf("telegram: %s's chats: %w", account, err)
	}
	kinds := chatKinds(c.user, elems)
	var out []domain.Grouping
	for _, f := range res.Filters {
		switch f := f.(type) {
		case *tg.DialogFilter:
			out = append(out, folder(c.user, f, kinds))
		case *tg.DialogFilterChatlist: // a shared folder: the chats it names
			out = append(out, domain.Grouping{Name: f.Title.Text, Rooms: named(c.user, append(slices.Clone(f.PinnedPeers), f.IncludePeers...), kinds)})
		}
	}
	return domain.AccountRooms(domain.ProtocolTelegram, fmt.Sprint(c.user)), out, nil
}

// folder is a folder's included chats, then chats of included kinds not excluded.
func folder(self int64, f *tg.DialogFilter, kinds map[domain.RoomID]chatKind) domain.Grouping {
	g := domain.Grouping{Name: f.Title.Text, Rooms: named(self, append(slices.Clone(f.PinnedPeers), f.IncludePeers...), kinds)}
	out := named(self, f.ExcludePeers, kinds)
	for room, k := range kinds {
		takes := (f.Contacts && k.contact) || (f.NonContacts && k.nonContact) || (f.Groups && k.group) ||
			(f.Broadcasts && k.broadcast) || (f.Bots && k.bot)
		if takes && (!f.ExcludeArchived || !k.archived) && !slices.Contains(out, room) && !slices.Contains(g.Rooms, room) {
			g.Rooms = append(g.Rooms, room)
		}
	}
	slices.Sort(g.Rooms)
	if f.ExcludeRead {
		g.Left = append(g.Left, "read chats are not left out: a copy keeps chats, not whether they are read")
	}
	if f.ExcludeMuted {
		g.Left = append(g.Left, "muted chats are not left out: a copy keeps chats, not whether they are muted")
	}
	return g
}

// named is the account's rooms among peers.
func named(self int64, peers []tg.InputPeerClass, kinds map[domain.RoomID]chatKind) []domain.RoomID {
	var out []domain.RoomID
	for _, p := range peers {
		var chat int64
		switch p := p.(type) {
		case *tg.InputPeerUser:
			chat = p.UserID
		case *tg.InputPeerSelf:
			chat = self
		case *tg.InputPeerChat:
			chat = -p.ChatID
		case *tg.InputPeerChannel:
			chat = -(channelMark + p.ChannelID)
		default:
			continue
		}
		if room := roomID(self, chat); !slices.Contains(out, room) {
			if _, ok := kinds[room]; ok {
				out = append(out, room)
			}
		}
	}
	return out
}

// chatKinds is each chat's chatKind.
func chatKinds(self int64, elems []dialog) map[domain.RoomID]chatKind {
	out := map[domain.RoomID]chatKind{}
	for _, e := range elems {
		var k chatKind
		var chat int64
		switch p := e.peer.(type) {
		case *tg.InputPeerSelf:
			chat = self
		case *tg.InputPeerUser:
			chat = p.UserID
			if u, ok := e.entities.User(p.UserID); ok {
				k.bot = u.Bot
				k.contact = !u.Bot && u.Contact
				k.nonContact = !u.Bot && !u.Contact && p.UserID != self
			}
		case *tg.InputPeerChat:
			chat, k.group = -p.ChatID, true
		case *tg.InputPeerChannel:
			chat = -(channelMark + p.ChannelID)
			if c, ok := e.entities.Channel(p.ChannelID); ok {
				k.broadcast, k.group = c.Broadcast, !c.Broadcast
			}
		default:
			continue
		}
		if e.info != nil {
			folder, _ := e.info.GetFolderID()
			k.archived = folder == archiveFolder
		}
		out[roomID(self, chat)] = k
	}
	return out
}
