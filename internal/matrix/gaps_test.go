package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// gapRoom is the one room every gap case is in.
const gapRoom = "!gap:x"

// gapEvent is one event of a generated room history.
type gapEvent struct {
	kind   string // "text", "edit" or "redact"
	target int    // the event an edit or redaction is about
}

// gapCase is one generated history and what happens while its gap fills.
type gapCase struct {
	events []gapEvent
	// held is how many of the oldest events the cache had before kith went away; live
	// is how many of the newest the limited sync carried.
	held, live int
	announce   bool
	// failAt fails that /messages request once (a stop mid-fill: kithd restarts);
	// forbidAt answers that one with M_FORBIDDEN (the room was left); liveAt deletes a
	// gap message from the live sync while that request is served. -1 is never.
	failAt, forbidAt, liveAt int
	// retry: after the failure kithd keeps running, and the next sync takes the gap up.
	retry bool
}

func (c gapCase) String() string {
	return fmt.Sprintf("events=%d held=%d live=%d announce=%v failAt=%d retry=%v forbidAt=%d liveAt=%d",
		len(c.events), c.held, c.live, c.announce, c.failAt, c.retry, c.forbidAt, c.liveAt)
}

// genGapCase makes a history whose gap may be small or bigger than the fill's cap, with
// edits and deletions inside the gap of messages from before and inside it.
func genGapCase(r *rand.Rand) gapCase {
	size := 8 + r.IntN(40)
	if r.IntN(4) == 0 {
		size = gapPages*gapPage + 50 + r.IntN(200) // past the cap
	}
	held := 2 + r.IntN(4)
	if r.IntN(3) == 0 {
		held = gapPage + 20 + r.IntN(gapPage) // the boundary is not on the room's first page
	}
	size += held
	c := gapCase{held: held, live: 1 + r.IntN(3), announce: r.IntN(2) == 0, failAt: -1, forbidAt: -1, liveAt: -1}
	for i := range size {
		e := gapEvent{kind: "text"}
		if i > c.held && r.IntN(5) == 0 {
			e = gapEvent{kind: []string{"edit", "redact"}[r.IntN(2)], target: r.IntN(i)}
			for c.isChange(e.target, i) {
				e.target--
			}
		}
		c.events = append(c.events, e)
	}
	switch r.IntN(4) {
	case 0:
		c.failAt, c.retry = r.IntN(4), r.IntN(2) == 0
	case 1:
		c.forbidAt = r.IntN(4)
	case 2:
		c.liveAt = r.IntN(4)
	}
	return c
}

// isChange: event i of the first upTo is an edit or a redaction, not a message.
func (c gapCase) isChange(i, upTo int) bool {
	return i < upTo && i >= 0 && i < len(c.events) && c.events[i].kind != "text"
}

func gapEventID(i int) string { return fmt.Sprintf("$e%d:x", i) }

// redacted reports whether event i is deleted by any event in the history.
func (c gapCase) redacted(i int) bool {
	for _, e := range c.events {
		if e.kind == "redact" && e.target == i {
			return true
		}
	}
	return false
}

// eventJSON is event i as the server returns it, deleted if any redaction names it.
func (c gapCase) eventJSON(i int) map[string]any {
	evt := map[string]any{
		"event_id": gapEventID(i), "sender": "@bob:x", "room_id": gapRoom,
		"origin_server_ts": 1_700_000_000_000 + int64(i)*1000,
	}
	e := c.events[i]
	switch {
	case e.kind == "redact":
		evt["type"] = "m.room.redaction"
		evt["redacts"] = gapEventID(e.target)
		evt["content"] = map[string]any{"redacts": gapEventID(e.target)}
	case c.redacted(i):
		evt["type"] = "m.room.message"
		evt["content"] = map[string]any{}
		evt["unsigned"] = map[string]any{"redacted_because": map[string]any{
			"type": "m.room.redaction", "sender": "@bob:x", "event_id": "$why:x", "origin_server_ts": 1,
		}}
	case e.kind == "edit":
		evt["type"] = "m.room.message"
		evt["content"] = map[string]any{
			"msgtype": "m.text", "body": "* " + c.bodyOf(i),
			"m.new_content": map[string]any{"msgtype": "m.text", "body": c.bodyOf(i)},
			"m.relates_to":  map[string]any{"rel_type": "m.replace", "event_id": gapEventID(e.target)},
		}
	default:
		evt["type"] = "m.room.message"
		evt["content"] = map[string]any{"msgtype": "m.text", "body": c.bodyOf(i)}
	}
	return evt
}

func (c gapCase) bodyOf(i int) string { return "said " + strconv.Itoa(i) }

// event is event i parsed, as a sync would hand it over.
func (c gapCase) event(t *testing.T, i int) *event.Event {
	t.Helper()
	raw, err := json.Marshal(c.eventJSON(i))
	if err != nil {
		t.Fatal(err)
	}
	var evt event.Event
	if err := json.Unmarshal(raw, &evt); err != nil {
		t.Fatal(err)
	}
	evt.RoomID = gapRoom
	parse(&evt)
	return &evt
}

// gapServer serves /messages over a case's history: token "t<i>" pages back from event i.
type gapServer struct {
	c      gapCase
	t      *testing.T
	mu     sync.Mutex
	asked  int
	failed bool
	// forbade and lived: the left room and the live deletion were served.
	forbade, lived bool
	onLive         func()
}

func (s *gapServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !strings.HasSuffix(r.URL.Path, "/messages") {
		_, _ = w.Write([]byte(`{"joined": {}}`))
		return
	}
	s.mu.Lock()
	n := s.asked
	s.asked++
	fail := n == s.c.failAt && !s.failed
	if fail {
		s.failed = true
	}
	live := n == s.c.liveAt
	s.forbade = s.forbade || n == s.c.forbidAt
	s.lived = s.lived || live
	s.mu.Unlock()
	switch {
	case fail:
		http.Error(w, `{"errcode":"M_UNKNOWN"}`, http.StatusInternalServerError)
		return
	case n == s.c.forbidAt:
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"not in the room"}`))
		return
	}
	if live && s.onLive != nil {
		s.onLive()
	}
	from, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Query().Get("from"), "t"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	start := max(from-limit, 0)
	chunk := []map[string]any{}
	for i := from - 1; i >= start; i-- {
		chunk = append(chunk, s.c.eventJSON(i))
	}
	resp := map[string]any{"start": "t" + strconv.Itoa(from), "chunk": chunk}
	if start > 0 {
		resp["end"] = "t" + strconv.Itoa(start)
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.t.Errorf("encode a page: %v", err)
	}
}

// runGapCase caches what kith held, takes the limited sync, fills the gap (restarting
// once if a request fails) and returns the backend, the server, every message counted
// as new and every one announced.
func runGapCase(t *testing.T, c gapCase) (*InProc, *gapServer, []domain.Message, []domain.Message) {
	t.Helper()
	srv := &gapServer{c: c, t: t}
	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)
	b, ctx := backendOn(t, hs), context.Background()

	// What the cache held: the oldest events, cached as they arrived live.
	var heldChunk []*event.Event
	for i := c.held - 1; i >= 0; i-- {
		heldChunk = append(heldChunk, c.event(t, i))
	}
	b.cachePage(ctx, gapRoom, heldChunk, "")

	var mu sync.Mutex
	var counted, announced []domain.Message
	b.onCached = func(msg domain.Message) {
		mu.Lock()
		defer mu.Unlock()
		counted = append(counted, msg)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for msg := range b.out.msgs {
			mu.Lock()
			announced = append(announced, msg)
			mu.Unlock()
		}
	}()
	// A live sync deleting a message in the gap, between two pages of the fill.
	liveTarget := c.held + (len(c.events)-c.live-c.held)/2
	srv.onLive = func() {
		red := &event.Event{
			ID: "$live-redaction:x", Type: event.EventRedaction, RoomID: gapRoom, Sender: "@bob:x",
			Redacts: id.EventID(gapEventID(liveTarget)), Timestamp: 1_800_000_000_000,
		}
		b.onRedaction(ctx, red)
	}

	first := len(c.events) - c.live
	resp := &mautrix.RespSync{}
	resp.Rooms.Join = map[id.RoomID]*mautrix.SyncJoinedRoom{gapRoom: {}}
	jr := resp.Rooms.Join[gapRoom]
	jr.Timeline.Limited = true
	jr.Timeline.PrevBatch = "t" + strconv.Itoa(first)
	for i := first; i < len(c.events); i++ {
		jr.Timeline.Events = append(jr.Timeline.Events, c.event(t, i))
	}
	if c.announce {
		b.syncs.Store(2)
	} else {
		b.syncs.Store(1)
	}
	b.recordGaps(ctx, resp, "since")
	b.gaps.wg.Wait()
	if c.failAt >= 0 {
		if !c.retry {
			// kithd stopped mid-fill: a new run takes the gap up from where it was left.
			b.gaps = gapFiller{}
			b.gaps.pending.Store(true)
		}
		// The next sync, with nothing limited in it.
		b.recordGaps(ctx, &mautrix.RespSync{}, "since")
		b.gaps.wg.Wait()
	}
	b.out.close()
	<-done
	return b, srv, counted, announced
}

// A gap a sync skipped is filled: every message in it cached once, deletions and edits
// inside it applied to messages before it too, counted once across a restart, announced
// only when not the catch-up, and kept, capped, past the limit. Generated, with restarts,
// a left room and a live deletion mid-fill.
func TestAGapASyncSkippedIsFilled(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(112, 2026))
	for n := range 60 {
		c := genGapCase(r)
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			t.Parallel()
			checkGapCase(t, c)
		})
	}
}

func checkGapCase(t *testing.T, c gapCase) {
	t.Helper()
	b, srv, counted, announced := runGapCase(t, c)
	ctx := context.Background()
	gaps, err := b.cache.Gaps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := b.cache.Messages(ctx, gapRoom, -1)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[domain.EventID]domain.Message{}
	for i := range cached {
		byID[cached[i].ID] = cached[i]
	}

	// Counted once each, never a message held before; announced only after the catch-up.
	seen := map[domain.EventID]int{}
	for i := range counted {
		if !counted[i].IsUpdate() {
			seen[counted[i].ID]++
		}
	}
	for id, times := range seen {
		if times > 1 {
			t.Errorf("%v: %s counted %d times", c, id, times)
		}
	}
	for i := range c.held {
		if seen[domain.EventID(gapEventID(i))] > 0 {
			t.Errorf("%v: %s was held before the gap but counted as new", c, gapEventID(i))
		}
	}
	for i := range announced {
		if !c.announce && !announced[i].IsUpdate() {
			t.Errorf("%v: the catch-up's gap announced %s", c, announced[i].ID)
		}
	}
	announcedNew := map[domain.EventID]int{}
	for i := range announced {
		m := &announced[i]
		if m.IsUpdate() {
			continue // a deletion's stub, as live
		}
		announcedNew[m.ID]++
		if !m.Late {
			t.Errorf("%v: %s announced without Late", c, m.ID)
		}
	}
	for id, times := range announcedNew {
		if times > 1 {
			t.Errorf("%v: %s announced %d times", c, id, times)
		}
	}

	if srv.forbade {
		if len(gaps) != 0 {
			t.Errorf("%v: a left room's gap was kept: %+v", c, gaps)
		}
		return
	}

	gapEnd := len(c.events) - c.live // events [held, gapEnd) are the gap
	fetchable := gapPages * gapPage
	if srv.failed {
		// The run that stopped read failAt pages; the next one starts its own count.
		fetchable += c.failAt * gapPage
	}
	capped := gapEnd-c.held > fetchable-1 // the boundary must be in what the fill reads
	if !capped {
		// Paging stops at the page holding the boundary (event held-1).
		need := (gapEnd - (c.held - 1) + gapPage - 1) / gapPage
		if srv.failed {
			need++
		}
		if srv.asked > need {
			t.Errorf("%v: %d pages read; the boundary is on page %d", c, srv.asked, need)
		}
	}
	if capped {
		if len(gaps) != 1 || !gaps[0].Capped {
			t.Fatalf("%v: a gap past the cap = %+v; want it kept, capped", c, gaps)
		}
	} else if len(gaps) != 0 {
		t.Fatalf("%v: gap kept after filling: %+v", c, gaps)
	}
	lowest := c.held
	if capped {
		lowest = gapEnd - fetchable
	}
	liveTarget := c.held + (len(c.events)-c.live-c.held)/2
	if srv.lived {
		if m, ok := byID[domain.EventID(gapEventID(liveTarget))]; ok && !m.Redacted && c.events[liveTarget].kind == "text" {
			t.Errorf("%v: %s deleted live while the gap filled is shown: %q", c, gapEventID(liveTarget), m.Body)
		}
	}
	for i := lowest; i < gapEnd; i++ {
		if c.events[i].kind != "text" || srv.lived && i == liveTarget {
			continue
		}
		m, ok := byID[domain.EventID(gapEventID(i))]
		if !ok {
			t.Errorf("%v: %s in the gap was not cached", c, gapEventID(i))
			continue
		}
		if !c.redacted(i) && seen[m.ID] != 1 {
			t.Errorf("%v: %s counted %d times, want once", c, m.ID, seen[m.ID])
		}
	}
	// The last edit inside what was filled shows, before the gap too.
	latest := map[int]int{}
	for i := lowest; i < gapEnd; i++ {
		if e := c.events[i]; e.kind == "edit" && c.events[e.target].kind == "text" && !c.redacted(e.target) {
			latest[e.target] = i
		}
	}
	for target, edit := range latest {
		if srv.lived && target == liveTarget {
			continue // deleted live
		}
		if m, ok := byID[domain.EventID(gapEventID(target))]; ok && m.Body != c.bodyOf(edit) {
			t.Errorf("%v: %s shows %q; want its edit inside the gap, %q", c, gapEventID(target), m.Body, c.bodyOf(edit))
		}
	}
	// Deletions inside what was filled reach their targets, before the gap too.
	for i := lowest; i < gapEnd; i++ {
		e := c.events[i]
		if e.kind != "redact" || c.events[e.target].kind != "text" {
			continue
		}
		if m, ok := byID[domain.EventID(gapEventID(e.target))]; ok && !m.Redacted {
			t.Errorf("%v: %s deleted inside the gap is still shown: %q", c, gapEventID(e.target), m.Body)
		}
	}
}

// Scrolling back from the newest message past a gap fills it; a page above it does not.
func TestScrollingPastAGapFillsIt(t *testing.T) {
	t.Parallel()
	c := gapCase{held: 3, live: 2, failAt: -1, forbidAt: -1, liveAt: -1}
	for range 20 {
		c.events = append(c.events, gapEvent{kind: "text"})
	}
	hs := httptest.NewServer(&gapServer{c: c, t: t})
	defer hs.Close()
	b, ctx := backendOn(t, hs), context.Background()
	var held []*event.Event
	for i := c.held - 1; i >= 0; i-- {
		held = append(held, c.event(t, i))
	}
	b.cachePage(ctx, gapRoom, held, "")
	if added, err := b.cache.AddGap(ctx, gapRoom, "t18", oldestOf([]*event.Event{c.event(t, 18)}), false); err != nil || !added {
		t.Fatalf("AddGap = %v, %v", added, err)
	}

	if _, err := b.Timeline(ctx, gapRoom, "t20", 5); err != nil {
		t.Fatal(err)
	}
	if gaps, _ := b.cache.Gaps(ctx); len(gaps) != 1 {
		t.Fatalf("a page above the gap dropped it: %+v", gaps)
	}
	if _, err := b.Timeline(ctx, gapRoom, "t15", 50); err != nil {
		t.Fatal(err)
	}
	if gaps, _ := b.cache.Gaps(ctx); len(gaps) != 0 {
		t.Fatalf("scrolled past the gap, still kept: %+v", gaps)
	}
}
