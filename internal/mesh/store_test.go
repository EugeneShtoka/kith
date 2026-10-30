package mesh_test

import (
	"crypto/ecdsa"
	"crypto/rand"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// storeKey is a store's key, made afresh: never a literal.
func storeKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, sealeddb.KeyLen)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func newKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := devicekey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id, err := devicekey.IDOf(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	return key, id.String()
}

func open(t *testing.T, path string, key []byte) *mesh.Store {
	t.Helper()
	s, err := mesh.Open(t.Context(), path, key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// roster is a store's members' IDs and names, and the IDs removed.
func roster(t *testing.T, s *mesh.Store) (members, removed []string) {
	t.Helper()
	ms, removed, err := s.Roster()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		members = append(members, m.ID+" "+m.Name+" "+string(m.Role))
	}
	return members, removed
}

// opsIn counts the ops the store at path holds.
func opsIn(t *testing.T, path string, key []byte) int {
	t.Helper()
	db, err := sealeddb.Open(t.Context(), path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM ops").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A store reopened holds what it held, and its next op follows its last.
func TestAStoreReopenedGoesOnWhereItStopped(t *testing.T) {
	t.Parallel()
	path, key := filepath.Join(t.TempDir(), "mesh.db"), storeKey(t)
	bridge, self := newKey(t)
	_, laptop := newKey(t)
	_, phone := newKey(t)
	now := time.Now().UTC().Truncate(time.Second)
	s := open(t, path, key)
	ctx := t.Context()
	if err := s.ChangeRoster(ctx, bridge, []domain.Device{{ID: self, Role: domain.RoleBridge, AddedAt: now, AddedBy: self}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeRoster(ctx, bridge, []domain.Device{{ID: laptop, Name: "laptop", Role: domain.RoleDevice, AddedAt: now}}, nil); err != nil {
		t.Fatal(err)
	}
	wantMembers, wantRemoved := roster(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open(t, path, key)
	t.Cleanup(func() { _ = s.Close() })
	if members, removed := roster(t, s); !slices.Equal(members, wantMembers) || !slices.Equal(removed, wantRemoved) {
		t.Fatalf("reopened: %v %v, want %v %v", members, removed, wantMembers, wantRemoved)
	}
	if err := s.ChangeRoster(ctx, bridge, []domain.Device{{ID: phone, Name: "phone", Role: domain.RoleDevice, AddedAt: now}}, []string{laptop}); err != nil {
		t.Fatalf("the next op after a reopen: %v", err)
	}
	if members, removed := roster(t, s); len(members) != 2 || !slices.Equal(removed, []string{laptop}) {
		t.Fatalf("after the next op: %v, removed %v", members, removed)
	}
	if err := s.ChangeRoster(ctx, bridge, nil, []string{laptop}); err == nil {
		t.Error("removing a device removed already was taken")
	}
}

// A store opens only as this kith wrote it: another key's, or a newer kith's (whose
// layout this one does not know), is refused and left as it was.
func TestAStoreThatIsNotThisKithsDoesNotOpen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	key := storeKey(t)
	bridge, self := newKey(t)
	path := filepath.Join(dir, "mesh.db")
	s := open(t, path, key)
	if err := s.ChangeRoster(t.Context(), bridge, []domain.Device{{ID: self, Role: domain.RoleBridge}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := mesh.Open(t.Context(), path, storeKey(t)); !errors.Is(err, sealeddb.ErrWrongKey) {
		t.Errorf("another key: %v, want %v", err, sealeddb.ErrWrongKey)
	}
	db, err := sealeddb.Open(t.Context(), path, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "PRAGMA user_version = 2"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := mesh.Open(t.Context(), path, key); !errors.Is(err, mesh.ErrNewer) {
		t.Errorf("a newer kith's: %v, want %v", err, mesh.ErrNewer)
	}
	if n := opsIn(t, path, key); n != 1 {
		t.Errorf("the newer store holds %d ops, want its 1 untouched", n)
	}
}

// A key the roster does not list as a bridge signs nothing into the store, and the
// refusal leaves the store as it was, on disk too.
func TestAKeyNotInTheRosterWritesNothing(t *testing.T) {
	t.Parallel()
	path, key := filepath.Join(t.TempDir(), "mesh.db"), storeKey(t)
	bridge, self := newKey(t)
	stranger, strangerID := newKey(t)
	s := open(t, path, key)
	ctx := t.Context()
	if err := s.ChangeRoster(ctx, bridge, []domain.Device{{ID: self, Role: domain.RoleBridge}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeRoster(ctx, bridge, []domain.Device{{ID: strangerID, Name: "laptop", Role: domain.RoleDevice}}, nil); err != nil {
		t.Fatal(err)
	}
	want, _ := roster(t, s)
	_, other := newKey(t)
	for name, err := range map[string]error{
		"a device":          s.ChangeRoster(ctx, stranger, []domain.Device{{ID: other, Role: domain.RoleDevice}}, nil),
		"as its own bridge": s.ChangeRoster(ctx, stranger, []domain.Device{{ID: strangerID, Role: domain.RoleBridge}}, nil),
	} {
		if !errors.Is(err, crdt.ErrNotAMember) {
			t.Errorf("%s signing: %v, want %v", name, err, crdt.ErrNotAMember)
		}
	}
	if got, _ := roster(t, s); !slices.Equal(got, want) {
		t.Errorf("after refusals: %v, want %v", got, want)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if n := opsIn(t, path, key); n != 2 {
		t.Errorf("the store holds %d ops, want 2", n)
	}
}
