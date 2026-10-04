package slack

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A listing is fetched, then written; a message can arrive between the two, in a
// conversation the listing did not know of yet (a channel joined, a DM begun). Over
// random interleavings of fetches, writes, messages and leaving: a room with a message
// cached after a listing was fetched keeps it when that listing is written, and a
// room the person left is swept by the first listing fetched after it was last heard.
func TestAListingNeverSweepsAMessageItDidNotKnowOf(t *testing.T) {
	t.Parallel()
	for seed := range uint64(25) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			listingRace(t, rand.New(rand.NewPCG(seed, 7)))
		})
	}
}

// fetchedListing is a listing in flight: what was joined when it was fetched.
type fetchedListing struct {
	joined []string
	at     time.Time
}

func listingRace(t *testing.T, rng *rand.Rand) {
	t.Helper()
	ctx := context.Background()
	work := Account{Name: "work", Workspace: "acme"}
	a, _ := cached(t, work)
	w := newWorkspace(work, Credentials{Team: "T1", User: "U1"}, "Acme", nil, 0)
	channels := []string{"C1", "C2", "C3", "C4", "C5"}
	joined := map[string]bool{"C1": true}
	lastHeard := map[string]time.Time{}
	sent := map[domain.EventID]string{} // message → its channel
	var inFlight []fetchedListing
	for step := range 60 {
		switch rng.IntN(4) {
		case 0: // fetch a listing
			var names []string
			for _, c := range channels {
				if joined[c] {
					names = append(names, c)
				}
			}
			inFlight = append(inFlight, fetchedListing{joined: names, at: time.Now()})
		case 1: // write the oldest listing in flight
			if len(inFlight) == 0 {
				continue
			}
			l := inFlight[0]
			inFlight = inFlight[1:]
			var convs []slackgo.Channel
			for _, c := range l.joined {
				convs = append(convs, conversation(c, func(ch *slackgo.Channel) { ch.Name = c }))
			}
			if err := a.save(ctx, w, listed("T1", "Acme", "me", convs, nil), l.at); err != nil {
				t.Fatal(err)
			}
			checkAfterListing(ctx, t, a, l, lastHeard, sent, channels)
		case 2: // a message arrives, in a conversation one is now in
			c := channels[rng.IntN(len(channels))]
			joined[c] = true
			msg := domain.Message{
				ID: messageID("T1", c, fmt.Sprintf("%d.0", step)), RoomID: roomID("T1", c),
				Body: "hi", Timestamp: time.Unix(int64(step), 0),
			}
			if _, ok := a.record(ctx, w, msg.RoomID, []domain.Message{msg}); !ok {
				t.Fatal("record failed")
			}
			lastHeard[c] = time.Now()
			sent[msg.ID] = c
		case 3: // leave a conversation; never C1, as no one leaves every one (the cache
			// sweeps nothing on an empty listing, by design)
			delete(joined, channels[1+rng.IntN(len(channels)-1)])
		}
	}
}

// checkAfterListing holds a listing written to what it promises, and every room it
// kept to the messages cached in it.
func checkAfterListing(ctx context.Context, t *testing.T, a *Adapter, l fetchedListing, lastHeard map[string]time.Time, sent map[domain.EventID]string, channels []string) {
	t.Helper()
	rooms, err := a.Rooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cached := map[string]bool{}
	for i := range rooms {
		cached[domain.ParseID(string(rooms[i].ID)).Native] = true
	}
	for _, c := range channels {
		heardSince := !lastHeard[c].IsZero() && !lastHeard[c].Before(l.at)
		listed := slices.Contains(l.joined, c)
		switch {
		case (listed || heardSince) && !cached[c]:
			t.Errorf("%s (listed %v, heard since the fetch %v) was swept", c, listed, heardSince)
		case !listed && !heardSince && cached[c]:
			t.Errorf("%s was neither listed nor heard since the fetch, and was kept", c)
		}
	}
	for id, c := range sent {
		if !cached[c] {
			delete(sent, id) // left, and swept with its room: a later message rejoins it afresh
			continue
		}
		if _, found, err := a.cache.MessageByID(ctx, roomID("T1", c), id); err != nil || !found {
			t.Errorf("%s is cached, but its message %s is gone (%v)", c, id, err)
		}
	}
}
