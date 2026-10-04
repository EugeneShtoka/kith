package whatsapp

import (
	"context"
	"path/filepath"
	"testing"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"google.golang.org/protobuf/proto"
)

// A device linked from an opened store asks its phone for all the history it holds:
// the properties sent with the link say so.
func TestLinkingAsksForTheWholeHistory(t *testing.T) {
	t.Parallel()
	s, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "whatsapp.db"), NewStoreLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	payload := s.container.NewDevice().GetClientPayload() // not linked yet: what linking sends
	var props waCompanionReg.DeviceProps
	if err := proto.Unmarshal(payload.GetDevicePairingData().GetDeviceProps(), &props); err != nil {
		t.Fatal(err)
	}
	if !props.GetRequireFullSync() {
		t.Error("linking asks for a recent slice of history, want all of it")
	}
}
