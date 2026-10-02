package whatsapp

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// offline is an adapter over a fresh cache and store, never connected.
func offline(t *testing.T, accounts ...Account) (*Adapter, *db.Cache, *Store) {
	t.Helper()
	ctx := context.Background()
	cache, err := db.Open(ctx, filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "whatsapp.db"), NewStoreLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return New(cache, store, accounts, nil), cache, store
}

var waRoom = domain.RoomID("whatsapp:" + ownDigits + "/1203@g.us")

// The adapter lists WhatsApp's rooms from the shared cache and none of Matrix's.
func TestTheAdapterListsOnlyWhatsAppRooms(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, cache, _ := offline(t)
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!a:x"}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, ownDigits), []domain.Room{{ID: waRoom}}); err != nil {
		t.Fatal(err)
	}
	rooms, err := a.Rooms(ctx)
	if err != nil || len(rooms) != 1 || rooms[0].ID != waRoom {
		t.Errorf("Rooms = (%v, %v), want only the WhatsApp room", rooms, err)
	}
}

// Stars and spam verdicts on WhatsApp rooms are kith's own, kept in the cache.
func TestStarsAndSpamAreKeptLocally(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, cache, _ := offline(t)
	if err := cache.SaveRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, ownDigits), []domain.Room{{ID: waRoom}}); err != nil {
		t.Fatal(err)
	}
	if err := a.StarMessage(ctx, waRoom, "whatsapp:"+ownDigits+"/3EB0", true); err != nil {
		t.Fatal(err)
	}
	if stars, _ := cache.Starred(ctx, waRoom); len(stars) != 1 {
		t.Errorf("stars = %v, want the one", stars)
	}
	if err := a.StarMessage(ctx, waRoom, "whatsapp:"+ownDigits+"/3EB0", false); err != nil {
		t.Fatal(err)
	}
	if stars, _ := cache.Starred(ctx, waRoom); len(stars) != 0 {
		t.Errorf("stars after unstarring = %v", stars)
	}
	if err := a.MarkSpam(ctx, domain.SpamVerdict{Room: waRoom, Rule: domain.SpamFirstMessage}); err != nil {
		t.Fatal(err)
	}
	if spam, _ := cache.SpamRooms(ctx); len(spam) != 1 || spam[0].Room != waRoom {
		t.Errorf("spam = %+v, want the verdict", spam)
	}
}

// What comes with messages is refused, saying so, rather than pretending to work.
func TestWhatComesLaterIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, _, _ := offline(t)
	for name, err := range map[string]error{
		"Redact":   a.Redact(ctx, waRoom, "e", ""),
		"SendFile": a.SendFile(ctx, waRoom, "/f", ""),
		"React":    a.SendReaction(ctx, waRoom, "e", "👍"),
	} {
		if !errors.Is(err, api.ErrNotOnNetwork) {
			t.Errorf("%s = %v, want ErrNotOnNetwork", name, err)
		}
	}
	encrypted, err := a.RoomEncryption(ctx, []domain.RoomID{waRoom})
	if err != nil || !encrypted[waRoom] {
		t.Errorf("RoomEncryption = (%v, %v), want encrypted: every WhatsApp chat is", encrypted, err)
	}
}

// Pairing refuses an account the config does not list, and one already linked.
func TestPairingRefusesUnknownAndLinkedAccounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, _, store := offline(t, Account{Name: "bg", Digits: ownDigits})
	never := func(string) error { t.Error("a code was asked for"); return nil }

	if _, err := a.PairWhatsApp(ctx, "il", never); !errors.Is(err, errNoAccount) {
		t.Errorf("an unknown account = %v, want errNoAccount", err)
	}
	device := store.container.NewDevice()
	jid := types.NewJID(ownDigits, types.DefaultUserServer)
	device.ID = &jid
	device.Account = &waAdv.ADVSignedDeviceIdentity{
		Details: []byte{0}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64),
	}
	if err := store.container.PutDevice(ctx, device); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PairWhatsApp(ctx, "bg", never); !errors.Is(err, errAlreadyLinked) {
		t.Errorf("a linked account = %v, want errAlreadyLinked", err)
	}
	// While one pairing runs, a second for the same account is refused before it
	// looks at the store (where it would find nothing linked yet).
	if !a.beginPairing(a.accounts[0]) {
		t.Fatal("a free account could not be claimed")
	}
	if _, err := a.PairWhatsApp(ctx, "bg", never); !errors.Is(err, errPairing) {
		t.Errorf("a second pairing at once = %v, want errPairing", err)
	}
	a.endPairing(a.accounts[0])
}

// Stopping ends every stream, so the router's merge and the daemon's pumps finish.
func TestStoppingEndsTheStreams(t *testing.T) {
	t.Parallel()
	a, _, _ := offline(t)
	a.Stop()
	a.Stop() // twice is harmless
	for name, closed := range map[string]bool{
		"messages":  isClosed(a.Messages()),
		"unread":    isClosed(a.Unread()),
		"reactions": isClosed(a.Reactions()),
		"activity":  isClosed(a.Activity()),
	} {
		if !closed {
			t.Errorf("%s still open after Stop", name)
		}
	}
}

func isClosed[T any](ch <-chan T) bool {
	select {
	case _, ok := <-ch:
		return !ok
	default:
		return false
	}
}
