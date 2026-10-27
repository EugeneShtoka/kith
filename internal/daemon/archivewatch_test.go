package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// The rooms the push tests file (WhatsApp groups): each in a space named after it.
const (
	roomR  domain.RoomID = "whatsapp:15550100/R@g.us"
	roomR2 domain.RoomID = "whatsapp:15550100/R2@g.us"
	roomR3 domain.RoomID = "whatsapp:15550100/R3@g.us"
)

// network is a fake network archive: what SetArchived was asked, and what it holds.
type network struct {
	mu       sync.Mutex
	archived map[domain.RoomID]bool
	calls    []string // "+room" archived, "-room" unarchived, as asked
	refuse   map[domain.RoomID]bool
	// block, when set, holds the first call until it is closed; entered says it began.
	block, entered chan struct{}
}

var errRefused = errors.New("the network refused")

func newNetwork() *network {
	return &network{archived: map[domain.RoomID]bool{}, refuse: map[domain.RoomID]bool{}}
}

func (n *network) set(_ context.Context, room domain.RoomID, archived bool) error {
	n.mu.Lock()
	block, entered := n.block, n.entered
	n.block = nil
	n.mu.Unlock()
	if block != nil {
		close(entered)
		<-block
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	sign := "-"
	if archived {
		sign = "+"
	}
	n.calls = append(n.calls, sign+string(room))
	if n.refuse[room] {
		return errRefused
	}
	n.archived[room] = archived
	return nil
}

// took returns the calls since the last took.
func (n *network) took() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	calls := n.calls
	n.calls = nil
	return calls
}

func (n *network) holds(room domain.RoomID) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.archived[room]
}

// pushWorld is a config of profiles A and B that may archive rooms by their own rule,
// and the rooms as kithd's scope sees them.
type pushWorld struct {
	// holds is, per profile, the rooms its archive rule names; pushes whether it pushes.
	holds  map[string][]domain.RoomID
	pushes map[string]bool
	// picked is filed into the shared archive by hand.
	picked []domain.RoomID
	// gone is the profiles a reload took away.
	gone map[string]bool
}

func (w pushWorld) config() config.Config {
	cfg := config.Config{Tags: []config.Tag{{Name: "Archived", Rule: []string{"space:Nowhere"}}}}
	for _, id := range w.picked {
		cfg.Tags[0].Picked = append(cfg.Tags[0].Picked, string(id))
	}
	for _, name := range []string{"A", "B"} {
		if w.gone[name] {
			continue
		}
		rule := []string{"space:Nowhere"}
		for _, id := range w.holds[name] {
			rule = append(rule, "space:"+spaceOf(id))
		}
		push := w.pushes[name]
		cfg.Profiles = append(cfg.Profiles, config.Profile{
			Name: name, Include: []string{"*"}, Archive: config.ProfileArchive{Rooms: rule, Push: &push},
		})
	}
	if len(cfg.Profiles) == 0 {
		cfg.Profiles = []config.Profile{{Name: "rest", Include: []string{"*"}}}
	}
	return cfg
}

// spaceOf is the space a test room is in: R for R@g.us.
func spaceOf(id domain.RoomID) string {
	native := string(id)[strings.LastIndex(string(id), "/")+1:]
	return native[:strings.Index(native, "@")]
}

// reload hands the watcher the world's tags and profiles, as Reload does.
func (w pushWorld) reload(t *testing.T, watch *archiveWatch) domain.TagSet {
	t.Helper()
	tags, err := w.load(watch)
	if err != nil {
		t.Fatal(err)
	}
	return tags
}

func (w pushWorld) load(watch *archiveWatch) (domain.TagSet, error) {
	cfg := w.config()
	tags, _, err := setup.Tags(cfg)
	if err != nil {
		return domain.TagSet{}, err
	}
	profiles, err := setup.Profiles(cfg)
	if err != nil {
		return domain.TagSet{}, err
	}
	watch.setPushing(tags, profiles)
	return tags, nil
}

// facts are the rooms as the scope judges them: a room the network archived is in the
// archive (WhatsApp's archive is followed).
func facts(tags domain.TagSet, net *network, rooms ...domain.RoomID) []domain.RoomFacts {
	out := make([]domain.RoomFacts, 0, len(rooms))
	for _, id := range rooms {
		f := domain.RoomFacts{ID: string(id), Protocol: domain.ProtocolWhatsApp, Spaces: []string{spaceOf(id)}}
		if net.holds(id) {
			f.ArchivedIn = "Archived"
		}
		f.Tags = tags.Of(f)
		out = append(out, f)
	}
	return out
}

// Over every order of two profiles starting and stopping to archive R, and of the
// phone archiving and unarchiving it: no call contradicts what the pushing profiles
// hold together; a profile starting to archive R archives it on the network; the last
// pushing profile letting go unarchives it, though the network now reports it archived
// (follow) because kith archived it; and kith never unarchives what the phone archived
// first. With B not pushing, B's archive moves nothing.
func TestPushingProfilesAddUp(t *testing.T) {
	t.Parallel()
	events := []string{"A+", "B+", "A-", "B-", "phone+", "phone-"}
	orders := 0
	for _, pushB := range []bool{true, false} {
		for _, order := range permutations(events) {
			if before(order, "A-", "A+") || before(order, "B-", "B+") || before(order, "phone-", "phone+") {
				continue
			}
			orders++
			if err := runPushOrder(order, pushB); err != nil {
				t.Errorf("B pushing %v, order %v: %v", pushB, order, err)
			}
		}
	}
	if orders != 180 {
		t.Fatalf("ran %d orders, want 90 for each of B pushing and not", orders)
	}
}

func runPushOrder(order []string, pushB bool) error {
	net, watch := newNetwork(), newArchiveWatch()
	w := pushWorld{holds: map[string][]domain.RoomID{}, pushes: map[string]bool{"A": true, "B": pushB}}
	// phoneOn: the phone archived R and has not unarchived it; phoneFirst: it did so
	// while the network did not have R archived, so the archive is the phone's.
	phoneOn, phoneFirst := false, false
	holding := func() bool {
		return len(w.holds["A"]) > 0 || (pushB && len(w.holds["B"]) > 0)
	}
	for _, event := range order {
		was := holding()
		switch event {
		case "A+", "B+":
			w.holds[event[:1]] = []domain.RoomID{roomR}
		case "A-", "B-":
			w.holds[event[:1]] = nil
		case "phone+":
			phoneOn, phoneFirst = true, !net.holds(roomR)
			net.archived[roomR] = true
		case "phone-":
			phoneOn, phoneFirst = false, false
			net.archived[roomR] = false
		}
		tags, err := w.load(watch)
		if err != nil {
			return err
		}
		watch.reconcile(context.Background(), facts(tags, net, roomR), net.set)
		now := holding()
		for _, call := range net.took() {
			switch {
			case call == "-"+string(roomR) && now:
				return fmt.Errorf("after %s, R unarchived while a pushing profile archives it", event)
			case call == "-"+string(roomR) && phoneFirst:
				return fmt.Errorf("after %s, R unarchived though the phone archived it", event)
			case call == "+"+string(roomR) && !now:
				return fmt.Errorf("after %s, R archived while no pushing profile does", event)
			}
		}
		switch {
		case !was && now && !net.holds(roomR) && !strings.HasPrefix(event, "phone"):
			return fmt.Errorf("after %s, a pushing profile archives R and the network does not", event)
		case was && !now && !phoneOn && net.holds(roomR):
			return fmt.Errorf("after %s, no pushing profile archives R and the network still does", event)
		}
	}
	return nil
}

// before reports whether a comes before b in order.
func before(order []string, a, b string) bool {
	return slices.Index(order, a) < slices.Index(order, b)
}

func permutations(items []string) [][]string {
	if len(items) <= 1 {
		return [][]string{slices.Clone(items)}
	}
	var out [][]string
	for i := range items {
		rest := slices.Concat(items[:i], items[i+1:])
		for _, p := range permutations(rest) {
			out = append(out, append([]string{items[i]}, p...))
		}
	}
	return out
}

// A profile pushes only the rooms it holds: its archive rule naming a room outside its
// rooms archives nothing on the network.
func TestAProfilePushesOnlyItsRooms(t *testing.T) {
	t.Parallel()
	net, watch := newNetwork(), newArchiveWatch()
	cfg := pushWorld{holds: map[string][]domain.RoomID{"A": {roomR}}, pushes: map[string]bool{"A": true}}.config()
	cfg.Profiles[0].Include = []string{"space:Elsewhere"}
	tags, _, err := setup.Tags(cfg)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := setup.Profiles(cfg)
	if err != nil {
		t.Fatal(err)
	}
	watch.setPushing(tags, profiles)
	watch.reconcile(t.Context(), facts(tags, net, roomR), net.set)
	if got := net.took(); len(got) != 0 {
		t.Errorf("calls = %v, want none: A does not hold R", got)
	}
}

// A network refusing a push changes nothing kith holds, and the next reconcile asks it
// again, to archive and to unarchive alike.
func TestARefusedPushIsAskedAgain(t *testing.T) {
	t.Parallel()
	net, watch := newNetwork(), newArchiveWatch()
	w := pushWorld{holds: map[string][]domain.RoomID{"A": {roomR}}, pushes: map[string]bool{"A": true}}
	net.refuse[roomR] = true
	for range 2 {
		tags := w.reload(t, watch)
		watch.reconcile(t.Context(), facts(tags, net, roomR), net.set)
	}
	if got := net.took(); !slices.Equal(got, []string{"+" + string(roomR), "+" + string(roomR)}) {
		t.Errorf("calls = %v, want the refused archive asked again", got)
	}
	net.refuse[roomR] = false
	tags := w.reload(t, watch)
	watch.reconcile(t.Context(), facts(tags, net, roomR), net.set)
	if !net.holds(roomR) {
		t.Fatal("R is not archived once the network takes it")
	}

	net.took()
	w.holds["A"] = nil
	net.refuse[roomR] = true
	for range 2 {
		tags = w.reload(t, watch)
		watch.reconcile(t.Context(), facts(tags, net, roomR), net.set)
	}
	if got := net.took(); !slices.Equal(got, []string{"-" + string(roomR), "-" + string(roomR)}) {
		t.Errorf("calls = %v, want the refused unarchive asked again", got)
	}
}

// Filing a room into the archive by hand and out again while the first push is still
// on the network ends with the network as kith holds it: a reconcile never works from
// older facts than one that finished before it.
func TestHandFilingWhileAPushRuns(t *testing.T) {
	t.Parallel()
	net, watch := newNetwork(), newArchiveWatch()
	w := pushWorld{holds: map[string][]domain.RoomID{}, pushes: map[string]bool{"A": true}}
	var mu sync.Mutex
	tags := w.reload(t, watch)
	reads := make(chan struct{}, 8) // each facts read, as the reconciles take them
	read := func(context.Context) ([]domain.RoomFacts, error) {
		mu.Lock()
		defer mu.Unlock()
		reads <- struct{}{}
		return facts(tags, net, roomR), nil
	}
	refile := func(picked []domain.RoomID) {
		mu.Lock()
		w.picked = picked
		tags = w.reload(t, watch)
		mu.Unlock()
		watch.kick(read, net.set)
	}

	net.block, net.entered = make(chan struct{}), make(chan struct{})
	block, entered := net.block, net.entered
	refile([]domain.RoomID{roomR}) // filed in: the push starts and is held
	<-entered
	<-reads
	refile(nil) // filed out while the push is on the network
	// A reconcile reading the filing-out now, before the held push lands, would finish
	// first and be undone by it; give one the chance, then let the push land.
	select {
	case <-reads:
		time.Sleep(50 * time.Millisecond)
	case <-time.After(200 * time.Millisecond):
	}
	close(block)
	waitFor(t, func() bool { return net.asked("+" + string(roomR)) }, "the held push to land")
	settle(t, watch)

	if net.holds(roomR) {
		t.Errorf("R is archived on the network though filed out (calls %v)", net.took())
	}
}

// settle waits for the watcher's reconciles to end.
func settle(t *testing.T, watch *archiveWatch) {
	t.Helper()
	waitFor(t, func() bool {
		watch.mu.Lock()
		defer watch.mu.Unlock()
		return !watch.running && !watch.dirty
	}, "the reconciles to end")
}

// asked reports whether the network was asked call, without taking the calls.
func (n *network) asked(call string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Contains(n.calls, call)
}

// A reload taking away a pushing profile, even while a push is on the network, leaves
// the rooms only it archived as they are: only the person undoes them.
func TestARemovedProfileUnarchivesNothing(t *testing.T) {
	t.Parallel()
	net, watch := newNetwork(), newArchiveWatch()
	w := pushWorld{
		holds:  map[string][]domain.RoomID{"A": {roomR}, "B": {roomR2}},
		pushes: map[string]bool{"A": true, "B": true}, gone: map[string]bool{},
	}
	tags := w.reload(t, watch)
	watch.reconcile(t.Context(), facts(tags, net, roomR, roomR2, roomR3), net.set)
	if !net.holds(roomR) || !net.holds(roomR2) {
		t.Fatalf("archived %v, want R and R2", net.took())
	}

	// A starts archiving R3; while that push is on the network, a reload removes B.
	w.holds["A"] = []domain.RoomID{roomR, roomR3}
	tags = w.reload(t, watch)
	net.block, net.entered = make(chan struct{}), make(chan struct{})
	block, entered := net.block, net.entered
	done := make(chan struct{})
	go func() {
		watch.reconcile(context.Background(), facts(tags, net, roomR, roomR2, roomR3), net.set)
		close(done)
	}()
	<-entered
	w.gone["B"] = true
	tags = w.reload(t, watch)
	close(block)
	<-done
	for range 2 {
		watch.reconcile(t.Context(), facts(tags, net, roomR, roomR2, roomR3), net.set)
	}
	if !net.holds(roomR2) {
		t.Errorf("R2 unarchived after its profile was removed (calls %v)", net.took())
	}
	if !net.holds(roomR3) {
		t.Errorf("R3 not archived (calls %v)", net.took())
	}
}
