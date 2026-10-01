package route

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// waitClosed fails unless ch closes within a second, draining what is left first.
func waitClosed[T any](t *testing.T, what string, ch <-chan T) []T {
	t.Helper()
	var got []T
	deadline := time.After(time.Second)
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, v)
		case <-deadline:
			t.Fatalf("%s never closed", what)
		}
	}
}

// The merged stream carries every network's events and closes only once every
// network's has.
func TestTheMergedStreamCarriesEveryNetwork(t *testing.T) {
	t.Parallel()
	r, m, wa := twoNetworks(t)
	merged := r.Messages()
	if r.Messages() != merged {
		t.Fatal("each call built another merged stream")
	}
	m.messages <- domain.Message{ID: "$m1"}
	wa.messages <- domain.Message{ID: "wa1"}
	m.messages <- domain.Message{ID: "$m2"}
	close(m.messages)

	seen := map[domain.EventID]bool{}
	for range 3 {
		select {
		case msg := <-merged:
			seen[msg.ID] = true
		case <-time.After(time.Second):
			t.Fatalf("only %v arrived", seen)
		}
	}
	select {
	case _, ok := <-merged:
		if !ok {
			t.Fatal("the merged stream closed while WhatsApp's was still open")
		}
	case <-time.After(50 * time.Millisecond):
	}
	close(wa.messages)
	waitClosed(t, "the merged stream after every network closed", merged)
	if len(seen) != 3 {
		t.Errorf("arrived: %v, want all three", seen)
	}
}

// Stopping ends the merge even when nobody reads it any more and the networks never
// close theirs: a pump blocked on a full stream must not outlive the daemon.
func TestStoppingEndsAMergeNobodyReads(t *testing.T) {
	t.Parallel()
	r, m, wa := twoNetworks(t)
	merged := r.Reactions()
	// Fill the merged buffer and the networks' own, so the pumps are blocked sending.
	for range streamBuffer + 8 {
		select {
		case m.reactions <- domain.ReactionUpdate{}:
		default:
		}
		select {
		case wa.reactions <- domain.ReactionUpdate{}:
		default:
		}
	}
	time.Sleep(20 * time.Millisecond)
	r.Stop()
	waitClosed(t, "a merged stream nobody reads, after Stop", merged)
	if !m.stopped || !wa.stopped {
		t.Errorf("stopped: matrix %v, whatsapp %v; want both", m.stopped, wa.stopped)
	}
	r.Stop() // twice is harmless
}

// Start runs every network at once: one that blocks does not hold the other back,
// and each one's ending is reported.
func TestStartRunsEveryNetworkTogether(t *testing.T) {
	t.Parallel()
	r, m, wa := twoNetworks(t)
	wa.startErr = errFake
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()
	for name, started := range map[string]chan struct{}{"matrix": m.started, "whatsapp": wa.started} {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("%s never started while the other ran", name)
		}
	}
	select {
	case err := <-done:
		t.Fatalf("Start returned before ctx ended: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, errFake) {
			t.Errorf("Start = %v, want WhatsApp's ending among it", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not return after ctx ended")
	}
}
