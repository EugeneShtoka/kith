package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
	"github.com/EugeneShtoka/kith/internal/schedule"
)

// nopScope is a backend that took part in no thread.
type nopScope struct{ *apitest.Nop }

func (nopScope) ThreadParticipant(context.Context, domain.RoomID, domain.EventID) bool { return false }

// A quiet stream outlives the answer timeout: its headers go out when it opens, not
// with its first event.
func TestAQuietStreamOutlivesTheAnswerTimeout(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "d.sock")
	ln, err := Listen(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := &apitest.Nop{}
	streams := NewStreams()
	go streams.Run(ctx, b)
	notifications, err := NewNotifications(config.Config{Profiles: everyRoom}, nopScope{b})
	if err != nil {
		t.Fatal(err)
	}
	uploads, err := NewUploads(filepath.Join(t.TempDir(), "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		Backend: b, Streams: streams, MessageLog: noMessages{}, State: NewState(), Notifications: notifications,
		Reload:      func(context.Context) error { return nil },
		CheckConfig: func(context.Context, config.Config) error { return nil },
		Config:      NewConfigFile(filepath.Join(t.TempDir(), "config.toml")),
		Scheduler:   NewScheduler(schedule.New(filepath.Join(t.TempDir(), "scheduled.toml")), b, func() time.Duration { return time.Hour }, nil),
		Logins:      NewLogins(ctx, func() []api.LoginLeader { return nil }),
		Uploads:     uploads,
		Log:         logging.Discard(),
		Paired:      NewDevices(filepath.Join(t.TempDir(), "devices.toml"), func() (tls.Certificate, error) { return tls.Certificate{}, errors.New("no key") }, logging.Discard()),
	}
	served := make(chan error, 1)
	go func() { served <- Serve(ctx, ln, d) }()
	t.Cleanup(func() { cancel(); <-served })

	const answer = 300 * time.Millisecond
	r := newRemote(dialWithAnswerTimeout(path, answer))
	go func() { _ = r.Start(ctx) }()
	t.Cleanup(r.Stop)

	deadline := time.Now().Add(5 * time.Second)
	for streams.Attached() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if streams.Attached() < 1 {
		t.Fatal("never attached")
	}
	select {
	case attached := <-r.Attached():
		t.Fatalf("attachment changed to %v with the daemon up: a quiet stream hit the answer timeout", attached)
	case <-time.After(6 * answer):
	}
}

// noMessages is a cache that never changes.
type noMessages struct{}

func (noMessages) LastChange(context.Context) (string, int64, error) { return "test", 0, nil }

func (noMessages) ChangesAfter(context.Context, int64, int) (string, []domain.MessageChange, error) {
	return "test", nil, nil
}
