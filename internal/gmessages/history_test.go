package gmessages

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// phonePage is the phone's answer to FetchMessages: up to count of history (oldest
// first) before cursor, newest first. inclusive is a phone that also sends what is in
// the cursor's own millisecond, the anchor too; the bridge's reading allows for both.
func phonePage(history []*gmproto.Message, cursor *gmproto.Cursor, count int, inclusive bool) []*gmproto.Message {
	var page []*gmproto.Message
	for i := len(history) - 1; i >= 0 && len(page) < count; i-- {
		ms := history[i].GetTimestamp() / 1000
		if ms < cursor.GetLastItemTimestamp() || (inclusive && ms == cursor.GetLastItemTimestamp()) {
			page = append(page, history[i])
		}
	}
	return page
}

// aHistory is n messages in conv, oldest first, a few sharing a millisecond when ties.
func aHistory(rng *rand.Rand, conv string, n int, ties bool) []*gmproto.Message {
	history := make([]*gmproto.Message, n)
	ms := int64(1_000_000)
	for i := range history {
		step := 1 + rng.Int64N(3)
		if ties && rng.IntN(6) == 0 {
			step = 0
		}
		ms += step
		history[i] = text(conv, fmt.Sprintf("m%03d", i), "2", fmt.Sprintf("said %d", i), ms, gmproto.MessageStatusType_INCOMING_COMPLETE)
	}
	return history
}

func cachedIDs(t *testing.T, cache *db.Cache, room domain.RoomID) []domain.EventID {
	t.Helper()
	msgs, err := cache.Messages(context.Background(), room, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]domain.EventID, len(msgs))
	for i := range msgs {
		ids[i] = msgs[i].ID
	}
	return ids
}

// Reading a conversation back from the phone, page by page as the backfill does, while
// live messages keep arriving: it ends, at the beginning, asking a page at a time;
// every message whose millisecond is its own is cached, history is never announced and
// a live message is announced once; and the room is marked as cached from its
// beginning only then.
func TestReadingBackReachesTheBeginning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for seed := range uint64(120) {
		rng := rand.New(rand.NewPCG(seed, 105))
		a, c, cache, announced := cachedAdapter(t)
		conv := fmt.Sprintf("c%d", seed)
		room := roomID(self, conv)
		a.takeConversations(ctx, c, []*gmproto.Conversation{active(conv, "Dana", false, 1, false, pMe, pDana)})
		ties, inclusive := rng.IntN(2) == 0, rng.IntN(2) == 0
		history := aHistory(rng, conv, 1+rng.IntN(200), ties)
		count := 1 + rng.IntN(60)
		if ties && rng.IntN(2) == 0 {
			count = 1 + rng.IntN(4) // a page a run of ties can fill
		}
		// The sync's page: the newest few.
		synced := 1 + rng.IntN(min(len(history), 25))
		a.takeMessages(ctx, c, history[len(history)-synced:], false)
		live := 0
		oldest, ok, err := cache.OldestMessage(ctx, room)
		if err != nil || !ok {
			t.Fatalf("seed %d: nothing cached after the sync (%v)", seed, err)
		}
		cursor, _ := cursorAt(oldest)
		// Without ties each page brings a page's worth (less the anchor an inclusive
		// phone sends again): the phone is asked no more than that takes.
		per := count
		if inclusive {
			per--
		}
		most := 2*len(history) + 2
		if per > 0 {
			most = len(history)/per + 2
		}
		pages := 0
		for cursor != nil {
			if pages++; (!ties && pages > most) || pages > 2*len(history)+5 {
				t.Fatalf("seed %d (count %d, ties %v, inclusive %v): still paging after %d pages", seed, count, ties, inclusive, pages)
			}
			if rng.IntN(4) == 0 {
				live++
				m := text(conv, fmt.Sprintf("live%d", live), "2", "new", 2_000_000+int64(live), gmproto.MessageStatusType_INCOMING_COMPLETE)
				a.take(ctx, c, &libgm.WrappedMessage{Message: m})
			}
			started, serr := cache.HistoryStarts(ctx)
			if serr != nil {
				t.Fatal(serr)
			}
			if started[room] {
				t.Fatalf("seed %d: marked as cached from its beginning while still paging", seed)
			}
			cursor, err = a.takeOlder(ctx, c, room, cursor, phonePage(history, cursor, count, inclusive), count, time.Now())
			if err != nil {
				t.Fatal(err)
			}
		}
		started, err := cache.HistoryStarts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !started[room] {
			t.Fatalf("seed %d: read back to the beginning, not marked so", seed)
		}
		have := cachedIDs(t, cache, room)
		perMs := map[int64]int{}
		for _, m := range history {
			perMs[m.GetTimestamp()/1000]++
		}
		for _, m := range history {
			id := eventID(self, conv, m.GetMessageID())
			if perMs[m.GetTimestamp()/1000] == 1 && !slices.Contains(have, id) {
				t.Fatalf("seed %d (count %d, ties %v, inclusive %v): %s is not cached", seed, count, ties, inclusive, m.GetMessageID())
			}
		}
		if len(have) > len(history)+live {
			t.Fatalf("seed %d: %d cached of %d sent", seed, len(have), len(history)+live)
		}
		if len(*announced) != live {
			t.Fatalf("seed %d: announced %d, want the %d live ones", seed, len(*announced), live)
		}
	}
}

// Scrolling back pages the cache to its oldest message: each page once, in order. A
// room cached from its beginning ends there; one that is not, with the phone away,
// ends in an error (so scrolling asks again), never in a beginning.
func TestTimelinePagesBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for seed := range uint64(60) {
		rng := rand.New(rand.NewPCG(seed, 7))
		a, c, cache, _ := cachedAdapter(t)
		conv := fmt.Sprintf("c%d", seed)
		room := roomID(self, conv)
		a.takeConversations(ctx, c, []*gmproto.Conversation{active(conv, "Dana", false, 1, false, pMe, pDana)})
		a.takeMessages(ctx, c, aHistory(rng, conv, 1+rng.IntN(120), true), false)
		fromStart := rng.IntN(2) == 0
		if fromStart {
			if err := cache.SetHistoryStart(ctx, map[domain.RoomID]bool{room: true}); err != nil {
				t.Fatal(err)
			}
		}
		limit := 1 + rng.IntN(40)
		var got []domain.EventID
		from := ""
		var err error
		for range 1000 {
			var page domain.TimelinePage
			page, err = a.Timeline(ctx, room, from, limit)
			if err != nil {
				break
			}
			ids := make([]domain.EventID, len(page.Messages))
			for i := range page.Messages {
				ids[i] = page.Messages[i].ID
			}
			got = append(ids, got...)
			if from = page.Next; from == "" {
				break
			}
		}
		want := cachedIDs(t, cache, room)
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d (limit %d): paged %d messages, the cache holds %d", seed, limit, len(got), len(want))
		}
		if fromStart && err != nil {
			t.Fatalf("seed %d: a room cached from its beginning ended in %v", seed, err)
		}
		if !fromStart && err == nil {
			t.Fatalf("seed %d: a room not cached from its beginning ended as if it were, the phone away", seed)
		}
	}
}

// The backfill takes the listed conversations not yet cached from their beginning, the
// most recently active first; not one only the cache knows (a message there would ask
// for a sync).
func TestTheBackfillTakesListedConversationsNewestFirst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	for i, conv := range []string{"a", "b", "c", "d"} {
		a.takeConversations(ctx, c, []*gmproto.Conversation{active(conv, conv, false, 1, false, pMe, pDana)})
		a.takeMessages(ctx, c, []*gmproto.Message{text(conv, "m", "2", "hi", int64(1000*(4-i)), gmproto.MessageStatusType_INCOMING_COMPLETE)}, false)
	}
	if err := cache.SetHistoryStart(ctx, map[domain.RoomID]bool{roomID(self, "b"): true}); err != nil {
		t.Fatal(err)
	}
	c.applying.Lock()
	delete(c.inbox.convs, "d") // cached, not listed this connection
	c.applying.Unlock()
	rooms, err := a.backfillRooms(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.RoomID{roomID(self, "a"), roomID(self, "c")}
	if !slices.Equal(rooms, want) {
		t.Errorf("backfills %v, want %v", rooms, want)
	}
}

// The backfill holds back while another browser or a bridge has the session, or the
// phone is not answering, and goes on when it answers again.
func TestTheBackfillWaitsForTheSessionAndThePhone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, _, _ := cachedAdapter(t)
	client := new(libgm.Client)
	c.mu.Lock()
	c.client = client
	c.mu.Unlock()
	if c.free() != client {
		t.Fatal("a connected, active session is not free")
	}
	a.alert(ctx, c, client, gmproto.AlertType_BROWSER_INACTIVE)
	if c.free() != nil {
		t.Error("free while another browser has the session")
	}
	c.inactive.Store(false)
	a.react(ctx, c, client, &events.PhoneNotResponding{})
	if c.free() != nil {
		t.Error("free while the phone is not answering")
	}
	a.react(ctx, c, client, &events.PhoneRespondingAgain{})
	if c.free() != client {
		t.Error("not free once the phone answers again")
	}
	c.close()
	if c.free() != nil {
		t.Error("free after the connection was let go")
	}
}
