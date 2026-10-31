package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/zalando/go-keyring"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	devicev1 "github.com/EugeneShtoka/kith/internal/api/device/v1"
	"github.com/EugeneShtoka/kith/internal/api/device/v1/devicev1connect"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// roomsKithd is kithd as user commands meet it: each profile's rooms on its rail, their
// messages, who this person is, and what was sent.
type roomsKithd struct {
	backendv1connect.UnimplementedBackendServiceHandler
	rails    map[string][]*v1.RailRoom // by profile
	messages map[string][]*v1.Message  // by room

	mu   sync.Mutex
	sent []string // "room: body"
}

func (k *roomsKithd) Rail(_ context.Context, req *connect.Request[v1.RailRequest]) (*connect.Response[v1.RailResponse], error) {
	profile := req.Header().Get("Kith-Profile")
	if profile == "" {
		profile = "Home" // kithd answers a request naming none for the config's first
	}
	return connect.NewResponse(&v1.RailResponse{Rooms: k.rails[profile]}), nil
}

func (k *roomsKithd) Selves(context.Context, *connect.Request[v1.SelvesRequest]) (*connect.Response[v1.SelvesResponse], error) {
	return connect.NewResponse(&v1.SelvesResponse{Ids: []string{"@me:x"}}), nil
}

func (k *roomsKithd) CachedTimeline(_ context.Context, req *connect.Request[v1.CachedTimelineRequest]) (*connect.Response[v1.CachedTimelineResponse], error) {
	return connect.NewResponse(&v1.CachedTimelineResponse{Messages: k.messages[req.Msg.GetRoomId()]}), nil
}

func (k *roomsKithd) FetchEvent(_ context.Context, req *connect.Request[v1.FetchEventRequest]) (*connect.Response[v1.FetchEventResponse], error) {
	// Found wherever it is, as a cache keyed by event alone would: the relay checks the room.
	for _, msgs := range k.messages {
		for _, m := range msgs {
			if m.GetId() == req.Msg.GetEventId() {
				return connect.NewResponse(&v1.FetchEventResponse{Message: m}), nil
			}
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, errors.New("no such event"))
}

func (k *roomsKithd) Send(_ context.Context, req *connect.Request[v1.SendRequest]) (*connect.Response[v1.SendResponse], error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.sent = append(k.sent, req.Msg.GetRoomId()+": "+req.Msg.GetDraft().GetBody())
	return connect.NewResponse(&v1.SendResponse{}), nil
}

func (k *roomsKithd) said() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.sent)
}

// serve serves k on socket for the test.
func (k *roomsKithd) serve(t *testing.T, socket string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(backendv1connect.NewBackendServiceHandler(k))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

func railRoom(id, name string, spaces ...string) *v1.RailRoom {
	return &v1.RailRoom{Facts: &v1.RoomFacts{Id: id, Name: name, Spaces: spaces, Protocol: "matrix"}}
}

func said(id, room, sender, body string) *v1.Message {
	return &v1.Message{Id: id, RoomId: room, Sender: sender, Body: body}
}

// commandsWorld is a relay over roomsKithd, with a config, a device file and a commands
// directory beside them, and dumps where its scripts write what they were given.
type commandsWorld struct {
	dir, commands, dumps string
	kithd                *roomsKithd
	device               devicev1connect.DeviceServiceClient
}

// dumping is a script that writes what it was given (its argv, its KITH_* variables and
// its stdin) to dumps/<name>, then prints out.
func dumping(dumps, name, out string) string {
	return "#!/bin/sh\n" +
		"{ echo \"argc=$#\"; env | grep '^KITH_' | sort; echo 'stdin:'; cat; } > '" + filepath.Join(dumps, name) + "'\n" +
		"printf '%s' '" + out + "'\n"
}

func newCommandsWorld(t *testing.T, device string, scripts map[string]string) commandsWorld {
	t.Helper()
	dir := t.TempDir()
	path, socket := relayConfig(t, dir)
	w := commandsWorld{dir: dir, commands: filepath.Join(dir, "commands"), dumps: filepath.Join(dir, "dumps")}
	for _, d := range []string{w.commands, w.dumps} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	handWrite(t, config.DevicePath(path), device)
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(w.commands, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	w.kithd = &roomsKithd{
		rails: map[string][]*v1.RailRoom{
			"Home": {railRoom("!a:x", "Family", "Home"), railRoom("!w:x", "Standup", "Work")},
			"Work": {railRoom("!w:x", "Standup", "Work")},
		},
		messages: map[string][]*v1.Message{
			"!a:x": {said("$1", "!a:x", "@ann:x", "first"), said("$2", "!a:x", "@me:x", "see https://example.org/x")},
			"!w:x": {said("$9", "!w:x", "@boss:x", "work only")},
		},
	}
	w.kithd.serve(t, socket)
	w.device = devicev1connect.NewDeviceServiceClient(daemon.Dial(relayFor(t, socket, path)), relayBase)
	return w
}

// run runs name in room as the window would: in profile, on event.
func (w commandsWorld) run(ctx context.Context, profile, name, room, event string) (*devicev1.RunCommandResponse, error) {
	req := connect.NewRequest(&devicev1.RunCommandRequest{Name: name, RoomId: room, EventId: event})
	if profile != "" {
		req.Header().Set("Kith-Profile", profile)
	}
	res, err := w.device.RunCommand(ctx, req)
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// dump is what the script name was given, "" when it never ran.
func (w commandsWorld) dump(name string) string {
	data, err := os.ReadFile(filepath.Join(w.dumps, name))
	if err != nil {
		return ""
	}
	return string(data)
}

const plainDevice = `
[[command]]
name = "zoom"
needs = ["message", "history:2"]

[[command]]
name = "post"
output = "send"

[[command]]
name = "work"
in = ["space:Work"]
output = "status"
`

// The window names a command, and the relay runs only an executable file of that name
// in the commands directory: no other path (up, down or absolute), no file that is not
// executable, no directory, nothing missing. Each is NOT_FOUND, and nothing ran.
func TestOnlyAnExecutableOfTheCommandsDirectoryRuns(t *testing.T) {
	t.Parallel()
	w := newCommandsWorld(t, plainDevice, nil)
	outside := filepath.Join(w.dir, "outside")
	scripts := map[string]string{
		filepath.Join(w.commands, "zoom"):        dumping(w.dumps, "zoom", "ok"),
		outside:                                  dumping(w.dumps, "outside", "escaped"),
		filepath.Join(w.commands, "sub", "deep"): dumping(w.dumps, "deep", "escaped"),
		filepath.Join(w.commands, "note"):        dumping(w.dumps, "note", "escaped"),
	}
	for path, body := range scripts {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o700)
		if filepath.Base(path) == "note" {
			mode = 0o600 // a note, not a command
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"", ".", "..", "../outside", outside, "sub/deep", "sub", "note", "missing", "zoom/../../outside", "zoom words"} {
		_, err := w.run(t.Context(), "Home", name, "!a:x", "")
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("%q: %v, want NOT_FOUND", name, err)
		}
	}
	for _, ran := range []string{"outside", "deep", "note"} {
		if got := w.dump(ran); got != "" {
			t.Errorf("%s ran: %q", ran, got)
		}
	}
	if res, err := w.run(t.Context(), "Home", "zoom", "!a:x", ""); err != nil || res.GetBody() != "ok" {
		t.Errorf("zoom: %v %v, want it run", res, err)
	}
	listed, err := w.device.Commands(t.Context(), withProfile(connect.NewRequest(&devicev1.CommandsRequest{RoomId: "!a:x"}), "Home"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range listed.Msg.GetCommands() {
		names = append(names, c.GetName())
	}
	if !slices.Equal(names, []string{"zoom"}) {
		t.Errorf("listed %v, want only zoom: the list never offers what a run refuses", names)
	}
}

func withProfile[T any](req *connect.Request[T], profile string) *connect.Request[T] {
	req.Header().Set("Kith-Profile", profile)
	return req
}

// A command runs only in a room of the window's profile, named on the request by main:
// a room of another profile, a room of none, or no profile named is refused before
// anything runs. Outside its `in` places it is refused too, and listed as not applying.
func TestACommandRunsOnlyInARoomOfTheProfileAndItsPlaces(t *testing.T) {
	t.Parallel()
	w := newCommandsWorld(t, plainDevice, nil)
	for _, name := range []string{"zoom", "work"} {
		if err := os.WriteFile(filepath.Join(w.commands, name), []byte(dumping(w.dumps, name, "ran")), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	refused := []struct {
		profile, name, room string
		code                connect.Code
	}{
		{"Work", "zoom", "!a:x", connect.CodePermissionDenied}, // Home's room, asked in Work
		{"Home", "zoom", "!nowhere:x", connect.CodePermissionDenied},
		{"Home", "zoom", "", connect.CodePermissionDenied},
		{"", "zoom", "!a:x", connect.CodePermissionDenied},
		{"Home", "work", "!a:x", connect.CodeFailedPrecondition}, // in = space:Work
	}
	for _, r := range refused {
		_, err := w.run(t.Context(), r.profile, r.name, r.room, "")
		if connect.CodeOf(err) != r.code {
			t.Errorf("%s in %q, profile %q: %v, want %v", r.name, r.room, r.profile, err, r.code)
		}
	}
	if w.dump("zoom") != "" || w.dump("work") != "" {
		t.Fatalf("a refused command ran: %q %q", w.dump("zoom"), w.dump("work"))
	}
	if res, err := w.run(t.Context(), "Work", "work", "!w:x", ""); err != nil || res.GetBody() != "ran" || res.GetOutput() != "status" {
		t.Errorf("work in its space: %v %v", res, err)
	}
	listed, err := w.device.Commands(t.Context(), withProfile(connect.NewRequest(&devicev1.CommandsRequest{RoomId: "!a:x"}), "Home"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range listed.Msg.GetCommands() {
		if want := c.GetName() != "work"; c.GetApplies() != want {
			t.Errorf("%s applies %v in !a:x, want %v", c.GetName(), c.GetApplies(), want)
		}
	}
	if _, err := w.device.Commands(t.Context(), withProfile(connect.NewRequest(&devicev1.CommandsRequest{RoomId: "!a:x"}), "Work")); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("listing for another profile's room: %v, want PERMISSION_DENIED", err)
	}
}

// What a program is given is what the relay looked up, never what the window said: no
// argument, KITH_ARG empty, the message and history as kithd holds them, the message
// named only if it is in the room run in (another room's is no message here), and its
// secrets from the keyring, never the window.
func TestAProgramIsGivenOnlyWhatTheRelayLookedUp(t *testing.T) {
	t.Parallel()
	w := newCommandsWorld(t, plainDevice+`
[[command]]
name = "keyed"
secrets = ["token"]
`, nil)
	for _, name := range []string{"zoom", "keyed"} {
		if err := os.WriteFile(filepath.Join(w.commands, name), []byte(dumping(w.dumps, name, "ok")), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.run(t.Context(), "Home", "zoom", "!a:x", "$1"); err != nil {
		t.Fatal(err)
	}
	got := w.dump("zoom")
	for _, want := range []string{"argc=0", "KITH_ARG=\n", "KITH_MESSAGE_ID=$1", "KITH_ROOM_ID=!a:x", "KITH_ROOM=Family", "KITH_USER=@me:x"} {
		if !strings.Contains(got, want) {
			t.Errorf("zoom was not given %q:\n%s", want, got)
		}
	}
	_, stdin, _ := strings.Cut(got, "stdin:\n")
	var context domain.ScriptContext
	if err := json.Unmarshal([]byte(stdin), &context); err != nil {
		t.Fatalf("stdin %q: %v", stdin, err)
	}
	if context.Message == nil || context.Message.Body != "first" || len(context.History) != 2 || !context.History[1].Mine {
		t.Errorf("stdin = %+v, want $1 and the last two, the second mine", context)
	}
	// Another room's message, named here, is no message here.
	res, err := w.run(t.Context(), "Home", "zoom", "!a:x", "$9")
	if err != nil || res.GetWhy() == "" {
		t.Errorf("run on another room's message: %v %v, want it said there is none", res, err)
	}

	res, err = w.run(t.Context(), "Home", "keyed", "!a:x", "")
	if err != nil || !strings.Contains(res.GetWhy(), "token") || w.dump("keyed") != "" {
		t.Errorf("keyed with no secret kept: %v %v, ran %q; want refused, naming it", res, err, w.dump("keyed"))
	}
	if err := keyring.Set("kith", "secret|"+config.SecretRef("keyed", "token"), "s3cret"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.run(t.Context(), "Home", "keyed", "!a:x", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.dump("keyed"), "KITH_SECRET_TOKEN=s3cret") {
		t.Errorf("keyed was not given its secret:\n%s", w.dump("keyed"))
	}
}

// Output goes where the block says: send is sent into the room it was run in, by the
// relay, and comes back done; compose comes back to be put in the composer.
func TestACommandsOutputGoesWhereItsBlockSays(t *testing.T) {
	t.Parallel()
	w := newCommandsWorld(t, plainDevice, nil)
	for name, out := range map[string]string{"zoom": "a link", "post": "posted"} {
		if err := os.WriteFile(filepath.Join(w.commands, name), []byte(dumping(w.dumps, name, out)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	res, err := w.run(t.Context(), "Home", "post", "!a:x", "")
	if err != nil || res.GetOutput() != "send" || res.GetBody() != "" {
		t.Errorf("post: %v %v, want sent, no body back", res, err)
	}
	if got := w.kithd.said(); !slices.Equal(got, []string{"!a:x: posted"}) {
		t.Errorf("sent %v, want posted into !a:x", got)
	}
	res, err = w.run(t.Context(), "Home", "zoom", "!a:x", "")
	if err != nil || res.GetOutput() != "compose" || res.GetBody() != "a link" {
		t.Errorf("zoom: %v %v, want its output back to compose", res, err)
	}
}

// One command at a time, as the TUI's: a second run while one runs is refused. A run
// whose request ends (the window reloaded) kills its program, and the next runs.
func TestARunEndsWithItsRequestAndOneRunsAtATime(t *testing.T) {
	t.Parallel()
	w := newCommandsWorld(t, plainDevice, nil)
	started := filepath.Join(w.dumps, "started")
	finished := filepath.Join(w.dumps, "finished")
	slow := "#!/bin/sh\ntouch '" + started + "'\nsleep 2\ntouch '" + finished + "'\necho done\n"
	if err := os.WriteFile(filepath.Join(w.commands, "zoom"), []byte(slow), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() {
		_, err := w.run(ctx, "Home", "zoom", "!a:x", "")
		first <- err
	}()
	within(t, 5*time.Second, "the program starting", func() bool {
		for {
			if _, err := os.Stat(started); err == nil {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	if _, err := w.run(t.Context(), "Home", "zoom", "!a:x", ""); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("a second run while one runs: %v, want FAILED_PRECONDITION", err)
	}
	cancel() // the window reloads
	if err := within(t, 3*time.Second, "the canceled run ending", func() error { return <-first }); err == nil {
		t.Error("the canceled run answered")
	}
	// Another file: a shell reads its script as it runs, so rewriting zoom would end it too.
	if err := os.WriteFile(filepath.Join(w.commands, "next"), []byte("#!/bin/sh\necho again\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	// The canceled run holds its turn until its program is gone (killed, its output's
	// writers waited out), which may be after the window was told it ended.
	if res, err := within(t, 3*time.Second, "the next run", func() result {
		for {
			res, err := w.run(t.Context(), "Home", "next", "!a:x", "")
			if connect.CodeOf(err) != connect.CodeFailedPrecondition {
				return result{res, err}
			}
			time.Sleep(20 * time.Millisecond)
		}
	}).unpack(); err != nil || res.GetBody() != "again" {
		t.Errorf("the next run: %v %v, want it run", res, err)
	}
	time.Sleep(3 * time.Second)
	if _, err := os.Stat(finished); err == nil {
		t.Error("the program of the canceled run went on")
	}
}

type result struct {
	res *devicev1.RunCommandResponse
	err error
}

func (r result) unpack() (*devicev1.RunCommandResponse, error) { return r.res, r.err }

// The relay leaving with its window kills a command still running, and waits for it.
func TestNoCommandOutlivesTheRelay(t *testing.T) {
	t.Parallel()
	runs := &commandRuns{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if _, ok := runs.begin("zoom", cancel); !ok {
		t.Fatal("no run began")
	}
	stopped := make(chan struct{})
	go func() { runs.stop(); close(stopped) }()
	<-ctx.Done() // stop kills the run
	select {
	case <-stopped:
		t.Fatal("stop returned while the run was still ending")
	case <-time.After(50 * time.Millisecond):
	}
	runs.end()
	within(t, time.Second, "stop returning", func() struct{} { <-stopped; return struct{}{} })
	if _, ok := runs.begin("zoom", cancel); ok {
		t.Error("a run began after the relay stopped")
	}
}
