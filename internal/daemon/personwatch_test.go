package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// personBackend is spamWorld() plus what the sender moves read and write: every cached
// message, whom you wrote to, and the person marks, with a release refusing a mark.
type personBackend struct {
	*spamBackend

	pmu    sync.Mutex
	cached []domain.Message
	wrote  []string
	people []domain.PersonVerdict
	// readErr fails the reads named in failing, for the fail-safe cases.
	readErr error
	failing map[string]bool
}

func (p *personBackend) failed(read string) error {
	if p.failing[read] {
		return p.readErr
	}
	return nil
}

func (p *personBackend) SenderMessages(_ context.Context, sender string, since time.Time, limit int) ([]domain.Message, error) {
	p.pmu.Lock()
	defer p.pmu.Unlock()
	// The first-message read asks for everything; the window read gives a time.
	read := "window"
	if since.IsZero() {
		read = "first"
	}
	if err := p.failed(read); err != nil {
		return nil, err
	}
	var out []domain.Message
	for i := range p.cached {
		if m := &p.cached[i]; m.Sender == sender && !m.Timestamp.Before(since) {
			out = append(out, *m)
		}
	}
	slices.SortStableFunc(out, func(a, b domain.Message) int { return a.Timestamp.Compare(b.Timestamp) })
	return out[:min(len(out), limit)], nil
}

func (p *personBackend) WrittenTo(context.Context, []string) ([]string, error) {
	p.pmu.Lock()
	defer p.pmu.Unlock()
	return slices.Clone(p.wrote), p.failed("wrote")
}

func (p *personBackend) PersonVerdicts(context.Context) ([]domain.PersonVerdict, error) {
	p.pmu.Lock()
	defer p.pmu.Unlock()
	return slices.Clone(p.people), nil
}

func (p *personBackend) MarkPerson(_ context.Context, v domain.PersonVerdict) error {
	p.pmu.Lock()
	defer p.pmu.Unlock()
	set, err := domain.WithPersonVerdict(p.people, v)
	if err != nil {
		return err
	}
	p.people = set
	return nil
}

func (p *personBackend) cache(m domain.Message) {
	p.pmu.Lock()
	defer p.pmu.Unlock()
	p.cached = append(p.cached, m)
}

func (p *personBackend) marked() []string {
	p.pmu.Lock()
	defer p.pmu.Unlock()
	var out []string
	for _, v := range p.people {
		if v.In() {
			out = append(out, v.Person)
		}
	}
	slices.Sort(out)
	return out
}

// personNotifier is the daemon's notifier over a person-capable backend.
func personNotifier(t *testing.T, cfg config.Config, src *personBackend) *daemon.Notifications {
	t.Helper()
	n, err := daemon.NewNotifications(cfg, src, func(config.Notifications) notify.Notifier { return &recorder{} })
	if err != nil {
		t.Fatalf("NewNotifications: %v", err)
	}
	n.UseSelves(func() []string { return []string{me} })
	n.Synced(mx, time.Now())
	n.Synced(mx, time.Now())
	return n
}

// The generator's people: three Matrix users, and one WhatsApp number seen natively and
// as a bridge puppet, which the directory links.
const (
	puppet = "matrix:@whatsapp_15550000001:example.org"
	native = "whatsapp:15550000001@s.whatsapp.net"
)

var personPool = []string{"matrix:@s1:example.org", "matrix:@s2:example.org", "matrix:@s3:example.org", puppet, native, me}

// personOf is who an ID is, for the oracle: the puppet and the number are one.
func personOf(id string) string {
	if id == puppet {
		return native
	}
	return id
}

var personRooms = []domain.RoomID{chatRm, mx + "!g1:example.org", mx + "!g2:example.org", mx + "!g3:example.org"}

// personCase is one generated run.
type personCase struct {
	filters  []config.Filter
	wrote    []string
	released []string
	old      []domain.Message // cached before the run, not delivered
	stream   []domain.Message
}

func genPersonCase(rng *rand.Rand, now time.Time) personCase {
	pick := func() string { return personPool[rng.IntN(len(personPool))] }
	var c personCase
	// One or two filters into Spam, each with its own window: a week, or half an hour
	// (shorter than the old messages within the week).
	for i := range 1 + rng.IntN(2) {
		f := config.Filter{Name: fmt.Sprintf("runs%d", i), Tag: "Spam", Words: []string{"*bitcoin*"},
			Floor: 2 + rng.IntN(3), Ratio: []float64{0.5, 0.75, 1}[rng.IntN(3)], Spread: 2 + rng.IntN(2),
			Window: []string{"168h", "30m"}[rng.IntN(2)]}
		for _, move := range []string{config.FilterMoveSenderFirst, config.FilterMoveSenderRatio, config.FilterMoveSenderSpread} {
			if rng.IntN(2) == 0 {
				f.Moves = append(f.Moves, move)
			}
		}
		if len(f.Moves) == 0 {
			f.Moves = []string{config.FilterMoveSenderSpread}
		}
		c.filters = append(c.filters, f)
	}
	for range rng.IntN(2) {
		c.wrote = append(c.wrote, pick())
	}
	for range rng.IntN(2) {
		c.released = append(c.released, pick())
	}
	bodies := []string{"buy bitcoin now", "Bitcoin invest ", "morning", "hello"}
	for i := range rng.IntN(4) {
		// Some old enough to be outside the window, all counting as said before.
		age := []time.Duration{time.Hour, 10 * 24 * time.Hour}[rng.IntN(2)]
		c.old = append(c.old, domain.Message{ID: domain.EventID(fmt.Sprintf(mx+"$old%d", i)), RoomID: personRooms[rng.IntN(len(personRooms))],
			Sender: pick(), Body: bodies[rng.IntN(len(bodies))], Timestamp: now.Add(-age)})
	}
	n := 8 + rng.IntN(8)
	for i := range n {
		c.stream = append(c.stream, domain.Message{ID: domain.EventID(fmt.Sprintf(mx+"$%d", i)), RoomID: personRooms[rng.IntN(len(personRooms))],
			Sender: pick(), Body: bodies[rng.IntN(len(bodies))], Timestamp: now.Add(-time.Duration(n-i) * time.Second)})
	}
	return c
}

// oracle marks by the rules as written, over the stream in order: a caught message of
// someone not marked, released or written to marks them when it is their first, when
// it reached spread rooms, or when most of their window is caught.
func (c personCase) oracle() (marked []string, silenced map[domain.EventID]bool) {
	settled := map[string]bool{}
	for _, id := range c.released {
		settled[personOf(id)] = true
	}
	hidden := map[string]bool{}
	silenced = map[domain.EventID]bool{}
	seen := slices.Clone(c.old)
	for i := range c.stream {
		m := &c.stream[i]
		seen = append(seen, *m)
		who := personOf(m.Sender)
		if m.Sender == me {
			continue // your own messages are never judged
		}
		if hidden[who] {
			silenced[m.ID] = true
			continue
		}
		if !strings.Contains(strings.ToLower(m.Body), "bitcoin") {
			continue
		}
		for j := range c.filters {
			if settled[who] || !c.fires(&c.filters[j], seen, m) {
				continue
			}
			if slices.ContainsFunc(c.wrote, func(id string) bool { return personOf(id) == who }) {
				continue
			}
			settled[who], hidden[who] = true, true
			silenced[m.ID] = true
			marked = append(marked, m.Sender)
		}
	}
	slices.Sort(marked)
	return marked, silenced
}

// fires is whether f's sender moves fire for m, given everything seen up to it.
func (c personCase) fires(f *config.Filter, seen []domain.Message, m *domain.Message) bool {
	moves := map[string]bool{}
	for _, move := range f.Moves {
		moves[move] = true
	}
	window, _ := time.ParseDuration(f.Window)
	{
		first, total, caught := true, 0, 0
		rooms := map[domain.RoomID]bool{m.RoomID: true}
		for i := range seen {
			s := &seen[i]
			if s.Sender != m.Sender {
				continue
			}
			if s.ID != m.ID {
				first = false
			}
			if s.Timestamp.Before(m.Timestamp.Add(-window)) {
				continue
			}
			total++
			if strings.Contains(strings.ToLower(s.Body), "bitcoin") {
				caught++
				if strings.EqualFold(strings.TrimSpace(s.Body), strings.TrimSpace(m.Body)) {
					rooms[s.RoomID] = true
				}
			}
		}
		return (moves[config.FilterMoveSenderFirst] && first) ||
			(moves[config.FilterMoveSenderSpread] && len(rooms) >= f.Spread) ||
			(moves[config.FilterMoveSenderRatio] && total >= f.Floor && float64(caught) >= f.Ratio*float64(total))
	}
}

// Whatever the stream, the sender moves mark exactly whom the rules name: never a
// person released (by any of their IDs) or written to, and the message that marks
// them does not notify, nor does anything of theirs after.
func TestTheSenderMovesMarkExactly(t *testing.T) {
	t.Parallel()

	for seed := range uint64(300) {
		rng := rand.New(rand.NewPCG(seed, 0x7377)) // #nosec G404 -- reproducible
		now := time.Now()
		c := genPersonCase(rng, now)
		src := &personBackend{spamBackend: spamWorld(), wrote: c.wrote}
		for _, room := range personRooms[1:] {
			src.rooms = append(src.rooms, domain.Room{ID: room, Name: string(room)})
		}
		for _, id := range c.released {
			src.people = append(src.people, domain.PersonVerdict{Person: id, Tag: "spam", Released: true, At: now})
		}
		for _, m := range c.old {
			src.cache(m)
		}
		n := personNotifier(t, spamConfig(withFilters(c.filters...)), src)
		dir := domain.NewDirectory(nil, []domain.PersonLink{{Source: "book", ID: puppet, Other: native}})
		n.UseDirectory(func(context.Context) (domain.Directory, error) { return dir, nil })
		n.UseHidden(func(_ context.Context, m domain.Message) (bool, error) {
			return slices.ContainsFunc(src.marked(), func(id string) bool { return personOf(id) == personOf(m.Sender) }), nil
		})
		want, silenced := c.oracle()
		for _, m := range c.stream {
			src.cache(m)
			_, notified := n.Deliver(context.Background(), m)
			if silenced[m.ID] && notified {
				t.Fatalf("seed %d: %s from %s notified after its sender was marked\n%+v", seed, m.ID, m.Sender, c)
			}
		}
		if got := src.marked(); !slices.Equal(got, want) {
			t.Fatalf("seed %d: marked %v, want %v\nfilters %+v wrote %v released %v\nold %v\nstream %v",
				seed, got, want, c.filters, c.wrote, c.released, c.old, c.stream)
		}
	}
}

// A read the sender moves need that fails marks nobody: unknown never reads as "their
// first message" or "nobody you wrote to". The message still notifies.
func TestAFailedReadMarksNobody(t *testing.T) {
	t.Parallel()

	first := config.Filter{Name: "first", Tag: "Spam", Words: []string{"*bitcoin*"}, Moves: []string{config.FilterMoveSenderFirst}}
	for _, read := range []string{"first", "window", "wrote", "directory"} {
		src := &personBackend{spamBackend: spamWorld(), readErr: errors.New("cache closed"), failing: map[string]bool{read: true}}
		// A release on another person makes the directory needed to tell them apart.
		src.people = []domain.PersonVerdict{{Person: "matrix:@s3:example.org", Tag: "spam", Released: true}}
		n := personNotifier(t, spamConfig(withFilters(first)), src)
		n.UseLogger(slog.New(slog.DiscardHandler))
		n.UseDirectory(func(context.Context) (domain.Directory, error) {
			return domain.Directory{}, src.failed("directory")
		})
		m := msg(chatRm, "matrix:@s1:example.org", "buy bitcoin now")
		src.cache(m)
		if _, notified := n.Deliver(context.Background(), m); !notified {
			t.Errorf("%s failing: the message was silenced", read)
		}
		if got := src.marked(); len(got) != 0 {
			t.Errorf("%s failing: marked %v", read, got)
		}
	}
}

// rescanWant is whom a rescan of everything cached should mark: anyone not released
// or written to, at a caught message whose history up to it fires a sender move other
// than sender_first.
func (c personCase) rescanWant() []string {
	settled := map[string]bool{}
	for _, id := range c.released {
		settled[personOf(id)] = true
	}
	all := slices.Concat(c.old, c.stream)
	slices.SortStableFunc(all, func(a, b domain.Message) int { return a.Timestamp.Compare(b.Timestamp) })
	var out []string
	for i := range all {
		m := &all[i]
		who := personOf(m.Sender)
		if m.Sender == me || settled[who] || slices.Contains(out, m.Sender) || !strings.Contains(strings.ToLower(m.Body), "bitcoin") ||
			slices.ContainsFunc(c.wrote, func(id string) bool { return personOf(id) == who }) {
			continue
		}
		for j := range c.filters {
			f := c.filters[j]
			f.Moves = slices.DeleteFunc(slices.Clone(f.Moves), func(move string) bool { return move == config.FilterMoveSenderFirst })
			if c.fires(&f, all[:i+1], m) {
				out = append(out, m.Sender)
				break
			}
		}
	}
	slices.Sort(out)
	return out
}

// A rescan of what is cached finds exactly whom the sender moves would have marked, as
// things stood at each caught message, never by sender_first; applying it to some marks
// only those, and a second rescan finds them settled.
func TestARescanFindsThePeopleTheRulesName(t *testing.T) {
	t.Parallel()

	for seed := range uint64(300) {
		rng := rand.New(rand.NewPCG(seed, 0x7273)) // #nosec G404 -- reproducible
		now := time.Now()
		c := genPersonCase(rng, now)
		src := &personBackend{spamBackend: spamWorld(), wrote: c.wrote}
		for _, room := range personRooms[1:] {
			src.rooms = append(src.rooms, domain.Room{ID: room, Name: string(room)})
		}
		for _, id := range c.released {
			src.people = append(src.people, domain.PersonVerdict{Person: id, Tag: "spam", Released: true, At: now})
		}
		for _, m := range slices.Concat(c.old, c.stream) {
			src.cache(m)
			src.timeline[m.RoomID] = append(src.timeline[m.RoomID], m)
		}
		n := personNotifier(t, spamConfig(withFilters(c.filters...)), src)
		dir := domain.NewDirectory(nil, []domain.PersonLink{{Source: "book", ID: puppet, Other: native}})
		n.UseDirectory(func(context.Context) (domain.Directory, error) { return dir, nil })
		ctx := context.Background()
		found, err := n.RescanPeople(ctx, time.Time{}, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, f := range found {
			got = append(got, f.Verdict.Person)
		}
		slices.Sort(got)
		if want := c.rescanWant(); !slices.Equal(got, want) {
			t.Fatalf("seed %d: rescan found %v, want %v\nfilters %+v wrote %v released %v\nold %v\nstream %v",
				seed, got, want, c.filters, c.wrote, c.released, c.old, c.stream)
		}
		if len(got) == 0 {
			continue
		}
		if _, err := n.RescanPeople(ctx, time.Time{}, true, got[:1]); err != nil {
			t.Fatal(err)
		}
		if marked := src.marked(); !slices.Equal(marked, got[:1]) {
			t.Fatalf("seed %d: applying %v marked %v", seed, got[:1], marked)
		}
		// The marked person is settled by every ID: the number and its puppet are one.
		others := slices.DeleteFunc(slices.Clone(got), func(id string) bool { return personOf(id) == personOf(got[0]) })
		again, _ := n.RescanPeople(ctx, time.Time{}, false, nil)
		if len(again) != len(others) {
			t.Fatalf("seed %d: after marking %s, the rescan found %d, want %v", seed, got[0], len(again), others)
		}
	}
}
