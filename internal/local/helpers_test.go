package local

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// testCache is an empty cache in a temporary directory.
func testCache(t *testing.T) *db.Cache {
	t.Helper()
	cache, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	return cache
}

// fakeNetwork is one person (me: their IDs, on any network) with nothing encrypted,
// counting rewinds.
type fakeNetwork struct {
	me      []string
	rewinds int
}

func (n *fakeNetwork) Me() []string { return n.me }
func (n *fakeNetwork) RoomEncryption(_ context.Context, rooms []domain.RoomID) (map[domain.RoomID]bool, error) {
	out := make(map[domain.RoomID]bool, len(rooms))
	for _, room := range rooms {
		out[room] = false
	}
	return out, nil
}
func (n *fakeNetwork) RewindSync(context.Context) error { n.rewinds++; return nil }

// backendWithCache is a service over a fresh cache for the account me ("" for none).
func backendWithCache(t *testing.T, me string) *Service {
	t.Helper()
	if me == "" {
		return backendAs(t)
	}
	return backendAs(t, me)
}

// backendAs is a service over a fresh cache for a person with these IDs.
func backendAs(t *testing.T, me ...string) *Service {
	t.Helper()
	return New(testCache(t), &fakeNetwork{me: me})
}
