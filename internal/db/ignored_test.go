package db

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func freshCache(t *testing.T) *Cache {
	t.Helper()
	c, err := Open(context.Background(), filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestSetMatrixIgnoredReplacesTheSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := freshCache(t)

	if err := c.SetMatrixIgnored(ctx, domain.ProtocolMatrix, []string{"@ann:x", "@bob:x"}); err != nil {
		t.Fatal(err)
	}
	got, err := c.MatrixIgnored(ctx, domain.ProtocolMatrix)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "@ann:x" || got[1] != "@bob:x" {
		t.Errorf("after first write = %v, want [@ann:x @bob:x]", got)
	}

	// A second write hands the whole set; the row for the dropped MXID goes.
	if werr := c.SetMatrixIgnored(ctx, domain.ProtocolMatrix, []string{"@ann:x", "@carl:x"}); werr != nil {
		t.Fatal(werr)
	}
	got, err = c.MatrixIgnored(ctx, domain.ProtocolMatrix)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "@ann:x" || got[1] != "@carl:x" {
		t.Errorf("after replacement = %v, want [@ann:x @carl:x]", got)
	}

	// An empty set clears the mirror.
	if werr := c.SetMatrixIgnored(ctx, domain.ProtocolMatrix, nil); werr != nil {
		t.Fatal(werr)
	}
	got, err = c.MatrixIgnored(ctx, domain.ProtocolMatrix)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("after clear = %v, want empty", got)
	}
}

func TestMatrixIgnoredIsPerNetwork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := freshCache(t)
	if err := c.SetMatrixIgnored(ctx, domain.ProtocolMatrix, []string{"@ann:x"}); err != nil {
		t.Fatal(err)
	}
	// Another network reading its own ignored list sees nothing.
	got, err := c.MatrixIgnored(ctx, domain.ProtocolTelegram)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("Telegram ignored = %v, want empty — Matrix's set must not leak", got)
	}
}
