package llamacpp

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"testing"
	"time"
)

// attached is a Predictor already holding a server, which is the state every ask after
// the first one is in.
func attached(s *stub) *Predictor {
	return &Predictor{
		settings: Settings{Command: "llama-server", Model: "/models/x.gguf"},
		running:  s.server(),
	}
}

// The strip belongs to NextWords rather than to every caller. The composer hands over a
// draft ending in a space — that is what a word boundary *is* over there — and with the
// space left on, this model answers "2", "1", "3" instead of words.
func TestNextWordsStripsTheDraftsTrailingSpace(t *testing.T) {
	t.Parallel()

	s := newStub(t, " weather", " data", " status")
	got, err := attached(s).NextWords(t.Context(), "I think we should check the ", 2)
	if err != nil {
		t.Fatalf("NextWords: %v", err)
	}
	if s.asked != "I think we should check the" {
		t.Errorf("asked %q, want the draft without its trailing space", s.asked)
	}
	if want := []string{"weather", "data"}; !slices.Equal(got, want) {
		t.Errorf("NextWords = %v, want %v", got, want)
	}
}

// Slack over what is wanted, because the filtering happens after the answer arrives: ask
// for exactly two and a full stop and a duplicate leave one.
func TestNextWordsAsksForMoreThanItNeeds(t *testing.T) {
	t.Parallel()

	s := newStub(t, " a")
	if _, err := attached(s).NextWords(t.Context(), "hello", 2); err != nil {
		t.Fatalf("NextWords: %v", err)
	}
	if s.probes < 2+probeSpare {
		t.Errorf("asked for %d tokens, want at least %d", s.probes, 2+probeSpare)
	}
}

func TestNextWordsWithNothingToAskAnswersNothing(t *testing.T) {
	t.Parallel()

	s := newStub(t, " a")
	p := attached(s)
	// A draft that is nothing but the boundary. Not an error: it is what the composer
	// holds for the first keystroke of a message, and there is no sentence to continue.
	got, err := p.NextWords(t.Context(), "   ", 4)
	if err != nil || len(got) != 0 {
		t.Errorf("NextWords = %v, %v; want nothing and no error", got, err)
	}
	if got, err := p.NextWords(t.Context(), "hello", 0); err != nil || len(got) != 0 {
		t.Errorf("NextWords for zero options = %v, %v", got, err)
	}
}

// A server that has died — a suspended laptop, the OOM killer, somebody's pkill — is
// forgotten rather than talked to for the rest of the session.
func TestAFailedAskDropsTheServer(t *testing.T) {
	t.Parallel()

	s := newStub(t)
	p := attached(s)
	// Closing the stub is the cheapest honest version of a dead server: the port stops
	// answering, which is exactly what the process going away looks like from here.
	s.Close()

	if _, err := p.NextWords(t.Context(), "hello", 4); err == nil {
		t.Fatal("a dead server answered")
	}
	p.mu.Lock()
	running := p.running
	p.mu.Unlock()
	if running != nil {
		t.Error("the dead server is still held, so the next ask would reach the same closed port")
	}
}

// forget is called from a failed request, which may land after another goroutine has
// already replaced the server. Stopping *that* one would turn one slow completion into a
// restart loop.
func TestForgetLeavesAServerThatHasAlreadyBeenReplaced(t *testing.T) {
	t.Parallel()

	first, second := newStub(t), newStub(t)
	p := attached(first)
	replacement := second.server()
	p.running = replacement

	p.forget(first.server())

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running != replacement {
		t.Error("forgetting a stale server dropped the live one")
	}
}

// Close is final: a request racing a shutdown must not spawn a process nobody is holding.
func TestNothingIsSpawnedAfterClose(t *testing.T) {
	t.Parallel()

	p := NewPredictor(Settings{Command: "llama-server", Model: "/models/x.gguf"})
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := p.NextWords(t.Context(), "hello", 4); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("NextWords after Close: %v, want ErrNotConfigured", err)
	}
}

// Two states, two fixes: a model that is not installed is a download this client can do,
// and a server that is not installed is a package manager. The offer has to say which, so
// they are separate errors rather than a bool.
func TestWhyNamesWhichHalfIsMissing(t *testing.T) {
	t.Parallel()

	if err := NewPredictor(Settings{Command: "llama-server"}).Why(); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("no model: %v, want ErrNotConfigured", err)
	}
	unlikely := "kith-no-such-program"
	if _, err := exec.LookPath(unlikely); err == nil {
		t.Skipf("%s exists on this machine", unlikely)
	}
	err := NewPredictor(Settings{Command: unlikely, Model: "/models/x.gguf"}).Why()
	if !errors.Is(err, ErrNoServer) {
		t.Errorf("no server: %v, want ErrNoServer", err)
	}
	if p := NewPredictor(Settings{Command: unlikely, Model: "/models/x.gguf"}); p.Available() {
		t.Error("Available is true with no server to ask")
	}
}

// The idle stop is the whole reason the daemon can hold this at all: 460 MB resident
// against 420ms to get it back.
func TestTheServerIsStoppedOnceItGoesQuiet(t *testing.T) {
	t.Parallel()

	s := newStub(t)
	p := attached(s)
	srv := p.running
	// A clock that has already run past the window, so the first tick decides. The
	// alternative is a test that sleeps for the real idle period.
	srv.now = func() time.Time { return time.Now().Add(time.Hour) }

	done := make(chan struct{})
	go func() { defer close(done); p.watchIdle(srv, 4*time.Second) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the idle watch never stopped a server that had been quiet for an hour")
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running != nil {
		t.Error("the idle watch stopped the process and kept the handle")
	}
}

// A window of zero means the default and a negative one means "never stop it", which is
// the setting for a machine with memory to spare.
func TestIdleZeroIsTheDefaultAndNegativeNeverStops(t *testing.T) {
	t.Parallel()

	if got := idleOr(0); got != idleDefault {
		t.Errorf("idleOr(0) = %v, want %v", got, idleDefault)
	}
	if got := idleOr(-1); got != -1 {
		t.Errorf("idleOr(-1) = %v, want it left alone", got)
	}
	// And the watch for it returns rather than ticking forever over a server it will
	// never stop.
	done := make(chan struct{})
	go func() { defer close(done); (&Predictor{}).watchIdle(nil, -1) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the idle watch is still running for a window that says never")
	}
}

// The command line follows from decisions this package has already made, and two of them
// are worth pinning: loopback only, because the model reads drafts, and one slot, because
// four divide the context window while multiplying the KV cache.
func TestTheServerIsSpawnedOnLoopbackWithOneSlot(t *testing.T) {
	t.Parallel()

	got := args(Settings{Model: "/models/x.gguf", Threads: 4, Context: 2048}, 8099)
	for _, want := range [][2]string{
		{"--host", "127.0.0.1"},
		{"--port", "8099"},
		{"--parallel", "1"},
		{"--ctx-size", "2048"},
		{"--threads", "4"},
		{"--model", "/models/x.gguf"},
	} {
		at := slices.Index(got, want[0])
		if at < 0 || at+1 >= len(got) || got[at+1] != want[1] {
			t.Errorf("%v: want %s %s", got, want[0], want[1])
		}
	}
	// Threads unset leaves llama-server's own default alone rather than passing zero,
	// which it reads as "no threads at all".
	if plain := args(Settings{Model: "/models/x.gguf"}, 1); slices.Contains(plain, "--threads") {
		t.Errorf("%v: --threads passed for a setting nobody made", plain)
	}
}

// Only a dead server is forgotten. One that is slow or answers badly is kept: a cold
// prompt on a slow CPU outlasts the timeout, and forgetting it would respawn the server
// on every keystroke.
func TestOnlyADeadServerIsForgotten(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		answer func(release <-chan struct{}) http.HandlerFunc
		died   bool
		kept   bool
	}{
		{"slower than the timeout", func(release <-chan struct{}) http.HandlerFunc {
			return func(http.ResponseWriter, *http.Request) { <-release }
		}, false, true},
		{"an error status", func(<-chan struct{}) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, `{"error":{"message":"busy"}}`, http.StatusServiceUnavailable)
			}
		}, false, true},
		{"not JSON", func(<-chan struct{}) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>") }
		}, false, true},
		{"its process exited", func(<-chan struct{}) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "", http.StatusBadGateway) }
		}, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			release := make(chan struct{})
			ts := httptest.NewServer(c.answer(release))
			t.Cleanup(ts.Close)
			t.Cleanup(func() { close(release) }) // runs first: Close waits for the handler
			srv := &server{base: ts.URL, http: ts.Client(), now: time.Now, exited: make(chan struct{})}
			if c.died {
				close(srv.exited)
			}
			p := &Predictor{
				settings: Settings{Command: "llama-server", Model: "/m.gguf", Timeout: 50 * time.Millisecond},
				running:  srv,
			}

			if _, err := p.NextWords(t.Context(), "hello", 4); err == nil {
				t.Fatal("the ask succeeded")
			}
			p.mu.Lock()
			kept := p.running == srv
			p.mu.Unlock()
			if kept != c.kept {
				t.Errorf("server kept = %v, want %v", kept, c.kept)
			}
		})
	}
}
