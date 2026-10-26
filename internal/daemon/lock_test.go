package daemon_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/daemon"
)

// One kith-notify per instance shows its notifications, however often kith starts it:
// a second waits its turn, and gets the lock once the first lets go. The daemon's own
// lock is a different one.
func TestOneNotifierPerInstance(t *testing.T) {
	t.Parallel()
	storage := lockUser(t)
	release, held, err := daemon.AcquireNotifier(storage)
	if err != nil || !held {
		t.Fatalf("the first: held %v, %v", held, err)
	}
	_, again, err := daemon.AcquireNotifier(storage)
	if err != nil || again {
		t.Fatalf("a second while the first runs: held %v, %v; want it refused", again, err)
	}
	lock, held, err := daemon.Acquire(storage)
	if err != nil || !held {
		t.Fatalf("kithd beside kith-notify: held %v, %v", held, err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	if err = release(); err != nil {
		t.Fatal(err)
	}
	release, held, err = daemon.AcquireNotifier(storage)
	if err != nil || !held {
		t.Fatalf("after the first let go: held %v, %v", held, err)
	}
	_ = release()
}
