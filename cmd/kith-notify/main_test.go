package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
	"github.com/EugeneShtoka/kith/internal/notify"
	"github.com/EugeneShtoka/kith/internal/schedule"
)

const mx = "matrix:0123456789abcdef/"

// Rooms: one only the first profile holds, one all three do, one only the third does.
const (
	firstOnly domain.RoomID = mx + "!first:x"
	both      domain.RoomID = mx + "!both:x"
	thirdOnly domain.RoomID = mx + "!third:x"
)

// threeProfiles is a config whose profiles name those rooms; every message notifies.
func threeProfiles() config.Config {
	return config.Config{
		Notifications: config.Notifications{Rules: []config.Rule{{Show: "all"}}},
		Profiles: []config.Profile{
			{Name: "First", Include: []string{string(firstOnly), string(both)}},
			{Name: "Second", Include: []string{string(both)}},
			{Name: "Third", Include: []string{string(thirdOnly), string(both)}},
		},
	}
}

// world is a backend knowing no rooms; a room is judged by its ID.
type world struct{ apitest.Nop }

func (world) ThreadParticipant(context.Context, domain.RoomID, domain.EventID) bool { return false }

// kithd serves cfg on socket until stopped; its own sinks show nothing.
type kithd struct {
	n    *daemon.Notifications
	stop func()
}

func serve(t *testing.T, socket string, cfg config.Config) kithd {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	backend := world{}
	n, err := daemon.NewNotifications(cfg, backend)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := daemon.Listen(t.Context(), socket)
	if err != nil {
		t.Fatal(err)
	}
	uploads, err := daemon.NewUploads(filepath.Join(dir, "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = daemon.Serve(ctx, ln, &daemon.Daemon{
			Backend: backend, Streams: daemon.NewStreams(), MessageLog: noMessages{}, State: daemon.NewState(), Notifications: n,
			Reload:      func(context.Context) error { return nil },
			CheckConfig: func(context.Context, config.Config) error { return nil },
			Config:      daemon.NewConfigFile(path),
			Scheduler: daemon.NewScheduler(schedule.New(filepath.Join(dir, "scheduled.toml")), backend,
				func() time.Duration { return time.Hour }, nil),
			Logins:  daemon.NewLogins(ctx, func() []api.LoginLeader { return nil }),
			Uploads: uploads,
			Log:     logging.Discard(),
			Paired: daemon.NewDevices(filepath.Join(dir, "devices.toml"),
				func() (tls.Certificate, error) { return tls.Certificate{}, errors.New("no key") }, logging.Discard()),
		})
	}()
	k := kithd{n: n, stop: func() { cancel(); <-served }}
	t.Cleanup(k.stop)
	return k
}

// noMessages is a cache that never changes: the agent reads notifications, not messages.
type noMessages struct{}

func (noMessages) LastChange(context.Context) (string, int64, error) { return "test", 0, nil }

func (noMessages) ChangesAfter(context.Context, int64, int) (string, []domain.MessageChange, error) {
	return "test", nil, nil
}

// says has kithd notify about a message in room whose body is body.
func (k kithd) says(room domain.RoomID, body string) {
	owner := domain.OwnerOf(room)
	k.n.Synced(owner, time.Now())
	k.n.Synced(owner, time.Now()) // past the catch-up batch
	k.n.Deliver(context.Background(), domain.Message{ID: domain.EventID(mx + "$" + body), RoomID: room, Sender: "matrix:@alice:x", Body: body, Timestamp: time.Now()})
}

// shown records what the agent showed, and with which settings.
type shown struct {
	mu     sync.Mutex
	bodies []string
	sounds map[string]string // by body
}

func (s *shown) Notify(n notify.Notification) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies = append(s.bodies, n.Body)
	if s.sounds == nil {
		s.sounds = map[string]string{}
	}
	s.sounds[n.Body] = n.Sound
}

// soundOf is the sound the notification with body was shown with.
func (s *shown) soundOf(body string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sounds[body]
}

func (s *shown) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

// logBuffer is a log the test reads while the agent writes it.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) count(s string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), s)
}

// notificationsOn is a device file turning notifications on.
const notificationsOn = "[notifications]\nenabled = true\n"

// deviceFile is a device file holding body, by its path.
func deviceFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), config.DeviceFile)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// listening runs an agent on profiles against socket, showing to rec while the device's
// notifications are on, and waits until it has listened n times.
func listening(t *testing.T, socket string, rec *shown, profiles ...string) *logBuffer {
	t.Helper()
	return listeningWith(t, socket, deviceFile(t, notificationsOn), rec, profiles...)
}

// listeningWith is listening with the device file at device.
func listeningWith(t *testing.T, socket, device string, rec *shown, profiles ...string) *logBuffer {
	t.Helper()
	logs := &logBuffer{}
	sinks := func(notifs config.DeviceNotifications) notify.Notifier {
		if !notifs.Enabled {
			return notify.Nop{}
		}
		return rec
	}
	a := newAgent(slog.New(slog.NewTextHandler(logs, nil)), func() *daemon.Remote { return daemon.NewRemote(socket) },
		sinks, device, config.Device{Notifications: config.DeviceNotifications{Enabled: true}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); a.run(ctx, profiles) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, func() bool { return logs.count("listening for notifications") >= len(profiles) }, "the agent to listen")
	return logs
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// settled waits for the agent to show want, then a while longer for anything more.
func settled(t *testing.T, rec *shown, want []string) {
	t.Helper()
	waitFor(t, func() bool { return len(rec.all()) >= len(want) }, "the notifications")
	time.Sleep(100 * time.Millisecond)
	if got := rec.all(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("shown %v, want %v", got, want)
	}
}

func socketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "s")
}

// A message in both profiles listened to is shown once; one only in a profile not
// listened to is not shown.
func TestANotificationInTwoProfilesIsShownOnce(t *testing.T) {
	t.Parallel()
	socket := socketPath(t)
	k := serve(t, socket, threeProfiles())
	rec := &shown{}
	listening(t, socket, rec, "second", "THIRD")

	k.says(both, "in both")
	k.says(firstOnly, "in the first")
	k.says(thirdOnly, "in the third")
	settled(t, rec, []string{"in both", "in the third"})
}

// A kithd that restarts is listened to again, and what it publishes after is shown:
// its notifications are not taken for ones shown before it restarted.
func TestARestartedKithdIsListenedToAgain(t *testing.T) {
	t.Parallel()
	socket := socketPath(t)
	k := serve(t, socket, threeProfiles())
	rec := &shown{}
	logs := listening(t, socket, rec, "first")
	k.says(firstOnly, "before")
	settled(t, rec, []string{"before"})

	k.stop()
	k = serve(t, socket, threeProfiles())
	waitFor(t, func() bool { return logs.count("listening for notifications") >= 2 }, "the agent to listen again")
	k.says(firstOnly, "after")
	settled(t, rec, []string{"before", "after"})
}

// The agent shows with the device file's notification settings as they are when a
// notification arrives: turned off there, nothing is shown; on again, it is.
func TestTheAgentFollowsTheDeviceFile(t *testing.T) {
	t.Parallel()
	socket := socketPath(t)
	k := serve(t, socket, threeProfiles())
	rec := &shown{}
	device := deviceFile(t, notificationsOn)
	listeningWith(t, socket, device, rec, "first")
	if err := os.WriteFile(device, []byte("[notifications]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	k.says(firstOnly, "while off")
	time.Sleep(200 * time.Millisecond)
	if got := rec.all(); len(got) != 0 {
		t.Errorf("shown %v with the device's notifications off", got)
	}
	if err := os.WriteFile(device, []byte(notificationsOn), 0o600); err != nil {
		t.Fatal(err)
	}
	k.says(firstOnly, "on again")
	settled(t, rec, []string{"on again"})
}

// A profile the config lacks is refused, naming those it has; none named is the first.
func TestProfilesAreTheConfigs(t *testing.T) {
	t.Parallel()
	cfg := threeProfiles()
	if got, err := profileNames(cfg, nil); err != nil || strings.Join(got, ",") != "First" {
		t.Errorf("no --profile = %v, %v; want First", got, err)
	}
	if got, err := profileNames(cfg, []string{" third ", "SECOND"}); err != nil || strings.Join(got, ",") != "Third,Second" {
		t.Errorf("--profile third --profile SECOND = %v, %v; want Third, Second", got, err)
	}
	if _, err := profileNames(cfg, []string{"home"}); err == nil || !strings.Contains(err.Error(), "First, Second, Third") {
		t.Errorf("--profile home = %v, want it refused naming First, Second, Third", err)
	}
}

// run finds kithd's socket and the profiles in the config: a profile the config lacks
// stops it, naming those it has; with ones it has, it listens to each until stopped,
// and a second for the same config leaves at once.
func TestRunListensForTheConfigsProfiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := threeProfiles()
	cfg.Matrix = config.Matrix{Accounts: []config.MatrixAccount{{Name: "me", Homeserver: "https://x", User: "@me:x"}}}
	cfg.Storage = config.Storage{
		Instance: "t", DataDir: filepath.Join(dir, "data"), StateDir: filepath.Join(dir, "state"),
		CacheDir: filepath.Join(dir, "cache"), RuntimeDir: filepath.Join(dir, "run"),
	}
	path := filepath.Join(dir, "config.toml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	quiet := slog.New(slog.DiscardHandler)
	err := run(t.Context(), quiet, &slog.LevelVar{}, "", path, []string{"home"})
	if err == nil || !strings.Contains(err.Error(), "First, Second, Third") {
		t.Fatalf("run --profile home = %v, want it refused naming First, Second, Third", err)
	}

	if err := os.MkdirAll(cfg.Storage.RuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	serve(t, filepath.Join(cfg.Storage.RuntimeDir, "t.sock"), cfg)
	logs := &logBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, slog.New(slog.NewTextHandler(logs, nil)), &slog.LevelVar{}, "", path, []string{"first", "third"})
	}()
	waitFor(t, func() bool { return logs.count("listening for notifications") == 2 }, "both profiles to be listened to")

	// kith starts one every time it starts: a second for the same config leaves at once.
	second := &logBuffer{}
	if err := run(t.Context(), slog.New(slog.NewTextHandler(second, nil)), &slog.LevelVar{}, "", path, nil); err != nil {
		t.Errorf("a second run = %v, want it to leave quietly", err)
	}
	if second.count("listening") != 0 {
		t.Errorf("a second run logged %q, want it not to listen", second.buf.String())
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("run stopped with %v, want nil", err)
	}
}

// codesFrom is threeProfiles finding codes in every room; no message notifies, which a
// code ignores.
func codesFrom() config.Config {
	cfg := threeProfiles()
	cfg.Notifications.Rules = []config.Rule{{Show: "none"}}
	cfg.Clipboard = config.Clipboard{AutoCopy: true}
	cfg.Codes.Include = []string{string(firstOnly), string(both), string(thirdOnly)}
	return cfg
}

// A code kithd found is copied where it is shown, once, however many profiles listened
// to carry it, and one popup carries it. Without a graphical session it is not copied,
// and the popup still carries the code.
func TestTheAgentCopiesACodeOnce(t *testing.T) {
	clip := filepath.Join(t.TempDir(), "clipboard")
	t.Setenv("WAYLAND_DISPLAY", "wayland-test")
	socket := socketPath(t)
	k := serve(t, socket, codesFrom())
	rec := &shown{}
	// The device copies by appending to clip.
	listeningWith(t, socket, deviceFile(t, notificationsOn+"\n[clipboard]\ncommand = 'cat >> "+clip+"'\n"), rec, "second", "third")

	k.says(both, "Your code is 482910")
	waitFor(t, func() bool { return len(rec.all()) >= 1 }, "the first code's popup")
	time.Sleep(100 * time.Millisecond)
	if got := rec.all(); len(got) != 1 || !strings.Contains(got[0], "482910") {
		t.Errorf("shown %q, want one popup carrying the code", got)
	}
	if got, err := os.ReadFile(clip); err != nil || string(got) != "482910" {
		t.Errorf("clipboard = %q, %v; want the code, once", got, err)
	}

	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	k.says(thirdOnly, "Your code is 777123")
	waitFor(t, func() bool { return len(rec.all()) == 2 }, "the second code's popup")
	if got := rec.all()[1]; !strings.Contains(got, "777123") {
		t.Errorf("popup %q, want the code", got)
	}
	if got, _ := os.ReadFile(clip); string(got) != "482910" {
		t.Errorf("clipboard = %q after a copy that could not run", got)
	}
}

// soundRules notify everything; two rules name the room both profiles hold, Alice's the
// narrower, and one silences the third's room.
func soundRules() config.Config {
	cfg := threeProfiles()
	cfg.Notifications.Rules = append(cfg.Notifications.Rules,
		config.Rule{Name: "boss", Match: string(both), Sender: "matrix:@alice:x"},
		config.Rule{Name: "both", Match: string(both)},
		config.Rule{Name: "third", Match: string(thirdOnly), Ring: "none"},
	)
	return cfg
}

// Two devices listening to one kithd sound its notifications each its own way: the
// most specific rule the device gives a sound wins, a rule it gives none is passed
// over, a notification no named rule of it matched takes its default, and a silenced
// one plays nothing though the device has a sound for its rule.
func TestEachDevicePicksItsOwnSound(t *testing.T) {
	t.Parallel()
	socket := socketPath(t)
	k := serve(t, socket, soundRules())
	laptop, phone := &shown{}, &shown{}
	listeningWith(t, socket, deviceFile(t, notificationsOn+`sound = "/l/default.oga"

[notifications.sounds]
boss  = "/l/boss.oga"
both  = "/l/both.oga"
third = "/l/third.oga"
`), laptop, "first", "third")
	listeningWith(t, socket, deviceFile(t, notificationsOn+`sound = "/p/default.oga"

[notifications.sounds]
both = "/p/both.oga"
`), phone, "first", "third")

	k.says(both, "in both")
	k.says(firstOnly, "in the first")
	k.says(thirdOnly, "in the third")
	// Two profiles' streams: the order they arrive in is theirs.
	for _, rec := range []*shown{laptop, phone} {
		waitFor(t, func() bool { return len(rec.all()) >= 3 }, "the notifications")
	}
	time.Sleep(100 * time.Millisecond)
	for _, rec := range []*shown{laptop, phone} {
		if got := rec.all(); len(got) != 3 {
			t.Errorf("shown %v, want each once", got)
		}
	}
	for _, c := range []struct {
		rec         *shown
		device      string
		body, sound string
	}{
		{laptop, "laptop", "in both", "/l/boss.oga"},
		{phone, "phone", "in both", "/p/both.oga"},
		{laptop, "laptop", "in the first", "/l/default.oga"},
		{phone, "phone", "in the first", "/p/default.oga"},
		{laptop, "laptop", "in the third", ""},
		{phone, "phone", "in the third", ""},
	} {
		if got := c.rec.soundOf(c.body); got != c.sound {
			t.Errorf("the %s sounded %q %q, want %q", c.device, c.body, got, c.sound)
		}
	}
}
