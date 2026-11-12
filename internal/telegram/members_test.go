package telegram

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/gotd/td/tgtest"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A basic group's and a supergroup's members are fetched and replace the cached ones,
// named; whoever only invited or promoted them, and the banned, are no members. A
// channel that lists its subscribers to admins alone, a private chat, and an account
// not connected keep what the cache knows, without an error.
func TestAGroupsMembersAreFetched(t *testing.T) {
	t.Parallel()
	const supergroup, broadcast = int64(33), int64(34)
	eli := func() *tg.User { return &tg.User{ID: 8, AccessHash: 80, FirstName: "Eli"} }
	inviter := func() *tg.User { return &tg.User{ID: 9, AccessHash: 90, FirstName: "Ivo"} }
	banned := func() *tg.User { return &tg.User{ID: 10, AccessHash: 100, FirstName: "Bo"} }
	f := newFakeTelegram(t)
	d := f.cluster.Dispatch(2, "dc2")
	var mu sync.Mutex
	var asked []string
	d.HandleFunc(tg.MessagesGetFullChatRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.MessagesGetFullChatRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		return sendResult(s, r, &tg.MessagesChatFull{
			FullChat: &tg.ChatFull{ID: req.ChatID, Participants: &tg.ChatParticipants{ChatID: req.ChatID, Participants: []tg.ChatParticipantClass{
				&tg.ChatParticipant{UserID: 7, InviterID: 9}, &tg.ChatParticipantCreator{UserID: 8},
			}}},
			Users: []tg.UserClass{&tg.User{ID: 7, AccessHash: 70, FirstName: "Dana"}, eli(), inviter()},
		})
	})
	d.HandleFunc(tg.ChannelsGetParticipantsRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.ChannelsGetParticipantsRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		channel, _ := req.Channel.(*tg.InputChannel)
		mu.Lock()
		asked = append(asked, req.Filter.TypeName())
		mu.Unlock()
		if channel == nil || channel.ChannelID == broadcast || req.Limit != membersPage {
			return s.SendErr(r, tgerr.New(400, "CHAT_ADMIN_REQUIRED"))
		}
		return sendResult(s, r, &tg.ChannelsChannelParticipants{
			Count: 3,
			Participants: []tg.ChannelParticipantClass{
				&tg.ChannelParticipant{UserID: 7}, &tg.ChannelParticipantAdmin{UserID: 8, PromotedBy: 9},
				&tg.ChannelParticipantBanned{Peer: &tg.PeerUser{UserID: 10}, KickedBy: 9},
			},
			Users: []tg.UserClass{&tg.User{ID: 7, AccessHash: 70, FirstName: "Dana"}, eli(), inviter(), banned()},
		})
	})
	st := openStore(t)
	for _, channel := range []int64{supergroup, broadcast} {
		if err := st.SetChannelAccessHash(t.Context(), 42, channel, channel*10); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetUserAccessHash(t.Context(), 42, 7, 70); err != nil { // a private chat it can reach
		t.Fatal(err)
	}
	a, cache := loggedInWithStore(t, f, st)
	ctx := t.Context()
	group, super, channel, private := roomID(42, -11), roomID(42, -(channelMark+supergroup)), roomID(42, -(channelMark+broadcast)), roomID(42, 7)
	gone := []domain.Member{{UserID: "telegram:12", DisplayName: "Gone"}}
	for _, room := range []domain.RoomID{group, channel, private} {
		if err := cache.SaveMembers(ctx, room, gone); err != nil {
			t.Fatal(err)
		}
	}

	members := []domain.Member{{UserID: "telegram:7", DisplayName: "Dana"}, {UserID: "telegram:8", DisplayName: "Eli"}}
	for room, want := range map[domain.RoomID][]domain.Member{group: members, super: members, channel: gone, private: gone} {
		got, err := a.RefreshMembers(ctx, room)
		if err != nil {
			t.Errorf("RefreshMembers(%s) = %v", room, err)
		}
		if !slices.Equal(got, want) {
			t.Errorf("RefreshMembers(%s) = %v, want %v", room, got, want)
		}
		if cached, _ := a.Members(ctx, room, 0); !slices.Equal(cached, want) {
			t.Errorf("cached members of %s = %v, want %v", room, cached, want)
		}
	}
	mu.Lock()
	if want := []string{"channelParticipantsRecent", "channelParticipantsRecent"}; !slices.Equal(asked, want) {
		t.Errorf("asked the supergroup and the channel for %v, want their recent members", asked)
	}
	mu.Unlock()

	offline, offlineCache := cachedAdapter(t, &memSecrets{values: map[string]string{}})
	if err := offlineCache.SaveMembers(ctx, group, gone); err != nil {
		t.Fatal(err)
	}
	if got, err := offline.RefreshMembers(ctx, group); err != nil || !slices.Equal(got, gone) {
		t.Errorf("RefreshMembers not connected = (%v, %v), want the cached %v", got, err, gone)
	}
}

// Mention completion offers who spoke last first, then who is mentioned most, then
// everyone else; a room whose members are not known offers its recent speakers.
func TestTelegramMentionsRankSpeakersFirst(t *testing.T) {
	t.Parallel()
	a, cache := cachedAdapter(t, &memSecrets{values: map[string]string{}})
	ctx := t.Context()
	group, channel := roomID(42, -11), roomID(42, -(channelMark+34))
	if err := cache.SaveMembers(ctx, group, []domain.Member{
		{UserID: "telegram:7", DisplayName: "Dana"}, {UserID: "telegram:8", DisplayName: "Eli"}, {UserID: "telegram:9", DisplayName: "Finn"},
	}); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1_700_000_000, 0)
	for room, senders := range map[domain.RoomID][]string{group: {"telegram:9"}, channel: {"telegram:7", "telegram:-1000000000034"}} {
		var msgs []domain.Message
		for i, sender := range senders {
			id := domain.EventID(string(room) + "/" + string(rune('1'+i)))
			msgs = append(msgs, domain.Message{ID: id, RoomID: room, Sender: sender, Body: "hi", Timestamp: at.Add(time.Duration(i) * time.Minute)})
		}
		if err := cache.SaveMessages(ctx, room, msgs); err != nil {
			t.Fatal(err)
		}
	}
	if err := cache.RecordMention(ctx, group, "telegram:8", at.UnixMilli()); err != nil {
		t.Fatal(err)
	}

	for room, want := range map[domain.RoomID][]string{
		group:   {"telegram:9", "telegram:8", "telegram:7"},
		channel: {"telegram:-1000000000034", "telegram:7"},
	} {
		got, err := a.MentionCandidates(ctx, room, 0)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, m := range got {
			ids = append(ids, m.UserID)
		}
		if !slices.Equal(ids, want) {
			t.Errorf("MentionCandidates(%s) = %v, want %v", room, ids, want)
		}
	}
}
