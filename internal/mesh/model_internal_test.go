package mesh

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	mrand "math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// The model's size: runs of modelSteps steps over three to five replicas, one per seed.
const (
	modelSeeds = 16
	modelSteps = 120
)

// TestEveryReplicaConvergesOverAnyInterleaving is the mesh's class test (Plan step 2's
// failure review). Its generator interleaves writes on every replica (devices paired
// and unpaired, accounts published, a bridge renamed, a bridge removed, two bridges
// removing each other at once) with syncs over a changing partition, whose frames are
// duplicated, reordered (ops split and shuffled) and replayed in later sessions, cut
// mid-batch, and delivered to a relay that restarts mid-batch; clocks skewed within
// and past the bound; a replica dormant past the horizon while the others compact; and
// an origin that signs two ops under one dot. Once the partition heals and the clocks
// are set right (and the fork's two keys are written again, by hand as it were), every
// bridge still in the mesh holds the same entries; and no replica ever syncs directly
// with a bridge every member holds removed. Every replica keeps a cache following its
// store, and reads are made, reported and kept throughout (reads_internal_test.go).
func TestEveryReplicaConvergesOverAnyInterleaving(t *testing.T) {
	t.Parallel()
	for seed := range uint64(modelSeeds) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			m := newModel(t, seed)
			for range modelSteps {
				m.step()
			}
			m.settle()
		})
	}
}

// A snapshot holding a write from past the clock bound is not taken early, as an op is
// not: refused until this bridge's clock gets there, then taken whole.
func TestASnapshotFromTheFutureWaitsForTheClock(t *testing.T) {
	t.Parallel()
	m := newModel(t, 0)
	a, b := m.r[0], m.r[1]
	_, id := newDeviceID(t)
	m.must(a.store.ChangeRoster(m.ctx, a.key, []domain.Device{{ID: id, Name: "laptop", Role: domain.RoleDevice}}, nil))
	written := time.Now()
	a.store.mu.Lock()
	snap := a.store.state.Snapshot(a.store.clock)
	a.store.mu.Unlock()
	paired := func() bool {
		members, _, err := b.store.Roster()
		m.must(err)
		return slices.ContainsFunc(members, func(d domain.Device) bool { return d.ID == id })
	}
	if _, err := b.store.merge(m.ctx, snap, written.Add(-2*MaxClockGap)); !errors.Is(err, errAhead) || paired() {
		t.Fatalf("a snapshot two bounds ahead: %v, paired %v; want %v and nothing taken", err, paired(), errAhead)
	}
	if _, err := b.store.merge(m.ctx, snap, written); err != nil || !paired() {
		t.Fatalf("the same snapshot once the clock got there: %v, paired %v", err, paired())
	}
}

// modelReplica is one bridge of the model: its key and its store.
type modelReplica struct {
	key   *ecdsa.PrivateKey
	id    devicekey.ID
	path  string
	dbKey []byte
	store *Store
	skew  time.Duration // its clock less the model's
	reads replicaReads  // its cache, and what the model knows of it
}

type model struct {
	t   *testing.T
	ctx context.Context
	rng *mrand.Rand
	log *slog.Logger
	r   []*modelReplica
	now time.Time
	// cut are the pairs that cannot reach each other now.
	cut map[[2]int]bool
	// replay are frames kept to be delivered again, later, to a replica.
	replay map[int][]*meshv1.SyncFrame
	// devices counts the devices paired, for fresh names; deviceIDs are their IDs.
	devices   int
	deviceIDs map[string]bool
	// removals counts the bridges removed; forger is the replica that signed two ops
	// under one dot (-1: none yet) at forkSeq; dormant is the replica sent past the
	// horizon (-1: none yet).
	removals int
	forger   int
	forkSeq  uint64
	dormant  int
	// rd is what the model knows of reads (reads_internal_test.go).
	rd modelReads
}

// skews are the clock offsets a replica may run at: within the bound of every other,
// and past it.
var skews = []time.Duration{0, 0, 0, 20 * time.Second, -25 * time.Second, 45 * time.Second, -50 * time.Second, 90 * time.Second, -3 * time.Minute}

func newModel(t *testing.T, seed uint64) *model {
	m := &model{
		t: t, ctx: t.Context(), rng: mrand.New(mrand.NewPCG(seed, 2)), log: slog.New(slog.DiscardHandler),
		now: time.Now(), cut: map[[2]int]bool{}, replay: map[int][]*meshv1.SyncFrame{}, forger: -1, dormant: -1,
		deviceIDs: map[string]bool{},
	}
	n := 3 + m.rng.IntN(3)
	dir := t.TempDir()
	for i := range n {
		key, err := devicekey.Generate()
		if err != nil {
			t.Fatal(err)
		}
		id, err := devicekey.IDOf(key.Public())
		if err != nil {
			t.Fatal(err)
		}
		r := &modelReplica{key: key, id: id, path: filepath.Join(dir, fmt.Sprintf("mesh-%d.db", i)), dbKey: make([]byte, sealeddb.KeyLen)}
		if _, err := rand.Read(r.dbKey); err != nil {
			t.Fatal(err)
		}
		r.store = m.open(r)
		m.r = append(m.r, r)
	}
	// The first is the genesis; it adds the others as bridges, and each joins (adopt).
	first := m.r[0]
	m.must(first.store.ChangeRoster(m.ctx, first.key, []domain.Device{{ID: first.id.String(), Role: domain.RoleBridge}}, nil))
	for i, r := range m.r[1:] {
		der, err := x509.MarshalPKIXPublicKey(r.key.Public())
		if err != nil {
			t.Fatal(err)
		}
		m.must(first.store.Bridge(m.ctx, first.key, domain.Device{ID: r.id.String(), Name: fmt.Sprint("bridge", i+1)},
			der, []string{"192.0.2.1:7443"}))
		first.store.mu.Lock()
		snap := first.store.state.Snapshot(first.store.clock)
		first.store.mu.Unlock()
		if _, err := r.store.adopt(m.ctx, snap, r.key, r.id); err != nil {
			t.Fatalf("replica %d joins: %v", i+1, err)
		}
	}
	m.openCaches()
	return m
}

func (m *model) open(r *modelReplica) *Store {
	s, err := Open(m.ctx, r.path, r.dbKey)
	if err != nil {
		m.t.Fatal(err)
	}
	m.t.Cleanup(func() { _ = s.Close() })
	return s
}

func (m *model) must(err error) {
	m.t.Helper()
	if err != nil {
		m.t.Fatal(err)
	}
}

// at is replica i's clock now.
func (m *model) at(i int) time.Time { return m.now.Add(m.r[i].skew) }

// step is one step of the generator.
func (m *model) step() {
	m.now = m.now.Add(time.Duration(m.rng.IntN(15)) * time.Second)
	m.rd.step++
	switch n := m.rng.IntN(130); {
	case n < 30:
		m.write(m.rng.IntN(len(m.r)))
	case n < 70:
		i, j := m.pair()
		m.exchange(i, j, true)
	case n < 78:
		m.partition()
	case n < 84:
		m.r[m.rng.IntN(len(m.r))].skew = skews[m.rng.IntN(len(skews))]
	case n < 88:
		m.restart(m.rng.IntN(len(m.r)))
	case n < 92:
		i := m.rng.IntN(len(m.r))
		m.must(m.r[i].store.compact(m.ctx, m.at(i)))
	case n < 95:
		m.equivocate()
	case n < 97:
		m.sleepPastTheHorizon()
	case n < 100:
		m.removeEachOther()
	case n < 108:
		m.networkReport(m.rng.IntN(len(m.r)))
	case n < 110:
		m.openCache(m.rng.IntN(len(m.r))) // rebuilt under a mesh that holds positions
	default:
		// A read, more often than the other writes: one while the follower lags is the one
		// that must not write a position back.
		if i := m.rng.IntN(len(m.r)); m.member(i) && i != m.forger {
			m.read(i, m.rng.IntN(4) == 0)
		}
	}
	m.checkReads(m.followSome())
	m.checkRemovedAreRefused()
}

// pair is two distinct replicas.
func (m *model) pair() (int, int) {
	i := m.rng.IntN(len(m.r))
	j := (i + 1 + m.rng.IntN(len(m.r)-1)) % len(m.r)
	return i, j
}

// partition cuts a random set of pairs, never every pair of the first replica's.
func (m *model) partition() {
	clear(m.cut)
	for i := range m.r {
		for j := i + 1; j < len(m.r); j++ {
			if m.rng.IntN(3) == 0 && i != 0 {
				m.cut[[2]int{i, j}] = true
			}
		}
	}
	if m.dormant >= 0 {
		m.isolate(m.dormant)
	}
}

func (m *model) isolate(d int) {
	for i := range m.r {
		if i != d {
			m.cut[[2]int{min(i, d), max(i, d)}] = true
		}
	}
}

// member reports whether replica i is a bridge of its own roster.
func (m *model) member(i int) bool { return m.lists(i, i) }

// lists reports whether replica i's roster lists replica j as a bridge.
func (m *model) lists(i, j int) bool {
	members, _, err := m.r[i].store.Roster()
	m.must(err)
	return slices.ContainsFunc(members, func(d domain.Device) bool { return d.ID == m.r[j].id.String() && d.Role == domain.RoleBridge })
}

// write is one change made on replica i, as its kithd would make it.
func (m *model) write(i int) {
	r := m.r[i]
	if !m.member(i) {
		return
	}
	members, removed, err := r.store.Roster()
	m.must(err)
	var devices, bridges []domain.Device
	for _, d := range members {
		switch {
		case d.ID == r.id.String():
		case d.Role == domain.RoleDevice:
			devices = append(devices, d)
		default:
			bridges = append(bridges, d)
		}
	}
	var unpaired []string
	for _, id := range removed {
		if m.deviceIDs[id] {
			unpaired = append(unpaired, id)
		}
	}
	// A forger writes only device pairings after its fork: what it wrote there differs
	// between replicas, and only a key someone else may write again converges.
	n := m.rng.IntN(18)
	if i == m.forger {
		n = 0
	}
	switch {
	case n < 4 || (n < 7 && len(devices) == 0) || (n == 7 && len(unpaired) == 0) || (n == 10 && len(bridges) == 0):
		m.devices++
		_, id := newDeviceID(m.t)
		m.deviceIDs[id] = true
		m.must(r.store.ChangeRoster(m.ctx, r.key, []domain.Device{{
			ID: id, Name: fmt.Sprint("d", m.devices), Role: domain.RoleDevice, AddedAt: time.Unix(1_800_000_000, 0).UTC(), AddedBy: r.id.String(),
		}}, nil))
	case n < 6:
		d := devices[m.rng.IntN(len(devices))]
		m.must(r.store.ChangeRoster(m.ctx, r.key, nil, []string{d.ID}))
	case n == 6:
		d := devices[m.rng.IntN(len(devices))]
		d.Name = fmt.Sprint("renamed", m.rng.IntN(100))
		m.must(r.store.ChangeRoster(m.ctx, r.key, []domain.Device{d}, nil))
	case n == 7:
		// Paired again after an unpairing: an add that has seen the removal.
		id := unpaired[m.rng.IntN(len(unpaired))]
		m.must(r.store.ChangeRoster(m.ctx, r.key, []domain.Device{{ID: id, Name: fmt.Sprint("again", m.rng.IntN(100)), Role: domain.RoleDevice}}, nil))
	case n < 10:
		var accounts []domain.MeshAccount
		for k := range m.rng.IntN(3) {
			accounts = append(accounts, domain.MeshAccount{Network: []string{"whatsapp", "telegram", "slack"}[k], ID: fmt.Sprint("acct", m.rng.IntN(4))})
		}
		m.must(r.store.PublishAccounts(m.ctx, r.key, accounts))
	case n == 10:
		b := bridges[m.rng.IntN(len(bridges))]
		b.Name = fmt.Sprint("renamed", m.rng.IntN(100))
		m.must(r.store.ChangeRoster(m.ctx, r.key, []domain.Device{b}, nil))
	case n == 12:
		// A chat merged here: bridges merging the same chat at once, and (as no bridge
		// would, but the mesh must still agree) naming different rooms.
		aliases := map[domain.RoomID]domain.RoomID{}
		for range 1 + m.rng.IntN(2) {
			from := domain.RoomID(fmt.Sprintf("whatsapp:15550100/8800%d@lid", m.rng.IntN(3)))
			aliases[from] = domain.RoomID(fmt.Sprintf("whatsapp:15550100/1555%d@s.whatsapp.net", m.rng.IntN(2)))
			m.rd.named[from] = append(m.rd.named[from], aliases[from])
		}
		m.must(r.store.PublishAliases(m.ctx, r.key, aliases))
	case n > 12 && n < 16:
		m.read(i, n == 15)
	case n > 15:
		m.mark(i)
	default:
		target := m.rng.IntN(len(m.r))
		if target == 0 || m.removals >= 2 || !slices.ContainsFunc(members, func(d domain.Device) bool { return d.ID == m.r[target].id.String() }) {
			return
		}
		m.removals++
		m.must(r.store.ChangeRoster(m.ctx, r.key, nil, []string{m.r[target].id.String()}))
	}
}

// removeEachOther has two bridges remove each other, neither having seen the other's.
func (m *model) removeEachOther() {
	i, j := m.pair()
	if i == 0 || j == 0 || m.removals > 0 || !m.member(i) || !m.member(j) || !m.lists(i, j) || !m.lists(j, i) {
		return
	}
	m.removals += 2
	m.must(m.r[i].store.ChangeRoster(m.ctx, m.r[i].key, nil, []string{m.r[j].id.String()}))
	m.must(m.r[j].store.ChangeRoster(m.ctx, m.r[j].key, nil, []string{m.r[i].id.String()}))
}

// equivocate has a replica sign two ops under one dot: one it keeps and sends as any
// other, the other handed to another replica as if relayed.
func (m *model) equivocate() {
	e := 1 + m.rng.IntN(len(m.r)-1)
	if m.forger >= 0 || !m.member(e) {
		return
	}
	r := m.r[e]
	r.store.mu.Lock()
	before := r.store.state.Vector
	r.store.mu.Unlock()
	m.devices++
	_, kept := newDeviceID(m.t)
	_, other := newDeviceID(m.t)
	m.must(r.store.ChangeRoster(m.ctx, r.key, []domain.Device{{ID: kept, Name: fmt.Sprint("d", m.devices), Role: domain.RoleDevice}}, nil))
	seq := before[r.id].Seq + 1
	held := r.store.opAt(m.ctx, nil, r.id, seq)
	if held == nil {
		m.t.Fatal("the forger's op is not kept")
	}
	m.devices++
	value, err := crdt.Member{Name: fmt.Sprint("d", m.devices), Role: crdt.RoleDevice}.Encode()
	m.must(err)
	twin := crdt.Op{Origin: r.id, Seq: seq, Clock: held.Clock, Context: held.Context, Writes: []crdt.Write{{Key: crdt.RosterKey(mustID(m.t, other)), Value: value}}}
	m.must(twin.Sign(r.key))
	m.forger, m.forkSeq = e, seq
	to := (e + 1 + m.rng.IntN(len(m.r)-1)) % len(m.r)
	if _, err := m.r[to].store.receive(m.ctx, []crdt.Op{twin}, m.at(to)); err != nil {
		m.t.Fatal(err)
	}
}

// sleepPastTheHorizon sends a replica dormant: cut off past the horizon, while the
// others go on writing, syncing and compacting, so it comes back to logs that no longer
// hold what it lacks.
func (m *model) sleepPastTheHorizon() {
	d := 1 + m.rng.IntN(len(m.r)-1)
	if m.dormant >= 0 {
		return
	}
	m.dormant = d
	m.isolate(d)
	m.write(d) // an unsent write of its own
	m.now = m.now.Add(Horizon + 24*time.Hour)
	for range 3 * len(m.r) {
		m.write(m.rng.IntN(len(m.r)))
		i, j := m.pair()
		m.exchange(i, j, false)
	}
	for i := range m.r {
		if i != d {
			m.must(m.r[i].store.compact(m.ctx, m.at(i)))
		}
	}
	m.dormant = -1
	m.partition()
}

// restart closes replica i's store and opens it again: what it held back is gone.
func (m *model) restart(i int) {
	m.must(m.r[i].store.Close())
	m.r[i].store = m.open(m.r[i])
	m.reopened(i)
}

// exchange is one sync session between i and j, as Sync runs it: refused as either
// side would refuse it, then a Hello each way, then what each lacks, every frame
// answered as it arrives. With faults, frames are duplicated, split and shuffled, kept
// to be replayed later, and the session cut, the receiver's store restarting mid-batch
// at times.
func (m *model) exchange(i, j int, faults bool) {
	if m.cut[[2]int{min(i, j), max(i, j)}] {
		return
	}
	a, b := m.r[i], m.r[j]
	if a.store.stillPaired(b.id) != nil || b.store.stillPaired(a.id) != nil {
		return
	}
	ab, ba := make(chan *meshv1.SyncFrame, 1), make(chan *meshv1.SyncFrame, 1)
	var hello sync.WaitGroup
	var fromA crdt.Vector
	var errB error
	hello.Go(func() { fromA, errB = b.store.greet(m.ctx, b.id, a.id, pipe{in: ab, out: ba}, m.at(j)) })
	fromB, errA := a.store.greet(m.ctx, a.id, b.id, pipe{in: ba, out: ab}, m.at(i))
	hello.Wait()
	if errA != nil || errB != nil {
		return // a clock past the bound: refused both ways
	}
	toB, toA := &queue{}, &queue{}
	for _, side := range []struct {
		s      *Store
		out    *queue
		theirs crdt.Vector
	}{{a.store, toB, fromB}, {b.store, toA, fromA}} {
		m.must(sendOps(side.out, side.s.headProofs(m.ctx, side.theirs)))
		if _, err := side.s.sendMissing(m.ctx, side.out, side.theirs); err != nil {
			m.t.Fatal(err)
		}
	}
	ends := []struct {
		to, from int
		in, back *queue
		snap     *crdt.Snapshot
	}{{j, i, toB, toA, nil}, {i, j, toA, toB, nil}}
	if faults {
		for e := range ends {
			ends[e].in.frames = append(m.replay[ends[e].to], ends[e].in.frames...)
			delete(m.replay, ends[e].to)
		}
	}
	for len(toA.frames)+len(toB.frames) > 0 {
		for e := range ends {
			end := &ends[e]
			if len(end.in.frames) == 0 {
				continue
			}
			f := end.in.frames[0]
			end.in.frames = end.in.frames[1:]
			if faults {
				switch m.fault(end.from, end.to, f, end.in) {
				case cutHere:
					return
				case passOn:
					continue
				case deliver:
				}
			}
			r := m.r[end.to]
			before := m.dots(end.to)
			var err error
			end.snap, err = r.store.take(m.ctx, r.key, m.r[end.from].id, f, end.snap, end.back, m.log, m.at(end.to))
			if errors.Is(err, errAhead) {
				return // the session ends; the next sends the snapshot again
			}
			m.must(err)
			m.checkNothingFromTheFuture(end.to, before)
		}
	}
}

// What becomes of a frame on its way.
type fate int

const (
	deliver fate = iota // it arrives
	passOn              // it does not arrive now; what replaced it is queued
	cutHere             // the session ends here
)

// fault does what may go wrong with frame f on its way from replica from to replica
// to, the rest of the session's frames in in.
func (m *model) fault(from, to int, f *meshv1.SyncFrame, in *queue) fate {
	switch n := m.rng.IntN(100); {
	case n < 3:
		return cutHere
	case n < 6 && len(f.GetOps().GetOps()) > 1:
		// The relay restarts mid-batch: some of the ops land, then it is gone.
		ops := f.GetOps().GetOps()
		part := &meshv1.SyncFrame{Frame: &meshv1.SyncFrame_Ops{Ops: &meshv1.Ops{Ops: ops[:m.rng.IntN(len(ops))]}}}
		r := m.r[to]
		_, err := r.store.take(m.ctx, r.key, m.r[from].id, part, nil, &queue{}, m.log, m.at(to))
		m.must(err)
		m.restart(to)
		return cutHere
	case n < 20 && (f.GetOps() != nil || f.GetAck() != nil):
		in.frames = slices.Insert(in.frames, m.rng.IntN(len(in.frames)+1), cloneFrame(f)) // again, later
	case n < 30 && f.GetOps() != nil:
		m.replay[to] = append(m.replay[to], cloneFrame(f)) // again, in a later session
	case n < 45 && len(f.GetOps().GetOps()) > 1:
		// Split into one op per frame, shuffled.
		var split []*meshv1.SyncFrame
		for _, op := range f.GetOps().GetOps() {
			split = append(split, &meshv1.SyncFrame{Frame: &meshv1.SyncFrame_Ops{Ops: &meshv1.Ops{Ops: []*meshv1.Op{op}}}})
		}
		m.rng.Shuffle(len(split), func(x, y int) { split[x], split[y] = split[y], split[x] })
		in.frames = append(split, in.frames...)
		return passOn
	}
	return deliver
}

func cloneFrame(f *meshv1.SyncFrame) *meshv1.SyncFrame {
	c, _ := proto.Clone(f).(*meshv1.SyncFrame) // a SyncFrame's clone is one
	return c
}

// dots are the ops whose writes replica i holds.
func (m *model) dots(i int) map[crdt.Dot]bool {
	s := m.r[i].store
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[crdt.Dot]bool{}
	for _, entries := range s.state.Entries {
		for _, e := range entries {
			out[e.Dot] = true
		}
	}
	return out
}

// checkNothingFromTheFuture holds that replica i took no other replica's op (one not
// among before) whose clock is more than MaxClockGap past its own: such an op waits
// until its clock gets there.
func (m *model) checkNothingFromTheFuture(i int, before map[crdt.Dot]bool) {
	s := m.r[i].store
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, entries := range s.state.Entries {
		for _, e := range entries {
			if !before[e.Dot] && e.Dot.Origin != m.r[i].id && e.Clock.Time().After(m.at(i).Add(MaxClockGap)) {
				m.t.Fatalf("replica %d took %s's write of %s from %s ahead of its clock", i, e.Dot.Origin, key, e.Clock.Time().Sub(m.at(i)))
			}
		}
	}
}

// checkRemovedAreRefused holds that once every member holds a bridge's removal, no
// member syncs with it: its new ops reach no one directly.
func (m *model) checkRemovedAreRefused() {
	for k, gone := range m.r {
		everyone := true
		for i := range m.r {
			if i == k || !m.member(i) {
				continue
			}
			_, removed, err := m.r[i].store.Roster()
			m.must(err)
			everyone = everyone && slices.Contains(removed, gone.id.String())
		}
		if !everyone {
			continue
		}
		for i := range m.r {
			if i != k && m.member(i) && m.r[i].store.stillPaired(gone.id) == nil {
				m.t.Fatalf("replica %d holds replica %d removed, and would still sync with it", i, k)
			}
		}
	}
}

// settle heals the partition, sets every clock right and syncs every pair until
// nothing changes; then writes again by hand what the fork made differ, settles again,
// and holds every bridge still in the mesh to the same entries.
func (m *model) settle() {
	clear(m.cut)
	m.rd.settling = true
	m.dormant = -1
	for _, r := range m.r {
		r.skew = 0
	}
	m.now = m.now.Add(5 * time.Minute)
	m.quiesce()
	if m.forger >= 0 {
		forged := crdt.Dot{Origin: m.r[m.forger].id, Seq: m.forkSeq}
		// Each pairing the forged ops made is unpaired again (one unpaired already is
		// paired and unpaired again), as a person cleaning up would.
		for i, r := range m.r {
			if i == m.forger || !m.lists(0, i) {
				continue
			}
			var again []domain.Device
			var remove []string
			r.store.mu.Lock()
			roster, err := r.store.state.Roster()
			for key, entries := range r.store.state.Entries {
				id, ok := strings.CutPrefix(key, "roster/")
				if !ok || !slices.ContainsFunc(entries, func(e crdt.Entry) bool { return e.Dot == forged }) {
					continue
				}
				if roster[mustID(m.t, id)].Removed {
					again = append(again, domain.Device{ID: id, Role: domain.RoleDevice})
				}
				remove = append(remove, id)
			}
			r.store.mu.Unlock()
			m.must(err)
			if len(again) > 0 {
				m.must(r.store.ChangeRoster(m.ctx, r.key, again, nil))
			}
			if len(remove) > 0 {
				m.must(r.store.ChangeRoster(m.ctx, r.key, nil, remove))
			}
		}
		m.quiesce()
	}
	var members []int
	for i := range m.r {
		if m.lists(0, i) {
			members = append(members, i)
		}
	}
	if len(members) == 0 || members[0] != 0 {
		m.t.Fatalf("the first replica left the mesh; members %v", members)
	}
	if m.forger >= 0 && m.lists(0, m.forger) && m.forked() {
		m.t.Fatalf("replica %d signed two ops under one dot, was found out, and is still a bridge of the mesh", m.forger)
	}
	want := m.text(0)
	for _, i := range members[1:] {
		if got := m.text(i); got != want {
			m.t.Fatalf("replica %d and replica 0 differ:\n%s", i, diffLines(got, want))
		}
	}
	m.checkReadsSettled(members)
}

// forked reports whether a bridge of the mesh found the forger out (one removed itself
// may not remove it).
func (m *model) forked() bool {
	for i, r := range m.r {
		if !m.member(i) || !m.lists(0, i) {
			continue
		}
		r.store.mu.Lock()
		found := r.store.forked[m.r[m.forger].id]
		r.store.mu.Unlock()
		if found {
			return true
		}
	}
	return false
}

// quiesce syncs every pair of members, without faults, until a round changes nothing.
func (m *model) quiesce() {
	for round := range 2 * len(m.r) {
		m.followAll()
		m.checkReads(nil)
		before := m.texts()
		for i := range m.r {
			for j := i + 1; j < len(m.r); j++ {
				m.exchange(i, j, false)
			}
		}
		if slices.Equal(before, m.texts()) && round > 0 {
			return
		}
	}
	m.t.Fatal("the mesh never settled")
}

func (m *model) texts() []string {
	out := make([]string, len(m.r))
	for i := range m.r {
		out[i] = m.text(i)
	}
	return out
}

// text is replica i's entries and vector, canonical: the forger's hash left out, as
// the two ops under its dot stay where each landed.
func (m *model) text(i int) string {
	s := m.r[i].store
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, key := range slices.Sorted(func(yield func(string) bool) {
		for k := range s.state.Entries {
			if !yield(k) {
				return
			}
		}
	}) {
		for _, e := range s.state.Entries[key] {
			fmt.Fprintf(&b, "%s %s@%d %d %s\n", key, e.Dot.Origin, e.Dot.Seq, e.Clock, e.Value)
		}
	}
	// The merged chats as each reads them, not only the writes it holds.
	aliases := s.state.Aliases()
	for _, from := range slices.Sorted(maps.Keys(aliases)) {
		fmt.Fprintf(&b, "merged %s into %s\n", from, aliases[from])
	}
	origins := make([]devicekey.ID, 0, len(s.state.Vector))
	for o := range s.state.Vector {
		origins = append(origins, o)
	}
	slices.SortFunc(origins, func(x, y devicekey.ID) int { return bytes.Compare(x[:], y[:]) })
	for _, o := range origins {
		h := s.state.Vector[o]
		if m.forger >= 0 && o == m.r[m.forger].id {
			fmt.Fprintf(&b, "vv %s %d\n", o, h.Seq)
			continue
		}
		fmt.Fprintf(&b, "vv %s %d %x\n", o, h.Seq, h.Hash)
	}
	return b.String()
}

// diffLines is the lines only one of got and want has.
func diffLines(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	var b strings.Builder
	for _, l := range g {
		if !slices.Contains(w, l) {
			b.WriteString("+ " + l + "\n")
		}
	}
	for _, l := range w {
		if !slices.Contains(g, l) {
			b.WriteString("- " + l + "\n")
		}
	}
	return b.String()
}

// queue is one direction of a session's frames, kept in order.
type queue struct{ frames []*meshv1.SyncFrame }

func (q *queue) Send(f *meshv1.SyncFrame) error {
	q.frames = append(q.frames, f)
	return nil
}

func (q *queue) Receive() (*meshv1.SyncFrame, error) {
	f := q.frames[0]
	q.frames = q.frames[1:]
	return f, nil
}

// pipe is both directions of a Hello, one frame each way.
type pipe struct{ in, out chan *meshv1.SyncFrame }

func (p pipe) Send(f *meshv1.SyncFrame) error {
	p.out <- f
	return nil
}

func (p pipe) Receive() (*meshv1.SyncFrame, error) { return <-p.in, nil }

func newDeviceID(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := devicekey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id, err := devicekey.IDOf(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	return key, id.String()
}

func mustID(t *testing.T, s string) devicekey.ID {
	t.Helper()
	id, err := devicekey.ParseID(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
