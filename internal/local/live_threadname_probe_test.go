package local_test

// An opt-in probe that names real threads from a real cache, to judge name quality by eye.
//
// It is skipped unless KITH_CACHE names a file, so `make check` never runs it.
//
// **Two modes, and the first one sends nothing.**
//
//	# What would be sent, assembled from the real cache and not transmitted:
//	KITH_CACHE=~/.local/share/kith/cache-<id>.db \
//	  go test ./internal/matrix/ -run TestLiveThreadNames -v
//
//	# And for real, against a model. A local one sends nothing off the machine:
//	llama-server -m ~/.local/share/kith/models/smollm2-360m.gguf --port 8081 &
//	KITH_CACHE=~/.local/share/kith/cache-<id>.db \
//	  KITH_MODEL_ENDPOINT=http://127.0.0.1:8081/v1/chat/completions \
//	  KITH_MODEL_NAME=smollm2 \
//	  go test ./internal/matrix/ -run TestLiveThreadNames -v
//
// It reads a **copy** of the cache, never the file a running daemon has open.
//
// **What it prints.** By default: the name, and the *shape* of what was sent — how many
// messages, how many characters — but not the conversation. Set KITH_SHOW_PROMPT=1 to print
// the prompt itself, which is real messages from real rooms and is the thing to be careful
// with. The dry-run mode exists because you should be able to see exactly what would leave
// the machine before anything does.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/local"
	"github.com/EugeneShtoka/kith/internal/modelsetup"
	"github.com/EugeneShtoka/kith/internal/session"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// liveThreadCount is how many threads to name. Small: each one is a request.
const liveThreadCount = 3

func TestLiveThreadNames(t *testing.T) {
	path := os.Getenv("KITH_CACHE")
	if path == "" {
		t.Skip("KITH_CACHE not set")
	}
	ctx := context.Background()

	// A copy: Open migrates, and must not touch the running daemon's cache.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	copied := filepath.Join(t.TempDir(), "cache.db")
	if werr := os.WriteFile(copied, data, 0o600); werr != nil {
		t.Fatalf("copy the cache: %v", werr)
	}
	cache, err := db.Open(ctx, copied)
	if err != nil {
		t.Fatalf("open the copy: %v", err)
	}
	defer func() { _ = cache.Close() }()

	threads := liveThreadsWorthNaming(ctx, t, cache)
	if len(threads) == 0 {
		t.Skip("no thread in this cache has enough replies to be worth naming")
	}

	// The real configuration when KITH_CONFIG names one.
	assist := config.Assist{}
	switch path := os.Getenv("KITH_CONFIG"); {
	case path != "":
		cfg, cerr := config.Load(path)
		if cerr != nil {
			t.Fatalf("load %s: %v", path, cerr)
		}
		assist = cfg.Assist
		t.Logf("from %s: endpoint=%q model=%q rooms=%v except=%v",
			path, assist.Endpoint, assist.Model, assist.Rooms, assist.Except)
		t.Logf("note: [assist] encrypted=%v is NOT applied here — it needs the Matrix "+
			"client's state store, which this harness has no connection to", assist.Encrypted)
	default:
		assist = config.Assist{
			Endpoint: os.Getenv("KITH_MODEL_ENDPOINT"), Model: os.Getenv("KITH_MODEL_NAME"),
			Encrypted: true,
		}
	}
	// Sending is opt-in via KITH_SEND, separately from the endpoint.
	dry := os.Getenv("KITH_SEND") == ""
	if dry && assist.Endpoint == "" {
		// The endpoint check runs before the dry-run branch, so give it a placeholder.
		assist.Endpoint, assist.Model = "https://dry-run.invalid/v1/chat/completions", "dry-run"
	}
	if dry {
		t.Log("dry run — assembling the requests and sending nothing (KITH_SEND=1 to ask for real)")
	}
	endpoint, model := assist.Endpoint, assist.Model
	backend := local.New(cache, nil)
	key := ""
	if !dry {
		key, _ = session.Secret(assist.KeyRef)
	}
	backend.UseModel(local.ModelSettings{
		Endpoint: endpoint,
		Model:    model,
		Key:      key,
		Scope: domain.ModelScope{
			Only: assist.Rooms, Except: assist.Except,
			// The encrypted gate needs the client's state store, which this harness lacks.
			Encrypted: true,
		},
		Budget:    assist.BudgetOrDefault(),
		Budgets:   setup.ModelBudgets(assist, config.CompleteModel{}),
		Tasks:     modelsetup.ModelTasks(assist),
		PerMinute: config.DefaultModelPerMinute,
		// KITH_MODEL_TIMEOUT overrides the (remote-sized) default for local models.
		Timeout: probeTimeout(t, assist),
	})

	for _, th := range threads {
		result, rerr := backend.ModelTask(ctx, domain.ModelRequest{
			Task:    domain.ModelThreadName,
			RoomID:  th.room,
			ReplyTo: th.root,
			DryRun:  dry,
		})
		switch {
		case rerr != nil:
			t.Errorf("%s: %v", th.root, rerr)
			continue
		case result.Refusal != "":
			t.Logf("%s (%d replies): refused — %s", short(th.root), th.replies, result.Refusal)
			continue
		}
		if dry {
			reportPrompt(t, th, result.Text)
			continue
		}
		name := domain.ThreadNameFrom(result.Text)
		t.Logf("%s (%d replies)\n    raw:   %q\n    name:  %q  [%d runes]",
			short(th.root), th.replies, result.Text, name, len([]rune(name)))
		if name == "" {
			t.Errorf("%s: the model answered %q and nothing usable came out of it", th.root, result.Text)
		}
		if len([]rune(name)) > domain.ThreadNameLimit {
			t.Errorf("%s: name is %d runes, over the %d limit", th.root, len([]rune(name)), domain.ThreadNameLimit)
		}
	}
}

// reportPrompt says what would have been sent, in as much detail as was asked for.
func reportPrompt(t *testing.T, th liveThread, prompt string) {
	t.Helper()
	lines := strings.Count(prompt, "\n") + 1
	t.Logf("%s (%d replies): would send %d chars over %d lines",
		short(th.root), th.replies, len(prompt), lines)
	if os.Getenv("KITH_SHOW_PROMPT") == "" {
		t.Log("    (set KITH_SHOW_PROMPT=1 to print it — it is real conversation)")
		return
	}
	for line := range strings.SplitSeq(prompt, "\n") {
		t.Log("    | " + line)
	}
}

// probeTimeout is the assist timeout, or KITH_MODEL_TIMEOUT when the probe is told one.
func probeTimeout(t *testing.T, assist config.Assist) time.Duration {
	t.Helper()
	spelled := os.Getenv("KITH_MODEL_TIMEOUT")
	if spelled == "" {
		return assist.TimeoutOrDefault()
	}
	d, err := time.ParseDuration(spelled)
	if err != nil {
		t.Fatalf("KITH_MODEL_TIMEOUT=%q: %v", spelled, err)
	}
	return d
}

type liveThread struct {
	room    domain.RoomID
	root    domain.EventID
	replies int
}

// liveThreadsWorthNaming is the busiest threads in the cache that have no name.
func liveThreadsWorthNaming(ctx context.Context, t *testing.T, cache *db.Cache) []liveThread {
	t.Helper()
	rooms, err := cache.Rooms(ctx)
	if err != nil {
		t.Fatalf("read rooms: %v", err)
	}
	var found []liveThread
	for i := range rooms {
		threads, terr := cache.Threads(ctx, nil, rooms[i].ID)
		if terr != nil {
			continue
		}
		for j := range threads {
			if threads[j].Count >= config.DefaultNameThreadsAfter {
				found = append(found, liveThread{
					room: rooms[i].ID, root: threads[j].Root, replies: threads[j].Count,
				})
			}
		}
	}
	for i := range found {
		for j := i + 1; j < len(found); j++ {
			if found[j].replies > found[i].replies {
				found[i], found[j] = found[j], found[i]
			}
		}
	}
	if len(found) > liveThreadCount {
		found = found[:liveThreadCount]
	}
	return found
}

// short is an event ID trimmed to something readable in a log line.
func short(id domain.EventID) string {
	s := string(id)
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return fmt.Sprint(s)
}
