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
	n.Synced(time.Now())
	n.Synced(time.Now())
	return n
}

// The generator's people: three Matrix users, and one WhatsApp number seen natively and
// as a bridge puppet, which the directory links.
const (
	puppet = "@whatsapp_15550000001:example.org"
	native = "whatsapp:15550000001@s.whatsapp.net"
)

var personPool = []string{"@s1:example.org", "@s2:example.org", "@s3:example.org", puppet, native}

// personOf is who an ID is, for the oracle: the puppet and the number are one.
func personOf(id string) string {
	if id == puppet {
		return native
	}
	return id
}

var personRooms = []domain.RoomID{chatRm, "!g1:example.org", "!g2:example.org", "!g3:example.org"}

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
		c.old = append(c.old, domain.Message{ID: domain.EventID(fmt.Sprintf("$old%d", i)), RoomID: personRooms[rng.IntN(len(personRooms))],
			Sender: pick(), Body: bodies[rng.IntN(len(bodies))], Timestamp: now.Add(-age)})
	}
	n := 8 + rng.IntN(8)
	for i := range n {
		c.stream = append(c.stream, domain.Message{ID: domain.EventID(fmt.Sprintf("$%d", i)), RoomID: personRooms[rng.IntN(len(personRooms))],
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
		src.people = []domain.PersonVerdict{{Person: "@s3:example.org", Tag: "spam", Released: true}}
		n := personNotifier(t, spamConfig(withFilters(first)), src)
		n.UseLogger(slog.New(slog.DiscardHandler))
		n.UseDirectory(func(context.Context) (domain.Directory, error) {
			return domain.Directory{}, src.failed("directory")
		})
		m := msg(chatRm, "@s1:example.org", "buy bitcoin now")
		src.cache(m)
		if _, notified := n.Deliver(context.Background(), m); !notified {
			t.Errorf("%s failing: the message was silenced", read)
		}
		if got := src.marked(); len(got) != 0 {
			t.Errorf("%s failing: marked %v", read, got)
		}
	}
}
