package db

import (
	"cmp"
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// What one person said is paged across rooms. The pages must join into exactly the
// messages they sent in scope, newest first, whatever the page size: across rooms a
// time, a seq and even an event ID can be shared, and none of those may lose or repeat
// a message at a page's edge.
func TestPagesOfWhatSomeoneSaidJoinWithoutLossOrRepeat(t *testing.T) {
	t.Parallel()

	rooms := []domain.RoomID{mx + "!a:x", mx + "!b:x", mx + "!c:x"}
	them := []string{"matrix:@dana:x", "whatsapp:dana"}
	senders := append(slices.Clone(them), "matrix:@ezra:x")
	for seed := range uint64(40) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewPCG(seed, 7))
			cache, ctx := openTemp(t), context.Background()
			var all []domain.Message
			for i := range 30 + rng.IntN(30) {
				room := rooms[rng.IntN(len(rooms))]
				// Few times, seqs and IDs, so ties are the rule: the same ID in two rooms too.
				id := domain.EventID(fmt.Sprintf(mx+"$%s", "xyz"[:1+rng.IntN(3)]+fmt.Sprint(rng.IntN(8))))
				if slices.ContainsFunc(all, func(m domain.Message) bool { return m.RoomID == room && m.ID == id }) {
					continue
				}
				msg := domain.Message{
					RoomID: room, ID: id, Sender: senders[rng.IntN(len(senders))], Body: fmt.Sprint(i),
					Timestamp: time.UnixMilli(int64(1000 * (1 + rng.IntN(3)))), Seq: int64(rng.IntN(3)),
				}
				mustSave(t, cache, room, msg)
				all = append(all, msg)
			}
			// They are hidden, and still listed: they were asked for.
			if err := cache.UseHidden(ctx, them[:1], nil, [][]string{them}); err != nil {
				t.Fatal(err)
			}
			scopes := []domain.RoomSet{domain.EveryRoom(), domain.TheseRooms(rooms[:2]), domain.TheseRooms(nil)}
			for _, scope := range scopes {
				want := expectedSaid(all, them, scope)
				var got []domain.Message
				var before domain.MessagePlace
				for range len(all) + 2 {
					page, err := cache.MessagesFrom(ctx, domain.SaidRequest{
						Senders: them, Rooms: scope, Before: before, Limit: 1 + rng.IntN(6),
					})
					if err != nil {
						t.Fatal(err)
					}
					if len(page) == 0 {
						break
					}
					// A page is oldest first; the pages run newest to oldest.
					for _, msg := range slices.Backward(page) {
						got = append(got, msg)
					}
					before = page[0].Place()
				}
				if g, w := placesOf(got), placesOf(want); !slices.Equal(g, w) {
					t.Fatalf("scope %v: pages joined into\n%v\nwant\n%v", scope, g, w)
				}
			}
		})
	}
}

// expectedSaid is the order MessagesFrom must give, worked out without SQL.
func expectedSaid(all []domain.Message, senders []string, scope domain.RoomSet) []domain.Message {
	var out []domain.Message
	for i := range all {
		if slices.Contains(senders, all[i].Sender) && (scope.All || slices.Contains(scope.IDs, all[i].RoomID)) {
			out = append(out, all[i])
		}
	}
	slices.SortFunc(out, func(a, b domain.Message) int {
		return -cmp.Or(a.Timestamp.Compare(b.Timestamp), cmp.Compare(a.Seq, b.Seq),
			cmp.Compare(len(a.ID), len(b.ID)), cmp.Compare(a.ID, b.ID), cmp.Compare(a.RoomID, b.RoomID))
	})
	return out
}

func placesOf(msgs []domain.Message) []string {
	out := make([]string, len(msgs))
	for i := range msgs {
		out[i] = fmt.Sprintf("%s/%s@%d.%d", msgs[i].RoomID, msgs[i].ID, msgs[i].Timestamp.UnixMilli(), msgs[i].Seq)
	}
	return out
}

// Each person's count is what MessagesFrom would read of them: their IDs, and an
// account their number reaches.
func TestMessageCountsCountWhatIsReadOfEachPerson(t *testing.T) {
	t.Parallel()
	cache, ctx := openTemp(t), context.Background()
	for i, sender := range []string{"matrix:@ann:x", "matrix:@ann:x", "matrix:@whatsapp_15550000001:example.org", "matrix:@bob:x"} {
		mustSave(t, cache, mx+"!a:x", domain.Message{ID: domain.EventID(fmt.Sprint("$", i)), Sender: sender, Body: "hi", Timestamp: time.UnixMilli(int64(i))})
	}
	got, err := cache.MessageCounts(ctx, [][]string{{"matrix:@ann:x", domain.PhoneID("15550000001")}, {"matrix:@bob:x"}, nil})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{3, 1, 0}; !slices.Equal(got, want) {
		t.Errorf("counts = %v, want %v", got, want)
	}
}

// What someone said is read by exactly the senders that hiding them hides: the IDs their
// person goes by (domain.MarkedPeople.IDs) and an account the directory never listed but
// whose number is theirs. Links, a group and messages are generated; the cache's own
// hidden list is the oracle.
func TestWhatSomeoneSaidIsReadByEveryIDThatHidesThem(t *testing.T) {
	t.Parallel()

	linkable := append(slices.Clone(hiddenPool), domain.PhoneID("15550000001"))
	for seed := range uint64(150) {
		rng := rand.New(rand.NewPCG(seed, 9))
		pick := func(from []string) string { return from[rng.IntN(len(from))] }
		cache, ctx := openTemp(t), context.Background()
		var links []domain.PersonLink
		for range rng.IntN(4) {
			if id, other := pick(linkable), pick(linkable); id != other {
				links = append(links, domain.PersonLink{Source: "book", ID: id, Other: other})
			}
		}
		if err := cache.SetPeople(ctx, "book", nil, links); err != nil {
			t.Fatal(err)
		}
		var groups [][]string
		if rng.IntN(2) == 0 {
			groups = [][]string{{pick(hiddenPool), pick(hiddenPool)}}
		}
		for i := range 12 {
			mustSave(t, cache, mx+"!a:x", domain.Message{
				ID: domain.EventID(fmt.Sprint("$", i)), Sender: pick(hiddenPool), Body: "hi", Timestamp: time.UnixMilli(int64(i)),
			})
		}
		target := pick(hiddenPool)
		if err := cache.UseHidden(ctx, []string{target}, nil, groups); err != nil {
			t.Fatal(err)
		}
		want := cache.hiddenSenders()
		slices.Sort(want)

		// The IDs as the client works them out: the directory and the identity group.
		dir := domain.NewDirectory(nil, links)
		var known []string
		for _, g := range groups {
			known = append(known, g...)
		}
		same := func(id string) []string {
			out := dir.Identifiers(id)
			for _, g := range groups {
				if slices.Contains(g, id) {
					out = append(slices.Clone(out), g...)
				}
			}
			return out
		}
		ids := domain.NewMarkedPeople([]string{target}, known, same).IDs()
		page, err := cache.MessagesFrom(ctx, domain.SaidRequest{Senders: ids, Rooms: domain.EveryRoom(), Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, msg := range page {
			if !slices.Contains(got, msg.Sender) {
				got = append(got, msg.Sender)
			}
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d: read %v, hidden %v (target %s, ids %v, links %v, groups %v)", seed, got, want, target, ids, links, groups)
		}
	}
}
