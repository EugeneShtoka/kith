package main

import (
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Each message carries its link: its network's by default, kith's own with
// `[agent] links = "kith"`.
func TestEachMessageCarriesItsLink(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	f.messages[mx+"!open:x"] = []domain.Message{
		{ID: mx + "$1", RoomID: mx + "!open:x", Sender: "matrix:@dana:x", Body: "morning", Timestamp: time.Unix(1_700_000_000, 0)},
	}
	for _, tc := range []struct {
		kith bool
		want string
	}{
		{false, "https://matrix.to/#/%21open:x/$1"},
		{true, domain.KithLink(mx+"!open:x", mx+"$1")},
	} {
		s := newServer(f, shareAllEncrypted)
		s.kithLinks = tc.kith
		msgs, _ := call(t, s, "read_room", map[string]any{"room": "Standup"})["messages"].([]any)
		if len(msgs) != 1 {
			t.Fatalf("read_room gave %d messages, want 1", len(msgs))
		}
		if got, _ := msgs[0].(map[string]any)["link"].(string); got != tc.want {
			t.Errorf("kith links %v: link = %q, want %q", tc.kith, got, tc.want)
		}
	}
}
