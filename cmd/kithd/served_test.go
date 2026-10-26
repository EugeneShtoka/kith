package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/session"
	"github.com/zalando/go-keyring"
)

// TestMain keeps tests off the system keyring: an in-memory one stands in, holding a
// Matrix session and the network stores' keys for the test's life.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

// servedFor builds the daemon's backend for cfg in a fresh instance, with a Matrix
// session saved when saved has one.
// keyringTurn is the in-memory keyring's turn: go-keyring's mock takes no concurrent
// callers, as the OS keyring does, so parallel tests take turns building a backend.
var keyringTurn sync.Mutex

func servedFor(t *testing.T, cfg config.Config, saved domain.Session) served {
	t.Helper()
	return servedWith(t, cfg, saved, nil)
}

// servedWith is servedFor that runs prepare on the instance's storage (nothing opened
// yet) first.
func servedWith(t *testing.T, cfg config.Config, saved domain.Session, prepare func(domain.Storage)) served {
	t.Helper()
	keyringTurn.Lock()
	defer keyringTurn.Unlock()
	dir := t.TempDir()
	storage := domain.Storage{Instance: "test", DataDir: dir, StateDir: dir, CacheDir: dir, RuntimeDir: dir, KeyringService: "kith-test"}
	if prepare != nil {
		prepare(storage)
	}
	if saved.AccessToken != "" {
		key := domain.AccountKey(cfg.Matrix.Accounts[0].User)
		if err := session.Save(session.MatrixStore(storage, key), saved, true); err != nil {
			t.Fatal(err)
		}
	}
	cache, err := db.Open(context.Background(), storage.CachePath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	backend := newServed(context.Background(), cache, slog.New(slog.DiscardHandler), cfg, storage)
	t.Cleanup(func() {
		backend.Stop()
		backend.Close(slog.New(slog.DiscardHandler))
	})
	return backend
}

// A WhatsApp-only config serves: Matrix has no account, empty lists, and nothing to
// wait for.
func TestADaemonWithoutMatrixServes(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Profiles: everyRoom, WhatsApp: config.WhatsApp{Accounts: []config.WhatsAppAccount{{Name: "home", Phone: "+44 7700 900001"}}}}
	backend := servedFor(t, cfg, domain.Session{})
	if n := backend.matrix.LoginNetwork(); len(n.Accounts) != 0 || backend.matrix.LoggedIn() {
		t.Errorf("Matrix without an account in the config = %+v, logged in %v; want no account", n, backend.matrix.LoggedIn())
	}
	ctx := context.Background()
	if _, err := backend.Rooms(ctx); err != nil {
		t.Errorf("Rooms = %v", err)
	}
	if spaces, err := backend.Spaces(ctx); err != nil || len(spaces) != 0 {
		t.Errorf("Spaces = (%v, %v), want none", spaces, err)
	}
	if got := expected(ctx, slog.New(slog.DiscardHandler), backend); len(got) != 0 {
		t.Errorf("expected = %v with nothing linked, want nothing to wait for", got)
	}
}

// A reload that hides someone recounts the rooms they wrote in, through the network
// that owns each room, onto the daemon's unread stream.
func TestAReloadThatHidesSomeoneRecountsTheirRooms(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := config.Config{Profiles: everyRoom, WhatsApp: config.WhatsApp{Accounts: []config.WhatsAppAccount{{Name: "home", Phone: "+1 555 000 0001"}}}}
	backend := servedFor(t, cfg, domain.Session{})
	const room domain.RoomID = "whatsapp:15550000001/120363@g.us"
	start := time.Unix(1_700_000_000, 0)
	if err := backend.cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "whatsapp:15550000001/A1", RoomID: room, Sender: "whatsapp:15550000002@s.whatsapp.net", Body: "hi", Timestamp: start.Add(time.Minute)},
		{ID: "whatsapp:15550000001/A2", RoomID: room, Sender: "whatsapp:15550000003@s.whatsapp.net", Body: "buy", Timestamp: start.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := backend.cache.SaveUnread(ctx, domain.Unread{RoomID: room}, start.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	// The hook is registered with the workers, as kithd starts them.
	if _, err := newWorkers(slog.New(slog.DiscardHandler), cfg, backend); err != nil {
		t.Fatal(err)
	}
	stream := backend.Unread()
	muted := false
	cfg.Tags = []config.Tag{{Name: "Spam", Notifies: &muted, People: []string{"whatsapp:15550000003@s.whatsapp.net"}}}
	if err := backend.useHidden(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	select {
	case u := <-stream:
		if u.RoomID != room || !u.Counted || u.Messages != 1 {
			t.Errorf("streamed %+v, want the room counted at 1 without the hidden person", u)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hiding someone streamed no recount")
	}
}

// A filter's mark through the daemon hides the person once their tag is muted, on a
// network that keeps its marks in the cache; a release shows them again.
func TestAMarkThroughTheDaemonHidesThePerson(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	muted := false
	cfg := config.Config{
		Profiles: everyRoom,
		WhatsApp: config.WhatsApp{Accounts: []config.WhatsAppAccount{{Name: "home", Phone: "+1 555 000 0001"}}},
		Tags:     []config.Tag{{Name: "Spam", Notifies: &muted}},
	}
	backend := servedFor(t, cfg, domain.Session{})
	if err := backend.useHidden(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	const spammer = "whatsapp:15550000003@s.whatsapp.net"
	msg := domain.Message{ID: "whatsapp:15550000001/A2", RoomID: "whatsapp:15550000001/120363@g.us", Sender: spammer, Body: "buy"}
	for _, step := range []struct {
		verdict domain.PersonVerdict
		hidden  bool
	}{
		{domain.PersonVerdict{Person: spammer, Tag: "Spam", Rule: domain.MoveAlways, Filter: "crypto"}, true},
		{domain.PersonVerdict{Person: spammer, Tag: "Spam", Released: true}, false},
	} {
		if err := backend.MarkPerson(ctx, step.verdict); err != nil {
			t.Fatalf("MarkPerson(%+v) = %v", step.verdict, err)
		}
		if hidden, err := backend.cache.Hides(ctx, msg); err != nil || hidden != step.hidden {
			t.Errorf("after %+v hidden = %v (%v), want %v", step.verdict, hidden, err, step.hidden)
		}
	}
	if err := backend.MarkPerson(ctx, domain.PersonVerdict{Person: spammer, Tag: "Spam", Rule: domain.MoveAlways}); !errors.Is(err, domain.ErrPersonReleased) {
		t.Errorf("a mark after the release = %v, want ErrPersonReleased", err)
	}
}

// The daemon's own wiring marks a sender: a sender_first filter catching someone's
// first message marks them in the cache, and the muted tag hides them from then on.
func TestAFilterMarksASenderThroughTheDaemon(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	muted := false
	cfg := config.Config{
		Profiles: everyRoom,
		WhatsApp: config.WhatsApp{Accounts: []config.WhatsAppAccount{{Name: "home", Phone: "+1 555 000 0001"}}},
		Tags:     []config.Tag{{Name: "Spam", Notifies: &muted}},
		Filters:  []config.Filter{{Name: "crypto", Tag: "Spam", Words: []string{"*bitcoin*"}, Moves: []string{config.FilterMoveSenderFirst}}},
	}
	backend := servedFor(t, cfg, domain.Session{})
	if err := backend.useHidden(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	w, err := newWorkers(slog.New(slog.DiscardHandler), cfg, backend)
	if err != nil {
		t.Fatal(err)
	}
	w.notifications.Synced(mxAccount, time.Now())
	w.notifications.Synced(mxAccount, time.Now())
	const room, spammer = "whatsapp:15550000001/120363@g.us", "whatsapp:15550000003@s.whatsapp.net"
	msg := domain.Message{ID: "whatsapp:15550000001/A1", RoomID: room, Sender: spammer, Body: "buy bitcoin now", Timestamp: time.Now()}
	if serr := backend.cache.SaveMessages(ctx, room, []domain.Message{msg}); serr != nil {
		t.Fatal(serr)
	}
	w.notifications.Deliver(ctx, msg)
	marks, err := backend.PersonVerdicts(ctx)
	if err != nil || len(marks) != 1 || marks[0].Person != spammer || marks[0].Rule != domain.MoveSenderFirst {
		t.Fatalf("marks = %+v (%v), want the sender marked by their first message", marks, err)
	}
	if hidden, err := backend.cache.Hides(ctx, msg); err != nil || !hidden {
		t.Errorf("the marked sender is not hidden (%v)", err)
	}
}

// Hiding someone moves their direct message you never wrote in into the muted tag,
// through the daemon's own hook.
func TestHidingSomeoneMovesTheirDirectMessage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := config.Config{Profiles: everyRoom, WhatsApp: config.WhatsApp{Accounts: []config.WhatsAppAccount{{Name: "home", Phone: "+1 555 000 0001"}}}}
	backend := servedFor(t, cfg, domain.Session{})
	w, werr := newWorkers(slog.New(slog.DiscardHandler), cfg, backend)
	if werr != nil {
		t.Fatal(werr)
	}
	const spammer = "whatsapp:15550000003@s.whatsapp.net"
	const room domain.RoomID = "whatsapp:15550000001/15550000003@s.whatsapp.net"
	if err := backend.cache.SaveRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, "15550000001"), []domain.Room{{ID: room, Name: "Stranger", IsDirect: true}}); err != nil {
		t.Fatal(err)
	}
	msg := domain.Message{ID: "whatsapp:15550000001/A1", RoomID: room, Sender: spammer, Body: "hello dear", Timestamp: time.Now()}
	if err := backend.cache.SaveMessages(ctx, room, []domain.Message{msg}); err != nil {
		t.Fatal(err)
	}
	muted := false
	cfg.Tags = []config.Tag{{Name: "Spam", Notifies: &muted, People: []string{spammer}}}
	// As the reloader does: the notifications learn the tags, then the cache who is hidden.
	if err := w.notifications.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	if err := backend.useHidden(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	verdicts, err := backend.TagVerdicts(ctx)
	if err != nil || len(verdicts) != 1 || verdicts[0].Room != room || verdicts[0].Rule != domain.MoveWithPerson {
		t.Errorf("verdicts = %+v (%v), want the direct message moved with its person", verdicts, err)
	}
}

// Every network is built whatever the config holds, so a first account needs no
// restart; an empty config waits for nothing.
func TestEveryNetworkIsBuiltWithoutAnAccount(t *testing.T) {
	t.Parallel()
	backend := servedFor(t, config.Config{}, domain.Session{})
	var got []domain.Protocol
	for _, n := range backend.networks {
		got = append(got, n.Network())
	}
	want := []domain.Protocol{
		domain.ProtocolMatrix, domain.ProtocolWhatsApp, domain.ProtocolSlack, domain.ProtocolTelegram, domain.ProtocolMessenger,
		domain.ProtocolLinkedIn, domain.ProtocolGMessages, domain.ProtocolInstagram, domain.ProtocolSignal,
	}
	if !slices.Equal(got, want) {
		t.Errorf("networks = %v, want %v", got, want)
	}
	var logins []string
	for _, l := range backend.loginLeaders() {
		logins = append(logins, l.LoginNetwork().Network)
	}
	if want := []string{"matrix", "whatsapp", "slack", "telegram", "messenger", "linkedin", "gmessages", "instagram", "signal"}; !slices.Equal(logins, want) {
		t.Errorf("logins = %v, want %v", logins, want)
	}
	if got := expected(context.Background(), slog.New(slog.DiscardHandler), backend); len(got) != 0 {
		t.Errorf("expected = %v with no account, want nothing to wait for", got)
	}
}

// Whatever keeps networks from connecting at start (the keyring refusing every key, a
// network's store that will not open), every network is still built and the cache
// answers as it does when nothing is wrong: each network's cached chats stay listed and
// readable. Not parallel: the keyring fault is the whole process's.
func TestNetworksThatCannotConnectStillServeTheCache(t *testing.T) {
	block := func(path func(domain.Storage) string) func(domain.Storage) {
		return func(s domain.Storage) {
			// A directory where the store's file should be: it cannot open.
			if err := os.MkdirAll(path(s), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	stores := map[string]func(domain.Storage) string{
		"WhatsApp": domain.Storage.WhatsAppPath, "Messenger": domain.Storage.MessengerPath, "Telegram": domain.Storage.TelegramPath,
	}
	faults := map[string]struct {
		keyring error
		prepare func(domain.Storage)
	}{
		"the keyring refuses every key": {keyring: errors.New("the keyring is locked")},
		"every store will not open": {prepare: func(s domain.Storage) {
			for _, path := range stores {
				block(path)(s)
			}
		}},
	}
	for name, path := range stores {
		faults[name+"'s store will not open"] = struct {
			keyring error
			prepare func(domain.Storage)
		}{prepare: block(path)}
	}

	ctx := context.Background()
	acct := domain.AccountKey("@me:example.org") // shaped as every network's account part may be
	// An account on each network with a store kithd opens or keys, so its sessions are read.
	cfg := config.Config{
		WhatsApp: config.WhatsApp{Accounts: []config.WhatsAppAccount{{Name: "home", Phone: "+44 7700 900001"}}},
		Signal:   config.Signal{Accounts: []config.SignalAccount{{Name: "phone"}}},
	}
	// serve builds the daemon, gives each network a cached chat with a message, and says
	// which networks it built and which chats it lists.
	serve := func(t *testing.T, prepare func(domain.Storage)) (networks []domain.Protocol, listed []domain.RoomID, backend served) {
		t.Helper()
		backend = servedWith(t, cfg, domain.Session{}, prepare)
		for _, n := range backend.networks {
			networks = append(networks, n.Network())
			owner := domain.AccountRooms(n.Network(), acct)
			room := domain.RoomID(domain.NativeID(n.Network(), acct, "!chat:x"))
			if err := backend.cache.SaveRooms(ctx, owner, []domain.Room{{ID: room, Name: "chat"}}); err != nil {
				t.Fatal(err)
			}
			msg := domain.Message{ID: domain.EventID(domain.NativeID(n.Network(), acct, "$1")), RoomID: room, Body: "kept", Timestamp: time.Unix(1, 0)}
			if err := backend.cache.SaveMessages(ctx, room, []domain.Message{msg}); err != nil {
				t.Fatal(err)
			}
		}
		rooms, err := backend.Rooms(ctx)
		if err != nil {
			t.Fatalf("Rooms = %v", err)
		}
		for _, r := range rooms {
			listed = append(listed, r.ID)
		}
		slices.Sort(listed)
		if _, err := backend.CachedUnread(ctx); err != nil {
			t.Errorf("CachedUnread = %v", err)
		}
		if got := expected(ctx, slog.New(slog.DiscardHandler), backend); len(got) != 0 {
			t.Errorf("expected = %v with nothing linked, want nothing to wait for", got)
		}
		return networks, listed, backend
	}

	wantNetworks, wantListed, _ := serve(t, nil)
	for _, network := range []domain.Protocol{domain.ProtocolWhatsApp, domain.ProtocolSignal} {
		if !slices.Contains(wantListed, domain.RoomID(domain.NativeID(network, acct, "!chat:x"))) {
			t.Fatalf("with nothing wrong, %s's cached chat is not listed: %v", network, wantListed)
		}
	}
	for name, fault := range faults {
		t.Run(name, func(t *testing.T) {
			if fault.keyring != nil {
				keyring.MockInitWithError(fault.keyring)
				t.Cleanup(keyring.MockInit)
			}
			networks, listed, backend := serve(t, fault.prepare)
			if !slices.Equal(networks, wantNetworks) {
				t.Errorf("networks = %v, want %v", networks, wantNetworks)
			}
			if !slices.Equal(listed, wantListed) {
				t.Errorf("listed = %v, want %v", listed, wantListed)
			}
			for _, room := range listed {
				msgs, err := backend.CachedTimeline(ctx, room)
				if err != nil || len(msgs) != 1 || msgs[0].Body != "kept" {
					t.Errorf("CachedTimeline(%s) = (%v, %v), want the cached message", room, msgs, err)
				}
			}
		})
	}
}

// Matrix is served logged out until a session takes; it is waited for only with a
// saved session.
func TestMatrixIsWaitedForOnlyWithASavedSession(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Matrix: config.Matrix{Accounts: []config.MatrixAccount{{Name: "me", Homeserver: "https://matrix.invalid", User: "@me:x"}}}, AllowTokenFile: true}
	ctx := context.Background()

	none := servedFor(t, cfg, domain.Session{})
	if none.loginLeaders()[0] != api.LoginLeader(none.matrix) || none.matrix.LoggedIn() {
		t.Fatal("want a Matrix adapter, logged out, that can be logged in")
	}
	if got := expected(ctx, slog.New(slog.DiscardHandler), none); len(got) != 0 {
		t.Errorf("expected = %v without a saved session, want nothing", got)
	}

	acc := cfg.Matrix.Accounts[0]
	saved := servedFor(t, cfg, domain.Session{Homeserver: acc.Homeserver, UserID: acc.User, DeviceID: "D", AccessToken: "t"})
	got := expected(ctx, slog.New(slog.DiscardHandler), saved)
	if len(got) != 1 || got[0].Network != string(domain.ProtocolMatrix) || got[0].Account != "me" {
		t.Errorf("expected = %v with a saved session, want the Matrix account", got)
	}
}

// A fresh instance's directories are made, private, before any store opens in them.
func TestStorageDirsAreMade(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	storage := domain.Storage{
		DataDir: filepath.Join(root, "data", "kith"), StateDir: filepath.Join(root, "state", "kith"),
		CacheDir: filepath.Join(root, "cache", "kith"),
	}
	if err := makeStorageDirs(storage); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{storage.DataDir, storage.StateDir, storage.CacheDir} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v, %v; want a 0700 directory", dir, info, err)
		}
	}
	if _, err := db.Open(context.Background(), storage.CachePath()); err != nil {
		t.Errorf("the cache does not open in a made directory: %v", err)
	}
}

// A cache that will not open stops the daemon, naming the file.
func TestACacheThatWillNotOpenIsAnError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A directory where the file should be: SQLite cannot open it.
	path := filepath.Join(dir, "cache.db")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	cache, err := openCache(context.Background(), slog.New(slog.DiscardHandler), path)
	if err == nil || cache != nil || !strings.Contains(err.Error(), path) {
		t.Errorf("openCache(a directory) = (%v, %v), want an error naming it", cache, err)
	}
}

// Each network judges its own section: an account with no name is refused naming its
// table, at start and on re-read, and the re-read then changes nothing.
func TestEveryNetworkChecksItsOwnSection(t *testing.T) {
	t.Parallel()
	backend := servedFor(t, config.Config{}, domain.Session{})
	ctx := context.Background()
	if err := backend.checkConfig(ctx, config.Config{Profiles: everyRoom}); err != nil {
		t.Fatalf("an empty config: %v", err)
	}
	for _, n := range (config.Config{}).Networks() {
		cfg := config.Config{Profiles: everyRoom}
		if err := cfg.Write(n.Table(), map[string]string{"name": ""}); err != nil {
			t.Fatal(err)
		}
		err := backend.checkConfig(ctx, cfg)
		if err == nil || !strings.Contains(err.Error(), n.Table()) {
			t.Errorf("%s: checkConfig = %v, want it refused naming %s", n.Network(), err, n.Table())
		}

		path := filepath.Join(t.TempDir(), "config.toml")
		if err := config.Save(path, cfg); err != nil {
			t.Fatal(err)
		}
		relevel := func(string) { t.Errorf("%s: a refused re-read changed the log level", n.Network()) }
		if err := reloader(configFile{path: path}, relevel, nil, backend, nil)(ctx); err == nil {
			t.Errorf("%s: the re-read took a config its network refuses", n.Network())
		}
	}
}

// Every network without a store of its own keeps its filters' moves and marks in the
// cache, through the router: kept (so they outlive a restart, which seeds from the
// cache), renamed with their tag, and a release still outranks a later move.
func TestEveryNetworkKeepsItsMovesInTheCache(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := servedFor(t, config.Config{}, domain.Session{})
	for _, network := range []string{"gmessages", "signal", "instagram", "messenger", "telegram"} {
		room, person := domain.RoomID(network+":1/2"), network+":9"
		if err := backend.MarkTag(ctx, domain.TagVerdict{Room: room, Tag: "Spam", Rule: domain.MoveAlways, Filter: "politics"}); err != nil {
			t.Errorf("%s: MarkTag = %v", network, err)
		}
		if err := backend.MarkPerson(ctx, domain.PersonVerdict{Person: person, Tag: "Spam", Rule: domain.MoveAlways, Filter: "politics"}); err != nil {
			t.Errorf("%s: MarkPerson = %v", network, err)
		}
	}
	if err := backend.RetagVerdicts(ctx, "Spam", "Junk"); err != nil {
		t.Fatalf("RetagVerdicts = %v", err)
	}
	rooms, err := backend.TagVerdicts(ctx)
	if err != nil || len(rooms) != 5 {
		t.Fatalf("room verdicts = %+v (%v), want one per network", rooms, err)
	}
	people, err := backend.PersonVerdicts(ctx)
	if err != nil || len(people) != 5 {
		t.Fatalf("person verdicts = %+v (%v), want one per network", people, err)
	}
	for _, v := range rooms {
		if !domain.SameTag(v.Tag, "Junk") || v.Filter != "politics" {
			t.Errorf("%s kept %+v, want the move under Junk", v.Room, v)
		}
	}
	for _, v := range people {
		if !domain.SameTag(v.Tag, "Junk") {
			t.Errorf("%s kept %+v, want the mark under Junk", v.Person, v)
		}
	}
	const room domain.RoomID = "gmessages:1/2"
	if err := backend.MarkTag(ctx, domain.TagVerdict{Room: room, Tag: "Junk", Released: true}); err != nil {
		t.Fatalf("release = %v", err)
	}
	if err := backend.MarkTag(ctx, domain.TagVerdict{Room: room, Tag: "Junk", Rule: domain.MoveAlways}); !errors.Is(err, domain.ErrReleased) {
		t.Errorf("a move after the release = %v, want ErrReleased", err)
	}
}

// A filter catching an SMS moves its room, and the move is in the cache: Google
// Messages had nowhere to keep one, so it held until the daemon restarted.
func TestAFilterMovesAnSMSRoomThroughTheDaemon(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := config.Config{
		Profiles:  everyRoom,
		GMessages: config.GMessages{Accounts: []config.GMessagesAccount{{Name: "SMS"}}},
		Tags:      []config.Tag{{Name: "Spam"}},
		Filters:   []config.Filter{{Name: "politics", Tag: "Spam", Words: []string{"*election*"}, Moves: []string{config.FilterMoveAlways}}},
	}
	backend := servedFor(t, cfg, domain.Session{})
	w, err := newWorkers(slog.New(slog.DiscardHandler), cfg, backend)
	if err != nil {
		t.Fatal(err)
	}
	w.notifications.Synced(mxAccount, time.Now())
	w.notifications.Synced(mxAccount, time.Now())
	const room domain.RoomID = "gmessages:1/7"
	msg := domain.Message{ID: "gmessages:1/70", RoomID: room, Sender: "gmessages:+15550000003", Body: "vote in the elections", Timestamp: time.Now()}
	if serr := backend.cache.SaveMessages(ctx, room, []domain.Message{msg}); serr != nil {
		t.Fatal(serr)
	}
	w.notifications.Deliver(ctx, msg)
	moves, err := backend.cache.TagVerdicts(ctx)
	if err != nil || len(moves) != 1 || moves[0].Room != room || moves[0].Filter != "politics" {
		t.Errorf("kept moves = %+v (%v), want the SMS room's", moves, err)
	}
}

// everyRoom is the profile the starter config writes.
var everyRoom = []config.Profile{{Name: "default", Include: []string{"*"}}}
