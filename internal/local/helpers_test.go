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

// fakeNetwork is one account with nothing encrypted, counting rewinds.
type fakeNetwork struct {
	account string
	rewinds int
}

func (n *fakeNetwork) Account() string { return n.account }
func (n *fakeNetwork) Me() []string    { return []string{n.account} }
func (n *fakeNetwork) RoomEncryption(_ context.Context, rooms []domain.RoomID) (map[domain.RoomID]bool, error) {
	out := make(map[domain.RoomID]bool, len(rooms))
	for _, room := range rooms {
		out[room] = false
	}
	return out, nil
}
func (n *fakeNetwork) RewindSync(context.Context) error { n.rewinds++; return nil }

// backendWithCache is a service over a fresh cache for the account me.
func backendWithCache(t *testing.T, me string) *Service {
	t.Helper()
	return New(testCache(t), &fakeNetwork{account: me})
}
