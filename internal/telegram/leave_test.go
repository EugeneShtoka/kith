package telegram

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgtest"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A forum is left on Telegram, and goes from the cache with its topics' rooms; a basic
// group is left by taking ourselves out; a topic and a private chat cannot be left,
// and say so.
func TestLeavingAForumTakesItsTopicsWithIt(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	d := f.cluster.Dispatch(2, "dc2")
	var mu sync.Mutex
	var left []string
	d.HandleFunc(tg.ChannelsLeaveChannelRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.ChannelsLeaveChannelRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		mu.Lock()
		left = append(left, "channel")
		mu.Unlock()
		return sendResult(s, r, &tg.Updates{})
	})
	d.HandleFunc(tg.MessagesDeleteChatUserRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		mu.Lock()
		left = append(left, "chat")
		mu.Unlock()
		return sendResult(s, r, &tg.Updates{})
	})
	st := openStore(t)
	if err := st.SetChannelAccessHash(t.Context(), 42, forum, 310); err != nil {
		t.Fatal(err)
	}
	a, cache := loggedInWithStore(t, f, st)
	ctx := t.Context()
	owner := domain.AccountRooms(domain.ProtocolTelegram, "42")
	group := roomID(42, -11)
	if err := cache.AddRooms(ctx, owner, []domain.Room{
		{ID: forumRoom, Name: "Hikers", Forum: true}, {ID: tripsRoom, Name: "Trips"}, {ID: group, Name: "Book club"}, {ID: roomID(42, 7), Name: "Dana"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := a.LeaveRoom(ctx, tripsRoom); !errors.Is(err, api.ErrNotOnNetwork) {
		t.Errorf("leaving a topic = %v, want it refused", err)
	}
	if err := a.LeaveRoom(ctx, roomID(42, 7)); !errors.Is(err, api.ErrNotOnNetwork) {
		t.Errorf("leaving a private chat = %v, want it refused", err)
	}
	if err := a.LeaveRoom(ctx, forumRoom); err != nil {
		t.Fatal(err)
	}
	if err := a.LeaveRoom(ctx, group); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if !slices.Equal(left, []string{"channel", "chat"}) {
		t.Errorf("left on Telegram: %v", left)
	}
	mu.Unlock()
	rooms, _ := a.Rooms(ctx)
	var kept []domain.RoomID
	for i := range rooms {
		kept = append(kept, rooms[i].ID)
	}
	if !slices.Equal(kept, []domain.RoomID{roomID(42, 7)}) {
		t.Errorf("rooms after leaving = %v, want only the private chat", kept)
	}
}

// A private chat is deleted, not left: in as many parts as Telegram takes, for you,
// or for both of you when asked, and it goes from the cache; your saved messages are
// deleted for you only; a group is left, not deleted. Each room says which it is.
func TestAPrivateChatIsDeletedNotLeft(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	d := f.cluster.Dispatch(2, "dc2")
	type deletion struct {
		peer   string
		revoke bool
	}
	var mu sync.Mutex
	var deleted []deletion
	parts := 0
	d.HandleFunc(tg.MessagesDeleteHistoryRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.MessagesDeleteHistoryRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		peer := "self"
		if u, ok := req.Peer.(*tg.InputPeerUser); ok {
			peer = fmt.Sprintf("user %d/%d", u.UserID, u.AccessHash)
		}
		deleted = append(deleted, deletion{peer, req.Revoke})
		parts++
		offset := 0
		if parts == 1 {
			offset = 100 // a long history: one more part to go
		}
		return sendResult(s, r, &tg.MessagesAffectedHistory{Offset: offset})
	})
	st := openStore(t)
	if err := st.SetUserAccessHash(t.Context(), 42, 7, 77); err != nil {
		t.Fatal(err)
	}
	a, cache := loggedInWithStore(t, f, st)
	ctx := t.Context()
	owner := domain.AccountRooms(domain.ProtocolTelegram, "42")
	dana, saved, group := roomID(42, 7), roomID(42, 42), roomID(42, -11)
	if err := cache.AddRooms(ctx, owner, []domain.Room{
		{ID: dana, Name: "Dana", IsDirect: true}, {ID: saved, Name: "Saved"}, {ID: group, Name: "Book club"}, {ID: tripsRoom, Name: "Trips"},
	}); err != nil {
		t.Fatal(err)
	}
	rooms, _ := a.Rooms(ctx)
	want := map[domain.RoomID]domain.ChatDeleting{
		dana: domain.ChatDeletedForEither, saved: domain.ChatDeletedForMe, group: domain.ChatLeft, tripsRoom: domain.ChatLeft,
	}
	for i := range rooms {
		if rooms[i].Deleting != want[rooms[i].ID] {
			t.Errorf("%s is deleted %v, want %v", rooms[i].ID, rooms[i].Deleting, want[rooms[i].ID])
		}
	}

	if err := a.DeleteChat(ctx, group, false); !errors.Is(err, api.ErrNotOnNetwork) {
		t.Errorf("deleting a group = %v, want it refused", err)
	}
	if err := a.DeleteChat(ctx, saved, true); !errors.Is(err, api.ErrNotOnNetwork) {
		t.Errorf("deleting your saved messages for someone else = %v, want it refused", err)
	}
	if err := a.DeleteChat(ctx, dana, true); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteChat(ctx, saved, false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if wantDeleted := []deletion{{"user 7/77", true}, {"user 7/77", true}, {"self", false}}; !slices.Equal(deleted, wantDeleted) {
		t.Errorf("deleted on Telegram: %v, want %v", deleted, wantDeleted)
	}
	mu.Unlock()
	rooms, _ = a.Rooms(ctx)
	var kept []domain.RoomID
	for i := range rooms {
		kept = append(kept, rooms[i].ID)
	}
	slices.Sort(kept)
	if wantKept := []domain.RoomID{group, tripsRoom}; !slices.Equal(kept, slices.Sorted(slices.Values(wantKept))) {
		t.Errorf("rooms after deleting = %v, want the group and the topic", kept)
	}
}
