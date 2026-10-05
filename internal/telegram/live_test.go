package telegram

import (
	"context"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/proto"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgtest"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// dana is a person the fake's messages come from.
var dana = &tg.User{ID: 7, AccessHash: 70, FirstName: "Dana"}

// updatesOf serves the fake's updates side: its state is pts, and a difference asked
// for from an older pts carries missed (the messages up to pts). It remembers the
// session that asked, to push updates to.
type updatesOf struct {
	mu      sync.Mutex
	pts     int
	missed  []tg.MessageClass
	session *tgtest.Session
	diffs   int
}

func (f *fakeTelegram) serveUpdates(u *updatesOf) {
	d := f.cluster.Dispatch(2, "dc2")
	d.HandleFunc(tg.UpdatesGetStateRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		u.mu.Lock()
		sess := r.Session
		u.session = &sess
		st := &tg.UpdatesState{Pts: u.pts, Date: int(time.Now().Unix()), Seq: 1}
		u.mu.Unlock()
		return s.SendResult(r, st)
	})
	d.HandleFunc(tg.UpdatesGetDifferenceRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.UpdatesGetDifferenceRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		u.mu.Lock()
		defer u.mu.Unlock()
		u.diffs++
		sess := r.Session
		u.session = &sess
		st := tg.UpdatesState{Pts: u.pts, Date: int(time.Now().Unix()), Seq: 1}
		if req.Pts >= u.pts || len(u.missed) == 0 {
			return s.SendResult(r, &tg.UpdatesDifferenceEmpty{Date: st.Date, Seq: st.Seq})
		}
		return s.SendResult(r, &tg.UpdatesDifference{NewMessages: u.missed, Users: []tg.UserClass{dana, f.user}, State: st})
	})
}

// push sends the account a new message, at pts.
func (u *updatesOf) push(t *testing.T, s *tgtest.Server, m *tg.Message, pts int) {
	t.Helper()
	u.mu.Lock()
	sess := u.session
	u.pts = pts
	u.mu.Unlock()
	if sess == nil {
		t.Fatal("no session asked for its updates")
	}
	err := s.Send(t.Context(), *sess, proto.MessageFromServer, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: m, Pts: pts, PtsCount: 1}},
		Users:   []tg.UserClass{dana}, Date: int(time.Now().Unix()),
	})
	if err != nil {
		t.Fatal(err)
	}
}

// loggedInWithStore is home logged in over the fake, its updates position in a store,
// its messages in a cache.
func loggedInWithStore(t *testing.T, f *fakeTelegram, st *Store) (*Adapter, *db.Cache) {
	t.Helper()
	f.dialogsOf(func() []*tg.Chat { return nil }, nil)
	cache, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	a := New(cache, &memSecrets{values: map[string]string{}}, st, []Account{home}, nil)
	heard := &sessions{seen: map[string]domain.AccountPhase{}, said: map[string]string{}}
	a.OnStatus(heard.hear)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done; a.Stop() })
	if _, err := a.login(ctx, home, testApp, &apitest.Talk{Answers: map[string][]string{"code": {"12345"}}}, f.dial); err != nil {
		t.Fatal(err)
	}
	connected(t, heard, "home")
	return a, cache
}

// openStore is a fresh store.
func openStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "telegram.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// waitFor waits for cond, a network's pace.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
	}
}

// cachedBodies is a room's cached messages' bodies, oldest first.
func cachedBodies(t *testing.T, cache *db.Cache, room domain.RoomID) []string {
	t.Helper()
	msgs, err := cache.Messages(t.Context(), room, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := range msgs {
		out = append(out, msgs[i].Body)
	}
	return out
}

// A message heard live is cached, handed to the clients, named after its sender, and
// the account's updates position kept past it — a hold left by an earlier connection
// released, as a connection begins anew.
func TestALiveMessageIsCachedAndHeard(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	u := &updatesOf{pts: 1}
	f.serveUpdates(u)
	st := openStore(t)
	st.hold(42) // held by an earlier connection's failure: this one starts anew
	a, cache := loggedInWithStore(t, f, st)
	waitFor(t, "no session asked for its updates", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return u.session != nil })

	u.push(t, f.server(), &tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 7}, Message: "hello", Date: int(time.Now().Unix())}, 2)
	room := domain.RoomID("telegram:42/7")
	waitFor(t, "the message was not cached", func() bool { return slices.Equal(cachedBodies(t, cache, room), []string{"hello"}) })
	select {
	case msg := <-a.Messages():
		if msg.ID != "telegram:42/7/5" || msg.Sender != "telegram:7" || msg.SenderName != "Dana" {
			t.Errorf("heard %+v", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing heard")
	}
	waitFor(t, "the position was not kept past the message", func() bool {
		s, ok, _ := st.GetState(t.Context(), 42)
		return ok && s.Pts == 2
	})
}

// A message sent while kith was not listening arrives when it connects again: the
// position kept is older than Telegram's, so the difference is asked for.
func TestAMessageMissedWhileAwayArrives(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	missed := &tg.Message{ID: 9, PeerID: &tg.PeerUser{UserID: 7}, Message: "while you were away", Date: int(time.Now().Unix())}
	u := &updatesOf{pts: 5, missed: []tg.MessageClass{missed}}
	f.serveUpdates(u)
	st := openStore(t)
	if err := st.SetState(t.Context(), 42, updatesState(4)); err != nil {
		t.Fatal(err)
	}
	_, cache := loggedInWithStore(t, f, st)
	waitFor(t, "the missed message did not arrive", func() bool {
		return slices.Equal(cachedBodies(t, cache, "telegram:42/7"), []string{"while you were away"})
	})
}

// The position kept never passes a message that was not cached: whatever order
// messages are cached or fail in, and reconnects come between, the position saved is
// at most the last one all of whose messages were cached — what a reconnect asks
// Telegram again from. (gotd saves its position after the handler, failed or not.)
func TestThePositionNeverPassesAnUncachedMessage(t *testing.T) {
	t.Parallel()
	for seed := range uint64(300) {
		rng := rand.New(rand.NewPCG(seed, 4))
		st := openStore(t)
		ctx := t.Context()
		if err := st.SetState(ctx, 42, updatesState(0)); err != nil {
			t.Fatal(err)
		}
		safe, pts, uncached := 0, 0, false
		for range 30 {
			switch rng.IntN(4) {
			case 0: // a reconnect: from the position saved, gotd's in memory with it
				st.release(42)
				saved, _, _ := st.GetState(ctx, 42)
				pts, uncached = saved.Pts, false
			default: // an update: the handler caches it or fails, then gotd saves
				pts++
				if rng.IntN(4) == 0 {
					st.hold(42) // what arrived does when caching fails
					uncached = true
				} else if !uncached {
					safe = pts
				}
				_ = st.SetPts(ctx, 42, pts)
			}
			if saved, _, _ := st.GetState(ctx, 42); saved.Pts > safe {
				t.Fatalf("seed %d: saved %d, past %d, the last position all of whose messages were cached", seed, saved.Pts, safe)
			}
		}
	}

	// And arrived holds the account when it cannot cache.
	st := openStore(t)
	cache, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	a := New(cache, &memSecrets{values: map[string]string{}}, st, []Account{home}, nil)
	_ = cache.Close()
	if err := a.arrived(t.Context(), home, 42, domain.Message{ID: "telegram:42/7/1", RoomID: "telegram:42/7"}); err == nil || !st.holding(42) {
		t.Errorf("arrived over a closed cache = %v, holding %v; want it failed and held", err, st.holding(42))
	}
}

// A message is shown from whom it came: the account for its own, the person in a
// private chat, the member who wrote in a group, a channel posting as itself; a reply
// points at its original in the same chat; an attachment reads as its label before
// the caption; a service message is not shown.
func TestMessagesAreShownAsTelegramSendsThem(t *testing.T) {
	t.Parallel()
	ent := peer.NewEntities(
		map[int64]*tg.User{7: dana, 42: {ID: 42, FirstName: "Me"}, 8: {ID: 8, FirstName: "Sam"}},
		nil,
		map[int64]*tg.Channel{21: {ID: 21, Title: "News"}},
	)
	now := int(time.Now().Unix())
	for name, c := range map[string]struct {
		msg          tg.MessageClass
		sender, name string
		body         string
		reply        domain.EventID
	}{
		"mine":          {&tg.Message{ID: 1, Out: true, PeerID: &tg.PeerUser{UserID: 7}, Message: "hi", Date: now}, "telegram:42", "Me", "hi", ""},
		"private":       {&tg.Message{ID: 2, PeerID: &tg.PeerUser{UserID: 7}, Message: "yo", Date: now}, "telegram:7", "Dana", "yo", ""},
		"group":         {&tg.Message{ID: 3, PeerID: &tg.PeerChat{ChatID: 11}, FromID: &tg.PeerUser{UserID: 8}, Message: "all", Date: now}, "telegram:8", "Sam", "all", ""},
		"channel post":  {&tg.Message{ID: 4, PeerID: &tg.PeerChannel{ChannelID: 21}, Message: "news", Date: now}, "telegram:-1000000000021", "News", "news", ""},
		"reply":         {&tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 7}, Message: "yes", Date: now, ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 2}}, "telegram:7", "Dana", "yes", "telegram:42/7/2"},
		"photo caption": {&tg.Message{ID: 6, PeerID: &tg.PeerUser{UserID: 7}, Message: "look", Date: now, Media: &tg.MessageMediaPhoto{}}, "telegram:7", "Dana", "[photo] look", ""},
	} {
		got, ok := incoming(42, c.msg, ent)
		if !ok || got.Sender != c.sender || got.SenderName != c.name || got.Body != c.body || got.ReplyTo != c.reply {
			t.Errorf("%s: (%+v, %v)", name, got, ok)
		}
	}
	if _, ok := incoming(42, &tg.MessageService{ID: 9, PeerID: &tg.PeerChat{ChatID: 11}}, ent); ok {
		t.Error("a service message was shown")
	}
}

// A chat's history is read newest first and given oldest first, a page at a time,
// the next page asked from the oldest message of the last; each page is cached.
func TestHistoryIsReadPageByPage(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.serveUpdates(&updatesOf{pts: 1})
	d := f.cluster.Dispatch(2, "dc2")
	var mu sync.Mutex
	var offsets []int
	d.HandleFunc(tg.MessagesGetHistoryRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.MessagesGetHistoryRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		mu.Lock()
		offsets = append(offsets, req.OffsetID)
		mu.Unlock()
		top := 10
		if req.OffsetID != 0 {
			top = req.OffsetID - 1
		}
		res := &tg.MessagesMessagesSlice{Count: 10, Users: []tg.UserClass{dana}}
		for id := top; id > 0 && id > top-req.Limit; id-- {
			res.Messages = append(res.Messages, &tg.Message{ID: id, PeerID: &tg.PeerUser{UserID: 7}, Message: "m", Date: 1000 + id})
		}
		return s.SendResult(r, res)
	})
	a, cache := loggedInWithStore(t, f, openStore(t))
	a.mu.Lock()
	a.conns[home.Name].hashes.users[7] = dana.AccessHash
	a.mu.Unlock()
	room := domain.RoomID("telegram:42/7")
	page, err := a.Timeline(t.Context(), room, "", 4)
	if err != nil || len(page.Messages) != 4 || page.Messages[0].ID != "telegram:42/7/7" || page.Next != "7" {
		t.Fatalf("first page = (%d messages, next %q, %v)", len(page.Messages), page.Next, err)
	}
	page, err = a.Timeline(t.Context(), room, page.Next, 4)
	if err != nil || len(page.Messages) != 4 || page.Messages[3].ID != "telegram:42/7/6" {
		t.Fatalf("second page = (%+v, %v)", page.Messages, err)
	}
	if got := cachedBodies(t, cache, room); len(got) != 8 {
		t.Errorf("cached %d messages, want both pages'", len(got))
	}
}

// server is the fake's data center the client talks to.
func (f *fakeTelegram) server() *tgtest.Server {
	s, _ := f.cluster.DC(2, "dc2")
	return s
}

// updatesState is a kept position at pts.
func updatesState(pts int) updates.State {
	return updates.State{Pts: pts, Date: int(time.Now().Unix()), Seq: 1}
}

// A listing and live messages race for a room: whatever order a listing is fetched,
// messages arrive (in rooms it names or not: a chat just begun), and listings are
// written in, a room a message was cached in after a listing was fetched is never
// swept by that listing.
func TestAListingNeverSweepsARoomHeardSinceItWasFetched(t *testing.T) {
	t.Parallel()
	for seed := range uint64(60) {
		rng := rand.New(rand.NewPCG(seed, 5))
		a, cache := cachedAdapter(t, &memSecrets{values: map[string]string{}})
		ctx := t.Context()
		server := map[int64]bool{1: true, 2: true, 3: true}
		type fetchedListing struct {
			l  listing
			at time.Time
		}
		var pending []fetchedListing
		heardAt := map[domain.RoomID]time.Time{}
		for step := range 30 {
			switch rng.IntN(4) {
			case 0: // a listing fetched: the server's rooms now
				var l listing
				for id := range server {
					l.rooms = append(l.rooms, domain.Room{ID: roomID(42, -id), Name: "c", Membership: domain.MembershipJoin})
				}
				pending = append(pending, fetchedListing{l, time.Now()})
			case 1: // a message, in a room the server lists or a new one
				id := int64(1 + rng.IntN(5))
				room := roomID(42, -id)
				if _, err := a.record(ctx, 42, room, []domain.Message{{ID: messageID(42, -id, step), RoomID: room, Body: "m", Timestamp: time.Now()}}); err != nil {
					t.Fatal(err)
				}
				heardAt[room] = time.Now()
			case 2: // a listing written, maybe an older one
				if len(pending) == 0 {
					continue
				}
				i := rng.IntN(len(pending))
				p := pending[i]
				pending = slices.Delete(pending, i, i+1)
				before, _ := cache.Rooms(ctx)
				if err := a.save(ctx, 42, p.l, p.at); err != nil {
					t.Fatal(err)
				}
				after, _ := cache.Rooms(ctx)
				for room, at := range heardAt {
					was := slices.ContainsFunc(before, func(r domain.Room) bool { return r.ID == room })
					is := slices.ContainsFunc(after, func(r domain.Room) bool { return r.ID == room })
					if at.After(p.at) && was && !is {
						t.Fatalf("seed %d: a listing fetched before %s was heard from swept it", seed, room)
					}
				}
			case 3: // a chat left on the server
				delete(server, int64(1+rng.IntN(3)))
			}
			time.Sleep(time.Microsecond)
		}
	}
}
