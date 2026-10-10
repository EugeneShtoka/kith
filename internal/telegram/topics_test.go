package telegram

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgtest"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// forum is the supergroup with topics the topic tests use: channel 31, its room.
const forum = int64(31)

var forumRoom = roomID(42, -(channelMark + forum))

// fmsg is message id of the forum's own room (General), as kith names it.
func fmsg(id int) domain.EventID { return messageID(42, -(channelMark + forum), id) }

// tripsRoom is the forum's topic 10, Trips, as a room; tmsg is message id in it.
var tripsRoom = topicRoomID(42, -(channelMark + forum), 10)

func tmsg(id int) domain.EventID { return inRoom(tripsRoom, id) }

// A message in a forum's topic is in the topic's room, posted to the topic or replying
// to one of its messages; one in General is the forum's own room's. A reply outside a
// forum stays a plain reply, and the message that began a topic, which only says its
// title, is not shown: the title is the room's name.
func TestTopicsAreRooms(t *testing.T) {
	t.Parallel()
	ent := peer.NewEntities(map[int64]*tg.User{7: dana}, nil, nil)
	at := &tg.PeerChannel{ChannelID: forum}
	now := int(time.Now().Unix())
	general := &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 5}
	general.SetReplyToTopID(generalTopic)
	for name, c := range map[string]struct {
		msg         tg.MessageClass
		room        domain.RoomID
		id, replyTo domain.EventID
		shown       bool
	}{
		"topic begun": {&tg.MessageService{ID: 10, PeerID: at, FromID: &tg.PeerUser{UserID: 7}, Date: now,
			Action: &tg.MessageActionTopicCreate{Title: "Trips"}}, "", "", "", false},
		"in the topic": {&tg.Message{ID: 11, PeerID: at, Message: "where to?", Date: now,
			ReplyTo: &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 10}}, tripsRoom, tmsg(11), "", true},
		"a reply in it": {&tg.Message{ID: 12, PeerID: at, Message: "Rila", Date: now,
			ReplyTo: replyInTopic(11, 10)}, tripsRoom, tmsg(12), tmsg(11), true},
		"general": {&tg.Message{ID: 13, PeerID: at, Message: "hi", Date: now}, forumRoom, fmsg(13), "", true},
		"a reply in general": {&tg.Message{ID: 14, PeerID: at, Message: "yes", Date: now, ReplyTo: general},
			forumRoom, fmsg(14), fmsg(5), true},
		"a plain reply": {&tg.Message{ID: 15, PeerID: at, Message: "ok", Date: now,
			ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 13}}, forumRoom, fmsg(15), fmsg(13), true},
		"someone joined": {&tg.MessageService{ID: 16, PeerID: at, Date: now, Action: &tg.MessageActionChatJoinedByLink{}}, "", "", "", false},
	} {
		got, ok := incoming(42, c.msg, ent)
		if ok != c.shown || (ok && (got.RoomID != c.room || got.ID != c.id || got.ReplyTo != c.replyTo || got.ThreadRoot != "")) {
			t.Errorf("%s: (%+v, %v)", name, got, ok)
		}
	}
}

// replyInTopic is a reply header to message reply, in topic.
func replyInTopic(reply, topic int) *tg.MessageReplyHeader {
	h := &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: reply}
	h.SetReplyToTopID(topic)
	return h
}

// A draft written in a topic's room goes into the topic, replying to a message there or
// to none; one in a chat's own room goes as a plain reply, or as none.
func TestADraftGoesIntoItsTopic(t *testing.T) {
	t.Parallel()
	inTrips := chat{conn: conn{user: 42}, id: -(channelMark + forum), topic: 10}
	inForum := chat{conn: conn{user: 42}, id: -(channelMark + forum)}
	for name, c := range map[string]struct {
		ch         chat
		draft      domain.Draft
		reply, top int
		replies    bool
	}{
		"into a topic":   {inTrips, domain.Draft{}, 10, 10, true},
		"a reply in one": {inTrips, domain.Draft{ReplyTo: tmsg(11)}, 11, 10, true},
		"a plain reply":  {inForum, domain.Draft{ReplyTo: fmsg(11)}, 11, 0, true},
		"neither":        {inForum, domain.Draft{}, 0, 0, false},
	} {
		to, ok := replyTo(c.ch, c.draft)
		if ok != c.replies || (ok && (to.ReplyToMsgID != c.reply || to.TopMsgID != c.top)) {
			t.Errorf("%s: (%+v, %v)", name, to, ok)
		}
	}
}

// A forum is a space of rooms: its own (General) and one per topic, each named after
// it, out of the account's space. A listing caches each topic's latest message in its
// room and counts its unread as Telegram does; marking a topic's room read reads the
// topic on Telegram; its history is the topic's. What was cached of the topics while
// they were threads of the forum's room is dropped.
func TestAForumIsASpaceOfItsTopics(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.serveUpdates(&updatesOf{pts: 1})
	d := f.cluster.Dispatch(2, "dc2")
	channel := &tg.Channel{ID: forum, AccessHash: 310, Title: "Hikers", Forum: true, Megagroup: true, Photo: &tg.ChatPhotoEmpty{}}
	at := &tg.PeerChannel{ChannelID: forum}
	inTrips := func(id, reply int, words string) *tg.Message {
		return &tg.Message{ID: id, PeerID: at, FromID: &tg.PeerUser{UserID: 7}, Message: words, Date: 2000 + id, ReplyTo: replyInTopic(reply, 10)}
	}
	d.HandleFunc(tg.MessagesGetForumTopicsRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		topic := func(id int, title string, top, read, unread, mentions int) tg.ForumTopicClass {
			return &tg.ForumTopic{ID: id, Title: title, Date: 1000 + id, TopMessage: top, ReadInboxMaxID: read,
				UnreadCount: unread, UnreadMentionsCount: mentions, Peer: at, FromID: &tg.PeerUser{UserID: 7}, NotifySettings: tg.PeerNotifySettings{}}
		}
		return sendResult(s, r, &tg.MessagesForumTopics{
			Topics:   []tg.ForumTopicClass{topic(1, "General", 20, 18, 2, 0), topic(10, "Trips", 13, 11, 3, 1)},
			Messages: []tg.MessageClass{inTrips(13, 12, "Rila"), &tg.Message{ID: 20, PeerID: at, Message: "hi all", Date: 2020}},
			Chats:    []tg.ChatClass{channel}, Users: []tg.UserClass{dana}, Count: 2,
		})
	})
	d.HandleFunc(tg.MessagesGetRepliesRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.MessagesGetRepliesRequest
		if err := req.Decode(r.Buf); err != nil || req.MsgID != 10 {
			return sendResult(s, r, &tg.MessagesChannelMessages{})
		}
		return sendResult(s, r, &tg.MessagesChannelMessages{Messages: []tg.MessageClass{inTrips(12, 11, "Rila"), inTrips(11, 10, "where to?")},
			Count: 2, Chats: []tg.ChatClass{channel}, Users: []tg.UserClass{dana}})
	})
	var mu sync.Mutex
	var discussed []int
	d.HandleFunc(tg.MessagesReadDiscussionRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.MessagesReadDiscussionRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		mu.Lock()
		discussed = append(discussed, req.MsgID, req.ReadMaxID)
		mu.Unlock()
		return sendResult(s, r, &tg.BoolTrue{})
	})
	st := openStore(t)
	if err := st.SetChannelAccessHash(t.Context(), 42, forum, channel.AccessHash); err != nil {
		t.Fatal(err)
	}
	a, cache := loggedInWithStore(t, f, st)
	// The account's dialogs: the forum (after logging in, whose own listing is empty).
	d.HandleFunc(tg.MessagesGetDialogsRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.MessagesGetDialogsRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		res := &tg.MessagesDialogs{Users: []tg.UserClass{f.user, dana}}
		if folder, _ := req.GetFolderID(); folder == 0 {
			res.Dialogs = []tg.DialogClass{&tg.Dialog{Peer: at, TopMessage: 20, UnreadCount: 9}}
			res.Chats = []tg.ChatClass{channel}
		}
		return sendResult(s, r, res)
	})
	ctx := t.Context()
	// As the forum's topics were cached while they were threads of its room.
	if err := cache.SaveMessages(ctx, forumRoom, []domain.Message{
		{ID: fmsg(10), RoomID: forumRoom, Body: "Trips", Timestamp: time.Unix(1010, 0)},
		{ID: fmsg(11), RoomID: forumRoom, Body: "where to?", ThreadRoot: fmsg(10), Timestamp: time.Unix(2011, 0)},
		{ID: fmsg(19), RoomID: forumRoom, Body: "general talk", Timestamp: time.Unix(2019, 0)},
	}); err != nil {
		t.Fatal(err)
	}

	rooms, err := a.RefreshRooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[domain.RoomID]string{}
	for i := range rooms {
		names[rooms[i].ID] = rooms[i].Name
	}
	if names[forumRoom] != "Hikers" || names[tripsRoom] != "Trips" || len(names) != 2 {
		t.Fatalf("rooms = %v, want the forum's own and its topic's", names)
	}
	spaces, err := a.Spaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range spaces {
		switch s.ID {
		case forumSpaceID(42, -(channelMark + forum)):
			// Left by leaving its chat, General, which takes the topics with it.
			if s.Name != "Hikers" || !slices.Equal(s.Children, []domain.RoomID{forumRoom, tripsRoom}) ||
				s.Leaving != domain.LeftByRoom || s.LeaveBy != forumRoom {
				t.Errorf("forum space = %+v", s)
			}
		case accountSpaceID(42):
			if len(s.Children) != 0 || s.Leaving != domain.LeftBySigningOut {
				t.Errorf("the account's space holds %v (leaving %v), want none of the forum's rooms, left by signing out", s.Children, s.Leaving)
			}
		}
	}
	if len(spaces) != 2 {
		t.Errorf("spaces = %+v, want the account's and the forum's", spaces)
	}
	if home, _ := a.CanonicalParent(ctx, tripsRoom); home != forumSpaceID(42, -(channelMark+forum)) {
		t.Errorf("the topic's home = %q, want the forum", home)
	}

	unread := map[domain.RoomID]domain.Unread{}
	rows, _ := a.CachedUnread(ctx)
	for _, u := range rows {
		unread[u.RoomID] = u
	}
	if u := unread[tripsRoom]; u.Notifications != 3 || u.Highlights != 1 || u.ReadEvent != tmsg(11) {
		t.Errorf("Trips' unread = %+v, want Telegram's 3, one mention, read to 11", u)
	}
	if u := unread[forumRoom]; u.Notifications != 2 {
		t.Errorf("General's unread = %+v, want its own 2, not the forum's 9", u)
	}
	if _, held, _ := cache.MessageByID(ctx, tripsRoom, tmsg(13)); !held {
		t.Error("the topic's latest message is not cached in its room")
	}
	for _, gone := range []domain.EventID{fmsg(10), fmsg(11)} {
		if _, held, _ := cache.MessageByID(ctx, forumRoom, gone); held {
			t.Errorf("%s, of the topic as a thread, is still in the forum's room", gone)
		}
	}
	if _, held, _ := cache.MessageByID(ctx, forumRoom, fmsg(19)); !held {
		t.Error("General's own message was dropped with the threads")
	}

	page, err := a.Timeline(ctx, tripsRoom, "", 20)
	if err != nil || len(page.Messages) != 2 || page.Messages[0].ID != tmsg(11) || page.Messages[1].ReplyTo != tmsg(11) {
		t.Fatalf("Trips' history = %+v, %v", page.Messages, err)
	}
	if result, err := a.MarkRoomsRead(ctx, []domain.RoomID{tripsRoom}, false); err != nil || result.Marked != 1 {
		t.Fatalf("MarkRoomsRead = %+v, %v", result, err)
	}
	mu.Lock()
	if !slices.Equal(discussed, []int{10, 13}) {
		t.Errorf("read on Telegram: %v, want topic 10 up to 13", discussed)
	}
	mu.Unlock()
}

// A supergroup with topics is listed as a forum, and cached so: a plain group or
// channel is not, and a group that drops its topics is a forum no more.
func TestAListingSaysWhichChatsAreForums(t *testing.T) {
	t.Parallel()
	channels := func(forum bool) map[int64]*tg.Channel {
		return map[int64]*tg.Channel{
			31: {ID: 31, AccessHash: 310, Title: "Hikers", Forum: forum, Megagroup: true},
			32: {ID: 32, AccessHash: 320, Title: "News", Broadcast: true},
		}
	}
	dialogs := func(forum bool) []dialog {
		ent := peer.NewEntities(nil, map[int64]*tg.Chat{11: {ID: 11, Title: "Old"}}, channels(forum))
		return []dialog{
			{peer: &tg.InputPeerChannel{ChannelID: 31, AccessHash: 310}, entities: ent},
			{peer: &tg.InputPeerChannel{ChannelID: 32, AccessHash: 320}, entities: ent},
			{peer: &tg.InputPeerChat{ChatID: 11}, entities: ent},
		}
	}
	a, cache := cachedAdapter(t, &memSecrets{values: map[string]string{}})
	forums := func() []domain.RoomID {
		rooms, err := cache.Rooms(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var out []domain.RoomID
		for i := range rooms {
			if rooms[i].Forum {
				out = append(out, rooms[i].ID)
			}
		}
		return out
	}
	if err := a.save(t.Context(), 42, listed(42, dialogs(true)), time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := forums(); !slices.Equal(got, []domain.RoomID{forumRoom}) {
		t.Errorf("forums = %v, want the supergroup with topics", got)
	}
	if err := a.save(t.Context(), 42, listed(42, dialogs(false)), time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := forums(); len(got) != 0 {
		t.Errorf("forums = %v after the group dropped its topics", got)
	}
}

// A message deleted in a forum, named only by number, is found in whichever topic's
// room holds it; someone typing in a topic types in its room.
func TestDeletionsAndTypingFindTheirTopic(t *testing.T) {
	t.Parallel()
	a, cache := cachedAdapter(t, &memSecrets{values: map[string]string{}})
	ctx := t.Context()
	chat := -(channelMark + forum)
	if err := cache.SaveMessages(ctx, tripsRoom, []domain.Message{{ID: tmsg(12), RoomID: tripsRoom, Body: "Rila", Timestamp: time.Unix(2012, 0)}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveMessages(ctx, forumRoom, []domain.Message{{ID: fmsg(13), RoomID: forumRoom, Body: "hi", Timestamp: time.Unix(2013, 0)}}); err != nil {
		t.Fatal(err)
	}
	a.deletedIn(ctx, 42, chat, []int{12, 13})
	for room, id := range map[domain.RoomID]domain.EventID{tripsRoom: tmsg(12), forumRoom: fmsg(13)} {
		if got, _, _ := cache.MessageByID(ctx, room, id); !got.Redacted {
			t.Errorf("%s was not marked deleted", id)
		}
	}

	a.typingNotice(42, chat, 10, 7, &tg.SendMessageTypingAction{})
	for {
		act := <-a.Activity()
		if len(act.Typing) == 0 {
			continue // the deletions' notices
		}
		if act.RoomID != tripsRoom {
			t.Errorf("typing in %s, want the topic's room", act.RoomID)
		}
		break
	}
}
