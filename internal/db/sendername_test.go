package db

import (
	"context"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A message read back is named by its sender's name in the room, else the name it
// carried, else their name in any other room: a bridge's ghost may write where it is no
// member (Slackbot announcing a huddle in a DM).
func TestASenderIsNamedFromAnyRoomWhenTheirOwnDoesNot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	if err := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!dm:x"}, {ID: mx + "!bot:x"}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveMembers(ctx, mx+"!bot:x", []domain.Member{{UserID: "matrix:@slackbot:x", DisplayName: "Slackbot"}, {UserID: "matrix:@dana:x", DisplayName: "Dana elsewhere"}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveMembers(ctx, mx+"!dm:x", []domain.Member{{UserID: "matrix:@dana:x", DisplayName: "Dana"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	msgs := []domain.Message{
		{ID: mx + "$1", Sender: "matrix:@slackbot:x", Body: "A huddle started", Timestamp: now},
		{ID: mx + "$2", Sender: "matrix:@dana:x", Body: "hi", Timestamp: now.Add(time.Second)},
		{ID: mx + "$3", Sender: "matrix:@eli:x", SenderName: "Eli as sent", Body: "yo", Timestamp: now.Add(2 * time.Second)},
		{ID: mx + "$4", Sender: "matrix:@nobody:x", Body: "?", Timestamp: now.Add(3 * time.Second)},
	}
	if err := cache.SaveMessages(ctx, mx+"!dm:x", msgs); err != nil {
		t.Fatal(err)
	}
	got, err := cache.Messages(ctx, mx+"!dm:x", 10)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, m := range got {
		names[string(m.ID)] = m.SenderName
	}
	want := map[string]string{mx + "$1": "Slackbot", mx + "$2": "Dana", mx + "$3": "Eli as sent", mx + "$4": ""}
	for id, name := range want {
		if names[id] != name {
			t.Errorf("%s named %q, want %q", id, names[id], name)
		}
	}
}
