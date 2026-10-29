package keystore

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// A call that does not answer in time is said and left running; tries meanwhile wait
// on it instead of asking again, and its late answer is taken, a value but not a
// failure.
func TestAKeyringCallThatDoesNotAnswerIsAskedOnce(t *testing.T) {
	t.Parallel()
	const wait = 20 * time.Millisecond
	var asked atomic.Int32
	answer := make(chan error)
	op := func(value string) func() (string, error) {
		return func() (string, error) {
			asked.Add(1)
			return value, <-answer
		}
	}
	for range 3 {
		if _, err := bounded("get\x00test-late", wait, op("the key")); !errors.Is(err, ErrKeyringNotAnswering) {
			t.Fatalf("a call not answering = %v; want ErrKeyringNotAnswering", err)
		}
	}
	if n := asked.Load(); n != 1 {
		t.Fatalf("the keyring was asked %d times while one call hung; want once", n)
	}
	answer <- nil // it answers, late
	got, err := bounded("get\x00test-late", wait, op("another"))
	if err != nil || got != "the key" || asked.Load() != 1 {
		t.Fatalf("after a late answer = %q, %v, asked %d; want the late value, not asked again", got, err, asked.Load())
	}

	// A late failure is not taken: the keyring is asked again.
	go func() { answer <- errors.New("locked") }()
	if _, err := bounded("get\x00test-failed", time.Second, op("x")); err == nil {
		t.Fatal("a failure answered in time was not said")
	}
	if _, err := bounded("get\x00test-failed2", wait, op("x")); !errors.Is(err, ErrKeyringNotAnswering) {
		t.Fatalf("= %v", err)
	}
	answer <- errors.New("locked") // the late answer, a failure
	calls.Lock()
	late := calls.running["get\x00test-failed2"]
	calls.Unlock()
	<-late.done
	go func() { answer <- nil }()
	if got, err := bounded("get\x00test-failed2", time.Second, op("asked again")); err != nil || got != "asked again" {
		t.Fatalf("after a late failure = %q, %v; want the keyring asked again", got, err)
	}
}
