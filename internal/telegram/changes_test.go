package telegram

import (
	"math/rand/v2"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/gotd/td/tgtest"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// send pushes the account updates, Telegram's state then at pts.
func (u *updatesOf) send(t *testing.T, s *tgtest.Server, pts int, updates ...tg.UpdateClass) {
	t.Helper()
	u.mu.Lock()
	sess := u.session
	u.pts = max(u.pts, pts)
	u.mu.Unlock()
	if sess == nil {
		t.Fatal("no session asked for its updates")
	}
	err := s.Send(t.Context(), *sess, proto.MessageFromServer, &tg.Updates{
		Updates: updates, Users: []tg.UserClass{dana}, Date: int(time.Now().Unix()),
	})
	if err != nil {
		t.Fatal(err)
	}
}

// reacted is a message's reactions: emoji, counted, who chose them where Telegram
// says.
func reacted(counts map[string]int, recent map[string][]int64, mine ...string) tg.MessageReactions {
	var rs tg.MessageReactions
	for _, key := range slices.Sorted(func(yield func(string) bool) {
		for k := range counts {
			if !yield(k) {
				return
			}
		}
	}) {
		c := tg.ReactionCount{Reaction: &tg.ReactionEmoji{Emoticon: key}, Count: counts[key]}
		if slices.Contains(mine, key) {
			c.SetChosenOrder(1)
		}
		rs.Results = append(rs.Results, c)
		for _, who := range recent[key] {
			rs.RecentReactions = append(rs.RecentReactions, tg.MessagePeerReaction{PeerID: &tg.PeerUser{UserID: who}, Reaction: &tg.ReactionEmoji{Emoticon: key}})
		}
	}
	return rs
}

// tallies is a room's cached reactions as "key sender" lines, in order.
func tallies(t *testing.T, cache *db.Cache, room domain.RoomID) []string {
	t.Helper()
	rs, err := cache.Reactions(t.Context(), room)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rs {
		out = append(out, r.Key+" "+r.Sender)
	}
	slices.Sort(out)
	return out
}

// unreadOf is a room's cached unread row.
func unreadOf(t *testing.T, cache *db.Cache, room domain.RoomID) domain.Unread {
	t.Helper()
	rows, err := cache.Unread(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range rows {
		if u.RoomID == room {
			return u
		}
	}
	return domain.Unread{}
}

// What changes a message, or a chat, while kith listens reaches the cache and the
// clients: an edit, reactions, a read on another device, someone typing, a deletion.
func TestChangesHeardLiveReachTheCache(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	u := &updatesOf{pts: 1}
	f.serveUpdates(u)
	a, cache := loggedInWithStore(t, f, openStore(t))
	waitFor(t, "no session asked for its updates", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return u.session != nil })
	room, s := domain.RoomID("telegram:42/7"), f.server()
	now := int(time.Now().Unix())

	u.push(t, s, &tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 7}, Message: "helo", Date: now}, 2)
	waitFor(t, "the message was not cached", func() bool { return slices.Equal(cachedBodies(t, cache, room), []string{"helo"}) })
	waitFor(t, "the message was not counted unread", func() bool { return unreadOf(t, cache, room).Notifications == 1 })

	u.send(t, s, 3, &tg.UpdateEditMessage{Message: &tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 7}, Message: "hello", Date: now, EditDate: now + 1}, Pts: 3, PtsCount: 1})
	waitFor(t, "the edit was not folded", func() bool { return slices.Equal(cachedBodies(t, cache, room), []string{"hello"}) })

	u.send(t, s, 3, &tg.UpdateMessageReactions{Peer: &tg.PeerUser{UserID: 7}, MsgID: 5, Reactions: reacted(map[string]int{"👍": 2}, map[string][]int64{"👍": {7, 42}}, "👍")})
	waitFor(t, "the reactions were not cached", func() bool {
		return slices.Equal(tallies(t, cache, room), []string{"👍 telegram:42", "👍 telegram:7"})
	})

	u.send(t, s, 4, &tg.UpdateReadHistoryInbox{Peer: &tg.PeerUser{UserID: 7}, MaxID: 5, StillUnreadCount: 0, Pts: 4, PtsCount: 1})
	waitFor(t, "the read was not kept", func() bool {
		got := unreadOf(t, cache, room)
		return got.ReadEvent == "telegram:42/7/5" && got.Notifications == 0
	})

	u.send(t, s, 4, &tg.UpdateUserTyping{UserID: 7, Action: &tg.SendMessageTypingAction{}})
	waitFor(t, "the typist was not heard", func() bool {
		for {
			select {
			case act := <-a.Activity():
				if act.RoomID == room && slices.Equal(act.Typing, []string{"telegram:7"}) {
					return true
				}
			default:
				return false
			}
		}
	})

	u.send(t, s, 5, &tg.UpdateDeleteMessages{Messages: []int{5}, Pts: 5, PtsCount: 1})
	waitFor(t, "the deletion was not kept", func() bool {
		m, ok, _ := cache.MessageByID(t.Context(), room, "telegram:42/7/5")
		return ok && m.Redacted
	})
}

// What kith does to a message or a chat goes to Telegram as its requests: an edit, a
// deletion (a channel's through channels), a reaction toggled (the account's other
// reactions kept, or replaced where it may keep one), a read, a mark, a typing notice.
func TestChangesMadeHereGoToTelegram(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.serveUpdates(&updatesOf{pts: 1})
	d := f.cluster.Dispatch(2, "dc2")
	var mu sync.Mutex
	var asked []string
	var reactions [][]string
	tooMany := true
	handle := func(id uint32, decode func(*bin.Buffer) (string, error), answer func() bin.Encoder) {
		d.HandleFunc(id, func(s *tgtest.Server, r *tgtest.Request) error {
			what, err := decode(r.Buf)
			if err != nil {
				return err
			}
			mu.Lock()
			asked = append(asked, what)
			mu.Unlock()
			return s.SendResult(r, answer())
		})
	}
	affected := func() bin.Encoder { return &tg.MessagesAffectedMessages{Pts: 1} }
	handle(tg.MessagesEditMessageRequestTypeID, func(b *bin.Buffer) (string, error) {
		var req tg.MessagesEditMessageRequest
		err := req.Decode(b)
		return "edit " + strconv.Itoa(req.ID) + " " + req.Message, err
	}, func() bin.Encoder {
		return &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateEditMessage{
			Message: &tg.Message{ID: 5, Out: true, PeerID: &tg.PeerUser{UserID: 7}, Message: "fixed", Date: 1000, EditDate: 2000},
		}}, Users: []tg.UserClass{dana, f.user}}
	})
	handle(tg.MessagesDeleteMessagesRequestTypeID, func(b *bin.Buffer) (string, error) {
		var req tg.MessagesDeleteMessagesRequest
		err := req.Decode(b)
		return "delete " + strconv.Itoa(req.ID[0]) + " revoke=" + strconv.FormatBool(req.Revoke), err
	}, affected)
	handle(tg.MessagesReadHistoryRequestTypeID, func(b *bin.Buffer) (string, error) {
		var req tg.MessagesReadHistoryRequest
		err := req.Decode(b)
		return "read " + strconv.Itoa(req.MaxID), err
	}, affected)
	handle(tg.MessagesMarkDialogUnreadRequestTypeID, func(b *bin.Buffer) (string, error) {
		var req tg.MessagesMarkDialogUnreadRequest
		err := req.Decode(b)
		return "unread " + strconv.FormatBool(req.Unread), err
	}, func() bin.Encoder { return &tg.BoolTrue{} })
	handle(tg.MessagesSetTypingRequestTypeID, func(b *bin.Buffer) (string, error) {
		var req tg.MessagesSetTypingRequest
		err := req.Decode(b)
		return "typing " + req.Action.TypeName(), err
	}, func() bin.Encoder { return &tg.BoolTrue{} })
	d.HandleFunc(tg.MessagesSendReactionRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.MessagesSendReactionRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		var keys []string
		for _, k := range req.Reaction {
			keys = append(keys, k.(*tg.ReactionEmoji).Emoticon)
		}
		mu.Lock()
		reactions = append(reactions, keys)
		refuse := tooMany && len(keys) > 1
		mu.Unlock()
		if refuse {
			return s.SendErr(r, tgerr.New(400, "REACTIONS_TOO_MANY"))
		}
		counts := map[string]int{}
		for _, k := range keys {
			counts[k] = 1
		}
		return s.SendResult(r, &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateMessageReactions{
			Peer: &tg.PeerUser{UserID: 7}, MsgID: 5, Reactions: reacted(counts, nil, keys...),
		}}})
	})
	st := openStore(t)
	knowDana(t, st)
	a, cache := loggedInWithStore(t, f, st)
	ctx, room := t.Context(), domain.RoomID("telegram:42/7")
	if err := cache.SaveMessages(ctx, room, []domain.Message{{ID: "telegram:42/7/5", RoomID: room, Sender: "telegram:42", Body: "fxied", Timestamp: time.Unix(1000, 0)}}); err != nil {
		t.Fatal(err)
	}

	if err := a.Send(ctx, room, domain.Draft{Body: "fixed", Edits: "telegram:42/7/5"}); err != nil {
		t.Fatal(err)
	}
	if got := cachedBodies(t, cache, room); !slices.Equal(got, []string{"fixed"}) {
		t.Errorf("after the edit, cached %q", got)
	}
	if err := a.SendReaction(ctx, room, "telegram:42/7/5", "👍"); err != nil {
		t.Fatal(err)
	}
	if err := a.SendReaction(ctx, room, "telegram:42/7/5", "🎉"); err != nil { // one kept: replaced
		t.Fatal(err)
	}
	if got := tallies(t, cache, room); !slices.Equal(got, []string{"🎉 telegram:42"}) {
		t.Errorf("after two reactions, cached %q", got)
	}
	if err := a.SendReaction(ctx, room, "telegram:42/7/5", "🎉"); err != nil { // taken back
		t.Fatal(err)
	}
	if got := tallies(t, cache, room); len(got) != 0 {
		t.Errorf("after taking it back, cached %q", got)
	}
	if err := a.MarkRead(ctx, room, "telegram:42/7/5", false); err != nil {
		t.Fatal(err)
	}
	if got := unreadOf(t, cache, room); got.ReadEvent != "telegram:42/7/5" {
		t.Errorf("after reading, the position is %q", got.ReadEvent)
	}
	if err := a.MarkRoomUnread(ctx, room, true); err != nil {
		t.Fatal(err)
	}
	if got := unreadOf(t, cache, room); !got.Marked {
		t.Error("marked unread was not kept")
	}
	if err := a.SendTyping(ctx, room, true, 0); err != nil {
		t.Fatal(err)
	}
	if err := a.Redact(ctx, room, "telegram:42/7/5", ""); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := cache.MessageByID(ctx, room, "telegram:42/7/5"); !m.Redacted {
		t.Error("the deleted message is not marked deleted")
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"edit 5 fixed", "read 5", "unread true", "typing sendMessageTypingAction", "delete 5 revoke=true"}
	if !slices.Equal(asked, want) {
		t.Errorf("asked %q, want %q", asked, want)
	}
	if !slices.EqualFunc(reactions, [][]string{{"👍"}, {"👍", "🎉"}, {"🎉"}, nil}, slices.Equal) {
		t.Errorf("reactions sent %q", reactions)
	}
}

// A deletion names a message by its number within the account: it is found in the
// chat that holds it, never in a channel (whose numbers are its own) or another
// account's chat.
func TestADeletionFindsItsChatByNumber(t *testing.T) {
	t.Parallel()
	a, cache := cachedAdapter(t, &memSecrets{values: map[string]string{}})
	ctx := t.Context()
	for _, room := range []domain.RoomID{"telegram:42/7", "telegram:42/-1000000000021", "telegram:43/7"} {
		if err := cache.SaveMessages(ctx, room, []domain.Message{{ID: domain.EventID(string(room) + "/5"), RoomID: room, Body: "m", Timestamp: time.Now()}}); err != nil {
			t.Fatal(err)
		}
	}
	a.deleted(ctx, 42, []int{5})
	for room, gone := range map[domain.RoomID]bool{"telegram:42/7": true, "telegram:42/-1000000000021": false, "telegram:43/7": false} {
		if m, _, _ := cache.MessageByID(ctx, room, domain.EventID(string(room)+"/5")); m.Redacted != gone {
			t.Errorf("%s: deleted %v, want %v", room, m.Redacted, gone)
		}
	}
}

// A message's versions arrive in any order — live edits, history pages read before or
// after them — and the newest always shows: an older one never replaces it.
func TestAnOlderVersionNeverReplacesANewerOne(t *testing.T) {
	t.Parallel()
	ent := peer.NewEntities(map[int64]*tg.User{7: dana}, nil, nil)
	for seed := range uint64(40) {
		rng := rand.New(rand.NewPCG(seed, 6))
		a, cache := cachedAdapter(t, &memSecrets{values: map[string]string{}})
		ctx, room := t.Context(), domain.RoomID("telegram:42/7")
		newest := -1
		for range 12 {
			v := rng.IntN(6) // the version: 0 is as sent, n edited at 1000+n
			m := &tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 7}, Message: "v" + strconv.Itoa(v), Date: 1000}
			if v > 0 {
				m.EditDate = 1000 + v
			}
			msg, _ := incoming(42, m, ent)
			if rng.IntN(2) == 0 {
				if err := a.edited(ctx, home, 42, msg); err != nil {
					t.Fatal(err)
				}
			} else if _, err := a.record(ctx, 42, room, []domain.Message{msg}); err != nil { // a page
				t.Fatal(err)
			}
			newest = max(newest, v)
			if got := cachedBodies(t, cache, room); !slices.Equal(got, []string{"v" + strconv.Itoa(newest)}) {
				t.Fatalf("seed %d: shows %q, the newest version is v%d", seed, got, newest)
			}
		}
	}
}

// A history page is what a message's reactions were when it was read: a page read
// before a live change, written after it, never brings the old ones back.
func TestAHistoryPageNeverUndoesALiveReaction(t *testing.T) {
	t.Parallel()
	for seed := range uint64(60) {
		rng := rand.New(rand.NewPCG(seed, 7))
		a, cache := cachedAdapter(t, &memSecrets{values: map[string]string{}})
		ctx, room := t.Context(), domain.RoomID("telegram:42/7")
		server := 0 // how many 👍 the message has
		type read struct {
			count int
			at    time.Time
		}
		var pending []read
		shown := 0 // what the cache should show: the latest the adapter heard
		var heardAt time.Time
		for range 30 {
			switch rng.IntN(3) {
			case 0: // a live change
				server = rng.IntN(4)
				a.reactionsChanged(ctx, room, "telegram:42/7/5", messageReactions(42, 7, 5, reacted(map[string]int{"👍": server}, nil)))
				shown, heardAt = server, time.Now()
			case 1: // a page read
				pending = append(pending, read{server, time.Now()})
			case 2: // a page written, in any order
				if len(pending) == 0 {
					continue
				}
				i := rng.IntN(len(pending))
				p := pending[i]
				pending = slices.Delete(pending, i, i+1)
				raw := []tg.MessageClass{&tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 7}, Message: "m", Date: 1000, Reactions: reacted(map[string]int{"👍": p.count}, nil)}}
				a.pageReactions(ctx, 42, raw, p.at)
				if heardAt.Before(p.at) {
					shown = p.count
				}
			}
			if got := len(tallies(t, cache, room)); got != shown {
				t.Fatalf("seed %d: the cache shows %d 👍, the newest heard is %d", seed, got, shown)
			}
			time.Sleep(time.Microsecond)
		}
	}
}

// A listing is fetched, then written: whatever order live changes (a message arrives,
// a chat is read elsewhere) come between, a listing never writes its older count over
// one heard since it was fetched. Listings are written one at a time, in order.
func TestAListingNeverUndoesAnUnreadChangeHeardSince(t *testing.T) {
	t.Parallel()
	for seed := range uint64(60) {
		rng := rand.New(rand.NewPCG(seed, 8))
		a, cache := cachedAdapter(t, &memSecrets{values: map[string]string{}})
		ctx, room := t.Context(), domain.RoomID("telegram:42/-11")
		listed := []domain.Room{{ID: room}}
		server, shown := 0, 0 // Telegram's count; the newest count the adapter heard
		var heardAt time.Time
		type fetched struct {
			count int
			at    time.Time
		}
		var pending []fetched
		for step := range 30 {
			switch rng.IntN(4) {
			case 0: // a message arrives
				server++
				a.counted(ctx, domain.Message{ID: messageID(42, -11, step), RoomID: room}, false)
				shown, heardAt = server, time.Now()
			case 1: // read elsewhere
				server = rng.IntN(server + 1)
				a.readInbox(ctx, 42, -11, step, server)
				shown, heardAt = server, time.Now()
			case 2: // a listing fetched
				pending = append(pending, fetched{server, time.Now()})
			case 3: // the oldest listing written
				if len(pending) == 0 {
					continue
				}
				p := pending[0]
				pending = pending[1:]
				a.listedUnread(ctx, 42, []dialog{{info: &tg.Dialog{Peer: &tg.PeerChat{ChatID: 11}, UnreadCount: p.count}}}, listed, p.at)
				if heardAt.Before(p.at) {
					shown = p.count
				}
			}
			if got := unreadOf(t, cache, room).Notifications; got != shown {
				t.Fatalf("seed %d: the cache counts %d unread, the newest heard is %d", seed, got, shown)
			}
			time.Sleep(time.Microsecond)
		}
	}
}

// Messages arriving at once are each counted: none is lost to another's write.
func TestConcurrentArrivalsAreEachCounted(t *testing.T) {
	t.Parallel()
	a, cache := cachedAdapter(t, &memSecrets{values: map[string]string{}})
	ctx, room := t.Context(), domain.RoomID("telegram:42/-11")
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() { a.counted(ctx, domain.Message{ID: messageID(42, -11, i), RoomID: room}, false) })
	}
	wg.Wait()
	if got := unreadOf(t, cache, room).Notifications; got != 40 {
		t.Errorf("counted %d of 40", got)
	}
}

// Reactions are one per person named, the account's own among them however Telegram
// says it, and the rest of a count as no one's.
func TestReactionsAreOnePerPerson(t *testing.T) {
	t.Parallel()
	rs := messageReactions(42, -11, 5, reacted(map[string]int{"👍": 3, "❤": 1}, map[string][]int64{"👍": {7}}, "❤"))
	var got []string
	for _, r := range rs {
		got = append(got, r.Key+" "+r.Sender)
	}
	slices.Sort(got)
	if want := []string{"❤ telegram:42", "👍 ", "👍 ", "👍 telegram:7"}; !slices.Equal(got, want) {
		t.Errorf("reactions %q, want %q", got, want)
	}
	if tallied := domain.AggregateReactions(rs, "telegram:42"); len(tallied) != 2 {
		t.Errorf("tallied %+v", tallied)
	}
}

// A typist is forgotten when they cancel, or when their notices stop.
func TestATypistIsForgotten(t *testing.T) {
	t.Parallel()
	a, _ := cachedAdapter(t, &memSecrets{values: map[string]string{}})
	room := domain.RoomID("telegram:42/-11")
	a.typingNotice(42, -11, 7, &tg.SendMessageTypingAction{})
	a.typingNotice(42, -11, 42, &tg.SendMessageTypingAction{}) // ourselves: not shown
	if act := <-a.Activity(); act.RoomID != room || !slices.Equal(act.Typing, []string{"telegram:7"}) {
		t.Errorf("typing %+v", act)
	}
	a.typingNotice(42, -11, 7, &tg.SendMessageCancelAction{})
	if act := <-a.Activity(); len(act.Typing) != 0 {
		t.Errorf("after canceling, typing %+v", act)
	}
	a.typingNotice(42, -11, 7, &tg.SendMessageRecordAudioAction{})
	<-a.Activity()
	select {
	case act := <-a.Activity():
		if len(act.Typing) != 0 {
			t.Errorf("after the notices stopped, typing %+v", act)
		}
	case <-time.After(typingFor + 5*time.Second):
		t.Error("a typist whose notices stopped was not forgotten")
	}
}
