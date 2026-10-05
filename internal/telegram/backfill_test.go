package telegram

import (
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/gotd/td/tgtest"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// groupHistory serves messages.getHistory for basic groups: group id has sizes[id]
// messages, numbered 1…n; floods answers FLOOD_WAIT that many times first. It records
// each request as "<group>@<offset>".
type groupHistory struct {
	mu     sync.Mutex
	sizes  map[int64]int
	floods int
	asked  []string
}

func (g *groupHistory) serve(f *fakeTelegram) {
	f.cluster.Dispatch(2, "dc2").HandleFunc(tg.MessagesGetHistoryRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.MessagesGetHistoryRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		chat := req.Peer.(*tg.InputPeerChat).ChatID
		g.mu.Lock()
		if g.floods > 0 {
			g.floods--
			g.mu.Unlock()
			return s.SendErr(r, tgerr.New(420, "FLOOD_WAIT_1"))
		}
		g.asked = append(g.asked, strconv.FormatInt(chat, 10)+"@"+strconv.Itoa(req.OffsetID))
		top := g.sizes[chat]
		g.mu.Unlock()
		if req.OffsetID != 0 {
			top = req.OffsetID - 1
		}
		res := &tg.MessagesMessagesSlice{Count: top}
		for id := top; id > 0 && id > top-req.Limit; id-- {
			res.Messages = append(res.Messages, &tg.Message{ID: id, PeerID: &tg.PeerChat{ChatID: chat}, Message: "m", Date: 1000 + id})
		}
		return s.SendResult(r, res)
	})
}

func (g *groupHistory) requests() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.asked)
}

// Each chat is read back to its beginning, page by page, the chat last spoken in
// first; one read back already is not asked again.
func TestEachChatIsReadBackToItsBeginning(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.serveUpdates(&updatesOf{pts: 1})
	g := &groupHistory{sizes: map[int64]int{11: 150, 12: 30}}
	g.serve(f)
	st := openStore(t)
	a, cache := loggedInWithStore(t, f, st)
	ctx := t.Context()
	quiet, busy := roomID(42, -11), roomID(42, -12)
	for room, at := range map[domain.RoomID]int64{quiet: 1000, busy: 5000} { // 12 spoke last
		if err := cache.SaveMessages(ctx, room, []domain.Message{{ID: domain.EventID(string(room) + "/999"), RoomID: room, Body: "top", Timestamp: time.Unix(at, 0)}}); err != nil {
			t.Fatal(err)
		}
	}
	rooms := []domain.Room{{ID: quiet}, {ID: busy}}
	a.backfill(ctx, home, 42, rooms)
	if got := g.requests(); !slices.Equal(got, []string{"12@0", "11@0", "11@51"}) {
		t.Errorf("asked %v, want the busier chat first, then the other's two pages", got)
	}
	if msgs, _ := cache.Messages(ctx, quiet, 1000); len(msgs) != 151 { // its 150, and the top seeded
		t.Errorf("cached %d of the quiet chat's messages", len(msgs))
	}
	if _, fetched, done, _ := st.backfilled(ctx, 42, string(quiet)); !done || fetched != 150 {
		t.Errorf("the quiet chat's progress: %d read, done %v", fetched, done)
	}
	a.backfill(ctx, home, 42, rooms)
	if got := g.requests(); len(got) != 3 {
		t.Errorf("read back again: %v", got)
	}
}

// A chat whose reading stopped carries on from its place, and stops as deep as the
// cache keeps that room; asked to wait, it waits and asks again.
func TestReadingBackCarriesOnWhereItStopped(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.serveUpdates(&updatesOf{pts: 1})
	g := &groupHistory{sizes: map[int64]int{11: 5000}, floods: 1}
	g.serve(f)
	st := openStore(t)
	a, cache := loggedInWithStore(t, f, st)
	ctx, room := t.Context(), roomID(42, -11)
	cache.UseKeep(func(domain.RoomID) int { return 2000 })
	if err := st.keepBackfill(ctx, 42, string(room), "3001", 1950, false); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	a.backfill(ctx, home, 42, []domain.Room{{ID: room}})
	if got := g.requests(); !slices.Equal(got, []string{"11@3001"}) {
		t.Errorf("asked %v, want one page from where it stopped", got)
	}
	if waited := time.Since(began); waited < time.Second {
		t.Errorf("asked again after %v, before the second Telegram asked for", waited)
	}
	if _, fetched, done, _ := st.backfilled(ctx, 42, string(room)); !done || fetched != 2050 {
		t.Errorf("progress: %d read, done %v", fetched, done)
	}
}
