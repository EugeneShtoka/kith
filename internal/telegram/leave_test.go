package telegram

import (
	"errors"
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
		return s.SendResult(r, &tg.Updates{})
	})
	d.HandleFunc(tg.MessagesDeleteChatUserRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		mu.Lock()
		left = append(left, "chat")
		mu.Unlock()
		return s.SendResult(r, &tg.Updates{})
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
