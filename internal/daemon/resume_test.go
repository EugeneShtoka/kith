package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// burst commits n messages, one a commit, their bodies prefix0…
func burst(cache *numberedCache, prefix string, n int) []string {
	var bodies []string
	for i := range n {
		body := fmt.Sprintf("%s%d", prefix, i)
		cache.commit(message(body))
		bodies = append(bodies, body)
	}
	return bodies
}

// readBodies reads want's length of messages and fails unless they are want, in order.
func readBodies(t *testing.T, client *daemon.Remote, want []string) {
	t.Helper()
	for i, body := range want {
		if got := nextMessage(t, client.Messages()); got.Body != body {
			t.Fatalf("message %d is %q, want %q", i, got.Body, body)
		}
	}
}

// restart stops run and serves cache on path again, as a systemd restart does.
func restart(t *testing.T, run *daemonRun, path string, cache *numberedCache) *daemonRun {
	t.Helper()
	run.stop(t)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove socket: %v", err)
	}
	return runDaemon(t, path, &fakeBackend{Nop: nopWithChannels()}, cache.serves)
}

// Across a daemon restart in the middle of a burst far longer than any queue, with the
// UI not reading, the client gets every message once and in order: those it held, those
// it was cut off from, those committed while the daemon was down, and what follows.
func TestTheClientResumesWhereItStoppedAcrossARestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "kithd.sock")
	cache := &numberedCache{epoch: "one"}
	run := runDaemon(t, path, &fakeBackend{Nop: nopWithChannels()}, cache.serves)
	client := daemon.NewRemote(path)
	go func() { _ = client.Start(context.Background()) }()
	t.Cleanup(client.Stop)
	run.waitAttached(t, 1)

	want := burst(cache, "before", 300)
	time.Sleep(20 * time.Millisecond) // the client takes what its queue holds
	run = restart(t, run, path, cache)
	want = append(want, burst(cache, "down", 50)...)
	run.waitAttached(t, 1)
	want = append(want, burst(cache, "after", 50)...)
	readBodies(t, client, want)
	select {
	case c := <-client.Changed():
		t.Fatalf("Changed() said %v; the stream resumed, nothing to read again", c)
	default:
	}
}

// A client whose stream cannot resume (the cache was rebuilt or cleared while it was
// away) is told to read its messages again, and then reads on.
func TestAClientThatCannotResumeIsToldToReadAgain(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "kithd.sock")
	cache := &numberedCache{epoch: "one"}
	run := runDaemon(t, path, &fakeBackend{Nop: nopWithChannels()}, cache.serves)
	client := daemon.NewRemote(path)
	go func() { _ = client.Start(context.Background()) }()
	t.Cleanup(client.Stop)
	run.waitAttached(t, 1)
	readBodies(t, client, burst(cache, "before", 3))

	run.stop(t)
	cache.clear("two")
	run = restart(t, run, path, cache)
	run.waitAttached(t, 1)
	if c := take(t, client.Changed(), "Changed()"); c != domain.ChangeMessages {
		t.Fatalf("Changed() = %v, want ChangeMessages", c)
	}
	readBodies(t, client, burst(cache, "after", 3))
}

// Switching profile reopens every stream; the message stream resumes, so a burst
// committed across switches arrives once and in order.
func TestSwitchingProfileLosesNoMessage(t *testing.T) {
	t.Parallel()
	h, cache := serveNumbered(t)
	if err := h.notifications.Reload(config.Config{Profiles: []config.Profile{
		{Name: "home", Include: []string{"*"}}, {Name: "work", Include: []string{"dm"}},
	}}); err != nil {
		t.Fatal(err)
	}
	client := h.client()
	go func() { _ = client.Start(t.Context()) }()
	t.Cleanup(client.Stop)
	h.waitAttached(1)

	var want []string
	var committed sync.WaitGroup
	committed.Go(func() { want = burst(cache, "m", 400) })
	for i := range 10 {
		if err := client.SwitchProfile([]string{"work", "home"}[i%2]); err != nil {
			t.Fatalf("switch %d: %v", i, err)
		}
	}
	committed.Wait()
	readBodies(t, client, want)
}
