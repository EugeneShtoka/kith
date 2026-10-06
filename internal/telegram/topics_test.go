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

// fmsg is message id of the forum, as kith names it.
func fmsg(id int) domain.EventID { return messageID(42, -(channelMark + forum), id) }

// A message in a topic is in that topic's thread: posted to the topic, or replying to
// one of its messages. A topic's first message is the thread's root, its title the
// root's words. A reply outside a forum stays a plain reply; other service messages
// are not shown.
func TestTopicsAreThreads(t *testing.T) {
	t.Parallel()
	ent := peer.NewEntities(map[int64]*tg.User{7: dana}, nil, nil)
	at := &tg.PeerChannel{ChannelID: forum}
	now := int(time.Now().Unix())
	for name, c := range map[string]struct {
		msg          tg.MessageClass
		body         string
		thread, repl domain.EventID
		shown        bool
	}{
		"topic begun": {&tg.MessageService{ID: 10, PeerID: at, FromID: &tg.PeerUser{UserID: 7}, Date: now,
			Action: &tg.MessageActionTopicCreate{Title: "Trips"}}, "Trips", "", "", true},
		"in the topic": {&tg.Message{ID: 11, PeerID: at, Message: "where to?", Date: now,
			ReplyTo: &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 10}}, "where to?", fmsg(10), "", true},
		"a reply in it": {&tg.Message{ID: 12, PeerID: at, Message: "Rila", Date: now,
			ReplyTo: replyInTopic(11, 10)}, "Rila", fmsg(10), fmsg(11), true},
		"general": {&tg.Message{ID: 13, PeerID: at, Message: "hi", Date: now,
			ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 11}}, "hi", "", fmsg(11), true},
		"someone joined": {&tg.MessageService{ID: 14, PeerID: at, Date: now, Action: &tg.MessageActionChatJoinedByLink{}}, "", "", "", false},
	} {
		got, ok := incoming(42, c.msg, ent)
		if ok != c.shown || (ok && (got.Body != c.body || got.ThreadRoot != c.thread || got.ReplyTo != c.repl)) {
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

// A draft written in a topic goes into it, replying to a message there or to none; a
// reply outside a forum goes as a plain reply.
func TestADraftGoesIntoItsTopic(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		draft      domain.Draft
		reply, top int
		replies    bool
	}{
		"into a topic":   {domain.Draft{ThreadRoot: fmsg(10)}, 10, 10, true},
		"a reply in one": {domain.Draft{ThreadRoot: fmsg(10), ReplyTo: fmsg(11)}, 11, 10, true},
		"a plain reply":  {domain.Draft{ReplyTo: fmsg(11)}, 11, 0, true},
		"neither":        {domain.Draft{}, 0, 0, false},
	} {
		to, ok := replyTo(forumRoom, c.draft)
		if ok != c.replies || (ok && (to.ReplyToMsgID != c.reply || to.TopMsgID != c.top)) {
			t.Errorf("%s: (%+v, %v)", name, to, ok)
		}
	}
}

// A forum's topics are listed as its threads, each where it was read up to; a topic's
// messages are paged from Telegram; what is unread in a topic counts in the room's
// badge, and marking the topic read clears it, on Telegram too.
func TestAForumsTopicsAreListedReadAndCounted(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.serveUpdates(&updatesOf{pts: 1})
	d := f.cluster.Dispatch(2, "dc2")
	channel := &tg.Channel{ID: forum, AccessHash: 310, Title: "Hikers", Forum: true, Megagroup: true, Photo: &tg.ChatPhotoEmpty{}}
	at := &tg.PeerChannel{ChannelID: forum}
	topic := func(id int, title string, read int) tg.ForumTopicClass {
		return &tg.ForumTopic{ID: id, Title: title, Date: 1000 + id, TopMessage: 12, ReadInboxMaxID: read, Peer: at, FromID: &tg.PeerUser{UserID: 7}, NotifySettings: tg.PeerNotifySettings{}}
	}
	d.HandleFunc(tg.MessagesGetForumTopicsRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		return s.SendResult(r, &tg.MessagesForumTopics{Topics: []tg.ForumTopicClass{topic(1, "General", 0), topic(10, "Trips", 11)}, Chats: []tg.ChatClass{channel}})
	})
	d.HandleFunc(tg.MessagesGetRepliesRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var msgs []tg.MessageClass
		for _, id := range []int{12, 11} { // newest first
			msgs = append(msgs, &tg.Message{ID: id, PeerID: at, Message: "m", Date: 2000 + id, ReplyTo: &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 10}})
		}
		return s.SendResult(r, &tg.MessagesChannelMessages{Messages: msgs, Count: 2, Chats: []tg.ChatClass{channel}})
	})
	var mu sync.Mutex
	var readTo []int
	d.HandleFunc(tg.MessagesReadDiscussionRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.MessagesReadDiscussionRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		mu.Lock()
		readTo = append(readTo, req.MsgID, req.ReadMaxID)
		mu.Unlock()
		return s.SendResult(r, &tg.BoolTrue{})
	})
	st := openStore(t)
	if err := st.SetChannelAccessHash(t.Context(), 42, forum, channel.AccessHash); err != nil {
		t.Fatal(err)
	}
	a, cache := loggedInWithStore(t, f, st)
	ctx := t.Context()
	ent := peer.NewEntities(nil, nil, map[int64]*tg.Channel{forum: channel})
	a.listTopics(ctx, a.connected()[0].client.API(), 42, dialog{peer: &tg.InputPeerChannel{ChannelID: forum, AccessHash: 310}, entities: ent})
	// The room was read after the topics were begun and before the topic's messages,
	// so those count once paged (a topic begun since would count as one new thing).
	a.changeUnread(ctx, forumRoom, time.Time{}, func(u *domain.Unread) int64 {
		u.ReadEvent = fmsg(9)
		return time.Unix(1500, 0).UnixMilli()
	})
	if _, err := a.ThreadPage(ctx, forumRoom, fmsg(10), "", 20); err != nil {
		t.Fatal(err)
	}
	a.listTopics(ctx, a.connected()[0].client.API(), 42, dialog{peer: &tg.InputPeerChannel{ChannelID: forum, AccessHash: 310}, entities: ent})

	if _, held, _ := cache.MessageByID(ctx, forumRoom, fmsg(1)); held {
		t.Error("the General topic was made a thread: it is the room's own timeline")
	}
	threads, err := a.ListThreads(ctx, forumRoom)
	if err != nil || len(threads) != 1 || threads[0].Root != fmsg(10) || threads[0].Title != "Trips" || threads[0].Count != 2 || threads[0].Unread != 1 {
		t.Fatalf("threads = %+v, %v; want Trips, its two messages, the one after where it was read", threads, err)
	}
	unread := func() domain.Unread {
		rows, _ := a.CachedUnread(ctx)
		for _, u := range rows {
			if u.RoomID == forumRoom {
				return u
			}
		}
		return domain.Unread{}
	}
	if got := unread(); got.Messages != 1 || !slices.ContainsFunc(got.Threads, func(t domain.ThreadUnread) bool { return t.Root == fmsg(10) }) {
		t.Errorf("the room's unread = %+v, want the topic's one message", got)
	}
	if err := a.MarkThreadRead(ctx, forumRoom, fmsg(10), fmsg(12), false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if !slices.Equal(readTo, []int{10, 12}) {
		t.Errorf("read on Telegram %v", readTo)
	}
	mu.Unlock()
	if got := unread(); got.Messages != 0 {
		t.Errorf("after reading the topic, the room's unread = %+v", got)
	}
	if a.ThreadParticipant(ctx, forumRoom, fmsg(10)) {
		t.Error("a topic we neither began nor wrote in is ours")
	}
}
