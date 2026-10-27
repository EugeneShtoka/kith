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
	"matrix:@ann:example.org", "matrix:@bob:example.org", "matrix:@cat:example.org", "telegram:1001",
	"whatsapp:15550000001@s.whatsapp.net", "matrix:@whatsapp_15550000001:example.org",
	hiddenMe,
}

// hiddenWorld is one generated run: steps in order, and what was marked.
type hiddenWorld struct {
	steps  []func(t *testing.T, c *Cache)
	marked []string
	groups [][]string
	// verdicts is the person verdicts as they stand after every step.
	verdicts []domain.PersonVerdict
	names    []domain.PersonName
	links    []domain.PersonLink
	msgs     []domain.Message
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
				ID: domain.EventID(fmt.Sprintf(mx+"$%d", n)), RoomID: mx + "!a:x", Sender: pick(),
				Body: "hello", Timestamp: start.Add(time.Duration(n) * time.Minute),
			}
			if n > 0 && rng.IntN(3) == 0 {
				msg.ReplyTo = domain.EventID(fmt.Sprintf(mx+"$%d", rng.IntN(n)))
			}
			if n > 0 && rng.IntN(4) == 0 {
				msg.ThreadRoot = domain.EventID(fmt.Sprintf(mx+"$%d", rng.IntN(n)))
			}
			msg.Mentioned = rng.IntN(3) == 0
			n++
			batch = append(batch, msg)
		}
		w.msgs = append(w.msgs, batch...)
		w.steps = append(w.steps, func(t *testing.T, c *Cache) {
			t.Helper()
			if err := c.SaveMessages(ctx, mx+"!a:x", batch); err != nil {
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
			if err := c.UseHidden(ctx, w.marked, hiddenMuted, w.groups); err != nil {
				t.Fatal(err)
			}
		})
	}
	// verdict is a filter's mark into the muted tag or another, or a release; a mark
	// over a release is refused, as it is everywhere.
	verdict := func() {
		v := domain.PersonVerdict{Person: pick(), Tag: []string{"spam", "work"}[rng.IntN(2)], At: start}
		if rng.IntN(3) == 0 {
			v.Released = true
		} else {
			v.Rule, v.Filter = domain.MoveAlways, "crypto"
		}
		set, err := domain.WithPersonVerdict(w.verdicts, v)
		if err == nil {
			w.verdicts = set
		}
		w.steps = append(w.steps, func(t *testing.T, c *Cache) {
			t.Helper()
			if got := c.MarkPerson(ctx, v); (got != nil) != (err != nil) {
				t.Fatalf("MarkPerson(%+v) = %v, want %v", v, got, err)
			}
		})
	}
	// Marking lands anywhere among the writes; links, verdicts and messages come before
	// and after.
	actions := []func(){saveSome, saveSome, saveSome, link, link, mark, verdict, verdict, verdict}
	rng.Shuffle(len(actions), func(i, j int) { actions[i], actions[j] = actions[j], actions[i] })
	for _, a := range actions {
		a()
	}
	return w
}

// visible reports whether a message is shown: not from a hidden sender, not answering one.
func (w *hiddenWorld) visible() func(m *domain.Message) bool {
	marked := slices.Concat(w.marked, domain.HiddenByVerdict(w.verdicts, hiddenMuted))
	people := domain.NewHiddenPeople(marked, w.groups, domain.NewDirectory(w.names, w.links), []string{hiddenMe})
	senders := map[domain.EventID]string{}
	for i := range w.msgs {
		senders[w.msgs[i].ID] = w.msgs[i].Sender
	}
	return func(m *domain.Message) bool { return !people.Has(m.Sender) && !people.Has(senders[m.ReplyTo]) }
}

// hiddenMuted is the generator's muted tag: Spam hides, Work does not.
var hiddenMuted = []string{"Spam"}

// hiddenMe is the one of hiddenPool that is you.
const hiddenMe = "matrix:@me:example.org"

// want is every message search may return: none from a hidden sender, none answering one.
func (w *hiddenWorld) want() []domain.EventID {
	visible := w.visible()
	var out []domain.EventID
	for i := range w.msgs {
		if visible(&w.msgs[i]) {
			out = append(out, w.msgs[i].ID)
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
		c.UseSelves(func() []string { return []string{"matrix:@me:example.org"} })
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

// counts is what the badge should say: unread main-timeline messages and mentions
// after the read position and your newest message, and the threads' unread replies
// after your newest reply in each, none hidden and none yours.
func (w *hiddenWorld) counts(readMS int64) (messages, mentions, threaded, threadMentions int) {
	visible := w.visible()
	floor := map[domain.EventID]int64{"": readMS} // by thread root; "" is the main timeline
	for i := range w.msgs {
		if m := &w.msgs[i]; m.Sender == hiddenMe {
			floor[m.ThreadRoot] = max(floor[m.ThreadRoot], m.Timestamp.UnixMilli())
		}
	}
	for i := range w.msgs {
		m := &w.msgs[i]
		if m.Sender == hiddenMe || !visible(m) || m.Timestamp.UnixMilli() <= floor[m.ThreadRoot] {
			continue
		}
		if m.ThreadRoot == "" {
			messages++
			if m.Mentioned {
				mentions++
			}
			continue
		}
		threaded++
		if m.Mentioned {
			threadMentions++
		}
	}
	return messages, mentions, threaded, threadMentions
}

// Whatever order the messages, links and marks arrive in, the badge counts leave out
// exactly the hidden senders' messages and direct replies to them, threads included.
func TestUnreadCountsLeaveOutHiddenPeopleInAnyOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	me := []string{hiddenMe}
	for seed := range uint64(60) {
		rng := rand.New(rand.NewPCG(seed, 0x75)) // #nosec G404 -- reproducible
		w := genHiddenWorld(rng)
		c := openTemp(t)
		c.UseSelves(func() []string { return me })
		// Read up to some message, or to just before the first.
		readMS := time.Unix(1700000000, 0).Add(time.Duration(rng.IntN(len(w.msgs)+1)-1) * time.Minute).UnixMilli()
		if err := c.SaveUnread(ctx, domain.Unread{RoomID: mx + "!a:x"}, readMS); err != nil {
			t.Fatal(err)
		}
		for _, step := range w.steps {
			step(t, c)
		}
		wantMsgs, wantMentions, wantThreaded, wantThreadMentions := w.counts(readMS)
		messages, mentions, counted, err := c.CountUnread(ctx, me, mx+"!a:x")
		if err != nil || !counted {
			t.Fatalf("seed %d: CountUnread counted %v, %v", seed, counted, err)
		}
		all, err := c.CountUnreadAll(ctx, me)
		if err != nil {
			t.Fatal(err)
		}
		threads, err := c.CountThreadUnread(ctx, me, mx+"!a:x")
		if err != nil {
			t.Fatal(err)
		}
		var threaded, threadMentions int
		for _, th := range threads {
			threaded += th.Unread
			threadMentions += th.Mentions
		}
		where := fmt.Sprintf("seed %d: marked %v groups %v links %v", seed, w.marked, w.groups, w.links)
		if messages != wantMsgs || mentions != wantMentions || all[mx+"!a:x"].Messages != wantMsgs || all[mx+"!a:x"].Mentions != wantMentions {
			t.Fatalf("%s: counted %d (%d mentions), all %+v; want %d (%d)", where, messages, mentions, all[mx+"!a:x"], wantMsgs, wantMentions)
		}
		if threaded != wantThreaded || threadMentions != wantThreadMentions {
			t.Fatalf("%s: threads count %d (%d mentions), want %d (%d)", where, threaded, threadMentions, wantThreaded, wantThreadMentions)
		}
	}
}

// Senders written while the list is rebuilt are classified by the new marks.
func TestSendersWrittenDuringARebuildAreClassified(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	c := openTemp(t)
	if err := c.SaveMessages(ctx, mx+"!a:x", []domain.Message{{ID: mx + "$seed", RoomID: mx + "!a:x", Sender: "matrix:@ann:example.org", Body: "hello", Timestamp: time.Unix(1700000000, 0)}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			sender := fmt.Sprintf("matrix:@spam%d:example.org", i)
			msg := domain.Message{ID: domain.EventID(fmt.Sprintf(mx+"$%d", i)), RoomID: mx + "!a:x", Sender: sender, Body: "hello", Timestamp: time.Unix(1700000100+int64(i), 0)}
			if err := c.SaveMessages(ctx, mx+"!a:x", []domain.Message{msg}); err != nil {
				t.Error(err)
			}
		})
	}
	var marked []string
	for i := range 20 {
		marked = append(marked, fmt.Sprintf("matrix:@spam%d:example.org", i))
	}
	wg.Go(func() {
		if err := c.UseHidden(ctx, marked, nil, nil); err != nil {
			t.Error(err)
		}
	})
	wg.Wait()
	hidden := c.hiddenSenders(ctx)
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
	if err := c.SaveMessages(ctx, mx+"!a:x", []domain.Message{
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@ann:example.org", Body: "hi", Timestamp: time.Unix(1700000000, 0)},
		{ID: mx + "$2", RoomID: mx + "!a:x", Sender: "matrix:@bob:example.org", Body: "buy", Timestamp: time.Unix(1700000060, 0)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.UseHidden(ctx, []string{"matrix:@bob:example.org"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	members, err := c.SearchSenders(ctx, domain.EveryRoom(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].UserID != "matrix:@ann:example.org" {
		t.Errorf("offered %v, want Ann alone", members)
	}
}

// A rebuild that changes who is hidden names the rooms they wrote in, whether they
// were marked, linked to a marked person, or let go; one changing nothing is silent.
func TestAChangeOfWhoIsHiddenNamesTheirRooms(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	c := openTemp(t)
	c.UseSelves(func() []string { return []string{hiddenMe} })
	for i, m := range []struct {
		room   domain.RoomID
		sender string
	}{{mx + "!a:x", "matrix:@spam:example.org"}, {mx + "!b:x", "matrix:@ann:example.org"}, {mx + "!c:x", hiddenMe}, {mx + "!d:x", "matrix:@bob:example.org"}} {
		msg := domain.Message{ID: domain.EventID(fmt.Sprintf(mx+"$%d", i)), RoomID: m.room, Sender: m.sender, Body: "hello", Timestamp: time.Unix(1700000000+int64(i), 0)}
		if err := c.SaveMessages(ctx, m.room, []domain.Message{msg}); err != nil {
			t.Fatal(err)
		}
	}
	var heard [][]domain.RoomID
	c.OnHiddenChange(func(_ context.Context, rooms []domain.RoomID) {
		heard = append(heard, slices.Sorted(slices.Values(rooms)))
	})
	expect := func(step string, want ...domain.RoomID) {
		t.Helper()
		switch {
		case len(want) == 0 && len(heard) != 0:
			t.Errorf("%s: heard %v, want nothing", step, heard)
		case len(want) > 0 && (len(heard) != 1 || !slices.Equal(heard[0], want)):
			t.Errorf("%s: heard %v, want %v once", step, heard, want)
		}
		heard = nil
	}

	if err := c.UseHidden(ctx, []string{"matrix:@spam:example.org"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	expect("marking", mx+"!a:x")
	if err := c.UseHidden(ctx, []string{"matrix:@spam:example.org", hiddenMe}, nil, nil); err != nil {
		t.Fatal(err)
	}
	expect("marking yourself too")
	if err := c.SetPeople(ctx, "book", nil, []domain.PersonLink{{Source: "book", ID: "matrix:@ann:example.org", Other: "matrix:@spam:example.org"}}); err != nil {
		t.Fatal(err)
	}
	expect("linking Ann to the marked", mx+"!b:x")
	if err := c.UseHidden(ctx, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	expect("letting go", mx+"!a:x", mx+"!b:x")
}
