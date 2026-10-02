package whatsapp

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// linkedClient is a client for a device stored as linked to digits, never connected.
func linkedClient(t *testing.T, store *Store, digits string) *whatsmeow.Client {
	t.Helper()
	device := store.container.NewDevice()
	jid := types.NewJID(digits, types.DefaultUserServer)
	device.ID = &jid
	device.LID = types.NewJID("100000000000009", types.HiddenUserServer)
	device.Account = &waAdv.ADVSignedDeviceIdentity{
		Details: []byte{0}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64),
	}
	if err := store.container.PutDevice(context.Background(), device); err != nil {
		t.Fatal(err)
	}
	return whatsmeow.NewClient(device, newLogger(slog.New(slog.DiscardHandler), "test"))
}

// Start with nothing linked yet waits for the end and connects nothing.
func TestStartWithNothingLinkedWaits(t *testing.T) {
	t.Parallel()
	a, _, _ := offline(t, Account{Name: "bg", Digits: ownDigits})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Start(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Start = %v, want our own cancel", err)
	}
	if err := a.Start(context.Background()); !errors.Is(err, errStartedTwice) {
		t.Errorf("a second Start = %v", err)
	}
	if got := a.connected(); len(got) != 0 {
		t.Errorf("connected = %v with nothing linked", got)
	}
}

// A linked account is this person, by phone number and LID; the phone unlinking it
// drops it.
func TestALinkedAccountIsMeUntilThePhoneUnlinksIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "bg", Digits: ownDigits}
	a, _, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	a.clients[ownDigits] = client

	me := a.Me()
	if len(me) != 2 || me[0] != "whatsapp:"+ownDigits+"@s.whatsapp.net" || !strings.HasSuffix(me[1], "@lid") {
		t.Errorf("Me = %v, want the phone number and the LID", me)
	}
	if name := a.names(client)(ctx, pn(danaPhone)); name != "" {
		t.Errorf("an unknown contact's name = %q", name)
	}
	// Not connected: the refresh fails, saying so, and writes nothing.
	if _, err := a.RefreshRooms(ctx); err == nil {
		t.Error("RefreshRooms without a connection succeeded")
	}
	if _, err := a.RefreshMembers(ctx, waRoom); err == nil {
		t.Error("RefreshMembers without a connection succeeded")
	}
	a.handle(account, client, &events.Connected{}) // a refresh, which fails in the background
	a.handle(account, client, &events.LoggedOut{})
	if got := a.connected(); len(got) != 0 {
		t.Errorf("connected after the phone unlinked = %v", got)
	}
	if me := a.Me(); len(me) != 0 {
		t.Errorf("Me after unlinking = %v", me)
	}
}

// What needs nothing from WhatsApp answers from the cache, or with nothing.
func TestTheQuietAnswers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, cache, _ := offline(t)
	if err := cache.SaveRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, ownDigits), []domain.Room{{ID: waRoom}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveMembers(ctx, waRoom, []domain.Member{{UserID: "whatsapp:" + danaPhone + "@s.whatsapp.net", DisplayName: "Dana"}}); err != nil {
		t.Fatal(err)
	}
	if got, err := a.MentionCandidates(ctx, waRoom, 0); err != nil || len(got) != 1 {
		t.Errorf("MentionCandidates = (%v, %v)", got, err)
	}
	if got, err := a.RefreshMembers(ctx, waRoom); err != nil || len(got) != 1 {
		t.Errorf("RefreshMembers with no account connected = (%v, %v), want the cached", got, err)
	}
	if got, err := a.RefreshRooms(ctx); err != nil || len(got) != 0 {
		t.Errorf("RefreshRooms with nothing connected = (%v, %v)", got, err)
	}
	if got, err := a.CachedUnread(ctx); err != nil || got != nil {
		t.Errorf("CachedUnread = (%v, %v)", got, err)
	}
	if got, err := a.DirectCandidates(ctx, 5); err != nil || got != nil {
		t.Errorf("DirectCandidates = (%v, %v)", got, err)
	}
	if got, err := a.CanonicalParent(ctx, waRoom); err != nil || got != "" {
		t.Errorf("CanonicalParent = (%v, %v)", got, err)
	}
	if page, err := a.Timeline(ctx, waRoom, "", 10); err != nil || len(page.Messages) != 0 {
		t.Errorf("Timeline = (%v, %v)", page, err)
	}
	if err := a.SendTyping(ctx, waRoom, true, time.Second); err != nil {
		t.Errorf("SendTyping = %v", err)
	}
	if err := a.RewindSync(ctx); err != nil || a.Account() != "" {
		t.Errorf("RewindSync = %v, Account = %q", err, a.Account())
	}
	for name, err := range map[string]error{
		"MarkRoomUnread": a.MarkRoomUnread(ctx, waRoom, true),
		"MessageHistory": third(a.MessageHistory(ctx, waRoom, "e")),
		"FetchEvent":     second(a.FetchEvent(ctx, waRoom, "e")),
		"LoadImage":      second(a.LoadImage(ctx, waRoom, "e")),
	} {
		if !errors.Is(err, api.ErrNotOnNetwork) {
			t.Errorf("%s = %v, want ErrNotOnNetwork", name, err)
		}
	}
}

func second[T any](_ T, err error) error        { return err }
func third[T, U any](_ T, _ U, err error) error { return err }

// whatsmeow's log goes to kith's at the levels kith uses.
func TestWhatsmeowLogsThroughKith(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	log := NewStoreLogger(slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug}))).Sub("Client")
	log.Errorf("request %d failed", 1)
	log.Warnf("slow")
	log.Infof("bookkeeping")
	log.Debugf("detail")
	out := b.String()
	for _, want := range []string{"level=WARN msg=\"request 1 failed\"", "level=WARN msg=slow", "level=DEBUG msg=bookkeeping", "module=Client"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	if displayName() == "" || !strings.HasPrefix(displayName(), "Chrome (") {
		t.Errorf("displayName = %q, want WhatsApp's \"Browser (OS)\"", displayName())
	}
}
