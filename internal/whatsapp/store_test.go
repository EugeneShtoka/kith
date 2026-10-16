package whatsapp

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"

	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// A device linked from an opened store requests full history.
func TestLinkingAsksForTheWholeHistory(t *testing.T) {
	t.Parallel()
	s, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "whatsapp.db"), make([]byte, sealeddb.KeyLen), NewStoreLogger(nil))
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

// A plain store from before stores were encrypted opens encrypted, its linked device
// and its sessions all there, and the file no longer holds them in the clear.
func TestAPlainStoreFromBeforeOpensEncrypted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "whatsapp.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	plain := sqlstore.NewWithDB(raw, "sqlite3", NewStoreLogger(nil))
	if err = plain.Upgrade(ctx); err != nil {
		t.Fatal(err)
	}
	jid := types.JID{User: "15550100001", Device: 1, Server: types.DefaultUserServer}
	dev := plain.NewDevice()
	dev.ID = &jid
	dev.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte("d"), AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err = plain.PutDevice(ctx, dev); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"15550100009.0", "15550100010.0"} {
		if err = dev.Sessions.PutSession(ctx, addr, []byte("session of "+addr)); err != nil {
			t.Fatal(err)
		}
	}
	_ = raw.Close()

	key := bytes.Repeat([]byte{7}, sealeddb.KeyLen)
	s, err := OpenStore(ctx, path, key, NewStoreLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	got, err := s.container.GetDevice(ctx, jid)
	if err != nil || got == nil {
		t.Fatalf("the linked device is gone: %v", err)
	}
	for _, addr := range []string{"15550100009.0", "15550100010.0"} {
		if session, err := got.Sessions.GetSession(ctx, addr); err != nil || string(session) != "session of "+addr {
			t.Errorf("session %s read back as %q (%v)", addr, session, err)
		}
	}
	if file, _ := os.ReadFile(path); bytes.Contains(file, []byte("session of")) || bytes.HasPrefix(file, []byte("SQLite format 3")) {
		t.Error("the store is still in the clear")
	}
}
