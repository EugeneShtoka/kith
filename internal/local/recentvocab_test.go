package local

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

const vocabMe = "@me:x"

// completing is a backend with a cache holding a few rooms' messages.
func completing(t *testing.T) *Service {
	t.Helper()
	b := backendWithCache(t, vocabMe)
	ctx := context.Background()
	if err := b.cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!a:x"}, {ID: "!b:x"}}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	msgs := []domain.Message{
		{RoomID: "!a:x", Sender: "@dana:x", Body: "the deployment is done"},
		{RoomID: "!a:x", Sender: "@dana:x", Body: "deployment went fine"},
		{RoomID: "!b:x", Sender: "@sam:x", Body: "deploying now, deploying again"},
	}
	for i := range msgs {
		msgs[i].ID = domain.EventID(fmt.Sprintf("$m%d", i))
		msgs[i].Timestamp = at.Add(time.Duration(i) * time.Minute)
		if err := b.cache.SaveMessages(ctx, msgs[i].RoomID, msgs[i:i+1]); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

func complete(t *testing.T, b *Service, prefix string) []string {
	t.Helper()
	got, err := b.CompleteWord(context.Background(), domain.CompleteRequest{
		Prefix: prefix, RoomIDs: []domain.RoomID{"!a:x"}, SpaceRooms: []domain.RoomID{"!a:x", "!b:x"},
		Scope: "room", Sources: []string{sourceHistory}, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(got))
	for i, c := range got {
		out[i] = c.Word
	}
	return out
}

// This room's words outrank a sibling's, even used less often.
func TestCompletionWeighsThisRoomOverItsSpace(t *testing.T) {
	t.Parallel()
	b := completing(t)
	// deployment: 2 in the room (20) + 2 in the space (6) + 2 anywhere = 28;
	// deploying: 2 in the space (6) + 2 anywhere = 8.
	if got := complete(t, b, "dep"); !slices.Equal(got, []string{"deployment", "deploying"}) {
		t.Errorf("completions = %v", got)
	}
}

// A message arriving after the windows were built counts at once; a redaction takes its
// words back out.
func TestCompletionFollowsLiveMessagesAndRedactions(t *testing.T) {
	t.Parallel()
	b := completing(t)
	ctx := context.Background()
	_ = complete(t, b, "dep") // builds the windows

	live := domain.Message{ID: "$live", RoomID: "!a:x", Sender: "@dana:x", Body: "depends on the depot",
		Timestamp: time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)}
	if err := b.cache.SaveMessages(ctx, live.RoomID, []domain.Message{live}); err != nil {
		t.Fatal(err)
	}
	b.MessageCached(live)
	if got := complete(t, b, "dep"); !slices.Contains(got, "depends") {
		t.Fatalf("a live message's word is missing: %v", got)
	}

	if err := b.cache.MarkRedacted(ctx, "!a:x", "$live", "@dana:x", "", time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	b.RoomChanged("!a:x")
	if got := complete(t, b, "dep"); slices.Contains(got, "depends") {
		t.Errorf("a redacted message's word is still offered: %v", got)
	}
}

// Completions from RPC goroutines race messages from the sync goroutine.
func TestCompletionRacesIncomingMessages(t *testing.T) {
	t.Parallel()
	b := completing(t)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			for range 20 {
				_ = complete(t, b, "dep")
			}
		})
		wg.Go(func() {
			for j := range 20 {
				b.vocab.added(domain.Message{
					ID: domain.EventID(fmt.Sprintf("$r%d-%d", i, j)), RoomID: "!a:x", Sender: vocabMe, Body: "deploy it",
				}, vocabMe)
				if j%7 == 0 {
					b.vocab.changed("!b:x")
				}
			}
		})
	}
	wg.Wait()
}

// The scope decides which terms count: at "room" a sibling's word beats a distant one;
// at "global" only the plain count is left, and the tie goes alphabetically.
func TestCompletionScopeWidensAndNarrows(t *testing.T) {
	t.Parallel()
	b := backendWithCache(t, vocabMe)
	ctx := context.Background()
	if err := b.cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!here:x"}, {ID: "!sib:x"}, {ID: "!far:x"}}); err != nil {
		t.Fatal(err)
	}
	for i, m := range []domain.Message{
		{RoomID: "!sib:x", Sender: "@dana:x", Body: "parapet"},
		{RoomID: "!far:x", Sender: "@erin:x", Body: "paragon"},
	} {
		m.ID, m.Timestamp = domain.EventID(fmt.Sprintf("$s%d", i)), time.Unix(int64(1_700_000_000+i), 0)
		if err := b.cache.SaveMessages(ctx, m.RoomID, []domain.Message{m}); err != nil {
			t.Fatal(err)
		}
	}
	ask := func(scope string) []string {
		got, err := b.CompleteWord(ctx, domain.CompleteRequest{
			Prefix: "par", RoomIDs: []domain.RoomID{"!here:x"}, SpaceRooms: []domain.RoomID{"!here:x", "!sib:x"},
			Scope: scope, Sources: []string{sourceHistory}, Limit: 5,
		})
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(got))
		for i, c := range got {
			out[i] = c.Word
		}
		return out
	}
	if got := ask("room"); !slices.Equal(got, []string{"parapet", "paragon"}) {
		t.Errorf("room scope = %v, want the space's word first", got)
	}
	if got := ask("global"); !slices.Equal(got, []string{"paragon", "parapet"}) {
		t.Errorf("global scope = %v, want the place terms gone", got)
	}
}
