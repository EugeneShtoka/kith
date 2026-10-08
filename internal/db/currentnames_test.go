package db

import (
	"context"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A message read from the cache names its sender and the people it mentions as the
// room names them now: a member who renamed is read by the new name, in the
// timeline and in search alike, and someone the room does not list keeps the words
// the message was sent with.
func TestMessagesAreReadWithTheNamesKnownNow(t *testing.T) {
	t.Parallel()
	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID("slack:T1/C1")
	members := func(names map[string]string) {
		t.Helper()
		var list []domain.Member
		for id, name := range names {
			list = append(list, domain.Member{UserID: id, DisplayName: name})
		}
		if err := cache.SaveMembers(ctx, room, list); err != nil {
			t.Fatal(err)
		}
	}
	members(map[string]string{"slack:T1/U1": "Ann Old", "slack:T1/U2": "Bo Old"})
	mustSave(t, cache, room, domain.Message{
		ID: "slack:T1/C1/1.0", Sender: "slack:T1/U1", SenderName: "Ann Old", Body: "@Bo Old and @Cy, standup moved",
		Timestamp: time.UnixMilli(1_700_000_000_000),
		Mentions:  []domain.Mention{{UserID: "slack:T1/U2", Name: "@Bo Old"}, {UserID: "slack:T1/U3", Name: "@Cy"}},
	})
	members(map[string]string{"slack:T1/U1": "Ann New", "slack:T1/U2": "Bo New"})

	msgs, err := cache.Messages(ctx, room, 10)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("Messages = %v, %v", msgs, err)
	}
	check := func(where, sender string, mentions []domain.Mention) {
		t.Helper()
		if sender != "Ann New" {
			t.Errorf("%s: sender %q, want the name now", where, sender)
		}
		if len(mentions) != 2 || mentions[0].Known != "Bo New" || mentions[0].Name != "@Bo Old" || mentions[1].Known != "" {
			t.Errorf("%s: mentions %+v, want Bo known by the name now, the words kept, Cy known by none", where, mentions)
		}
	}
	check("timeline", msgs[0].SenderName, msgs[0].Mentions)
	hits, err := cache.SearchMessages(ctx, domain.SearchRequest{
		Filter: domain.SearchFilter{Terms: "standup"}, Rooms: domain.TheseRooms([]domain.RoomID{room}), Limit: 10,
	})
	if err != nil || len(hits) != 1 {
		t.Fatalf("search = %v, %v", hits, err)
	}
	check("search", hits[0].SenderName, hits[0].Mentions)
	if name, err := cache.RoomMemberName(ctx, room, "slack:T1/U2"); err != nil || name != "Bo New" {
		t.Errorf("RoomMemberName = %q, %v", name, err)
	}
}
