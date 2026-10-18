package signal_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/signal"
	"github.com/EugeneShtoka/kith/internal/signalhelper"
)

// The fake kith-signal is this test binary, run again under the helper's name with
// fakeMode set: it serves the real SignalService over the socket kithd hands it, and
// misbehaves as the mode says.
const (
	fakeMode    = "KITH_FAKE_SIGNAL_MODE"    // how it behaves (fakeHelper)
	fakeStarts  = "KITH_FAKE_SIGNAL_STARTS"  // a file it adds its PID to as it starts
	fakeDevices = "KITH_FAKE_SIGNAL_DEVICES" // the ACIs linked in its store, comma-separated
	fakeLinks   = "KITH_FAKE_SIGNAL_LINKS"   // the ACI a Link links
	fakeEvents  = "KITH_FAKE_SIGNAL_EVENTS"  // a file of events (protojson, a line each) Connect plays
	fakeAcks    = "KITH_FAKE_SIGNAL_ACKS"    // a file it adds a line to as each played event is acked
	fakeSent    = "KITH_FAKE_SIGNAL_SENT"    // a file it adds each Send and Typing to (protojson)
	fakeFails   = "KITH_FAKE_SIGNAL_FAILS"   // how many Sends fail first
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeMode); mode != "" {
		os.Exit(fakeHelper(mode))
	}
	os.Exit(m.Run())
}

// fakeHelper is kith-signal as a test needs it:
//
//	ok      serves
//	crash   exits at once, failing
//	exit    serves a second, then exits as if done
//	slow    starts serving after 2.5 s: one heartbeat goes unanswered, not three
//	freeze  serves a second, then stops (SIGSTOP): still there, answering nothing
//	mute    takes connections and never answers
func fakeHelper(mode string) int {
	if f, err := os.OpenFile(os.Getenv(fakeStarts), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_, _ = fmt.Fprintln(f, os.Getpid())
		_ = f.Close()
	}
	ln, err := net.FileListener(os.NewFile(3, "socket"))
	if err != nil {
		return 2
	}
	serve := func() int {
		network := &fakeNetwork{devices: os.Getenv(fakeDevices), links: os.Getenv(fakeLinks)}
		lapsed := func() { os.Exit(1) }
		if err := signalhelper.Serve(context.Background(), ln, network, 10*time.Second, lapsed, slog.New(slog.DiscardHandler)); err != nil {
			return 1
		}
		return 0
	}
	switch mode {
	case "crash":
		return 3
	case "exit":
		time.AfterFunc(time.Second, func() { os.Exit(0) })
	case "slow":
		time.Sleep(2500 * time.Millisecond)
	case "freeze":
		time.AfterFunc(time.Second, func() { _ = syscall.Kill(os.Getpid(), syscall.SIGSTOP) })
	case "mute":
		held := make(chan net.Conn, 64) // never answered, never closed
		for {
			conn, err := ln.Accept()
			if err != nil {
				return 1
			}
			held <- conn
		}
	}
	return serve()
}

type fakeNetwork struct {
	devices, links string
	mu             sync.Mutex
	failed         int
}

// Send records req, failing the first fakeFails of them.
func (f *fakeNetwork) Send(_ context.Context, req *signalv1.SendRequest) error {
	f.mu.Lock()
	fails, _ := strconv.Atoi(os.Getenv(fakeFails))
	failing := f.failed < fails
	f.failed++
	f.mu.Unlock()
	record(req)
	if failing {
		return errors.New("fake: the network is down")
	}
	return nil
}

// MarkRead records req.
func (f *fakeNetwork) MarkRead(_ context.Context, req *signalv1.MarkReadRequest) error {
	record(req)
	return nil
}

// Download is any attachment's bytes ("bytes of" its pointer) but a pointer "gone".
func (f *fakeNetwork) Download(_ context.Context, req *signalv1.DownloadRequest) ([]byte, error) {
	pointer := string(req.GetPointer())
	if pointer == "gone" {
		return nil, signalhelper.ErrGone
	}
	if hash := req.GetPlaintextHash(); len(hash) > 0 {
		pointer += " checked by " + string(hash)
	}
	return []byte("bytes of " + pointer), nil
}

// Upload records req and returns the file with the pointer "up:" and its name.
func (f *fakeNetwork) Upload(_ context.Context, req *signalv1.UploadRequest) (*signalv1.Attachment, error) {
	record(req)
	return &signalv1.Attachment{
		ContentType: req.GetContentType(), FileName: req.GetFileName(), Size: uint64(len(req.GetData())),
		Pointer: []byte("up:" + req.GetFileName()),
	}, nil
}

// Typing records req.
func (f *fakeNetwork) Typing(_ context.Context, req *signalv1.TypingRequest) error {
	record(req)
	return nil
}

func record(m proto.Message) {
	b, err := protojson.Marshal(m)
	if err != nil {
		return
	}
	if f, err := os.OpenFile(os.Getenv(fakeSent), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_, _ = fmt.Fprintln(f, string(b))
		_ = f.Close()
	}
}

// Connect says the account connected, then plays the events file's events, recording
// each one's ack (the body of a message, else its kind) as it returns. Each account's
// events play in order on their own, as each account's signalmeow client delivers.
func (f *fakeNetwork) Connect(_ context.Context, aci string, sink signalhelper.Sink) error {
	sink.Tell(&signalv1.EventsResponse{Account: aci, Event: &signalv1.EventsResponse_Connection{
		Connection: &signalv1.Connection{State: signalv1.Connection_STATE_CONNECTED},
	}})
	b, err := os.ReadFile(os.Getenv(fakeEvents))
	if err != nil {
		return nil //nolint:nilerr // no events to play
	}
	byAccount := map[string][]*signalv1.EventsResponse{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		ev := &signalv1.EventsResponse{}
		if line == "" || protojson.Unmarshal([]byte(line), ev) != nil {
			continue
		}
		if ev.GetAccount() == "" {
			ev.Account = aci
		}
		byAccount[ev.GetAccount()] = append(byAccount[ev.GetAccount()], ev)
	}
	for _, events := range byAccount {
		go play(sink, events)
	}
	return nil
}

func play(sink signalhelper.Sink, events []*signalv1.EventsResponse) {
	for _, ev := range events {
		sink.Deliver(ev)
		what := fmt.Sprintf("%T", ev.GetEvent())
		if m := ev.GetMessage(); m != nil {
			what = m.GetBody()
		}
		if f, err := os.OpenFile(os.Getenv(fakeAcks), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = fmt.Fprintln(f, what)
			_ = f.Close()
		}
	}
}

func (f *fakeNetwork) Open(context.Context, []byte) ([]signalhelper.Device, error) {
	var out []signalhelper.Device
	for aci := range strings.SplitSeq(f.devices, ",") {
		if aci != "" {
			out = append(out, signalhelper.Device{ACI: aci, Number: "+15550000"})
		}
	}
	return out, nil
}

func (f *fakeNetwork) Link(_ context.Context, _ string, show func(string) error) (signalhelper.Device, error) {
	if err := show("sgnl://linkdevice?uuid=fake"); err != nil {
		return signalhelper.Device{}, err
	}
	return signalhelper.Device{ACI: f.links, Number: "+15550001"}, nil
}

// memSecrets is a keyring in memory.
type memSecrets struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *memSecrets) Secret(ref string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[ref]
	return v, ok, nil
}

func (s *memSecrets) StoreSecret(ref, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[ref] = value
	return nil
}

func (s *memSecrets) DeleteSecret(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, ref)
	return nil
}

// rig is one Signal adapter running a fake helper.
type rig struct {
	t         *testing.T
	a         *signal.Adapter
	bin       string // where the fake kith-signal is installed ($PATH)
	starts    string
	acks      string
	sent      string
	events    string
	cache     *db.Cache
	cachePath string
	secrets   *memSecrets
	cancel    context.CancelFunc
	done      chan error

	mu       sync.Mutex
	statuses []domain.AccountStatus
}

// newRig installs the fake helper in mode, with devices linked in its store and the
// account "phone" linked to linkedTo ("" for not linked), and starts the adapter.
func newRig(t *testing.T, mode, devices, linkedTo string) *rig {
	t.Helper()
	return newRigPlaying(t, mode, devices, linkedTo, nil)
}

// newRigPlaying is newRig whose helper plays events once the account connects.
func newRigPlaying(t *testing.T, mode, devices, linkedTo string, events []*signalv1.EventsResponse) *rig {
	t.Helper()
	return newRigPrepared(t, mode, devices, linkedTo, events, nil)
}

// newRigPrepared is newRigPlaying that runs prepare on the rig (its cache open, nothing
// started) first.
func newRigPrepared(t *testing.T, mode, devices, linkedTo string, events []*signalv1.EventsResponse, prepare func(*rig)) *rig {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Short: a socket's path must fit in 108 bytes.
	dir, err := os.MkdirTemp("", "ks") //nolint:usetesting // t.TempDir's path can pass the 108 bytes a socket's may have
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	bin := filepath.Join(dir, "bin")
	if err = os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	r := &rig{
		t: t, bin: bin, starts: filepath.Join(dir, "starts"), acks: filepath.Join(dir, "acks"), sent: filepath.Join(dir, "sent"),
		events: filepath.Join(dir, "events"), secrets: &memSecrets{m: map[string]string{}},
	}
	r.install(self)
	var lines []string
	for _, ev := range events {
		b, merr := protojson.Marshal(ev)
		if merr != nil {
			t.Fatal(merr)
		}
		lines = append(lines, string(b))
	}
	if err = os.WriteFile(r.events, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeEvents, r.events)
	t.Setenv(fakeAcks, r.acks)
	t.Setenv(fakeSent, r.sent)
	cache, err := db.Open(context.Background(), filepath.Join(dir, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	r.cache, r.cachePath = cache, filepath.Join(dir, "cache.db")
	if prepare != nil {
		prepare(r)
	}
	t.Setenv("PATH", bin)
	t.Setenv(fakeMode, mode)
	t.Setenv(fakeStarts, r.starts)
	t.Setenv(fakeDevices, devices)
	t.Setenv(fakeLinks, "aci-new")
	if linkedTo != "" {
		r.secrets.m["signal|phone"] = linkedTo
	}
	paths := signal.Paths{Store: filepath.Join(dir, "signal.db"), Socket: filepath.Join(dir, "s.sock")}
	r.a = signal.New(r.cache, r.secrets, make([]byte, 32), paths, nil, nil)
	r.a.OnStatus(func(s domain.AccountStatus) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.statuses = append(r.statuses, s)
	})
	r.a.UseConfig(context.Background(), config.Config{Signal: config.Signal{Accounts: []config.SignalAccount{{Name: "phone"}}}})
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan error, 1)
	go func() { r.done <- r.a.Start(ctx) }()
	t.Cleanup(r.stop)
	return r
}

func (r *rig) install(self string) {
	r.t.Helper()
	if err := os.Symlink(self, filepath.Join(r.bin, "kith-signal")); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) uninstall() {
	r.t.Helper()
	if err := os.Remove(filepath.Join(r.bin, "kith-signal")); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	r.cancel = nil
	if err := <-r.done; !errors.Is(err, context.Canceled) {
		r.t.Errorf("Start = %v", err)
	}
	r.a.Stop()
	r.noneLeft()
}

// pids is every helper started so far, in order.
func (r *rig) pids() []int {
	b, _ := os.ReadFile(r.starts)
	var out []int
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		if pid, err := strconv.Atoi(line); err == nil {
			out = append(out, pid)
		}
	}
	return out
}

// alive is whether pid still runs (a stopped one does; a reaped one does not).
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// noneLeft fails if a helper started still runs.
func (r *rig) noneLeft() {
	r.t.Helper()
	for _, pid := range r.pids() {
		if alive(pid) {
			r.t.Errorf("helper %d still runs", pid)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// last is the account's latest state.
func (r *rig) last() (domain.AccountStatus, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.statuses) == 0 {
		return domain.AccountStatus{}, false
	}
	return r.statuses[len(r.statuses)-1], true
}

// until waits up to within for ok, failing with what it waited for.
func (r *rig) until(within time.Duration, what string, ok func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			s, _ := r.last()
			r.t.Fatalf("waited %v for %s; helpers %v, last state %+v", within, what, r.pids(), s)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (r *rig) phase(phase domain.AccountPhase, detail string) func() bool {
	return func() bool {
		s, ok := r.last()
		return ok && s.Phase == phase && strings.Contains(s.Detail, detail)
	}
}

func (r *rig) started(n int) func() bool { return func() bool { return len(r.pids()) >= n } }

// steady fails if another helper starts within d.
func (r *rig) steady(d time.Duration) {
	r.t.Helper()
	before := len(r.pids())
	time.Sleep(d)
	if after := len(r.pids()); after != before {
		r.t.Errorf("%d helpers started while it should have kept one", after-before)
	}
}

// The helper's every way of going: each ends in a state that says so, and a helper
// that ends, hangs or freezes is killed and replaced, never left running.
func TestTheHelperIsKeptRunning(t *testing.T) {
	for _, tc := range []struct {
		mode string
		// then checks what kithd did about it.
		then func(*testing.T, *rig)
	}{
		{"ok", func(t *testing.T, r *rig) {
			r.until(5*time.Second, "linked", r.phase(signal.Linked, ""))
			r.steady(3 * time.Second)
		}},
		{"slow", func(t *testing.T, r *rig) {
			// Slower than a heartbeat's answer, so one is missed: not killed for it.
			r.until(8*time.Second, "linked", r.phase(signal.Linked, ""))
			r.steady(3 * time.Second)
		}},
		{"crash", func(t *testing.T, r *rig) {
			r.until(10*time.Second, "three starts", r.started(3))
			r.until(time.Second, "the state to say so", r.phase(signal.Connecting, "stopped"))
			// Started again after a growing wait (1 s, 2 s, 4 s), never in a tight loop.
			time.Sleep(time.Second)
			if n := len(r.pids()); n > 4 {
				t.Errorf("%d starts of a helper that dies at once: no wait between them", n)
			}
		}},
		{"exit", func(t *testing.T, r *rig) {
			r.until(5*time.Second, "linked", r.phase(signal.Linked, ""))
			r.until(8*time.Second, "a second start", r.started(2))
		}},
		{"freeze", func(t *testing.T, r *rig) {
			r.until(5*time.Second, "linked", r.phase(signal.Linked, ""))
			first := r.pids()[0]
			r.until(20*time.Second, "the frozen helper killed", func() bool { return !alive(first) })
			r.until(5*time.Second, "a new helper", r.started(2))
			r.until(5*time.Second, "linked again", r.phase(signal.Linked, ""))
		}},
		{"mute", func(t *testing.T, r *rig) {
			r.until(5*time.Second, "a start", r.started(1))
			first := r.pids()[0]
			r.until(20*time.Second, "the mute helper killed", func() bool { return !alive(first) })
			r.until(5*time.Second, "a new helper", r.started(2))
		}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			r := newRig(t, tc.mode, "aci-1", "aci-1")
			tc.then(t, r)
			r.stop()
		})
	}
}

// Each account says whether it is linked: by kithd's record and the helper's store.
func TestAccountsSayWhetherTheyAreLinked(t *testing.T) {
	for _, tc := range []struct {
		name, devices, linkedTo string
		phase                   domain.AccountPhase
		detail                  string
	}{
		{"never linked", "", "", signal.Unlinked, "not linked yet"},
		{"unlinked from the phone", "", "aci-1", signal.Unlinked, "unlinked from the phone"},
		{"linked", "aci-1", "aci-1", signal.Linked, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, "ok", tc.devices, tc.linkedTo)
			r.until(5*time.Second, tc.name, r.phase(tc.phase, tc.detail))
			if tc.phase == signal.Linked {
				if me := r.a.Me(); !slices.Equal(me, []string{"signal:aci-1"}) {
					t.Errorf("Me = %v", me)
				}
			}
		})
	}
}

// No kith-signal installed: the account fails, saying why, and the helper starts once
// it is installed. Uninstalled while it runs: it is not started again.
func TestTheHelperStartsOnlyWhenInstalled(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, "exit", "aci-1", "aci-1")
	r.until(5*time.Second, "a start", r.started(1))
	r.uninstall()
	r.until(10*time.Second, "not installed", r.phase(domain.AccountFailed, "not installed"))
	r.steady(3 * time.Second)
	r.install(self)
	r.until(10*time.Second, "a start once installed", r.started(2))
}

// With no account left, or kithd stopping, the helper is killed and not started again.
func TestTheHelperEndsWithItsAccounts(t *testing.T) {
	r := newRig(t, "ok", "aci-1", "aci-1")
	r.until(5*time.Second, "linked", r.phase(signal.Linked, ""))
	first := r.pids()[0]
	r.a.UseConfig(context.Background(), config.Config{})
	r.until(5*time.Second, "the helper killed", func() bool { return !alive(first) })
	r.steady(3 * time.Second)

	r.a.UseConfig(context.Background(), config.Config{Signal: config.Signal{Accounts: []config.SignalAccount{{Name: "phone"}}}})
	r.until(5*time.Second, "a helper for the account back", r.started(2))
	r.until(5*time.Second, "linked", r.phase(signal.Linked, ""))
	second := r.pids()[1]
	r.stop()
	if alive(second) {
		t.Error("the helper outlived kithd's stop")
	}
}

// A state is said once: config re-reads that change nothing say nothing again.
func TestAStateIsSaidOnce(t *testing.T) {
	r := newRig(t, "ok", "aci-1", "aci-1")
	r.until(5*time.Second, "linked", r.phase(signal.Linked, ""))
	r.mu.Lock()
	before := len(r.statuses)
	r.mu.Unlock()
	cfg := config.Config{Signal: config.Signal{Accounts: []config.SignalAccount{{Name: "phone"}}}}
	for range 10 {
		r.a.UseConfig(context.Background(), cfg)
	}
	r.mu.Lock()
	after := len(r.statuses)
	r.mu.Unlock()
	if after != before {
		t.Errorf("ten re-reads said the state %d more times", after-before)
	}
}

// Login draws the helper's link as a QR code and keeps which Signal account it linked.
func TestLoginLinksByQR(t *testing.T) {
	r := newRig(t, "ok", "", "")
	r.until(5*time.Second, "not linked", r.phase(signal.Unlinked, "not linked yet"))
	talk := &apitest.Talk{}
	end, err := r.a.Login(context.Background(), "phone", talk)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(talk.Shown, []string{"sgnl://linkdevice?uuid=fake"}) {
		t.Errorf("shown = %v", talk.Shown)
	}
	if !strings.Contains(end.Done, "+15550001") {
		t.Errorf("done = %q", end.Done)
	}
	if aci, _, _ := r.secrets.Secret("signal|phone"); aci != "aci-new" {
		t.Errorf("kept %q as the account's Signal account", aci)
	}
	r.until(time.Second, "linked", r.phase(signal.Linked, ""))
	if me := r.a.Me(); !slices.Equal(me, []string{"signal:aci-new"}) {
		t.Errorf("Me = %v", me)
	}
	if _, err := r.a.Login(context.Background(), "nobody", talk); !errors.Is(err, signal.ErrNoAccount) {
		t.Errorf("a login for no such account = %v", err)
	}
}
