package matrix

import (
	"context"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A room is plain only when the state store holds its state without an encryption
// event; an unseen room reads as encrypted, since this gates assistant access.
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
	b := newBackend(nil)
	b.client = client

	got, err := b.RoomEncryption(ctx, []domain.RoomID{mx + "!plain:x", mx + "!sealed:x", mx + "!unseen:x"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[domain.RoomID]bool{mx + "!plain:x": false, mx + "!sealed:x": true, mx + "!unseen:x": true}
	for room, encrypted := range want {
		if got[room] != encrypted {
			t.Errorf("%s: encrypted = %v, want %v", room, got[room], encrypted)
		}
	}
}
