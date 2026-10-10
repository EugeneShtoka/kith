package telegram

import (
	"strings"
	"sync"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgtest"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A forum is made as a supergroup with topics, named as the room of its channel; the
// person the account has seen is added by their access hash, and one it has not is
// named in the partial failure. A kind Telegram does not make is refused.
func TestAChatIsMadeOnTelegram(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.serveUpdates(&updatesOf{pts: 1})
	d := f.cluster.Dispatch(2, "dc2")
	var mu sync.Mutex
	var made tg.ChannelsCreateChannelRequest
	var invited []tg.InputUserClass
	d.HandleFunc(tg.ChannelsCreateChannelRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.ChannelsCreateChannelRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		mu.Lock()
		made = req
		mu.Unlock()
		return sendResult(s, r, &tg.Updates{Date: 1000, Chats: []tg.ChatClass{
			&tg.Channel{ID: 77, AccessHash: 770, Title: req.Title, Megagroup: true, Forum: true, Photo: &tg.ChatPhotoEmpty{}},
		}})
	})
	d.HandleFunc(tg.ChannelsInviteToChannelRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.ChannelsInviteToChannelRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		mu.Lock()
		invited = req.Users
		mu.Unlock()
		return sendResult(s, r, &tg.MessagesInvitedUsers{Updates: &tg.Updates{Date: 1000}})
	})
	st := openStore(t)
	knowDana(t, st)
	a, _ := loggedInWithStore(t, f, st)
	ctx := t.Context()
	on := domain.AccountRooms(domain.ProtocolTelegram, "42")
	room, err := a.CreateRoom(ctx, domain.NewRoom{Name: "Hikers", On: on, Kind: domain.ChatForum, Invite: []string{personID(dana.ID), personID(99)}})
	if room != roomID(42, -(channelMark+77)) {
		t.Errorf("room = %q, want the new channel's", room)
	}
	if err == nil || !strings.Contains(err.Error(), personID(99)) || strings.Contains(err.Error(), personID(dana.ID)) {
		t.Errorf("err = %v, want the unseen person named alone", err)
	}
	mu.Lock()
	if made.Title != "Hikers" || !made.Megagroup || !made.Forum || made.Broadcast {
		t.Errorf("made %+v, want a forum called Hikers", made)
	}
	if len(invited) != 1 || invited[0].(*tg.InputUser).UserID != dana.ID || invited[0].(*tg.InputUser).AccessHash != dana.AccessHash {
		t.Errorf("invited %+v, want Dana by her hash", invited)
	}
	mu.Unlock()
	if hash, ok, _ := st.GetChannelAccessHash(ctx, 42, 77); !ok || hash != 770 {
		t.Errorf("the new channel's hash = (%d, %v), want it kept", hash, ok)
	}
	if _, err := a.CreateRoom(ctx, domain.NewRoom{Name: "x", On: on, Kind: domain.ChatCommunity}); err == nil {
		t.Error("a community was made on Telegram")
	}
}
