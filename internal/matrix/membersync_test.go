package matrix

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// memberEvent builds a parsed m.room.member as the syncer delivers it.
func memberEvent(roomID, userID, name string, membership event.Membership, source event.Source) *event.Event {
	key := userID
	evt := &event.Event{
		Type:     event.StateMember,
		RoomID:   id.RoomID(roomID),
		StateKey: &key,
	}
	evt.Content.Parsed = &event.MemberEventContent{Membership: membership, Displayname: name}
	evt.Mautrix.EventSource = source | event.SourceState
	return evt
}

// cachedNames reads a room's cached membership as a user-ID→name map.
func cachedNames(t *testing.T, b *InProc, roomID domain.RoomID) map[string]string {
	t.Helper()
	members, err := b.cache.Members(context.Background(), roomID, 0)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	names := make(map[string]string, len(members))
	for _, m := range members {
		names[m.UserID] = m.DisplayName
	}
	return names
}

func TestOnMemberCachesJoinsAndDropsLeaves(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := backendWithCache(t, "@me:x")

	b.onMember(ctx, memberEvent("!a:x", "@alice:x", "Alice", event.MembershipJoin, event.SourceJoin))
	b.onMember(ctx, memberEvent("!a:x", "@bob:x", "Bob", event.MembershipJoin, event.SourceJoin))
	if got := cachedNames(t, b, "!a:x"); len(got) != 2 || got["@alice:x"] != "Alice" {
		t.Fatalf("after two joins: %v", got)
	}

	// A rename arrives as another join and must not duplicate the row.
	b.onMember(ctx, memberEvent("!a:x", "@bob:x", "Robert", event.MembershipJoin, event.SourceJoin))
	if got := cachedNames(t, b, "!a:x"); len(got) != 2 || got["@bob:x"] != "Robert" {
		t.Fatalf("after a rename: %v", got)
	}

	// Leave, kick and ban all remove.
	for _, m := range []event.Membership{event.MembershipLeave, event.MembershipBan} {
		b.onMember(ctx, memberEvent("!a:x", "@alice:x", "Alice", event.MembershipJoin, event.SourceJoin))
		b.onMember(ctx, memberEvent("!a:x", "@alice:x", "", m, event.SourceJoin))
		if _, still := cachedNames(t, b, "!a:x")["@alice:x"]; still {
			t.Errorf("@alice:x survived %s", m)
		}
	}
}

// Invitees are not members, and rooms we are not in must not be registered.
func TestOnMemberIgnoresRoomsWeAreNotIn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := backendWithCache(t, "@me:x")

	b.onMember(ctx, memberEvent("!invited:x", "@alice:x", "Alice", event.MembershipJoin, event.SourceInvite))
	b.onMember(ctx, memberEvent("!left:x", "@bob:x", "Bob", event.MembershipJoin, event.SourceLeave))

	if got := cachedNames(t, b, "!invited:x"); len(got) != 0 {
		t.Errorf("cached membership of an invite-section room: %v", got)
	}
	if got := cachedNames(t, b, "!left:x"); len(got) != 0 {
		t.Errorf("cached membership of a leave-section room: %v", got)
	}
	rooms, err := b.cache.Rooms(ctx)
	if err != nil {
		t.Fatalf("Rooms: %v", err)
	}
	for _, r := range rooms {
		if r.ID == "!invited:x" || r.ID == "!left:x" {
			t.Errorf("a room we are not in was registered: %s", r.ID)
		}
	}
}

// Unparsed content or a missing state key must neither reach the cache nor panic.
func TestOnMemberIgnoresMalformedEvents(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := backendWithCache(t, "@me:x")

	noKey := memberEvent("!a:x", "@alice:x", "Alice", event.MembershipJoin, event.SourceJoin)
	noKey.StateKey = nil
	b.onMember(ctx, noKey)

	unparsed := memberEvent("!a:x", "@bob:x", "Bob", event.MembershipJoin, event.SourceJoin)
	unparsed.Content.Parsed = nil
	b.onMember(ctx, unparsed)

	if got := cachedNames(t, b, "!a:x"); len(got) != 0 {
		t.Errorf("a malformed member event reached the cache: %v", got)
	}
}

// A room's name map means "fully resolved", so a single member event must not seed one.
func TestSetMemberNameOnlyTouchesAnAlreadyFetchedRoom(t *testing.T) {
	t.Parallel()

	b := New(nil)

	b.names.rename("!cold:x", "@alice:x", "Alice")
	_, seeded := b.names.get("!cold:x")
	if seeded {
		t.Error("a member event seeded the memo for a room that was never fetched")
	}

	// A room already fetched must stay in step.
	b.names.putAt("!warm:x", 0, map[string]string{"@alice:x": "Alice", "@bob:x": "Bob"})

	b.names.rename("!warm:x", "@bob:x", "Robert")
	b.names.rename("!warm:x", "@alice:x", "") // a leave, or a member with no name

	names, _ := b.names.get("!warm:x")
	if names["@bob:x"] != "Robert" {
		t.Errorf("rename not applied: %v", names)
	}
	if _, still := names["@alice:x"]; still {
		t.Errorf("departed member kept in the memo: %v", names)
	}
}

// Regression: the syncer keys listeners by event.Type including class, and member
// events arrive in the state block; getting either wrong dropped every membership.
func TestSyncStateBlockReachesOnMember(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := backendWithCache(t, "@me:x")

	syncer := mautrix.NewDefaultSyncer()
	syncer.OnEventType(event.StateMember, b.onMember)

	joinKey, leaveKey := "@alice:x", "@bob:x"
	resp := &mautrix.RespSync{}
	resp.Rooms.Join = map[id.RoomID]*mautrix.SyncJoinedRoom{
		"!a:x": {
			State: mautrix.SyncEventsList{Events: []*event.Event{
				rawMember("@alice:x", &joinKey, `{"membership":"join","displayname":"Alice"}`),
				rawMember("@bob:x", &leaveKey, `{"membership":"leave"}`),
			}},
		},
	}
	if err := syncer.ProcessResponse(ctx, resp, ""); err != nil {
		t.Fatalf("ProcessResponse: %v", err)
	}

	got := cachedNames(t, b, "!a:x")
	if got["@alice:x"] != "Alice" {
		t.Errorf("a join in the state block never reached the cache: %v", got)
	}
	if _, gone := got["@bob:x"]; gone {
		t.Errorf("a leave in the state block was cached: %v", got)
	}
}

// rawMember builds an unparsed member event, so the syncer's own ParseRaw runs.
func rawMember(sender string, stateKey *string, content string) *event.Event {
	return &event.Event{
		Type:     event.StateMember,
		Sender:   id.UserID(sender),
		StateKey: stateKey,
		Content:  event.Content{VeryRaw: []byte(content)},
	}
}

// ClearCache must also rewind next_batch (kept in the crypto store), or the emptied
// cache is never refilled.
func TestClearCacheRewindsTheSyncPosition(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := backendWithCache(t, "@me:x")
	store := mautrix.NewMemorySyncStore()
	if err := store.SaveNextBatch(ctx, "@me:x", "s12345_678"); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	b.client.Store = store

	if err := b.ClearCache(ctx); err != nil {
		t.Fatalf("ClearCache: %v", err)
	}

	token, err := store.LoadNextBatch(ctx, "@me:x")
	if err != nil {
		t.Fatalf("LoadNextBatch: %v", err)
	}
	if token != "" {
		t.Errorf("sync token = %q after ClearCache, want empty — the next sync must be a full one", token)
	}
}

// ClearCache is reachable before Resume; rewinding must not panic without a client.
func TestResetSyncPositionWithoutAClient(t *testing.T) {
	t.Parallel()

	b := New(nil)
	if err := b.resetSyncPosition(context.Background()); err != nil { // must not panic
		t.Errorf("reset without a client: %v", err)
	}

	withClient := backendWithCache(t, "@me:x")
	withClient.client.Store = nil
	if err := withClient.resetSyncPosition(context.Background()); err != nil { // nor without a store
		t.Errorf("reset without a store: %v", err)
	}
}

// failingSyncStore refuses to save a sync position.
type failingSyncStore struct{ mautrix.SyncStore }

func (failingSyncStore) SaveNextBatch(context.Context, id.UserID, string) error {
	return errors.New("crypto store is read-only")
}

// A cache with no rooms is rewound on every start until something refills it, so a
// crash after a rebuild or a failed rewind cannot leave it empty for good. A cache
// holding rooms keeps its position.
func TestStartRewindsAnEmptiedCache(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	cases := []struct {
		name      string
		rooms     []domain.Room
		failing   bool
		wantToken string
		wantErr   bool
	}{
		{"empty cache", nil, false, "", false},
		{"cache holding a room", []domain.Room{{ID: "!a:x", Name: "Alpha"}}, false, "s12345_678", false},
		{"empty cache, rewind fails", nil, true, "s12345_678", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			b := backendWithCache(t, "@me:x")
			client := b.client
			b = New(reopenedCache(t, c.rooms)) // a first open counts as a rebuild
			b.client = client
			store := mautrix.NewMemorySyncStore()
			if err := store.SaveNextBatch(ctx, "@me:x", "s12345_678"); err != nil {
				t.Fatalf("seed token: %v", err)
			}
			b.client.Store = store
			if c.failing {
				b.client.Store = failingSyncStore{store}
			}

			err := b.resyncEmptiedCache(ctx)
			if (err != nil) != c.wantErr {
				t.Fatalf("resyncEmptiedCache = %v, want error: %v", err, c.wantErr)
			}
			if token, _ := store.LoadNextBatch(ctx, "@me:x"); token != c.wantToken {
				t.Errorf("sync token = %q, want %q", token, c.wantToken)
			}
		})
	}
}

// reopenedCache is a cache holding rooms, opened a second time the way a restart opens
// it, so it does not report Rebuilt.
func reopenedCache(t *testing.T, rooms []domain.Room) *db.Cache {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	first, err := db.Open(ctx, path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err = first.SaveRooms(ctx, rooms); err != nil {
		t.Fatalf("SaveRooms: %v", err)
	}
	if err = first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	again, err := db.Open(ctx, path)
	if err != nil {
		t.Fatalf("db.Open again: %v", err)
	}
	t.Cleanup(func() { _ = again.Close() })
	if again.Rebuilt() {
		t.Fatal("a second open reported a rebuild")
	}
	return again
}

// Whether a start rewinds is decided when the backend is built, not when Start runs:
// the daemon's startup room refresh, or a client's RPC, can write rooms into an
// emptied cache first, and the rewind was then skipped for good.
func TestAnEmptiedCacheRewindsEvenAfterRoomsArrive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		prepare   func(t *testing.T, b *InProc)
		wantToken string
	}{
		{"rooms refreshed in before Start", func(t *testing.T, b *InProc) {
			if err := b.cache.SaveRooms(ctx, []domain.Room{{ID: "!a:x", Name: "Alpha"}}); err != nil {
				t.Fatal(err)
			}
		}, ""},
		{"a rewind already done is not done again", func(t *testing.T, b *InProc) {
			if err := b.resyncEmptiedCache(ctx); err != nil {
				t.Fatal(err)
			}
			if err := b.client.Store.SaveNextBatch(ctx, "@me:x", "s2"); err != nil {
				t.Fatal(err)
			}
		}, "s2"},
		{"cleared before Start, on a store swapped in afterwards", func(t *testing.T, b *InProc) {
			if err := b.ClearCache(ctx); err != nil {
				t.Fatal(err)
			}
			// EnableEncryption's swap: the store Start syncs on still has the token.
			swapped := mautrix.NewMemorySyncStore()
			if err := swapped.SaveNextBatch(ctx, "@me:x", "s12345_678"); err != nil {
				t.Fatal(err)
			}
			b.client.Store = swapped
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := backendWithCache(t, "@me:x").client
			b := New(reopenedCache(t, nil)) // held no room when built
			b.client = client
			store := mautrix.NewMemorySyncStore()
			if err := store.SaveNextBatch(ctx, "@me:x", "s12345_678"); err != nil {
				t.Fatal(err)
			}
			b.client.Store = store
			tc.prepare(t, b)
			if err := b.resyncEmptiedCache(ctx); err != nil {
				t.Fatalf("resyncEmptiedCache: %v", err)
			}
			if token, _ := b.client.Store.LoadNextBatch(ctx, "@me:x"); token != tc.wantToken {
				t.Errorf("token = %q, want %q", token, tc.wantToken)
			}
		})
	}
}
