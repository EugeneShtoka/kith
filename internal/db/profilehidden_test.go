package db

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// profileWorld is a hiddenWorld with profiles that hide more people on top: each
// exclusion names IDs and, standing in for phonebook cards, a set of IDs whose person
// has a card the profile excludes. Exclusions change, cards are edited and removed, and
// profiles go, anywhere among the writes, links and marks.
type profileWorld struct {
	*hiddenWorld
	// excluded is each profile's exclusion as the last UseProfileHidden left it.
	excluded map[string]profileExclusion
}

// profileExclusion is one profile's people.exclude: IDs, and the IDs a card meeting
// one of its fields names (nil: no field).
type profileExclusion struct {
	ids  []string
	card []string
}

// people is the exclusion as the cache takes it.
func (e profileExclusion) people() domain.ExcludedPeople {
	out := domain.ExcludedPeople{IDs: e.ids}
	if e.card != nil {
		card := slices.Clone(e.card)
		out.Card = func(ids []string) bool {
			return slices.ContainsFunc(ids, func(id string) bool { return slices.Contains(card, id) })
		}
	}
	return out
}

// profileNames are the profiles the generator writes; "gone" is never given one, so it
// reads as everyone's hidden people alone, as a profile a reload took away does.
var profileNames = []string{"home", "work"}

func genProfileWorld(rng *rand.Rand) *profileWorld {
	w := &profileWorld{hiddenWorld: genHiddenWorld(rng)}
	pick := func() string { return hiddenPool[rng.IntN(len(hiddenPool))] }
	ctx := context.Background()
	// Each change lands somewhere among the writes, in the order made, so the last made
	// is the one in force.
	changes := 1 + rng.IntN(3)
	at := make([]int, changes)
	for i := range at {
		at[i] = rng.IntN(len(w.steps) + 1)
	}
	slices.Sort(at)
	for i := range changes {
		set := map[string]profileExclusion{}
		for _, name := range profileNames {
			if rng.IntN(4) == 0 {
				continue // this profile hides nobody more, or was removed
			}
			var e profileExclusion
			for range rng.IntN(3) {
				e.ids = append(e.ids, pick())
			}
			if rng.IntN(2) == 0 {
				e.card = []string{} // a field no card meets yet
				for range 1 + rng.IntN(2) {
					e.card = append(e.card, pick()) // you among them, sometimes
				}
			}
			set[name] = e
		}
		w.excluded = set
		people := make(map[string]domain.ExcludedPeople, len(set))
		for name, e := range set {
			people[name] = e.people()
		}
		w.steps = slices.Insert(w.steps, at[i]+i, func(t *testing.T, c *Cache) {
			t.Helper()
			if err := c.UseProfileHidden(ctx, people); err != nil {
				t.Fatal(err)
			}
		})
	}
	return w
}

// visibleIn reports whether a message shows in profile: not from someone everyone
// hides or the profile hides, not answering one; you never hidden.
func (w *profileWorld) visibleIn(profile string) func(m *domain.Message) bool {
	shared := w.visible()
	dir := domain.NewDirectory(w.names, w.links)
	here := domain.NewExcludedPeople(w.excluded[profile].people(), w.groups, dir, []string{hiddenMe})
	senders := map[domain.EventID]string{}
	for i := range w.msgs {
		senders[w.msgs[i].ID] = w.msgs[i].Sender
	}
	return func(m *domain.Message) bool {
		return shared(m) && !here.Has(m.Sender) && !here.Has(senders[m.ReplyTo])
	}
}

// wantIn is every message search may return in profile.
func (w *profileWorld) wantIn(profile string) []domain.EventID {
	visible := w.visibleIn(profile)
	var out []domain.EventID
	for i := range w.msgs {
		if visible(&w.msgs[i]) {
			out = append(out, w.msgs[i].ID)
		}
	}
	slices.Sort(out)
	return out
}

// countsIn is hiddenWorld.counts as profile counts.
func (w *profileWorld) countsIn(profile string, readMS int64) (messages, mentions, threaded, threadMentions int) {
	visible := w.visibleIn(profile)
	floor := map[domain.EventID]int64{"": readMS}
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

// Whatever order the messages, links, marks and each profile's exclusions arrive in,
// and however those exclusions change, a request in a profile reads search, Hides,
// unread and thread counts and sender completion without the people that profile
// hides, a request in no profile (or one gone) without everyone's hidden people alone,
// and you are never hidden.
func TestAProfileHidesItsPeopleEverywhereInAnyOrder(t *testing.T) {
	t.Parallel()

	me := []string{hiddenMe}
	for seed := range uint64(50) {
		rng := rand.New(rand.NewPCG(seed, 0x70)) // #nosec G404 -- reproducible
		w := genProfileWorld(rng)
		c := openTemp(t)
		c.UseSelves(func() []string { return me })
		readMS := time.Unix(1700000000, 0).Add(time.Duration(rng.IntN(len(w.msgs)+1)-1) * time.Minute).UnixMilli()
		if err := c.SaveUnread(context.Background(), domain.Unread{RoomID: mx + "!a:x"}, readMS); err != nil {
			t.Fatal(err)
		}
		for _, step := range w.steps {
			step(t, c)
		}
		for _, profile := range []string{"", "home", "work", "gone"} {
			ctx := domain.WithProfile(context.Background(), profile)
			where := fmt.Sprintf("seed %d, profile %q: marked %v groups %v links %v excluded %+v",
				seed, profile, w.marked, w.groups, w.links, w.excluded)
			want := w.wantIn(profile)

			hits, err := c.SearchMessages(ctx, domain.SearchRequest{
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
			if !slices.Equal(got, want) {
				t.Fatalf("%s: search found %v, want %v", where, got, want)
			}
			for i := range w.msgs {
				hidden, hideErr := c.Hides(ctx, w.msgs[i])
				if hideErr != nil {
					t.Fatal(hideErr)
				}
				if hidden == slices.Contains(want, w.msgs[i].ID) {
					t.Fatalf("%s: Hides(%s from %s) = %v", where, w.msgs[i].ID, w.msgs[i].Sender, hidden)
				}
			}

			wantMsgs, wantMentions, wantThreaded, wantThreadMentions := w.countsIn(profile, readMS)
			messages, mentions, counted, err := c.CountUnread(ctx, me, mx+"!a:x")
			if err != nil || !counted {
				t.Fatalf("%s: CountUnread counted %v, %v", where, counted, err)
			}
			all, err := c.CountUnreadAll(ctx, me)
			if err != nil {
				t.Fatal(err)
			}
			if messages != wantMsgs || mentions != wantMentions || all[mx+"!a:x"].Messages != wantMsgs || all[mx+"!a:x"].Mentions != wantMentions {
				t.Fatalf("%s: counted %d (%d mentions), all %+v; want %d (%d)", where, messages, mentions, all[mx+"!a:x"], wantMsgs, wantMentions)
			}
			threads, err := c.CountThreadUnreadAll(ctx, me)
			if err != nil {
				t.Fatal(err)
			}
			var threaded, threadMentions int
			for _, th := range threads[mx+"!a:x"] {
				threaded += th.Unread
				threadMentions += th.Mentions
			}
			if threaded != wantThreaded || threadMentions != wantThreadMentions {
				t.Fatalf("%s: threads count %d (%d mentions), want %d (%d)", where, threaded, threadMentions, wantThreaded, wantThreadMentions)
			}

			visible := w.visibleIn(profile)
			senders, err := c.SearchSenders(ctx, domain.EveryRoom(), 50)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range senders {
				if !visible(&domain.Message{Sender: s.UserID}) {
					t.Fatalf("%s: sender completion offers %s, hidden here", where, s.UserID)
				}
			}
		}
	}
}

// A change of whom a profile hides names the rooms its people wrote in to the profile
// listener alone: everyone's listener (which files their direct messages) hears nothing,
// since a profile moves no room. A profile removed lets its people go, named the same.
func TestAProfilesChangeIsHeardAsItsOwn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	c := openTemp(t)
	c.UseSelves(func() []string { return []string{hiddenMe} })
	for i, m := range []struct {
		room   domain.RoomID
		sender string
	}{{mx + "!a:x", "matrix:@ann:example.org"}, {mx + "!b:x", "matrix:@bob:example.org"}, {mx + "!c:x", hiddenMe}} {
		msg := domain.Message{ID: domain.EventID(fmt.Sprintf(mx+"$%d", i)), RoomID: m.room, Sender: m.sender, Body: "hello", Timestamp: time.Unix(1700000000+int64(i), 0)}
		if err := c.SaveMessages(ctx, m.room, []domain.Message{msg}); err != nil {
			t.Fatal(err)
		}
	}
	var shared, here [][]domain.RoomID
	c.OnHiddenChange(func(_ context.Context, rooms []domain.RoomID) {
		shared = append(shared, slices.Sorted(slices.Values(rooms)))
	})
	c.OnProfileHiddenChange(func(_ context.Context, rooms []domain.RoomID) {
		here = append(here, slices.Sorted(slices.Values(rooms)))
	})
	expect := func(step string, want ...domain.RoomID) {
		t.Helper()
		if len(shared) != 0 {
			t.Errorf("%s: everyone's listener heard %v", step, shared)
		}
		switch {
		case len(want) == 0 && len(here) != 0:
			t.Errorf("%s: heard %v, want nothing", step, here)
		case len(want) > 0 && (len(here) != 1 || !slices.Equal(here[0], want)):
			t.Errorf("%s: heard %v, want %v once", step, here, want)
		}
		shared, here = nil, nil
	}
	card := func(ids ...string) domain.ExcludedPeople {
		return profileExclusion{card: ids}.people()
	}

	if err := c.UseProfileHidden(ctx, map[string]domain.ExcludedPeople{"home": card("matrix:@ann:example.org")}); err != nil {
		t.Fatal(err)
	}
	expect("a card meets home's field", mx+"!a:x")
	if err := c.UseProfileHidden(ctx, map[string]domain.ExcludedPeople{"home": card("matrix:@bob:example.org")}); err != nil {
		t.Fatal(err)
	}
	expect("the card edited to another person", mx+"!a:x", mx+"!b:x")
	if err := c.UseProfileHidden(ctx, map[string]domain.ExcludedPeople{"home": card("matrix:@bob:example.org", hiddenMe)}); err != nil {
		t.Fatal(err)
	}
	expect("your own ID on the card")
	if err := c.UseProfileHidden(ctx, nil); err != nil {
		t.Fatal(err)
	}
	expect("home removed by a reload", mx+"!b:x")
}
