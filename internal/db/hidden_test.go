package db

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// hiddenPool is the senders the generator draws from: Matrix users, a Telegram user,
// and one WhatsApp number seen natively and as a bridge puppet (invented).
var hiddenPool = []string{
	"@ann:example.org", "@bob:example.org", "@cat:example.org", "telegram:1001",
	"whatsapp:15550000001@s.whatsapp.net", "@whatsapp_15550000001:example.org",
	"@me:example.org",
}

// hiddenWorld is one generated run: steps in order, and what was marked.
type hiddenWorld struct {
	steps  []func(t *testing.T, c *Cache)
	marked []string
	groups [][]string
	names  []domain.PersonName
	links  []domain.PersonLink
	msgs   []domain.Message
}

func genHiddenWorld(rng *rand.Rand) *hiddenWorld {
	w := &hiddenWorld{}
	pick := func() string { return hiddenPool[rng.IntN(len(hiddenPool))] }
	for range rng.IntN(3) {
		w.marked = append(w.marked, pick())
	}
	if rng.IntN(2) == 0 {
		w.groups = [][]string{{pick(), pick()}}
	}
	ctx := context.Background()
	start := time.Unix(1700000000, 0)
	n := 0
	saveSome := func() {
		var batch []domain.Message
		for range 1 + rng.IntN(4) {
			msg := domain.Message{
				ID: domain.EventID(fmt.Sprintf("$%d", n)), RoomID: "!a:x", Sender: pick(),
				Body: "hello", Timestamp: start.Add(time.Duration(n) * time.Minute),
			}
			if n > 0 && rng.IntN(3) == 0 {
				msg.ReplyTo = domain.EventID(fmt.Sprintf("$%d", rng.IntN(n)))
			}
			n++
			batch = append(batch, msg)
		}
		w.msgs = append(w.msgs, batch...)
		w.steps = append(w.steps, func(t *testing.T, c *Cache) {
			t.Helper()
			if err := c.SaveMessages(ctx, "!a:x", batch); err != nil {
				t.Fatal(err)
			}
		})
	}
	link := func() {
		// Two different IDs: the cache skips a link to itself.
		id, other := pick(), pick()
		for other == id {
			other = pick()
		}
		l := domain.PersonLink{Source: "book", ID: id, Other: other}
		w.links = append(w.links, l)
		names, links := slices.Clone(w.names), slices.Clone(w.links)
		w.steps = append(w.steps, func(t *testing.T, c *Cache) {
			t.Helper()
			if err := c.SetPeople(ctx, "book", names, links); err != nil {
				t.Fatal(err)
			}
		})
	}
	mark := func() {
		w.steps = append(w.steps, func(t *testing.T, c *Cache) {
			t.Helper()
			if err := c.UseHidden(ctx, w.marked, w.groups); err != nil {
				t.Fatal(err)
			}
		})
	}
	// Marking lands anywhere among the writes; links and messages come before and after.
	actions := []func(){saveSome, saveSome, saveSome, link, link, mark}
	rng.Shuffle(len(actions), func(i, j int) { actions[i], actions[j] = actions[j], actions[i] })
	for _, a := range actions {
		a()
	}
	return w
}

// want is every message search may return: none from a hidden sender, none answering one.
func (w *hiddenWorld) want() []domain.EventID {
	people := domain.NewHiddenPeople(w.marked, w.groups, domain.NewDirectory(w.names, w.links), []string{"@me:example.org"})
	senders := map[domain.EventID]string{}
	for i := range w.msgs {
		senders[w.msgs[i].ID] = w.msgs[i].Sender
	}
	var out []domain.EventID
	for i := range w.msgs {
		if m := &w.msgs[i]; !people.Has(m.Sender) && !people.Has(senders[m.ReplyTo]) {
			out = append(out, m.ID)
		}
	}
	slices.Sort(out)
	return out
}

// Whatever order the messages, links and marks arrive in, search leaves out exactly
// the hidden senders' messages and direct replies to them, and Hides agrees.
func TestSearchLeavesOutHiddenPeopleInAnyOrder(t *testing.T) {
	t.Parallel()

	for seed := range uint64(60) {
		rng := rand.New(rand.NewPCG(seed, 0x68)) // #nosec G404 -- reproducible
		w := genHiddenWorld(rng)
		c := openTemp(t)
		c.UseSelves(func() []string { return []string{"@me:example.org"} })
		for _, step := range w.steps {
			step(t, c)
		}
		hits, err := c.SearchMessages(context.Background(), domain.SearchRequest{
			Filter: domain.ParseSearch("hello", time.Now()), Rooms: domain.EveryRoom(), Limit: 500,
		})
		if err != nil {
			t.Fatal(err)
		}
		var got []domain.EventID
		for i := range hits {
			got = append(got, hits[i].EventID)
		}
		slices.Sort(got)
		want := w.want()
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d: search found %v, want %v\nmarked %v groups %v links %v", seed, got, want, w.marked, w.groups, w.links)
		}
		for i := range w.msgs {
			hidden, err := c.Hides(context.Background(), w.msgs[i])
			if err != nil {
				t.Fatal(err)
			}
			if hidden == slices.Contains(want, w.msgs[i].ID) {
				t.Fatalf("seed %d: Hides(%s from %s) = %v", seed, w.msgs[i].ID, w.msgs[i].Sender, hidden)
			}
		}
	}
}

// Senders written while the list is rebuilt are classified by the new marks.
func TestSendersWrittenDuringARebuildAreClassified(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	c := openTemp(t)
	if err := c.SaveMessages(ctx, "!a:x", []domain.Message{{ID: "$seed", RoomID: "!a:x", Sender: "@ann:example.org", Body: "hello", Timestamp: time.Unix(1700000000, 0)}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			sender := fmt.Sprintf("@spam%d:example.org", i)
			msg := domain.Message{ID: domain.EventID(fmt.Sprintf("$%d", i)), RoomID: "!a:x", Sender: sender, Body: "hello", Timestamp: time.Unix(1700000100+int64(i), 0)}
			if err := c.SaveMessages(ctx, "!a:x", []domain.Message{msg}); err != nil {
				t.Error(err)
			}
		})
	}
	var marked []string
	for i := range 20 {
		marked = append(marked, fmt.Sprintf("@spam%d:example.org", i))
	}
	wg.Go(func() {
		if err := c.UseHidden(ctx, marked, nil); err != nil {
			t.Error(err)
		}
	})
	wg.Wait()
	hidden := c.hiddenSenders()
	slices.Sort(hidden)
	slices.Sort(marked)
	if !slices.Equal(hidden, marked) {
		t.Errorf("hidden senders %v, want every marked one", hidden)
	}
}

// From: completion offers nobody hidden.
func TestSenderCompletionLeavesOutHiddenPeople(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	c := openTemp(t)
	if err := c.SaveMessages(ctx, "!a:x", []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@ann:example.org", Body: "hi", Timestamp: time.Unix(1700000000, 0)},
		{ID: "$2", RoomID: "!a:x", Sender: "@bob:example.org", Body: "buy", Timestamp: time.Unix(1700000060, 0)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.UseHidden(ctx, []string{"@bob:example.org"}, nil); err != nil {
		t.Fatal(err)
	}
	members, err := c.SearchSenders(ctx, domain.EveryRoom(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].UserID != "@ann:example.org" {
		t.Errorf("offered %v, want Ann alone", members)
	}
}
