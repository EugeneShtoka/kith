package matrix

import (
	"context"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A room is plain only when the state store holds its state and no encryption event.
// A store that has not seen the room (new, reset, not caught up with sync) holds no
// encryption row for it either, and "unknown" must read as encrypted: it decides
// whether an assistant may read the room's cached, decrypted history.
func TestARoomWhoseStateIsUnknownReadsAsEncrypted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := mautrix.NewMemoryStateStore()
	// The memory store cannot fail.
	_ = store.SetPowerLevels(ctx, "!plain:x", &event.PowerLevelsEventContent{})
	_ = store.SetPowerLevels(ctx, "!sealed:x", &event.PowerLevelsEventContent{})
	_ = store.SetEncryptionEvent(ctx, "!sealed:x", &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1})
	client, err := mautrix.NewClient("http://127.0.0.1:1", "@me:x", "token")
	if err != nil {
		t.Fatal(err)
	}
	client.StateStore = store
	b := New(nil)
	b.client = client

	got, err := b.RoomEncryption(ctx, []domain.RoomID{"!plain:x", "!sealed:x", "!unseen:x"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[domain.RoomID]bool{"!plain:x": false, "!sealed:x": true, "!unseen:x": true}
	for room, encrypted := range want {
		if got[room] != encrypted {
			t.Errorf("%s: encrypted = %v, want %v", room, got[room], encrypted)
		}
	}
}
