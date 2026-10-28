package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/session"
)

// An install with two profiles, each its own daemon, becomes one daemon running both
// accounts: the config names them as [[matrix.account]] and keeps the first's
// instance; each account's crypto store, session and keyring entries are where kithd
// reads them; the second's Matrix rooms, messages (searchable), marks, schedule and
// ledger join the first's cache and files, and its own copy of another network's
// account does not.
func TestProfilesBecomeOneDaemonsAccounts(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()
	dir := t.TempDir()
	dirs := domain.Storage{DataDir: dir, StateDir: dir, KeyringService: "kith"}
	configPath := filepath.Join(dir, "config.toml")
	write(t, configPath, `[storage]
instance = "base"

[display]
time_format = "12h"

[[display.name]]
target = "room:!club:h.example"
name = "Club"

[[profile]]
name = "home"
homeserver = "https://h.example"
user = "@me:h.example"

[[profile]]
name = "work"
homeserver = "https://w.example"
user = "@me:w.example"
`)
	home, work := domain.AccountKey("@me:h.example"), domain.AccountKey("@me:w.example")
	ownerOf := func(key string) domain.RoomOwner { return domain.AccountRooms(domain.ProtocolMatrix, key) }
	at := time.Unix(1_700_000_000, 0)
	for _, p := range []struct{ name, key, body, whatsapp string }{
		{"home", home, "hello from home", "whatsapp:44/1@g.us"},
		{"work", work, "hello from work", "whatsapp:44/2@g.us"},
	} {
		storage := dirs
		storage.Instance = "base-" + p.name
		owner := ownerOf(p.key)
		room := domain.RoomID(string(owner) + "!club:x")
		key, err := cacheKey("kith", storage.CachePath())
		if err != nil {
			t.Fatal(err)
		}
		c, err := db.Open(ctx, storage.CachePath(), key)
		if err != nil {
			t.Fatal(err)
		}
		must(t, c.SaveRooms(ctx, owner, []domain.Room{{ID: room, Name: "Club " + p.name}}))
		must(t, c.SaveMessages(ctx, room, []domain.Message{{ID: domain.EventID(string(owner) + "$1"), RoomID: room, Body: p.body, Timestamp: at}}))
		must(t, c.SetPersonVerdicts(ctx, owner, []domain.PersonVerdict{{Person: "matrix:@spam-" + p.name + ":x", Tag: "spam", Rule: domain.MoveAlways, At: at}}))
		wa := domain.AccountRooms(domain.ProtocolWhatsApp, "44")
		must(t, c.SaveRooms(ctx, wa, []domain.Room{{ID: domain.RoomID(p.whatsapp), Name: "Choir"}}))
		must(t, c.SaveReactionRefusal(ctx, "WhatsApp", "🦄-"+p.name, at))
		must(t, c.Close())
		write(t, storage.CachePath()+".pre-matrixids", "") // its IDs are migrated already
		write(t, storage.CryptoPath(), "crypto of "+p.name)
		write(t, storage.SessionFile(), "session of "+p.name)
		write(t, storage.SchedulePath(), "[[message]]\nid = \""+p.name+"\"\nroom = \""+string(room)+"\"\nbody = \"later\"\n")
		write(t, storage.LedgerPath(), `{"room":"`+string(room)+`","author":"a","outcome":"sent","text":"`+p.name+"\"}\n")
		must(t, session.Save(session.Store{Service: "kith", Account: storage.Instance}, domain.Session{UserID: p.name, AccessToken: p.name}, false))
		must(t, keyring.Set("kith", storage.Instance+"|pickle", "pickle of "+p.name))
	}

	if err := runProfiles(options{config: configPath, dataDir: dir, stateDir: dir, service: "kith", profiles: true}); err != nil {
		t.Fatal(err)
	}

	text, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Decode(string(text))
	if err != nil {
		t.Fatalf("the merged config does not load: %v\n%s", err, text)
	}
	want := []config.MatrixAccount{
		{Name: "home", Homeserver: "https://h.example", User: "@me:h.example"},
		{Name: "work", Homeserver: "https://w.example", User: "@me:w.example"},
	}
	if !slices.Equal(cfg.Matrix.Accounts, want) || cfg.Storage.Instance != "base-home" || cfg.Display.TimeFormat != "12h" {
		t.Errorf("config = %+v, instance %q, time format %q\n%s", cfg.Matrix.Accounts, cfg.Storage.Instance, cfg.Display.TimeFormat, text)
	}
	if got := cfg.Display.Names[0].Target; got != "room:"+string(ownerOf(home))+"!club:h.example" {
		t.Errorf("a room entry = %q, want it the first profile's account's", got)
	}
	kept := dirs
	kept.Instance = "base-home"
	for name, key := range map[string]string{"home": home, "work": work} {
		if got, _ := os.ReadFile(kept.MatrixCryptoPath(key)); string(got) != "crypto of "+name {
			t.Errorf("%s's crypto store where kithd opens it = %q", name, got)
		}
		store := session.MatrixStore(kept, key)
		if got, found, lerr := session.Load(store, false); lerr != nil || !found || got.AccessToken != name {
			t.Errorf("%s's session where kithd reads it = (%+v, %t, %v)", name, got, found, lerr)
		}
		if got, kerr := keyring.Get("kith", store.Account+"|pickle"); kerr != nil || got != "pickle of "+name {
			t.Errorf("%s's pickle key where kithd reads it = (%q, %v)", name, got, kerr)
		}
	}

	key, err := cacheKey("kith", kept.CachePath())
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.Open(ctx, kept.CachePath(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	rooms, err := c.Rooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rooms {
		ids = append(ids, string(r.ID))
	}
	slices.Sort(ids)
	wantIDs := []string{string(ownerOf(home)) + "!club:x", string(ownerOf(work)) + "!club:x", "whatsapp:44/1@g.us"}
	slices.Sort(wantIDs)
	if !slices.Equal(ids, wantIDs) {
		t.Errorf("rooms = %v, want both accounts' clubs and the first daemon's WhatsApp group alone", ids)
	}
	hits, err := c.SearchMessages(ctx, domain.SearchRequest{Filter: domain.SearchFilter{Terms: "work"}, Rooms: domain.EveryRoom(), Limit: 10})
	if err != nil || len(hits) != 1 {
		t.Errorf("searching the merged message = (%v, %v), want it found", hits, err)
	}
	verdicts, err := c.PersonVerdicts(ctx)
	if err != nil || len(verdicts) != 2 {
		t.Errorf("person verdicts = (%v, %v), want each account's", verdicts, err)
	}
	refusals, err := c.ReactionRefusals(ctx)
	if err != nil || len(refusals) != 2 {
		t.Errorf("refused reactions = (%v, %v), want both daemons' learned", refusals, err)
	}
	for _, f := range []string{kept.SchedulePath(), kept.LedgerPath()} {
		got, _ := os.ReadFile(f)
		if !strings.Contains(string(got), "home") || !strings.Contains(string(got), "work") {
			t.Errorf("%s = %q, want both profiles' entries", filepath.Base(f), got)
		}
	}
	if got, _ := os.ReadFile(kept.LedgerPath()); strings.Count(string(got), "\n") != 2 || strings.Contains(string(got), "\n\n") {
		t.Errorf("ledger = %q, want one entry a line", got)
	}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
