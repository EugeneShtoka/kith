package daemon

import (
	"sync"
	"testing"
)

// Once Serve is ending, no handler starts: one arriving after the drain counted none
// would run while the caller closes the stores.
func TestAShutGateAdmitsNoHandler(t *testing.T) {
	t.Parallel()
	var g handlerGate
	if !g.enter() {
		t.Fatal("an open gate refused a handler")
	}
	g.shut()
	if g.enter() {
		t.Error("a shut gate admitted a handler")
	}
	if n := g.running(); n != 1 {
		t.Errorf("running = %d, want the one admitted before", n)
	}
	g.leave()
	if n := g.running(); n != 0 {
		t.Errorf("running = %d after it left, want 0", n)
	}
}

// Handlers entering while the gate shuts: every one is either refused or counted, so
// a drain that sees none running has seen them all.
func TestTheGateCountsEveryHandlerItAdmits(t *testing.T) {
	t.Parallel()
	for range 100 {
		var g handlerGate
		var wg sync.WaitGroup
		var mu sync.Mutex
		in := 0
		for range 16 {
			wg.Go(func() {
				if g.enter() {
					mu.Lock()
					in++
					mu.Unlock()
				}
			})
		}
		wg.Go(g.shut)
		wg.Wait()
		mu.Lock()
		if g.running() != in {
			t.Fatalf("running = %d, admitted %d", g.running(), in)
		}
		mu.Unlock()
	}
}
