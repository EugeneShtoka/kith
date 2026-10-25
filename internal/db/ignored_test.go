package db

import (
	"context"
	"path/filepath"
	"testing"
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

	if err := c.SetMatrixIgnored(ctx, mxRooms, []string{"matrix:@ann:x", "matrix:@bob:x"}); err != nil {
		t.Fatal(err)
	}
	got, err := c.MatrixIgnored(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "matrix:@ann:x" || got[1] != "matrix:@bob:x" {
		t.Errorf("after first write = %v, want [@ann:x @bob:x]", got)
	}

	// A second write hands the whole set; the row for the dropped MXID goes.
	if werr := c.SetMatrixIgnored(ctx, mxRooms, []string{"matrix:@ann:x", "matrix:@carl:x"}); werr != nil {
		t.Fatal(werr)
	}
	got, err = c.MatrixIgnored(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "matrix:@ann:x" || got[1] != "matrix:@carl:x" {
		t.Errorf("after replacement = %v, want [@ann:x @carl:x]", got)
	}

	// An empty set clears the mirror.
	if werr := c.SetMatrixIgnored(ctx, mxRooms, nil); werr != nil {
		t.Fatal(werr)
	}
	got, err = c.MatrixIgnored(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("after clear = %v, want empty", got)
	}
}
