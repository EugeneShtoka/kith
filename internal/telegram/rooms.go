package telegram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// An account's dialogs are its rooms: private chats (the chat with oneself is Saved
// Messages), basic groups, supergroups and broadcast channels. A chat left, a basic
// group moved to a supergroup, and one the account was removed from are left out;
// secret chats never reach this client at all. The account itself is one space
// holding its rooms (accountSpaces).

// dialogsPage is how many dialogs one messages.getDialogs page asks for.
const dialogsPage = 100

// channelMark is what a channel's or supergroup's ID is marked with, as the Bot API
// writes it (-100…): users, basic groups and channels have overlapping IDs.
const channelMark = 1_000_000_000_000

// roomID is a Telegram chat as one account sees it, by its marked peer ID.
func roomID(self, peer int64) domain.RoomID {
	return domain.RoomID(domain.NativeID(domain.ProtocolTelegram, strconv.FormatInt(self, 10), strconv.FormatInt(peer, 10)))
}

// topicSep joins a forum's chat and one of its topics in the topic's room ID.
const topicSep = "~"

// topicRoomID is one topic of a forum as a room: the forum's chat, then the topic's
// number (the message that began it). The General topic is the forum's own room.
func topicRoomID(self, peer int64, topic int) domain.RoomID {
	return domain.RoomID(string(roomID(self, peer)) + topicSep + strconv.Itoa(topic))
}

// chatRoom is the room a message of chat is in: its topic's, for one in a topic of a
// forum (topic not 0), else the chat's own.
func chatRoom(self, peer int64, topic int) domain.RoomID {
	if topic != 0 {
		return topicRoomID(self, peer, topic)
	}
	return roomID(self, peer)
}

// inRoom is message id of the room it is in.
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

// accessHashes are what an account's later calls need to name a user or a channel to
// Telegram, by ID, as its dialogs revealed them.
type accessHashes struct {
	users, channels map[int64]int64
}

// listing is what one account's dialogs come to.
type listing struct {
	rooms   []domain.Room
	members map[domain.RoomID][]domain.Member
	hashes  accessHashes
	// archived is, for each listed room, whether it is in Telegram's Archived folder.
	archived map[domain.RoomID]bool
	// forums is, for each listed room, whether it is made of topics.
	forums map[domain.RoomID]bool
	// numbers is the names of the people the listing names whose number Telegram
	// shows, for the phone book.
	numbers []domain.NumberName
}

// dialog is one of an account's dialogs: its peer, as calls name it, its latest
// message (nil when the page did not carry it), and the users, chats and channels its
// page carried.
type dialog struct {
	peer     tg.InputPeerClass
	top      tg.MessageClass
	entities peer.Entities
	// info is the dialog as Telegram listed it (its unread counts, how far it was
	// read); nil when unknown.
	info *tg.Dialog
}

// listed turns an account's dialogs into rooms, a private chat's other person its
// member, and keeps the access hashes they carry. self is the account's own user ID.
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
		l.rooms = append(l.rooms, room)
		if member != nil {
			l.members[room.ID] = []domain.Member{*member}
		}
	}
	domain.SortRooms(l.rooms)
	l.numbers = numberNames(self, elems)
	return l
}

// numberNames is every person the dialogs' pages carried whose number Telegram shows:
// a contact by the name you saved, anyone else by their own.
func numberNames(self int64, elems []dialog) []domain.NumberName {
	seen := map[int64]bool{}
	var out []domain.NumberName
	for _, e := range elems {
		for id, u := range e.entities.Users() {
			if seen[id] || id == self || u.Phone == "" || u.Deleted {
				continue
			}
			seen[id] = true
			name := strings.TrimSpace(userName(u))
			if name == "" {
				continue
			}
			rank := domain.RankChosen
			if u.Contact {
				rank = domain.RankSaved
			}
			out = append(out, domain.NumberName{Phone: domain.PhoneDigits(u.Phone), Name: name, Rank: rank})
		}
	}
	return out
}

// room is one dialog as a room, and the person a private chat is with; ok false for
// one left out.
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

// personName is a user as a person reads them; a deleted account says so.
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

// errNotListed is a listing of an account not connected, or overtaken meanwhile.
var errNotListed = errors.New("telegram: the account is not connected")

// list reads an account's dialogs through client, all of them, and writes them over
// its cached rooms: one listing at a time, so an older one never lands over a newer.
func (a *Adapter) list(ctx context.Context, account Account, gen int, self int64, client *telegram.Client) ([]domain.Room, error) {
	a.refreshing.Lock()
	defer a.refreshing.Unlock()
	fetched := time.Now()
	elems, err := readDialogs(ctx, client.API())
	if err != nil {
		return nil, fmt.Errorf("telegram: list %s's chats: %w", account.Name, err)
	}
	l := listed(self, elems)
	if !a.keepHashes(account, gen, l.hashes) {
		return nil, errNotListed
	}
	// A forum's topics are rooms of the account, so they are listed with it: saving
	// sweeps the rooms a listing does not name.
	topics := map[int64]forumListing{}
	for _, e := range elems {
		if !isForum(e) {
			continue
		}
		forum := -(channelMark + e.peer.(*tg.InputPeerChannel).ChannelID)
		got, err := forumTopics(ctx, client.API(), e.peer)
		if err != nil {
			// The topics the cache holds stay, until a listing can read them again.
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
	a.cacheTops(ctx, self, elems, l.rooms)
	a.listedUnread(ctx, self, elems, l.rooms, fetched)
	for forum, got := range topics {
		a.forgetForumThreads(ctx, self, forum, got.topics)
		a.cacheTopicTops(ctx, self, got)
		a.listedTopicsUnread(ctx, self, forum, got.topics, fetched)
	}
	return l.rooms, nil
}

// cachedTopicRooms is the rooms the cache holds of a forum's topics.
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

// folders are the dialog folders an account's chats are in: the main list, and Archived.
var folders = []int{0, archiveFolder}

// readDialogs reads every dialog of an account, folder by folder, page by page. Not
// gotd's iterator: it asks again after the last page, one request more each listing.
//
// A page may hold fewer dialogs than asked for while more follow, so a folder is read
// until it has as many as Telegram counts in it; a page that brings none not already
// read ends it too, so a server that pages oddly cannot loop.
func readDialogs(ctx context.Context, api *tg.Client) ([]dialog, error) {
	var out []dialog
	for _, folder := range folders {
		req := &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: dialogsPage}
		req.SetFolderID(folder)
		seen := map[int64]bool{}
		for {
			res, err := api.MessagesGetDialogs(ctx, req)
			if err != nil {
				return nil, err //nolint:wrapcheck // list wraps it, naming the account
			}
			page, total, err := dialogPage(res)
			if err != nil {
				return nil, err
			}
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
	}
	return out, nil
}

// page is one messages.getDialogs answer: its dialogs, the top message of each, and
// the users, chats and channels they name.
type page struct {
	dialogs  []*tg.Dialog
	messages tg.MessageClassArray
	entities peer.Entities
}

// dialogPage is an answer as a page, and how many dialogs the folder holds in all.
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

// top is a dialog's latest message, as its page carried it.
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

// next sets req to the page after p: from its last dialog, and that dialog's top
// message. false when it cannot say where that is.
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

// keepHashes keeps a listing's access hashes on the account's connection from login
// gen, unless a newer login replaced it.
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

// save writes a listing, fetched then, over the account's cached rooms, and the
// people of its private chats. A room a message arrived in since the listing was
// fetched (a chat just begun) is kept, though the listing does not name it: the sweep
// would take its history.
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
	if err := a.cache.SetNumberNames(ctx, "telegram:"+strconv.FormatInt(self, 10), l.numbers); err != nil {
		return fmt.Errorf("telegram: cache the names of numbers: %w", err)
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

// keptRooms is an owner's cached rooms a listing fetched then must not sweep: those
// heard from since. Caller holds listing.
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

// cacheTops caches each listed chat's latest message, as the listing carried it: a
// chat shows its last message before it is opened. It is history: nothing is
// streamed or notified.
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

// Rooms is the Telegram rooms in the cache, whichever account sees them.
func (a *Adapter) Rooms(ctx context.Context) ([]domain.Room, error) {
	if a.cache == nil {
		return nil, nil
	}
	rooms, err := a.cache.Rooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("telegram: read cached rooms: %w", err)
	}
	return slices.DeleteFunc(rooms, func(r domain.Room) bool {
		return domain.NetworkOf(string(r.ID)) != domain.ProtocolTelegram
	}), nil
}

// RefreshRooms lists every connected account's dialogs again.
func (a *Adapter) RefreshRooms(ctx context.Context) ([]domain.Room, error) {
	var out []domain.Room
	for _, c := range a.connected() {
		rooms, err := a.list(ctx, c.account, c.gen, c.user, c.client)
		if errors.Is(err, errNotListed) {
			continue // replaced while listing: its successor lists itself
		}
		if err != nil {
			return nil, err
		}
		out = append(out, rooms...)
	}
	return out, nil
}

// accountSpaces is a space per configured account kith has logged in, named after the
// account, its children every room it sees but a forum's; and a space per forum, its
// children the forum's own room (General) and its topics'. Derived on read, not
// stored: a chat begun is in it at once.
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
			// Its rooms' home, as the account's space is the other chats'.
			Bridge: domain.ProtocolTelegram, Original: true,
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
			// Every chat's home: it is where the room belongs, not a space to file into.
			Bridge: domain.ProtocolTelegram, Original: true,
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

// CanonicalParent is a Telegram room's account space; a forum's room's (General's or a
// topic's), the forum's.
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

// Members is a room's members, from the cache: a private chat's other person.
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

// RefreshMembers is the members known: groups' come with their messages.
func (a *Adapter) RefreshMembers(ctx context.Context, roomID domain.RoomID) ([]domain.Member, error) {
	return a.Members(ctx, roomID, 0)
}

// MentionCandidates is who may be mentioned in a room: its members known.
func (a *Adapter) MentionCandidates(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	return a.Members(ctx, roomID, limit)
}

// DirectCandidates is who a private chat may be begun with: none yet.
func (a *Adapter) DirectCandidates(context.Context, int) ([]domain.Member, error) { return nil, nil }
