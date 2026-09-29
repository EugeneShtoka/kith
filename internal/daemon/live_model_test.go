package daemon_test

// An opt-in probe against a real, running kithd with the local completion model
// installed.

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestLiveLocalModel(t *testing.T) {
	user := os.Getenv("KITH_LIVE")
	if user == "" {
		t.Skip("KITH_LIVE not set")
	}
	ctx := context.Background()
	r, _ := attachLive(t, ctx, user)

	rooms, err := r.Rooms(ctx)
	if err != nil {
		t.Fatalf("Rooms: %v", err)
	}
	if len(rooms) == 0 {
		t.Fatal("no rooms: the cache is not delivering")
	}
	room := rooms[0].ID

	// What the daemon thinks there is to offer.
	found, err := r.DetectModel(ctx)
	if err != nil {
		t.Fatalf("DetectModel: %v", err)
	}
	t.Logf("DETECT offer=%t why=%q candidate=%+v", found.Offer, found.Why, found.Candidate)

	// A draft ending in a space, which is what the composer hands over at a word boundary.
	const draft = "I think we should check the "
	start := time.Now()
	result, err := r.ModelTask(ctx, domain.ModelRequest{
		Task: domain.ModelComplete, RoomID: room, Draft: draft,
	})
	took := time.Since(start)
	if err != nil {
		t.Fatalf("ModelTask: %v", err)
	}
	t.Logf("COMPLETE in %s endpoint=%q model=%q refusal=%q options=%v",
		took.Round(time.Millisecond), result.Endpoint, result.Model,
		result.Refusal, result.Options)

	if result.Refusal != "" {
		t.Fatalf("the daemon refused inside its sandbox: %s", result.Refusal)
	}
	if result.Endpoint != "this machine" {
		t.Fatalf("answered by %q rather than by the model on this machine", result.Endpoint)
	}
	if len(result.Options) == 0 {
		t.Fatal("the local model answered with nothing")
	}
	for _, option := range result.Options {
		if strings.ContainsAny(option, " \t\n") {
			t.Errorf("%q is not one word", option)
		}
	}

	// And then the number the composer's debounce is derived from.
	warm := warmRoundTrip(t, ctx, r, room)
	t.Logf("ROUND TRIP over %d warm asks: p50=%s p90=%s max=%s",
		len(warm), warm[len(warm)/2].Round(time.Millisecond),
		warm[len(warm)*9/10].Round(time.Millisecond),
		warm[len(warm)-1].Round(time.Millisecond))

	// The bar the debounce was sized against.
	if p50 := warm[len(warm)/2]; p50 > 200*time.Millisecond {
		t.Errorf("p50 round trip is %s — too slow for the debounce this client uses", p50)
	}
}

// warmRoundTrip times a completion for each of ten drafts, twice over, and returns the
// timings sorted.
func warmRoundTrip(t *testing.T, ctx context.Context, r *daemon.Remote, room domain.RoomID) []time.Duration {
	t.Helper()
	drafts := []string{
		"I think we should check the ", "let us meet ", "the deploy is ",
		"sorry, I was ", "can you send me the ", "thanks for the ",
		"I will be there in ", "have merged, will follow ", "that sounds ",
		"we need to talk about the ",
	}
	var took []time.Duration
	for range 2 {
		for _, draft := range drafts {
			start := time.Now()
			if _, err := r.ModelTask(ctx, domain.ModelRequest{
				Task: domain.ModelComplete, RoomID: room, Draft: draft,
			}); err != nil {
				t.Fatalf("%q: %v", draft, err)
			}
			took = append(took, time.Since(start))
		}
	}
	slices.Sort(took)
	return took
}
