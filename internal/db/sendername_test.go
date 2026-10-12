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
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!dm:x"}, {ID: "!bot:x"}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveMembers(ctx, "!bot:x", []domain.Member{{UserID: "@slackbot:x", DisplayName: "Slackbot"}, {UserID: "@dana:x", DisplayName: "Dana elsewhere"}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveMembers(ctx, "!dm:x", []domain.Member{{UserID: "@dana:x", DisplayName: "Dana"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	msgs := []domain.Message{
		{ID: "$1", Sender: "@slackbot:x", Body: "A huddle started", Timestamp: now},
		{ID: "$2", Sender: "@dana:x", Body: "hi", Timestamp: now.Add(time.Second)},
		{ID: "$3", Sender: "@eli:x", SenderName: "Eli as sent", Body: "yo", Timestamp: now.Add(2 * time.Second)},
		{ID: "$4", Sender: "@nobody:x", Body: "?", Timestamp: now.Add(3 * time.Second)},
	}
	if err := cache.SaveMessages(ctx, "!dm:x", msgs); err != nil {
		t.Fatal(err)
	}
	got, err := cache.Messages(ctx, "!dm:x", 10)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, m := range got {
		names[string(m.ID)] = m.SenderName
	}
	want := map[string]string{"$1": "Slackbot", "$2": "Dana", "$3": "Eli as sent", "$4": ""}
	for id, name := range want {
		if names[id] != name {
			t.Errorf("%s named %q, want %q", id, names[id], name)
		}
	}
}
