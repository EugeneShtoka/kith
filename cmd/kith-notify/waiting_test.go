package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/notify"
)

const lockedWhy = "the cache is locked: the keyring is not answering; unlock it"

// waitingKithd answers on socket as kithd does while its cache is locked, until the
// returned stop.
func waitingKithd(t *testing.T, socket string) (stop func()) {
	t.Helper()
	ln, err := daemon.Listen(t.Context(), socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() { defer close(served); _ = daemon.ServeWaiting(ctx, ln, func() string { return lockedWhy }) }()
	stop = func() { cancel(); <-served }
	t.Cleanup(stop)
	return stop
}

// A kithd that cannot start is told once, at once, with why, however many profiles
// retry how often; once it has served, a later wait is told again.
func TestAWaitingKithdIsToldOnceWithWhy(t *testing.T) {
	t.Parallel()
	socket := socketPath(t)
	stopWaiting := waitingKithd(t, socket)

	rec := &shown{}
	logs := &logBuffer{}
	sinks := func(config.Notifications) notify.Notifier { return rec }
	a := newAgent(slog.New(slog.NewTextHandler(logs, nil)), func() *daemon.Remote { return daemon.NewRemote(socket) },
		sinks, config.Config{Notifications: config.Notifications{Enabled: true}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); a.run(ctx, []string{"First", "Third"}) }()
	t.Cleanup(func() { cancel(); <-done })

	waitFor(t, func() bool { return len(rec.all()) >= 1 }, "the waiting to be told")
	// Each profile is refused again after that; nothing more is shown.
	retried := func(profile string) bool {
		return logs.count(`stopped; listening again" profile=`+profile) >= 2
	}
	waitFor(t, func() bool { return retried("First") && retried("Third") }, "both profiles to retry")
	told := rec.all()
	if len(told) != 1 || !strings.Contains(told[0], lockedWhy) {
		t.Fatalf("shown %q, want one popup saying kithd is waiting: %s", told, lockedWhy)
	}
	if logs.count("listening for notifications") != 0 {
		t.Error("a refused stream was taken for listening")
	}

	// kithd starts, serves, and later waits again: that is told once more.
	stopWaiting()
	k := serve(t, socket, threeProfiles())
	waitFor(t, func() bool { return logs.count("listening for notifications") >= 2 }, "the agent to listen")
	k.stop()
	waitingKithd(t, socket)
	waitFor(t, func() bool { return len(rec.all()) >= 2 }, "the second wait to be told")
	time.Sleep(100 * time.Millisecond)
	if got := len(rec.all()); got != 2 {
		t.Errorf("shown %d popups over two waits, want 2: %q", got, rec.all())
	}
}
