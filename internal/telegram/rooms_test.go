package telegram

import (
	"context"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgtest"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Each kind of dialog is a room named as Telegram shows it, by its marked peer ID; the
// chat with oneself is Saved Messages; a private chat's person is its member; a chat
// left, moved, or the account removed from, is left out; the access hashes are kept.
func TestDialogsAreRooms(t *testing.T) {
	t.Parallel()
	ent := peer.NewEntities(
		map[int64]*tg.User{7: {ID: 7, FirstName: "Dana", LastName: "Lee"}, 8: {ID: 8, Deleted: true}, 42: {ID: 42, Self: true}},
		map[int64]*tg.Chat{11: {ID: 11, Title: "Book club"}, 12: {ID: 12, Title: "Gone", Left: true}, 13: {ID: 13, Title: "Moved", Deactivated: true}},
		map[int64]*tg.Channel{21: {ID: 21, Title: "News", Broadcast: true}, 22: {ID: 22, Title: "Team", Megagroup: true}, 23: {ID: 23, Title: "Left", Left: true}},
	)
	elem := func(p tg.InputPeerClass) dialog { return dialog{peer: p, entities: ent} }
	l := listed(42, []dialog{
		elem(&tg.InputPeerUser{UserID: 7, AccessHash: 70}),
		elem(&tg.InputPeerUser{UserID: 8, AccessHash: 80}),
		elem(&tg.InputPeerSelf{}),
		elem(&tg.InputPeerUser{UserID: 9, AccessHash: 90}), // no entity: left out
		elem(&tg.InputPeerChat{ChatID: 11}),
		elem(&tg.InputPeerChat{ChatID: 12}),
		elem(&tg.InputPeerChat{ChatID: 13}),
		elem(&tg.InputPeerChannel{ChannelID: 21, AccessHash: 210}),
		elem(&tg.InputPeerChannel{ChannelID: 22, AccessHash: 220}),
		elem(&tg.InputPeerChannel{ChannelID: 23, AccessHash: 230}),
	})
	got := map[domain.RoomID]string{}
	for _, r := range l.rooms {
		got[r.ID] = r.Name
		if r.IsDirect != (r.ID == "telegram:42/7" || r.ID == "telegram:42/8" || r.ID == "telegram:42/42") {
			t.Errorf("%s: direct %v", r.ID, r.IsDirect)
		}
	}
	want := map[domain.RoomID]string{
		"telegram:42/7": "Dana Lee", "telegram:42/8": "Deleted Account", "telegram:42/42": "Saved Messages",
		"telegram:42/-11": "Book club", "telegram:42/-1000000000021": "News", "telegram:42/-1000000000022": "Team",
	}
	if len(got) != len(want) {
		t.Errorf("rooms = %v, want %v", got, want)
	}
	for id, name := range want {
		if got[id] != name {
			t.Errorf("%s = %q, want %q", id, got[id], name)
		}
	}
	if m := l.members["telegram:42/7"]; len(m) != 1 || m[0] != (domain.Member{UserID: "telegram:7", DisplayName: "Dana Lee"}) {
		t.Errorf("Dana's chat's members = %+v", m)
	}
	if l.hashes.users[7] != 70 || l.hashes.channels[22] != 220 || l.hashes.channels[23] != 0 {
		t.Errorf("hashes = %+v", l.hashes)
	}
}

// dialogsOf answers messages.getDialogs with chats, as one page, in the main folder;
// Archived is empty. chats is asked once per listing (for the main folder).
func (f *fakeTelegram) dialogsOf(chats func() []*tg.Chat, delay func() time.Duration) {
	d := f.cluster.Dispatch(2, "dc2")
	d.HandleFunc(tg.MessagesGetDialogsRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.MessagesGetDialogsRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		var list []*tg.Chat
		if folder, _ := req.GetFolderID(); folder == 0 {
			list = chats()
		}

		res := &tg.MessagesDialogs{Users: []tg.UserClass{f.user}}
		for _, c := range list {
			c.Photo = &tg.ChatPhotoEmpty{} // required on the wire
			res.Dialogs = append(res.Dialogs, &tg.Dialog{Peer: &tg.PeerChat{ChatID: c.ID}})
			res.Chats = append(res.Chats, c)
		}
		if delay == nil {
			return s.SendResult(r, res)
		}
		// Answered from aside, after a while: one settled early may arrive late.
		wait := delay()
		go func() {
			time.Sleep(wait)
			_ = s.SendResult(r, res)
		}()
		return nil
	})
}

// cachedAdapter is home's adapter over a fresh cache.
func cachedAdapter(t *testing.T, secrets *memSecrets) (*Adapter, *db.Cache) {
	t.Helper()
	cache, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	return New(cache, secrets, nil, []Account{home}, nil), cache
}

// cachedNames is the Telegram rooms in the cache, by name.
func cachedNames(t *testing.T, a *Adapter) []string {
	t.Helper()
	rooms, err := a.Rooms(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for i := range rooms {
		names = append(names, rooms[i].Name)
	}
	slices.Sort(names)
	return names
}

// Logged in, an account lists its chats into the cache and is their space, named after
// it; a chat gone from a later listing goes from the cache.
func TestALoggedInAccountListsItsChats(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	var mu sync.Mutex
	chats := []*tg.Chat{{ID: 11, Title: "Book club"}, {ID: 12, Title: "Work"}}
	f.dialogsOf(func() []*tg.Chat { mu.Lock(); defer mu.Unlock(); return slices.Clone(chats) }, nil)
	secrets := &memSecrets{values: map[string]string{}}
	a, _ := cachedAdapter(t, secrets)
	heard := &sessions{seen: map[string]domain.AccountPhase{}, said: map[string]string{}}
	a.OnStatus(heard.hear)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done; a.Stop() })
	if _, err := a.login(ctx, home, testApp, &apitest.Talk{Answers: map[string][]string{"code": {"12345"}}}, f.dial); err != nil {
		t.Fatal(err)
	}
	connected(t, heard, "home")
	if got := cachedNames(t, a); !slices.Equal(got, []string{"Book club", "Work"}) {
		t.Fatalf("cached rooms = %v, want both chats", got)
	}
	spaces, err := a.Spaces(ctx)
	if err != nil || len(spaces) != 1 || spaces[0].Name != "Telegram home" || len(spaces[0].Children) != 2 {
		t.Fatalf("spaces = (%+v, %v), want Telegram home holding both", spaces, err)
	}
	if parent, _ := a.CanonicalParent(ctx, spaces[0].Children[0]); parent != spaces[0].ID {
		t.Errorf("a chat's home = %q, want %q", parent, spaces[0].ID)
	}

	mu.Lock()
	chats = chats[:1]
	mu.Unlock()
	if _, err := a.RefreshRooms(ctx); err != nil {
		t.Fatal(err)
	}
	if got := cachedNames(t, a); !slices.Equal(got, []string{"Book club"}) {
		t.Errorf("after Work went: cached rooms = %v, want Book club alone", got)
	}
}

// Listings racing each other — several refreshes at once while the chats change, each
// answered after its own delay — never leave an older listing over a newer one: the
// cache ends as the last listing Telegram answered.
func TestAnOlderListingNeverLandsOverANewerOne(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	var mu sync.Mutex
	version := 0
	answered := 0
	f.dialogsOf(func() []*tg.Chat {
		mu.Lock()
		defer mu.Unlock()
		version++
		answered = version
		return []*tg.Chat{{ID: int64(version), Title: "v" + string(rune('0'+version%10))}}
	}, func() time.Duration { return time.Duration(rand.IntN(30)) * time.Millisecond })
	secrets := &memSecrets{values: map[string]string{}}
	a, _ := cachedAdapter(t, secrets)
	heard := &sessions{seen: map[string]domain.AccountPhase{}, said: map[string]string{}}
	a.OnStatus(heard.hear)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done; a.Stop() })
	if _, err := a.login(ctx, home, testApp, &apitest.Talk{Answers: map[string][]string{"code": {"12345"}}}, f.dial); err != nil {
		t.Fatal(err)
	}
	connected(t, heard, "home")
	for round := range 5 {
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				if _, err := a.RefreshRooms(ctx); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		mu.Lock()
		want := "v" + string(rune('0'+answered%10))
		mu.Unlock()
		if got := cachedNames(t, a); !slices.Equal(got, []string{want}) {
			t.Fatalf("round %d: cached %v, want the last answered listing's %s", round, got, want)
		}
	}
}

// More chats than a page holds are read page by page, each page asked for from the
// last dialog of the one before (its peer, and its top message's ID and date).
func TestManyChatsAreReadPageByPage(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	const total = dialogsPage + dialogsPage/2
	var mu sync.Mutex
	var asked []tg.MessagesGetDialogsRequest
	d := f.cluster.Dispatch(2, "dc2")
	d.HandleFunc(tg.MessagesGetDialogsRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.MessagesGetDialogsRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		mu.Lock()
		asked = append(asked, req)
		mu.Unlock()
		res := &tg.MessagesDialogsSlice{Count: total, Users: []tg.UserClass{f.user}}
		if folder, _ := req.GetFolderID(); folder != 0 {
			return s.SendResult(r, res)
		}
		from := 1
		if p, ok := req.OffsetPeer.(*tg.InputPeerChat); ok {
			from = int(p.ChatID) + 1
		}
		for id := from; id <= total && id < from+dialogsPage; id++ {
			res.Dialogs = append(res.Dialogs, &tg.Dialog{Peer: &tg.PeerChat{ChatID: int64(id)}, TopMessage: 1000 + id})
			res.Chats = append(res.Chats, &tg.Chat{ID: int64(id), Title: "c", Photo: &tg.ChatPhotoEmpty{}})
			res.Messages = append(res.Messages, &tg.Message{ID: 1000 + id, Date: 5000 - id, PeerID: &tg.PeerChat{ChatID: int64(id)}})
		}
		return s.SendResult(r, res)
	})
	// The fake's handshake is slow under -race with the package's other tests running
	// beside it; the deadline only bounds a hang.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	client := f.dial(testApp, newLoginSession(), nil)
	var got []dialog
	err := client.Run(ctx, func(ctx context.Context) error {
		var err error
		got, err = readDialogs(ctx, client.API())
		return err
	})
	if err != nil || len(got) != total {
		t.Fatalf("read %d dialogs (%v), want %d", len(got), err, total)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) < 2 {
		t.Fatalf("asked %d times, want a second page", len(asked))
	}
	second := asked[1]
	if p, ok := second.OffsetPeer.(*tg.InputPeerChat); !ok || p.ChatID != dialogsPage || second.OffsetID != 1000+dialogsPage || second.OffsetDate != 5000-dialogsPage {
		t.Errorf("second page asked from %+v (id %d, date %d), want chat %d, its top message and date", second.OffsetPeer, second.OffsetID, second.OffsetDate, dialogsPage)
	}
}

// A listing whose connection a newer login replaced writes nothing: its successor
// lists for itself.
func TestAReplacedConnectionsListingIsDropped(t *testing.T) {
	t.Parallel()
	a, cache := cachedAdapter(t, &memSecrets{values: map[string]string{}})
	a.mu.Lock()
	a.conns[home.Name] = &conn{account: home, gen: 2, cancel: func() {}}
	a.mu.Unlock()
	if a.keepHashes(home, 1, accessHashes{}) {
		t.Error("a listing of login 1 kept over login 2's connection")
	}
	if rooms, _ := cache.Rooms(t.Context()); len(rooms) != 0 {
		t.Errorf("cached %v", rooms)
	}
}
