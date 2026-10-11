package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// An account's dialogs are its rooms: private chats (self is Saved Messages), basic
// groups, supergroups and channels. Left, migrated and removed-from chats are skipped;
// secret chats never reach this client. Each account is a space (accountSpaces).

const dialogsPage = 100

// channelMark marks channel and supergroup IDs as the Bot API does (-100…), since
// user, chat and channel IDs overlap.
const channelMark = 1_000_000_000_000

// roomID is a Telegram chat as one account sees it, by its marked peer ID.
func roomID(self, peer int64) domain.RoomID {
	return domain.RoomID(domain.NativeID(domain.ProtocolTelegram, strconv.FormatInt(self, 10), strconv.FormatInt(peer, 10)))
}

// topicSep joins a forum's chat and one of its topics in the topic's room ID.
const topicSep = "~"

// topicRoomID is a forum topic's room: the chat's ID plus the topic's ID (its first
// message). The General topic is the forum's own room.
func topicRoomID(self, peer int64, topic int) domain.RoomID {
	return domain.RoomID(string(roomID(self, peer)) + topicSep + strconv.Itoa(topic))
}

// chatRoom is the topic's room when topic is non-zero, else the chat's.
func chatRoom(self, peer int64, topic int) domain.RoomID {
	if topic != 0 {
		return topicRoomID(self, peer, topic)
	}
	return roomID(self, peer)
}

// inRoom is the event ID of message id in room.
func inRoom(room domain.RoomID, id int) domain.EventID {
	return domain.EventID(string(room) + "/" + strconv.Itoa(id))
}

// forumSpaceID is a forum as a space: its topics are its rooms.
func forumSpaceID(self, peer int64) domain.SpaceID {
	return domain.SpaceID(domain.NativeID(domain.ProtocolTelegram, strconv.FormatInt(self, 10), "forum"+strconv.FormatInt(peer, 10)))
}

// accountSpaceID is the space an account is.
func accountSpaceID(self int64) domain.SpaceID {
	return domain.SpaceID(domain.NativeID(domain.ProtocolTelegram, strconv.FormatInt(self, 10), "account"))
}

// accessHashes are the user and channel access hashes from the dialogs, by ID.
type accessHashes struct {
	users, channels map[int64]int64
}

type listing struct {
	rooms    []domain.Room
	members  map[domain.RoomID][]domain.Member
	hashes   accessHashes
	archived map[domain.RoomID]bool
	forums   map[domain.RoomID]bool
	// people and links feed the directory; links tie a user to their phone number.
	people []domain.PersonName
	links  []domain.PersonLink
}

// dialog is one dialog with its latest message (nil if the page lacked it) and the
// entities its page carried.
type dialog struct {
	peer     tg.InputPeerClass
	top      tg.MessageClass
	entities peer.Entities
	// info carries unread counts and read position; nil when unknown.
	info *tg.Dialog
}

// listed turns dialogs into rooms (a private chat's peer as its member) and collects
// access hashes. self is the account's user ID.
func listed(self int64, elems []dialog) listing {
	l := listing{
		members:  map[domain.RoomID][]domain.Member{},
		archived: map[domain.RoomID]bool{},
		forums:   map[domain.RoomID]bool{},
		hashes:   accessHashes{users: map[int64]int64{}, channels: map[int64]int64{}},
	}
	for _, e := range elems {
		room, member, ok := l.room(self, e)
		if !ok {
			continue
		}
		if e.info != nil {
			folder, _ := e.info.GetFolderID()
			room.Archived = folder == archiveFolder
			l.archived[room.ID] = room.Archived
		}
		l.forums[room.ID] = room.Forum
		room.Deleting = deletingOf(room.ID)
		l.rooms = append(l.rooms, room)
		if member != nil {
			l.members[room.ID] = []domain.Member{*member}
		}
	}
	domain.SortRooms(l.rooms)
	l.people, l.links = peopleOf(self, elems)
	return l
}

// peopleOf is every user the pages carried, for the directory: contacts at saved rank,
// linked to their phone so names carry across networks. Skips deleted users and self.
func peopleOf(self int64, elems []dialog) ([]domain.PersonName, []domain.PersonLink) {
	seen := map[int64]bool{}
	var names []domain.PersonName
	var links []domain.PersonLink
	for _, e := range elems {
		for id, u := range e.entities.Users() {
			if seen[id] || id == self || u.Deleted {
				continue
			}
			seen[id] = true
			person := personID(id)
			if u.Phone != "" {
				links = append(links, domain.PersonLink{ID: person, Other: domain.PhoneID(domain.PhoneDigits(u.Phone))})
			}
			name := strings.TrimSpace(userName(u))
			if name == "" {
				continue
			}
			rank := domain.RankChosen
			if u.Contact {
				rank = domain.RankSaved
			}
			names = append(names, domain.PersonName{ID: person, Name: name, Rank: rank})
		}
	}
	return names, links
}

// room is one dialog as a room, plus a private chat's peer; false for a skipped dialog.
func (l *listing) room(self int64, e dialog) (domain.Room, *domain.Member, bool) {
	room := domain.Room{Membership: domain.MembershipJoin}
	switch p := e.peer.(type) {
	case *tg.InputPeerSelf:
		room.ID, room.Name, room.IsDirect = roomID(self, self), "Saved Messages", true
		return room, nil, true
	case *tg.InputPeerUser:
		if p.UserID == self {
			room.ID, room.Name, room.IsDirect = roomID(self, self), "Saved Messages", true
			return room, nil, true
		}
		u, ok := e.entities.User(p.UserID)
		if !ok {
			return room, nil, false
		}
		l.hashes.users[p.UserID] = p.AccessHash
		room.ID, room.Name, room.IsDirect = roomID(self, p.UserID), personName(u), true
		return room, &domain.Member{UserID: personID(p.UserID), DisplayName: room.Name}, true
	case *tg.InputPeerChat:
		c, ok := e.entities.Chat(p.ChatID)
		if !ok || c.Left || c.Deactivated || c.MigratedTo != nil {
			return room, nil, false
		}
		room.ID, room.Name = roomID(self, -p.ChatID), c.Title
		return room, nil, true
	case *tg.InputPeerChannel:
		c, ok := e.entities.Channel(p.ChannelID)
		if !ok || c.Left {
			return room, nil, false
		}
		l.hashes.channels[p.ChannelID] = p.AccessHash
		room.ID, room.Name, room.Forum = roomID(self, -(channelMark+p.ChannelID)), c.Title, c.Forum
		return room, nil, true
	}
	return room, nil, false
}

// personName is a user's display name, falling back to phone then ID.
func personName(u *tg.User) string {
	if u.Deleted {
		return "Deleted Account"
	}
	if name := strings.TrimSpace(userName(u)); name != "" {
		return name
	}
	if phone := u.Phone; phone != "" {
		return "+" + phone
	}
	return strconv.FormatInt(u.ID, 10)
}

// errNotListed: the account is not connected, or a newer login replaced it.
var errNotListed = errors.New("telegram: the account is not connected")

// list reads all of an account's dialogs and writes them over its cached rooms,
// serialized so an older listing never lands over a newer one.
func (a *Adapter) list(ctx context.Context, account Account, gen int, self int64, client *telegram.Client) ([]domain.Room, error) {
	a.refreshing.Lock()
	defer a.refreshing.Unlock()
	fetched := time.Now()
	elems, err := readDialogs(ctx, client.API(), a.log.With("account", account.Name))
	if err != nil {
		return nil, fmt.Errorf("telegram: list %s's chats: %w", account.Name, err)
	}
	l := listed(self, elems)
	if !a.keepHashes(account, gen, l.hashes) {
		return nil, errNotListed
	}
	// Topics must be in the listing, or saving sweeps them.
	topics := map[int64]forumListing{}
	for _, e := range elems {
		if !isForum(e) {
			continue
		}
		forum := -(channelMark + e.peer.(*tg.InputPeerChannel).ChannelID)
		got, err := forumTopics(ctx, client.API(), e.peer)
		if err != nil {
			// Keep the cached topics until a listing can read them again.
			a.log.Warn("list a forum's topics failed", "room", roomID(self, forum), "err", err)
			l.rooms = append(l.rooms, a.cachedTopicRooms(ctx, roomID(self, forum))...)
			continue
		}
		topics[forum] = got
		l.rooms = append(l.rooms, topicRooms(self, forum, got.topics)...)
	}
	if err := a.save(ctx, self, l, fetched); err != nil {
		return nil, err
	}
	a.settleFollowed(ctx, self, elems)
	a.cacheTops(ctx, self, elems, l.rooms)
	a.listedUnread(ctx, self, elems, l.rooms, fetched)
	for forum, got := range topics {
		a.forgetForumThreads(ctx, self, forum, got.topics)
		a.cacheTopicTops(ctx, self, got)
		a.listedTopicsUnread(ctx, self, forum, got.topics, fetched)
	}
	return l.rooms, nil
}

// cachedTopicRooms is the cached topic rooms of forum.
func (a *Adapter) cachedTopicRooms(ctx context.Context, forum domain.RoomID) []domain.Room {
	if a.cache == nil {
		return nil
	}
	rooms, err := a.cache.Rooms(ctx)
	if err != nil {
		return nil
	}
	var out []domain.Room
	for i := range rooms {
		if in, ok := forumOf(rooms[i].ID); ok && in == forum {
			out = append(out, rooms[i])
		}
	}
	return out
}

// archiveFolder is Telegram's Archived folder; 0 is the main list.
const archiveFolder = 1

var folders = []int{0, archiveFolder}

// readDialogs reads every dialog, folder by folder. Not gotd's iterator, which costs
// an extra request after the last page. A short page doesn't mean the end, so a folder
// is read until Telegram's count is reached or a page brings nothing new.
func readDialogs(ctx context.Context, api *tg.Client, log *slog.Logger) ([]dialog, error) {
	var out []dialog
	for _, folder := range folders {
		req := &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: dialogsPage}
		req.SetFolderID(folder)
		seen, total := map[int64]bool{}, 0
		for {
			res, err := api.MessagesGetDialogs(ctx, req)
			if err != nil {
				return nil, err //nolint:wrapcheck // list wraps it, naming the account
			}
			page, count, err := dialogPage(res)
			if err != nil {
				return nil, err
			}
			total = count
			fresh := 0
			for _, d := range page.dialogs {
				chat, ok := markedPeer(d.Peer)
				if !ok || seen[chat] {
					continue
				}
				seen[chat] = true
				fresh++
				if input, err := page.entities.ExtractPeer(d.Peer); err == nil {
					out = append(out, dialog{peer: input, top: page.top(d), entities: page.entities, info: d})
				}
			}
			if fresh == 0 || len(seen) >= total || !page.next(req) {
				break
			}
		}
		log.Info("listed a folder's chats", "folder", folder, "read", len(seen), "telegram_counts", total)
	}
	return out, nil
}

// page is one messages.getDialogs answer.
type page struct {
	dialogs  []*tg.Dialog
	messages tg.MessageClassArray
	entities peer.Entities
}

// dialogPage is an answer as a page, plus the folder's total dialog count.
func dialogPage(res tg.MessagesDialogsClass) (page, int, error) {
	var p page
	var all []tg.DialogClass
	total := 0
	switch r := res.(type) {
	case *tg.MessagesDialogs: // every dialog at once
		all, p.messages, p.entities = r.Dialogs, r.Messages, peer.EntitiesFromResult(r)
		total = len(r.Dialogs)
	case *tg.MessagesDialogsSlice:
		all, p.messages, p.entities, total = r.Dialogs, r.Messages, peer.EntitiesFromResult(r), r.Count
	default:
		return p, 0, fmt.Errorf("telegram: unexpected dialogs answer %T", res)
	}
	for _, d := range all {
		if dlg, ok := d.(*tg.Dialog); ok { // not a folder's own entry
			p.dialogs = append(p.dialogs, dlg)
		}
	}
	return p, total, nil
}

// top is a dialog's latest message from the page.
func (p page) top(d *tg.Dialog) tg.MessageClass {
	chat, ok := markedPeer(d.Peer)
	if !ok {
		return nil
	}
	for _, m := range p.messages {
		if m.GetID() != d.TopMessage {
			continue
		}
		if msg, ok := m.AsNotEmpty(); ok && samePeer(msg.GetPeerID(), chat) {
			return m
		}
	}
	return nil
}

// next points req past p's last dialog; false if that dialog's peer is unresolved.
func (p page) next(req *tg.MessagesGetDialogsRequest) bool {
	last := p.dialogs[len(p.dialogs)-1]
	input, err := p.entities.ExtractPeer(last.Peer)
	if err != nil {
		return false
	}
	req.OffsetPeer, req.OffsetID, req.OffsetDate = input, last.TopMessage, 0
	for _, m := range p.messages {
		if m.GetID() == last.TopMessage {
			if msg, ok := m.AsNotEmpty(); ok {
				req.OffsetDate = msg.GetDate()
			}
		}
	}
	return true
}

// keepHashes stores hashes on the connection of login gen; false if it was replaced.
func (a *Adapter) keepHashes(account Account, gen int, hashes accessHashes) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.conns[account.Name]
	if c == nil || c.gen != gen {
		return false
	}
	c.hashes = hashes
	return true
}

// save writes a listing over the account's cached rooms. Rooms heard from since
// fetched (e.g. a chat just begun) are kept so the sweep doesn't drop their history.
func (a *Adapter) save(ctx context.Context, self int64, l listing, fetched time.Time) error {
	if a.cache == nil {
		return nil
	}
	a.listing.Lock()
	defer a.listing.Unlock()
	owner := domain.AccountRooms(domain.ProtocolTelegram, strconv.FormatInt(self, 10))
	kept, err := a.keptRooms(ctx, owner, l.rooms, fetched)
	if err != nil {
		return err
	}
	if err := a.cache.SaveRooms(ctx, owner, append(slices.Clone(l.rooms), kept...)); err != nil {
		return fmt.Errorf("telegram: cache the chats: %w", err)
	}
	if err := a.cache.SetArchived(ctx, a.listedArchive(l.archived, fetched)); err != nil {
		return fmt.Errorf("telegram: cache which chats are archived: %w", err)
	}
	if err := a.cache.SetForums(ctx, l.forums); err != nil {
		return fmt.Errorf("telegram: cache which chats are forums: %w", err)
	}
	if err := a.cache.SetPeople(ctx, "telegram:"+strconv.FormatInt(self, 10), l.people, l.links); err != nil {
		return fmt.Errorf("telegram: cache who people are: %w", err)
	}
	for id, members := range l.members {
		if err := a.cache.SaveMembers(ctx, id, members); err != nil {
			return fmt.Errorf("telegram: cache the people of %s: %w", id, err)
		}
	}
	if a.onRoomsChanged != nil {
		a.onRoomsChanged()
	}
	return nil
}

// keptRooms is owner's unlisted rooms heard from since fetched. Caller holds listing.
func (a *Adapter) keptRooms(ctx context.Context, owner domain.RoomOwner, listed []domain.Room, fetched time.Time) ([]domain.Room, error) {
	rooms, err := a.cache.Rooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("telegram: read cached rooms: %w", err)
	}
	return slices.DeleteFunc(rooms, func(r domain.Room) bool {
		return !owner.Owns(r.ID) || a.heard[r.ID].Before(fetched) ||
			slices.ContainsFunc(listed, func(l domain.Room) bool { return l.ID == r.ID })
	}), nil
}

// cacheTops caches each listed chat's latest message as history (no stream, no
// notification), so a chat shows it before being opened.
func (a *Adapter) cacheTops(ctx context.Context, self int64, elems []dialog, listed []domain.Room) {
	if a.cache == nil {
		return
	}
	for _, e := range elems {
		if e.top == nil {
			continue
		}
		msg, ok := incoming(self, e.top, e.entities)
		if !ok || !slices.ContainsFunc(listed, func(r domain.Room) bool { return r.ID == msg.RoomID }) {
			continue
		}
		if err := a.cache.SaveMessages(ctx, msg.RoomID, []domain.Message{msg}); err != nil {
			a.log.Warn("cache a chat's latest message failed", "room", msg.RoomID, "err", err)
		}
	}
}

// Rooms is the cached Telegram rooms of every account.
func (a *Adapter) Rooms(ctx context.Context) ([]domain.Room, error) {
	if a.cache == nil {
		return nil, nil
	}
	rooms, err := a.cache.Rooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("telegram: read cached rooms: %w", err)
	}
	return withDeleting(slices.DeleteFunc(rooms, func(r domain.Room) bool {
		return domain.NetworkOf(string(r.ID)) != domain.ProtocolTelegram
	})), nil
}

// RefreshRooms lists every connected account's dialogs again.
func (a *Adapter) RefreshRooms(ctx context.Context) ([]domain.Room, error) {
	var out []domain.Room
	for _, c := range a.connected() {
		rooms, err := a.list(ctx, c.account, c.gen, c.user, c.client)
		if errors.Is(err, errNotListed) {
			continue // replaced; the new connection lists itself
		}
		if err != nil {
			return nil, err
		}
		out = append(out, rooms...)
	}
	return out, nil
}

// accountSpaces is a space per logged-in account holding its non-forum rooms, and a
// space per forum holding General and its topics. Derived on read, not stored.
func (a *Adapter) accountSpaces(ctx context.Context) ([]domain.Space, error) {
	rooms, err := a.Rooms(ctx)
	if err != nil {
		return nil, err
	}
	forums := map[domain.RoomID]*domain.Space{}
	var order []domain.RoomID
	for i := range rooms {
		if !rooms[i].Forum {
			continue
		}
		parsed := domain.ParseID(string(rooms[i].ID))
		self, err1 := strconv.ParseInt(parsed.Account, 10, 64)
		chat, err2 := strconv.ParseInt(parsed.Native, 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		forums[rooms[i].ID] = &domain.Space{
			ID: forumSpaceID(self, chat), Name: rooms[i].DisplayName(), Children: []domain.RoomID{rooms[i].ID},
			Bridge: domain.ProtocolTelegram, Original: true,
			// Leaving the forum's chat leaves every topic.
			Leaving: domain.LeftByRoom, LeaveBy: rooms[i].ID,
		}
		order = append(order, rooms[i].ID)
	}
	var spaces []domain.Space
	for _, account := range a.accountsNow() {
		self, ok := a.selfOf(account)
		if !ok {
			continue
		}
		owner := domain.AccountRooms(domain.ProtocolTelegram, strconv.FormatInt(self, 10))
		var children []domain.RoomID
		for i := range rooms {
			switch forum, inTopic := forumOf(rooms[i].ID); {
			case !owner.Owns(rooms[i].ID), forums[rooms[i].ID] != nil:
			case inTopic && forums[forum] != nil:
				forums[forum].Children = append(forums[forum].Children, rooms[i].ID)
			default:
				children = append(children, rooms[i].ID)
			}
		}
		spaces = append(spaces, domain.Space{
			ID: accountSpaceID(self), Name: "Telegram " + account.Name, Children: children,
			// The rooms' home, not a space to file into; left by signing out.
			Bridge: domain.ProtocolTelegram, Original: true, Leaving: domain.LeftBySigningOut,
		})
	}
	for _, id := range order {
		spaces = append(spaces, *forums[id])
	}
	return spaces, nil
}

// Spaces is each logged-in account's space.
func (a *Adapter) Spaces(ctx context.Context) ([]domain.Space, error) { return a.accountSpaces(ctx) }

// RefreshSpaces lists the accounts' dialogs again, then answers from the cache.
func (a *Adapter) RefreshSpaces(ctx context.Context) ([]domain.Space, error) {
	if _, err := a.RefreshRooms(ctx); err != nil {
		return nil, err
	}
	return a.accountSpaces(ctx)
}

// CanonicalParent is a room's account space, or its forum's space for forum rooms.
func (a *Adapter) CanonicalParent(ctx context.Context, id domain.RoomID) (domain.SpaceID, error) {
	parsed := domain.ParseID(string(id))
	if parsed.Network != domain.ProtocolTelegram {
		return "", nil
	}
	self, err := strconv.ParseInt(parsed.Account, 10, 64)
	if err != nil {
		return "", fmt.Errorf("telegram: %s names no account: %w", id, err)
	}
	forum, inTopic := forumOf(id)
	if !inTopic {
		forum = id
	}
	if inTopic || a.isForumRoom(ctx, forum) {
		if chat, err := strconv.ParseInt(domain.ParseID(string(forum)).Native, 10, 64); err == nil {
			return forumSpaceID(self, chat), nil
		}
	}
	return accountSpaceID(self), nil
}

// isForumRoom reports whether the cache holds room as a forum's own.
func (a *Adapter) isForumRoom(ctx context.Context, room domain.RoomID) bool {
	rooms, err := a.Rooms(ctx)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(rooms, func(r domain.Room) bool { return r.ID == room && r.Forum })
}

// Members is a room's cached members.
func (a *Adapter) Members(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	if a.cache == nil {
		return nil, nil
	}
	members, err := a.cache.Members(ctx, roomID, limit)
	if err != nil {
		return nil, fmt.Errorf("telegram: read the people of %s: %w", roomID, err)
	}
	return members, nil
}

// RefreshMembers reads the cache; group members arrive with their messages.
func (a *Adapter) RefreshMembers(ctx context.Context, roomID domain.RoomID) ([]domain.Member, error) {
	return a.Members(ctx, roomID, 0)
}

// MentionCandidates is a room's known members.
func (a *Adapter) MentionCandidates(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	return a.Members(ctx, roomID, limit)
}

// DirectCandidates is not supported yet.
func (a *Adapter) DirectCandidates(context.Context, int) ([]domain.Member, error) { return nil, nil }
