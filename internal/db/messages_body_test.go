package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Every readable body arrives once across pages, never redacted or empty ones, each
// marked whether it was mine.
func TestEachMessageBodyReadsEveryBodyOnceAcrossPages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)

	const waMe = "whatsapp:44880000001@s.whatsapp.net"
	const perRoom = 1500 // three rooms: more than two pages, under the per-room cap
	want := map[string]bool{}
	for r := range 3 {
		room := domain.RoomID(fmt.Sprintf(mx+"!r%d:x", r))
		msgs := make([]domain.Message, 0, perRoom+2)
		for i := range perRoom {
			body := fmt.Sprintf("r%d m%d", r, i)
			// This person writes as their Matrix and their WhatsApp account.
			sender := "matrix:@b:x"
			switch i % 10 {
			case 0:
				sender = "matrix:@me:x"
			case 5:
				sender = waMe
			}
			want[body] = sender == "matrix:@me:x" || sender == waMe
			msgs = append(msgs, domain.Message{
				ID: domain.EventID(fmt.Sprintf(mx+"$%d-%d", r, i)), RoomID: room, Sender: sender,
				Body: body, Timestamp: time.UnixMilli(int64(i) * 1000),
			})
		}
		msgs = append(msgs,
			domain.Message{ID: domain.EventID(fmt.Sprintf(mx+"$%d-gone", r)), RoomID: room, Sender: "matrix:@b:x", Body: "gone", Redacted: true, Timestamp: time.UnixMilli(1)},
			domain.Message{ID: domain.EventID(fmt.Sprintf(mx+"$%d-empty", r)), RoomID: room, Sender: "matrix:@b:x", Timestamp: time.UnixMilli(2)},
		)
		if err := cache.SaveMessages(ctx, room, msgs); err != nil {
			t.Fatalf("SaveMessages: %v", err)
		}
	}

	got := map[string]bool{}
	err := cache.EachMessageBody(ctx, []string{"matrix:@me:x", waMe}, func(body string, mine bool) {
		if _, dup := got[body]; dup {
			t.Errorf("%q arrived twice", body)
		}
		got[body] = mine
	})
	if err != nil {
		t.Fatalf("EachMessageBody: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d bodies, want %d", len(got), len(want))
	}
	for body, mine := range want {
		if seen, ok := got[body]; !ok || seen != mine {
			t.Fatalf("%q: got (mine=%v, seen=%v), want mine=%v", body, seen, ok, mine)
		}
	}
}
