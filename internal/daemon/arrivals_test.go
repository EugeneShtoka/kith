package daemon_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// vote moves any room a message says "vote" in, whoever's it is.
var vote = config.Filter{Name: "vote", Tag: "Spam", Words: []string{"vote"}, Moves: []string{"always"}}

// movedRooms is the rooms src recorded a move for.
func movedRooms(src *spamBackend) map[domain.RoomID]bool {
	out := map[domain.RoomID]bool{}
	for _, v := range src.verdicts() {
		out[v.Room] = true
	}
	return out
}

// waitMoved waits for every room in want to be moved, failing after a while.
func waitMoved(t *testing.T, src *spamBackend, want ...domain.RoomID) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		moved, all := movedRooms(src), true
		for _, room := range want {
			all = all && moved[room]
		}
		if all {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("moved %v, want %v", moved, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// runArrivals runs the worker until the test ends, and checks it stops then.
func runArrivals(t *testing.T, run func(context.Context)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("RunArrivals did not stop when its context ended")
		}
	})
}

// Messages cached without Deliver (a sync after an outage) are judged by the filters
// from the oldest that arrived: a room is moved by what arrived, not by what it said
// before, and of two arrivals in one room the older time holds.
func TestArrivedMessagesAreJudgedFromTheOldest(t *testing.T) {
	t.Parallel()
	const (
		day      = 24 * time.Hour
		gap      = mx + "!gap:example.org"
		earlier  = mx + "!earlier:example.org"
		widened  = mx + "!widened:example.org"
		narrowed = mx + "!narrowed:example.org"
	)
	src := spamWorld()
	src.rooms = append(src.rooms,
		domain.Room{ID: gap, Name: "Gap"}, domain.Room{ID: earlier, Name: "Earlier"},
		domain.Room{ID: widened, Name: "Widened"}, domain.Room{ID: narrowed, Name: "Narrowed"})
	src.put(gap, alice, "go vote today", time.Hour)            // arrived: moves it
	src.put(earlier, alice, "go vote today", 3*day)            // said long before what arrived
	src.put(earlier, alice, "morning", time.Hour)              // what arrived
	src.put(widened, alice, "go vote today", 2*day)            // in the older of two arrivals
	src.put(narrowed, alice, "go vote today", 2*day)           // in the older of two arrivals
	src.put(chatRm, me, "I vote for lunch at noon", time.Hour) // your own: never judged
	n, _ := spamNotifier(t, spamConfig(withFilters(vote)), src)
	runArrivals(t, n.RunArrivals)

	now := time.Now()
	n.Arrived(gap, now.Add(-2*time.Hour))
	n.Arrived(earlier, now.Add(-2*time.Hour))
	n.Arrived(widened, now.Add(-2*time.Hour))
	n.Arrived(widened, now.Add(-3*day))
	n.Arrived(narrowed, now.Add(-3*day))
	n.Arrived(narrowed, now.Add(-2*time.Hour))
	n.Arrived(chatRm, now.Add(-2*time.Hour))

	waitMoved(t, src, gap, widened, narrowed)
	moved := movedRooms(src)
	if moved[earlier] || moved[chatRm] {
		t.Errorf("moved %v: a message older than the arrival, or your own, moved a room", moved)
	}
}

// stallBackend holds its first timeline read until let go, so a room can arrive while
// the worker is judging.
type stallBackend struct {
	*spamBackend
	once    sync.Once
	reading chan struct{}
	release chan struct{}
}

func (s *stallBackend) CachedTimeline(ctx context.Context, room domain.RoomID) ([]domain.Message, error) {
	s.once.Do(func() {
		close(s.reading)
		<-s.release
	})
	return s.spamBackend.CachedTimeline(ctx, room)
}

// A room arriving while the worker judges others is judged in turn, not lost.
func TestARoomArrivingMidJudgementIsJudged(t *testing.T) {
	t.Parallel()
	const late = mx + "!late:example.org"
	src := &stallBackend{spamBackend: spamWorld(), reading: make(chan struct{}), release: make(chan struct{})}
	src.rooms = append(src.rooms, domain.Room{ID: late, Name: "Late"})
	src.put(chatRm, alice, "go vote today", time.Hour)
	src.put(late, alice, "go vote today", time.Hour)
	n, err := daemon.NewNotifications(asStarted(spamConfig(withFilters(vote))), src)
	if err != nil {
		t.Fatal(err)
	}
	runArrivals(t, n.RunArrivals)

	since := time.Now().Add(-2 * time.Hour)
	n.Arrived(chatRm, since)
	select {
	case <-src.reading:
	case <-time.After(10 * time.Second):
		t.Fatal("the worker never judged the first room")
	}
	n.Arrived(late, since)
	close(src.release)
	waitMoved(t, src.spamBackend, chatRm, late)
}

// setBackend keeps each room's moves as one set, read, changed and written back as the
// cache and Matrix account data do, with a pause between, so two writers overlap.
type setBackend struct {
	*spamBackend
	setsMu sync.Mutex
	sets   map[domain.RoomID][]domain.TagVerdict
}

func (s *setBackend) MarkTag(_ context.Context, v domain.TagVerdict) error {
	s.setsMu.Lock()
	set := slices.Clone(s.sets[v.Room])
	s.setsMu.Unlock()
	time.Sleep(5 * time.Millisecond)
	set, err := domain.WithVerdict(set, v)
	if err != nil {
		return err
	}
	s.setsMu.Lock()
	s.sets[v.Room] = set
	s.setsMu.Unlock()
	return nil
}

// Two writers moving one room into two tags at once (Deliver and a judgement of what
// arrived, or a rescan) both land: neither write undoes the other.
func TestTwoMovesOfOneRoomAtOnceBothLand(t *testing.T) {
	t.Parallel()
	src := &setBackend{spamBackend: spamWorld(), sets: map[domain.RoomID][]domain.TagVerdict{}}
	var rooms []domain.RoomID
	for i := range 12 {
		room := domain.RoomID(fmt.Sprintf("%s!r%d:example.org", mx, i))
		rooms = append(rooms, room)
		src.rooms = append(src.rooms, domain.Room{ID: room, Name: fmt.Sprint("R", i)})
		src.put(room, alice, "go vote today", time.Hour)
	}
	always := crypto
	always.Moves = []string{"always"}
	archive := vote
	archive.Tag = "Archived"
	cfg := spamConfig(withFilters(always, archive), func(c *config.Config) {
		c.Tags = append(c.Tags, config.Tag{Name: "Archived"})
	})
	n, err := daemon.NewNotifications(asStarted(cfg), src)
	if err != nil {
		t.Fatal(err)
	}
	n.UseSelves(func() []string { return []string{me} })
	ctx := context.Background()
	var writers sync.WaitGroup
	for _, room := range rooms {
		writers.Go(func() { n.Deliver(ctx, msg(room, bob, "double your bitcoin")) })
		writers.Go(func() {
			if _, err := n.RescanFilters(ctx, time.Time{}, true, []domain.RoomID{room}); err != nil {
				t.Error(err)
			}
		})
	}
	writers.Wait()
	src.setsMu.Lock()
	defer src.setsMu.Unlock()
	for _, room := range rooms {
		var tags []string
		for _, v := range src.sets[room] {
			tags = append(tags, v.Tag)
		}
		slices.Sort(tags)
		if !slices.Equal(tags, []string{"Archived", "Spam"}) {
			t.Errorf("%s holds %v, want both moves", room, tags)
		}
	}
}
