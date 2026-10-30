package mesh

import (
	"cmp"
	"crypto/rand"
	"crypto/x509"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// The reads of the mesh's class test (Plan step 4's failure review). Each replica keeps
// a cache that follows its store as kithd's does (followReads: what the mesh holds of a
// merged room moved under the room it joined, the chat merged in the cache as its
// network merges it, then every position and each new mark kept), at times lagging it
// for a few steps. The generator makes reads on every replica (a room's and a thread's,
// an older one from a stale window, one while the follower lags, one on a chat's LID
// key before or after its merge reached that replica), marks rooms unread and clears
// them, has networks report reads and marks (an older receipt echoed among them),
// rebuilds caches under the mesh, and gives each replica only some of the rooms. After
// every step it holds that no replica's mesh moves a position back or loses one but to
// a merge, that each cache that looked holds what its mesh holds (filed as the cache
// merged its chats) and what was read or reported there, no further, and nothing of a
// room it does not run, and that a Telegram chat's mark is the later of the mesh's (as
// it arrived) and the one set there; once settled, that no read a member made was lost
// and none is left under a merged chat's key.

// modelRooms are the model's rooms: WhatsApp chats known by a LID and by a number, as
// the aliases the generator writes name them, and two Telegram chats. Invented.
var modelRooms = []domain.RoomID{
	"whatsapp:15550100/88000@lid", "whatsapp:15550100/88001@lid", "whatsapp:15550100/88002@lid",
	"whatsapp:15550100/15550@s.whatsapp.net", "whatsapp:15550100/15551@s.whatsapp.net",
	"telegram:200/300", "telegram:200/301",
}

// modelRoots are where a read is: the room's own timeline, or one of two threads.
var modelRoots = []domain.EventID{"", "root0", "root1"}

// place is a room's timeline, or one of its threads.
type place struct {
	room domain.RoomID
	root domain.EventID
}

// replicaReads is one replica's cache and what the model knows of it.
type replicaReads struct {
	cache *db.Cache
	key   []byte
	// rooms are the rooms the replica runs: its cache holds them, and only them.
	rooms map[domain.RoomID]bool
	// local is the furthest each place was read here or reported by the network here,
	// since the cache was built; cached the furthest the cache held, which it never
	// moves back from.
	local, cached map[place]int64
	// seen is the furthest the mesh held each place: it never moves back.
	seen map[place]int64
	// lastMark is the step a room's mark was last set here, not from the mesh, and
	// localMark what to; arrived is the step each mark's write first was a room's mark
	// here (0: held when the store was opened again, so taken as kept, not owed to the
	// cache); rebuilt the step the cache was last rebuilt at.
	lastMark  map[domain.RoomID]int
	localMark map[domain.RoomID]bool
	arrived   map[crdt.Dot]int
	rebuilt   int
	// lagging is a follower that has not looked for a while.
	lagging bool
}

// modelReads is what the model knows of reads across replicas.
type modelReads struct {
	step int
	// settling is the model healing: every follower moves the reads of merged rooms.
	settling bool
	// made is every read made in kith, by replica; named every room each room was
	// merged into.
	made  map[int][]domain.ReadPosition
	named map[domain.RoomID][]domain.RoomID
}

// openCaches gives each replica its rooms and a cache: the first runs every room, each
// other some.
func (m *model) openCaches() {
	m.rd = modelReads{made: map[int][]domain.ReadPosition{}, named: map[domain.RoomID][]domain.RoomID{}}
	for i, r := range m.r {
		r.reads = replicaReads{key: make([]byte, sealeddb.KeyLen), rooms: map[domain.RoomID]bool{}, arrived: map[crdt.Dot]int{}}
		if _, err := rand.Read(r.reads.key); err != nil {
			m.t.Fatal(err)
		}
		for _, room := range modelRooms {
			if i == 0 || m.rng.IntN(3) > 0 {
				r.reads.rooms[room] = true
			}
		}
		m.openCache(i)
	}
}

// openCache gives replica i a new, empty cache holding its rooms, as one rebuilt.
func (m *model) openCache(i int) {
	r := &m.r[i].reads
	if r.cache != nil {
		m.must(r.cache.Close())
	}
	c, err := db.Open(m.ctx, filepath.Join(m.t.TempDir(), "cache.db"), r.key)
	m.must(err)
	m.t.Cleanup(func() { _ = c.Close() })
	for _, room := range slices.Sorted(maps.Keys(r.rooms)) {
		id := domain.ParseID(string(room))
		m.must(c.AddRooms(m.ctx, domain.AccountRooms(id.Network, id.Account), []domain.Room{{ID: room}}))
	}
	r.cache, r.rebuilt = c, m.rd.step
	r.local, r.cached = map[place]int64{}, map[place]int64{}
	r.lastMark, r.localMark = map[domain.RoomID]int{}, map[domain.RoomID]bool{}
}

// reopened notes the marks replica i's store holds as it is opened again: taken as
// kept, whether or not the cache was given them.
func (m *model) reopened(i int) {
	for _, mark := range marksOf(m.r[i].store) {
		if _, ok := m.r[i].reads.arrived[mark.Dot]; !ok {
			m.r[i].reads.arrived[mark.Dot] = 0
		}
	}
}

// pickRoom is one of the rooms replica i runs, most often one of those prefer picks
// when it runs any; false when it runs none now.
func (m *model) pickRoom(i int, prefer func(domain.RoomID) bool) (domain.RoomID, bool) {
	rooms := slices.Sorted(maps.Keys(m.r[i].reads.rooms))
	if preferred := slices.DeleteFunc(slices.Clone(rooms), func(room domain.RoomID) bool { return !prefer(room) }); len(preferred) > 0 && m.rng.IntN(4) > 0 {
		rooms = preferred
	}
	if len(rooms) == 0 {
		return "", false
	}
	return rooms[m.rng.IntN(len(rooms))], true
}

// telegram picks a Telegram chat: a network that marks chats unread itself.
func telegram(room domain.RoomID) bool {
	return domain.NetworkOf(string(room)) == domain.ProtocolTelegram
}

// behind picks a room whose position replica i's mesh holds further than its cache:
// one read while its follower has not caught up.
func (m *model) behind(i int) func(domain.RoomID) bool {
	r := &m.r[i].reads
	held, err := r.cache.HeldReads(m.ctx, func() []domain.ReadPosition {
		var of []domain.ReadPosition
		for _, room := range modelRooms {
			of = append(of, domain.ReadPosition{Room: room})
		}
		return of
	}())
	m.must(err)
	cached := map[domain.RoomID]int64{}
	for _, p := range held {
		cached[p.Room] = p.At
	}
	ahead := map[domain.RoomID]bool{}
	for _, p := range m.r[i].store.Reads() {
		if p.Root == "" && p.At > cached[p.Room] {
			ahead[p.Room] = true
		}
	}
	return func(room domain.RoomID) bool { return ahead[room] }
}

// readAt is a message's time to read through: mostly the latest, at times one a
// minute or two old, as a window open on older messages reads.
func (m *model) readAt() int64 {
	return m.now.UnixMilli() - int64(m.rng.IntN(3))*int64(m.rng.IntN(60_000))
}

// read is a read made in kith on replica i, of a room or a thread, as kithd makes it:
// the network's adapter keeps it in the cache first (and sends the receipt), then
// kithd writes into the mesh how far the cache holds the place read (readInMesh).
func (m *model) read(i int, thread bool) {
	r := &m.r[i].reads
	room, ok := m.pickRoom(i, m.behind(i))
	if !ok {
		return
	}
	p := place{room: room}
	if thread {
		p.root = modelRoots[1+m.rng.IntN(2)]
	}
	at := m.readAt()
	event := domain.EventID(fmt.Sprint("e", at))
	if p.root == "" {
		m.must(r.cache.SaveUnread(m.ctx, domain.Unread{RoomID: room, ReadEvent: event}, at))
		r.lastMark[room], r.localMark[room] = m.rd.step, false
	} else {
		m.must(r.cache.SaveThreadRead(m.ctx, room, p.root, event, at))
	}
	r.local[p] = max(r.local[p], at)
	held, err := r.cache.HeldReads(m.ctx, []domain.ReadPosition{{Room: room, Root: p.root}})
	m.must(err)
	m.must(m.r[i].store.Read(m.ctx, m.r[i].key, held))
	if mark := marksOf(m.r[i].store)[room]; p.root == "" && mark.Marked {
		m.t.Fatalf("replica %d read %s, and its mesh holds it marked unread still", i, room)
	}
	m.rd.made[i] = append(m.rd.made[i], domain.ReadPosition{Room: room, Root: p.root, Event: event, At: at})
}

// mark marks a room unread on replica i, or clears its mark, as kithd does: on the
// network and in the cache, then in the mesh.
func (m *model) mark(i int) {
	r := &m.r[i].reads
	room, ok := m.pickRoom(i, telegram)
	if !ok {
		return
	}
	marked := m.rng.IntN(2) == 0
	m.must(r.cache.SaveUnread(m.ctx, domain.Unread{RoomID: room, Marked: marked}, 0))
	r.lastMark[room], r.localMark[room] = m.rd.step, marked
	m.must(m.r[i].store.MarkUnread(m.ctx, m.r[i].key, room, marked))
}

// networkReport is a read the network reports on replica i: made on the phone, or the
// echo of a receipt another bridge sent, often an older one; at times with the room's
// mark, set or cleared on the phone. It is kept in the cache alone, never written to
// the mesh.
func (m *model) networkReport(i int) {
	r := &m.r[i].reads
	room, ok := m.pickRoom(i, telegram)
	if !ok {
		return
	}
	at := m.readAt() - int64(m.rng.IntN(120_000))
	marked := telegram(room) && m.rng.IntN(3) == 0
	m.must(r.cache.SaveUnread(m.ctx, domain.Unread{RoomID: room, ReadEvent: domain.EventID(fmt.Sprint("n", at)), Marked: marked}, at))
	p := place{room: room}
	r.local[p], r.lastMark[room], r.localMark[room] = max(r.local[p], at), m.rd.step, marked
}

// followAll has every replica's cache follow its store.
func (m *model) followAll() {
	for i := range m.r {
		m.follow(i)
	}
}

// followSome has the replicas' caches follow their stores but those lagging, and is
// which did: kithd's follower runs on its own, so a cache may lag its mesh for a while,
// reads made there meanwhile.
func (m *model) followSome() []bool {
	followed := make([]bool, len(m.r))
	for i, mr := range m.r {
		if mr.reads.lagging {
			mr.reads.lagging = m.rng.IntN(3) > 0
		} else {
			mr.reads.lagging = m.rng.IntN(4) == 0
		}
		if followed[i] = !mr.reads.lagging; followed[i] {
			m.follow(i)
		}
	}
	return followed
}

// follow is one look of replica i's followReads: the reads of merged rooms moved under
// the room each joined (by a member; a forger writes nothing but its pairings), the
// cache's chats merged as the mesh says (followMerges, on its own), then every position
// and each new mark kept. At times the move ran before a merge arrived and the cache
// merged after it: the cache then keeps a merged room's position under the room it
// joined.
func (m *model) follow(i int) {
	mr := m.r[i]
	r := &mr.reads
	if m.member(i) && i != m.forger && (m.rd.settling || m.rng.IntN(4) > 0) {
		m.must(mr.store.PublishAliases(m.ctx, mr.key, nil))
	}
	aliases := mr.store.Aliases()
	for _, from := range slices.Sorted(maps.Keys(aliases)) {
		into := aliases[from]
		if !r.rooms[from] {
			continue
		}
		_, err := r.cache.MergeRoom(m.ctx, domain.RoomMerge{From: from, Into: into})
		m.must(err)
		delete(r.rooms, from)
		r.rooms[into] = true
		for _, mp := range []map[place]int64{r.local, r.cached} {
			for p, at := range mp {
				if p.room == from {
					q := place{into, p.root}
					mp[q] = max(mp[q], at)
					delete(mp, p)
				}
			}
		}
	}
	_, err := r.cache.KeepReads(m.ctx, mr.store.Reads(), mr.store.TakeMarks())
	m.must(err)
}

// checkReads holds every replica's mesh, and the cache of each that just followed it
// (followed; nil: every one), to what was read, reported and marked so far. A mark
// arrives at a cache as its follower takes it.
func (m *model) checkReads(followed []bool) {
	for i, mr := range m.r {
		m.checkMesh(i)
		if len(followed) > 0 && !followed[i] {
			continue
		}
		for _, mark := range marksOf(mr.store) {
			if _, ok := mr.reads.arrived[mark.Dot]; !ok {
				mr.reads.arrived[mark.Dot] = m.rd.step
			}
		}
		m.checkCache(i)
	}
}

// checkMesh holds that replica i's mesh moved no position back, and dropped none but
// one of a room it holds merged into another (moved there).
func (m *model) checkMesh(i int) {
	r := &m.r[i].reads
	aliases := m.r[i].store.Aliases()
	now := map[place]int64{}
	for _, p := range m.r[i].store.Reads() {
		q := place{p.Room, p.Root}
		if p.At < r.seen[q] {
			m.t.Fatalf("replica %d's mesh moved the read position of %v back from %d to %d", i, q, r.seen[q], p.At)
		}
		now[q] = p.At
	}
	for q := range r.seen {
		if _, held := now[q]; !held && aliases[q.room] == "" {
			m.t.Fatalf("replica %d's mesh lost the read position of %v, a room merged into none", i, q)
		}
	}
	r.seen = now
}

// checkCache holds that replica i's cache holds each place of the rooms it runs as far
// as its mesh does (filed as the cache merged its chats) or as it was read or reported
// there, never back from where it was, no further, and nothing of a room it does not
// run; and that each mark the cache is owed is set.
func (m *model) checkCache(i int) {
	r := &m.r[i].reads
	filedAs, err := r.cache.RoomAliases(m.ctx)
	m.must(err)
	filed := func(room domain.RoomID) domain.RoomID {
		if into, ok := filedAs[room]; ok {
			return into
		}
		return room
	}
	want := maps.Clone(r.cached)
	for _, p := range m.r[i].store.Reads() {
		if q := (place{filed(p.Room), p.Root}); r.rooms[q.room] {
			want[q] = max(want[q], p.At)
		}
	}
	for q, at := range r.local {
		want[q] = max(want[q], at)
	}
	var of []domain.ReadPosition
	for _, room := range modelRooms {
		for _, root := range modelRoots {
			of = append(of, domain.ReadPosition{Room: room, Root: root})
		}
	}
	held, err := r.cache.HeldReads(m.ctx, of)
	m.must(err)
	got := map[place]int64{}
	for _, p := range held {
		got[place{p.Room, p.Root}] = p.At
	}
	places := slices.Collect(maps.Keys(want))
	for q := range got {
		places = append(places, q)
	}
	slices.SortFunc(places, func(a, b place) int { return cmp.Or(cmp.Compare(a.room, b.room), cmp.Compare(a.root, b.root)) })
	for _, q := range places {
		if !r.rooms[q.room] {
			m.t.Fatalf("replica %d's cache holds a read position of %v, a room it does not run", i, q)
		}
		if got[q] != want[q] {
			m.t.Fatalf("replica %d's cache holds %v read to %d; its mesh, its own reads and its past say %d", i, q, got[q], want[q])
		}
	}
	r.cached = got
	unread, err := r.cache.Unread(m.ctx)
	m.must(err)
	cachedMarks := map[domain.RoomID]bool{}
	for _, u := range unread {
		if !r.rooms[u.RoomID] {
			m.t.Fatalf("replica %d's cache holds unread state of %s, a room it does not run", i, u.RoomID)
		}
		cachedMarks[u.RoomID] = u.Marked
	}
	// A WhatsApp chat's mark is filed as the cache merged it; convergence holds its mesh
	// to the others'. A Telegram chat's mark is the later of the mesh's newest, as it
	// arrived, and the one set here: never the mesh's set again over a later one here.
	marks := marksOf(m.r[i].store)
	for room := range r.rooms {
		if domain.NetworkOf(string(room)) != domain.ProtocolTelegram {
			continue
		}
		w, inMesh := marks[room]
		want := false
		switch at := r.arrived[w.Dot]; {
		case inMesh && at > r.rebuilt && at > r.lastMark[room]:
			want = w.Marked
		case r.lastMark[room] > 0:
			want = r.localMark[room]
		case inMesh && at == 0:
			continue // held as the store opened, never given to the cache: what it was before
		}
		if cachedMarks[room] != want {
			m.t.Fatalf("replica %d's cache holds %s marked %v, want %v (mesh %+v arrived at step %d, set here at step %d, rebuilt at %d)",
				i, room, cachedMarks[room], want, w, r.arrived[w.Dot], r.lastMark[room], r.rebuilt)
		}
	}
}

// marksOf is the unread marks s holds, by room.
func marksOf(s *Store) map[domain.RoomID]crdt.Mark {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[domain.RoomID]crdt.Mark{}
	for room, mark := range s.state.Marks() {
		out[domain.RoomID(room)] = mark
	}
	return out
}

// checkReadsSettled holds, once the mesh settled, that each member holds no position
// or mark under a merged room's key (each moved under the room it joined), and every
// read a member made at least as far, under its room or a room it was merged into. (A
// removed replica's reads reach the mesh only through a member that had not yet heard
// of the removal, if any did.)
func (m *model) checkReadsSettled(members []int) {
	for _, i := range members {
		s := m.r[i].store
		aliases := s.Aliases()
		held := map[place]int64{}
		for _, p := range s.Reads() {
			if into, merged := aliases[p.Room]; merged {
				m.t.Fatalf("replica %d holds a read of %s, merged into %s, under its own key", i, p.Room, into)
			}
			held[place{p.Room, p.Root}] = p.At
		}
		for room := range marksOf(s) {
			if into, merged := aliases[room]; merged {
				m.t.Fatalf("replica %d holds the mark of %s, merged into %s, under its own key", i, room, into)
			}
		}
		for _, by := range members {
			for _, read := range m.rd.made[by] {
				furthest := held[place{read.Room, read.Root}]
				for _, into := range m.rd.named[read.Room] {
					furthest = max(furthest, held[place{into, read.Root}])
				}
				if furthest < read.At {
					m.t.Fatalf("replica %d lost replica %d's read of %s/%s through %d: it holds %d", i, by, read.Room, read.Root, read.At, furthest)
				}
			}
		}
	}
}

// A bridge joining a mesh brings the reads it made alone: written again as its own ops
// over the mesh's, and kept in its log, so a member takes them as ops (not a snapshot).
func TestAJoiningBridgeBringsItsReads(t *testing.T) {
	t.Parallel()
	m := newModel(t, 0)
	first := m.r[0]
	r := &modelReplica{path: filepath.Join(t.TempDir(), "mesh.db"), dbKey: make([]byte, sealeddb.KeyLen)}
	var err error
	if r.key, err = devicekey.Generate(); err != nil {
		t.Fatal(err)
	}
	if r.id, err = devicekey.IDOf(r.key.Public()); err != nil {
		t.Fatal(err)
	}
	r.store = m.open(r)
	m.must(r.store.ChangeRoster(m.ctx, r.key, []domain.Device{{ID: r.id.String(), Role: domain.RoleBridge}}, nil))
	read := domain.ReadPosition{Room: modelRooms[5], Event: "e5", At: 5000}
	m.must(r.store.Read(m.ctx, r.key, []domain.ReadPosition{read}))
	der, err := x509.MarshalPKIXPublicKey(r.key.Public())
	m.must(err)
	m.must(first.store.Bridge(m.ctx, first.key, domain.Device{ID: r.id.String(), Name: "joining"}, der, nil))
	first.store.mu.Lock()
	snap := first.store.state.Snapshot(first.store.clock)
	first.store.mu.Unlock()
	_, err = r.store.adopt(m.ctx, snap, r.key, r.id)
	m.must(err)
	ops, gotSnap, _, err := r.store.missing(m.ctx, snap.Vector)
	m.must(err)
	if gotSnap != nil {
		t.Fatal("the joined bridge sends a snapshot for what it wrote over the mesh's")
	}
	if _, err := first.store.receive(m.ctx, ops, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := first.store.Reads(); !slices.Contains(got, read) {
		t.Errorf("the mesh holds %+v, without the joining bridge's read %+v", got, read)
	}
}
