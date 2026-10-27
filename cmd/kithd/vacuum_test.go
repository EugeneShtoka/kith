package main

import (
	"context"
	"errors"
	"testing"
)

// A vacuum that panics (here, on a cache that is not there; on the rehearsal, SQLite
// out of its memory) is an error the loop logs and puts off, not the daemon's end.
func TestAVacuumThatPanicsIsAnError(t *testing.T) {
	t.Parallel()
	_, err := vacuumOnce(context.Background(), nil)
	if !errors.Is(err, errVacuumPanicked) {
		t.Fatalf("vacuumOnce(a panicking vacuum) = %v; want errVacuumPanicked", err)
	}
}
