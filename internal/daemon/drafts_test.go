package daemon_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh"
)

// draftRoom is the room every draft of these tests is written in.
const draftRoom = domain.RoomID("tg:-100777")

// draftBackend is a cache's drafts as kithd's backend keeps them (local.Service).
type draftBackend struct {
	apitest.Nop
	cache *db.Cache
}

func (b *draftBackend) ReplaceDraft(ctx context.Context, draft, over domain.StoredDraft) (bool, error) {
	return b.cache.ReplaceDraft(ctx, draft, over)
}

func (b *draftBackend) RebaseDraft(ctx context.Context, draft, base domain.StoredDraft, unanswered []domain.StoredDraft) (domain.StoredDraft, string, error) {
	return b.cache.RebaseDraft(ctx, draft, base, unanswered)
}

func (b *draftBackend) Drafts(ctx context.Context) ([]domain.StoredDraft, error) {
	return b.cache.Drafts(ctx)
}

func (b *draftBackend) TakeDraft(ctx context.Context, from, to domain.StoredDraft) (domain.StoredDraft, bool, error) {
	return b.cache.TakeDraft(ctx, from, to)
}

// A draft bridge is one kithd's cache, mesh store and drafts, with one window open on
// draftRoom and kith-mcp beside it. No socket, no loop: a test takes each step where a
// window, kith-mcp, the loop (followDrafts) or a sync would.
type draftBridge struct {
	t     *testing.T
	name  string
	dir   string
	key   *ecdsa.PrivateKey
	store *mesh.Store
	back  *draftBackend
	srv   daemon.DraftsServer
	// text is what the window shows, base the stored draft it last read.
	text string
	base domain.StoredDraft
}

// newDraftBridge is a bridge whose store is its own mesh's genesis and whose cache
// holds draftRoom.
func newDraftBridge(t *testing.T, name string) *draftBridge {
	t.Helper()
	b := &draftBridge{t: t, name: name, dir: t.TempDir(), key: newDeviceKey(t)}
	store, err := mesh.Open(t.Context(), filepath.Join(b.dir, "mesh.db"), storeKey(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	b.store = store
	if err = store.ChangeRoster(t.Context(), b.key, []domain.Device{{ID: idOf(t, b.key).String(), Name: name, Role: domain.RoleBridge}}, nil); err != nil {
		t.Fatal(err)
	}
	b.back = &draftBackend{}
	b.openCache()
	notes, err := daemon.NewNotifications(asStarted(config.Config{}), world())
	if err != nil {
		t.Fatal(err)
	}
	paired := daemon.NewDevices(filepath.Join(b.dir, "devices.toml"), func() (daemon.RosterStore, error) { return store, nil },
		identityOf(t, b.key), slog.New(slog.DiscardHandler))
	b.srv = daemon.NewDraftsServer(&daemon.Daemon{
		Backend: b.back, Streams: daemon.NewStreams(), Notifications: notes, Paired: paired, Log: slog.New(slog.DiscardHandler),
	})
	b.srv.Open("window", draftRoom)
	b.base = domain.StoredDraft{RoomID: draftRoom}
	return b
}

// openCache opens a new, empty cache holding draftRoom: a cache rebuilt.
func (b *draftBridge) openCache() {
	b.t.Helper()
	cache, err := db.Open(b.t.Context(), filepath.Join(b.t.TempDir(), "cache.db"), storeKey(b.t))
	if err != nil {
		b.t.Fatal(err)
	}
	b.t.Cleanup(func() { _ = cache.Close() })
	if err := cache.KeepRoomNetworks(b.t.Context(), map[domain.RoomID]domain.Protocol{draftRoom: domain.ProtocolTelegram}); err != nil {
		b.t.Fatal(err)
	}
	b.back.cache = cache
}

// stored is the cache's draft of draftRoom.
func (b *draftBridge) stored() domain.StoredDraft {
	b.t.Helper()
	drafts, err := b.back.Drafts(b.t.Context())
	if err != nil {
		b.t.Fatal(err)
	}
	for i := range drafts {
		if drafts[i].RoomID == draftRoom && drafts[i].ThreadRoot == "" {
			return drafts[i]
		}
	}
	return domain.StoredDraft{RoomID: draftRoom}
}

// meshDraft is the mesh's draft of draftRoom on b.
func (b *draftBridge) meshDraft() domain.MeshDraft {
	drafts := b.store.Drafts()
	for i := range drafts {
		if drafts[i].Room == draftRoom && drafts[i].Root == "" {
			return drafts[i]
		}
	}
	return domain.MeshDraft{Room: draftRoom, Draft: domain.StoredDraft{RoomID: draftRoom}}
}

// save is the window writing what it shows over what it last read; it shows what was
// stored.
func (b *draftBridge) save() {
	b.t.Helper()
	_, stored, err := b.srv.Save(b.t.Context(), domain.StoredDraft{RoomID: draftRoom, Body: b.text}, b.base, true)
	if err != nil {
		b.t.Fatalf("save on %s: %v", b.name, err)
	}
	stored.RoomID = draftRoom
	b.base, b.text = stored, stored.Body
}

// appendWords is kith-mcp adding words after what the cache holds, as appendDraft
// does: over what it read, again when another writer got there first.
func (b *draftBridge) appendWords(words string) {
	b.t.Helper()
	for range 3 {
		held := b.stored()
		next := held
		next.Body, next.Author = domain.JoinDraft(held.Body, words), domain.DraftAgent
		saved, _, err := b.srv.Save(b.t.Context(), next, held, false)
		if err != nil {
			b.t.Fatalf("append on %s: %v", b.name, err)
		}
		if saved {
			return
		}
	}
	b.t.Fatalf("append on %s never landed", b.name)
}

// reread is the window taking the stored draft (CHANGE_DRAFTS), when nothing it shows
// is unsaved.
func (b *draftBridge) reread() {
	if b.text == b.base.Body {
		b.base = b.stored()
		b.text = b.base.Body
	}
}

// rest is the loop's pass once saves have rested.
func (b *draftBridge) rest() {
	b.srv.Rested()
	b.srv.Follow(b.t.Context())
}

// joinDrafts has b join a's mesh, as `kith join --bridge` has it.
func joinDrafts(t *testing.T, a, b *draftBridge) {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(b.key.Public())
	if err != nil {
		t.Fatal(err)
	}
	bid := idOf(t, b.key)
	if err := a.store.Bridge(t.Context(), a.key, domain.Device{ID: bid.String(), Name: b.name, Role: domain.RoleBridge}, der, b.store.Sealing(), nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pa, pb := pipes(ctx.Done())
	var served sync.WaitGroup
	served.Go(func() { _ = a.store.Sync(ctx, a.key, bid, false, pa, slog.New(slog.DiscardHandler)) })
	if _, err := b.store.Join(ctx, bid, idOf(t, a.key), b.key, pb); err != nil {
		t.Fatalf("join: %v", err)
	}
	cancel()
	served.Wait()
}

// syncBridges syncs a and b until each holds what the other does.
func syncBridges(t *testing.T, a, b *draftBridge) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	pa, pb := pipes(ctx.Done())
	aid, bid := idOf(t, a.key), idOf(t, b.key)
	var running sync.WaitGroup
	running.Go(func() { _ = a.store.Sync(ctx, a.key, bid, true, pa, slog.New(slog.DiscardHandler)) })
	running.Go(func() { _ = b.store.Sync(ctx, b.key, aid, false, pb, slog.New(slog.DiscardHandler)) })
	defer func() {
		cancel()
		running.Wait()
	}()
	deadline := time.Now().Add(settle)
	for !storeCaughtUp(t, a.store, a.key, bid.String()) || !storeCaughtUp(t, b.store, b.key, aid.String()) {
		if time.Now().After(deadline) {
			t.Fatal("the bridges never caught up")
		}
		time.Sleep(time.Millisecond)
	}
}

// storeCaughtUp reports whether store is connected to peer and peer has acknowledged
// all it holds.
func storeCaughtUp(t *testing.T, store *mesh.Store, key *ecdsa.PrivateKey, peer string) bool {
	t.Helper()
	st, err := store.Status(idOf(t, key))
	if err != nil {
		t.Fatal(err)
	}
	for i := range st.Peers {
		if p := &st.Peers[i]; p.Bridge.ID == peer {
			return p.Connected && p.Lag == 0 && !p.Heard.IsZero()
		}
	}
	return false
}

// draftMesh is two bridges of one mesh, b having joined a's.
func draftMesh(t *testing.T) (a, b *draftBridge) {
	t.Helper()
	a, b = newDraftBridge(t, "a"), newDraftBridge(t, "b")
	joinDrafts(t, a, b)
	return a, b
}

// draftScenario is a mesh as steps leave it, and every word written: typed by a
// window, appended by kith-mcp, sent, or dropped by the person rewriting.
type draftScenario struct {
	b                      [2]*draftBridge
	words                  int
	written, sent, dropped []string
}

// word is a word no step wrote before.
func (s *draftScenario) word() string {
	s.words++
	w := fmt.Sprintf("w%d", s.words)
	s.written = append(s.written, w)
	return w
}

// A draft step is one thing that happens to a mesh of two bridges.
type draftStep struct {
	name string
	do   func(t *testing.T, s *draftScenario)
}

// draftSteps are every step, for each bridge where it is one bridge's.
func draftSteps() []draftStep {
	var steps []draftStep
	for i, name := range []string{"a", "b"} {
		steps = append(steps,
			draftStep{"type(" + name + ")", func(_ *testing.T, s *draftScenario) {
				b := s.b[i]
				b.text = domain.JoinDraft(b.text, s.word())
			}},
			draftStep{"rewrite(" + name + ")", func(_ *testing.T, s *draftScenario) {
				b := s.b[i]
				s.dropped = append(s.dropped, strings.Fields(b.text)...)
				b.text = s.word()
			}},
			draftStep{"save(" + name + ")", func(_ *testing.T, s *draftScenario) { s.b[i].save() }},
			draftStep{"append(" + name + ")", func(_ *testing.T, s *draftScenario) { s.b[i].appendWords(s.word()) }},
			draftStep{"send(" + name + ")", func(_ *testing.T, s *draftScenario) {
				b := s.b[i]
				s.sent = append(s.sent, strings.Fields(b.text)...)
				b.text = ""
				b.save()
			}},
			draftStep{"reread(" + name + ")", func(_ *testing.T, s *draftScenario) { s.b[i].reread() }},
			// The window leaves the room: its save lands after kithd heard it left.
			draftStep{"leave(" + name + ")", func(t *testing.T, s *draftScenario) {
				b := s.b[i]
				b.srv.Open("window", "")
				b.save()
				b.srv.Follow(t.Context())
				b.srv.Open("window", draftRoom)
			}},
			draftStep{"follow(" + name + ")", func(t *testing.T, s *draftScenario) { s.b[i].srv.Follow(t.Context()) }},
			draftStep{"rest(" + name + ")", func(_ *testing.T, s *draftScenario) { s.b[i].rest() }},
		)
	}
	return append(steps, draftStep{"sync", func(t *testing.T, s *draftScenario) { syncBridges(t, s.b[0], s.b[1]) }})
}

// quiesce is what the loop reaches once nobody writes: every window saved, every save
// rested and passed on, the bridges synced, and each window showing what is stored.
func (s *draftScenario) quiesce(t *testing.T) {
	t.Helper()
	for _, b := range s.b {
		if b.text != b.base.Body {
			b.save()
		}
	}
	for range 3 {
		for _, b := range s.b {
			b.rest()
			b.rest()
		}
		syncBridges(t, s.b[0], s.b[1])
	}
	for _, b := range s.b {
		b.reread()
	}
}

// checkQuiet is what holds once the mesh is quiet: every bridge's cache holds the
// mesh's draft and owes it nothing, every window shows it, and every word typed or
// appended is in a sent message or in that draft, unless the person rewrote it away.
func (s *draftScenario) checkQuiet(t *testing.T, trail string) {
	t.Helper()
	a, b := s.b[0], s.b[1]
	if as, bs := a.stored(), b.stored(); !domain.SameDraft(as, bs) {
		t.Errorf("%s: the bridges hold different drafts:\na %q\nb %q", trail, as.Body, bs.Body)
	}
	for _, br := range s.b {
		md := br.meshDraft()
		if !domain.SameDraft(md.Draft, br.stored()) || md.Owed || md.Moved {
			t.Errorf("%s: %s's cache holds %q, the mesh %q (owed %v, moved %v)", trail, br.name, br.stored().Body, md.Draft.Body, md.Owed, md.Moved)
		}
		if br.text != br.stored().Body {
			t.Errorf("%s: %s's window shows %q, the cache holds %q", trail, br.name, br.text, br.stored().Body)
		}
	}
	held := strings.Fields(a.stored().Body)
	for _, w := range s.written {
		if !slices.Contains(held, w) && !slices.Contains(s.sent, w) && !slices.Contains(s.dropped, w) {
			t.Errorf("%s: %s is lost: the draft holds %q, sent %v", trail, w, a.stored().Body, s.sent)
		}
	}
}

// draftStarts are named states the generator extends by every step: each a way a
// merge across bridges goes wrong.
var draftStarts = []struct {
	name  string
	steps []string
}{
	{"none", nil},
	{"a draft on both", []string{"type(a)", "rest(a)", "sync", "rest(b)", "reread(b)"}},
	// Each side saves while cut off from the other.
	{"saves partitioned", []string{"type(a)", "save(a)", "rest(a)", "type(b)", "save(b)", "rest(b)"}},
	{"appends partitioned", []string{"append(a)", "rest(a)", "append(b)", "rest(b)"}},
	// A version arrives while a save here has not rested.
	{"a version over a save not passed on", []string{
		"type(a)", "save(a)", "rest(a)", "sync", "rest(b)", "reread(b)",
		"type(b)", "save(b)", "follow(b)", "type(a)", "save(a)", "rest(a)", "sync", "follow(b)",
	}},
	// One side sends what both hold while the other appends to it.
	{"a send against an append", []string{"type(a)", "save(a)", "rest(a)", "sync", "rest(b)", "reread(b)", "send(a)", "append(b)"}},
	{"a send against typing", []string{"type(a)", "save(a)", "rest(a)", "sync", "rest(b)", "reread(b)", "send(a)", "rest(a)", "type(b)", "save(b)"}},
	{"a rewrite against an append", []string{"type(a)", "save(a)", "rest(a)", "sync", "rest(b)", "rewrite(a)", "save(a)", "rest(a)", "append(b)", "rest(b)"}},
	{"a room left mid-save", []string{"type(a)", "leave(a)", "type(b)", "save(b)"}},
}

// TestDraftsSurviveEveryInterleavingAcrossBridges is drafts' class test across bridges
// (merges writers), as TestDraftsSurviveEveryInterleaving is a window's on one: two
// bridges, each with a window and kith-mcp, partitioned until a sync, from each named
// start every further step, then quiet. No typed or appended word is lost, and every
// bridge reads the same draft.
func TestDraftsSurviveEveryInterleavingAcrossBridges(t *testing.T) {
	t.Parallel()
	steps := draftSteps()
	byName := map[string]draftStep{}
	for _, st := range steps {
		byName[st.name] = st
	}
	for _, start := range draftStarts {
		var runs [][]string
		for _, x := range steps {
			runs = append(runs, append(slices.Clone(start.steps), x.name))
		}
		t.Run(start.name, func(t *testing.T) {
			t.Parallel()
			for _, run := range runs {
				a, b := draftMesh(t)
				s := &draftScenario{b: [2]*draftBridge{a, b}}
				for _, name := range run {
					byName[name].do(t, s)
				}
				s.quiesce(t)
				s.checkQuiet(t, strings.Join(run, " "))
				if t.Failed() {
					return
				}
			}
		})
	}
}

// saveIn is a window on b writing text as the draft of room's thread root ("" for the
// room's own) over none.
func (b *draftBridge) saveIn(room domain.RoomID, root domain.EventID, text string) {
	b.t.Helper()
	place := domain.StoredDraft{RoomID: room, ThreadRoot: root}
	draft := place
	draft.Body = text
	if _, _, err := b.srv.Save(b.t.Context(), draft, place, true); err != nil {
		b.t.Fatal(err)
	}
}

// draftIn is b's cache's draft of room's thread root.
func (b *draftBridge) draftIn(room domain.RoomID, root domain.EventID) string {
	b.t.Helper()
	drafts, err := b.back.Drafts(b.t.Context())
	if err != nil {
		b.t.Fatal(err)
	}
	for i := range drafts {
		if drafts[i].RoomID == room && drafts[i].ThreadRoot == root {
			return drafts[i].Body
		}
	}
	return ""
}

// held is b with its window showing a's draft of draftRoom, text, which both hold.
func heldOnBoth(t *testing.T, a, b *draftBridge, text string) {
	t.Helper()
	a.text = text
	a.save()
	a.rest()
	syncBridges(t, a, b)
	b.rest()
	b.reread()
	if b.text != text {
		t.Fatalf("b shows %q, want %q", b.text, text)
	}
}

// Sending empties the draft over what that bridge holds; an append made concurrently on
// another bridge survives it, and the draft comes back with only the appended words.
func TestASendConcurrentWithAnAppendLeavesTheAppendAlone(t *testing.T) {
	t.Parallel()
	a, b := draftMesh(t)
	heldOnBoth(t, a, b, "sent words")
	a.text = ""
	a.save()
	b.appendWords("appended")
	s := &draftScenario{b: [2]*draftBridge{a, b}}
	s.quiesce(t)
	for _, br := range s.b {
		if got := br.stored().Body; got != "appended" {
			t.Errorf("%s holds %q, want only the appended words", br.name, got)
		}
	}
}

// A draft too large for an op stays on its bridge, which says so in its log and keeps
// it owed; a later draft that fits crosses.
func TestADraftOver1MiBStaysOnItsBridge(t *testing.T) {
	t.Parallel()
	a, b := draftMesh(t)
	big := strings.Repeat("long ", 1<<18) // 1.25 MiB
	a.text = big
	a.save()
	a.rest()
	if md := a.meshDraft(); !md.Draft.Empty() || !md.Owed {
		t.Fatalf("a draft over 1 MiB reached the mesh (%d bytes), owed %v", len(md.Draft.Body), md.Owed)
	}
	if got := a.stored().Body; got != big {
		t.Fatalf("a keeps %d bytes, want the draft whole", len(got))
	}
	syncBridges(t, a, b)
	b.rest()
	if got := b.stored().Body; got != "" {
		t.Fatalf("b holds %q of a draft that stays on a", got[:min(len(got), 20)])
	}
	a.text = "short"
	a.save()
	a.rest()
	syncBridges(t, a, b)
	b.rest()
	if got := b.stored().Body; got != "short" {
		t.Fatalf("b holds %q, want the draft that fits", got)
	}
}

// A cache rebuilt empty takes its drafts back from the mesh, rooms' and threads'.
func TestARebuiltCacheTakesItsDraftsBack(t *testing.T) {
	t.Parallel()
	a := newDraftBridge(t, "a")
	a.text = "kept"
	a.save()
	a.saveIn(draftRoom, "$root", "in the thread")
	a.rest()
	a.openCache()
	a.rest()
	if got := a.stored().Body; got != "kept" {
		t.Errorf("the rebuilt cache holds %q, want the room's draft back", got)
	}
	if got := a.draftIn(draftRoom, "$root"); got != "in the thread" {
		t.Errorf("the rebuilt cache holds %q in the thread, want its draft back", got)
	}
}

// A thread's draft crosses as its own place, beside the room's.
func TestAThreadsDraftCrossesBesideTheRooms(t *testing.T) {
	t.Parallel()
	a, b := draftMesh(t)
	a.saveIn(draftRoom, "$root", "in the thread")
	a.text = "in the room"
	a.save()
	a.rest()
	syncBridges(t, a, b)
	b.rest()
	if got := b.draftIn(draftRoom, "$root"); got != "in the thread" {
		t.Errorf("b's thread draft is %q", got)
	}
	if got := b.stored().Body; got != "in the room" {
		t.Errorf("b's room draft is %q", got)
	}
}

// A bridge joining a mesh brings its drafts: each beside the mesh's, on every bridge.
func TestAJoiningBridgeBringsItsDrafts(t *testing.T) {
	t.Parallel()
	a, b := newDraftBridge(t, "a"), newDraftBridge(t, "b")
	for _, br := range []*draftBridge{a, b} {
		br.text = "from " + br.name
		br.save()
		br.rest()
	}
	joinDrafts(t, a, b)
	s := &draftScenario{b: [2]*draftBridge{a, b}}
	s.quiesce(t)
	for _, br := range s.b {
		if got := br.stored().Body; !strings.Contains(got, "from a") || !strings.Contains(got, "from b") {
			t.Errorf("%s holds %q, want both bridges' drafts", br.name, got)
		}
	}
}

// A room merged into another (a WhatsApp chat by LID joining its number's) moves its
// draft under the room it joined, in a version over both.
func TestAMergedRoomsDraftMovesUnderTheRoomItJoined(t *testing.T) {
	t.Parallel()
	a := newDraftBridge(t, "a")
	const lid = domain.RoomID("wa:lid-room")
	if err := a.back.cache.KeepRoomNetworks(t.Context(), map[domain.RoomID]domain.Protocol{lid: domain.ProtocolWhatsApp}); err != nil {
		t.Fatal(err)
	}
	a.text = "joined room's"
	a.save()
	a.saveIn(lid, "", "merged room's")
	a.rest()
	if err := a.store.PublishAliases(t.Context(), a.key, map[domain.RoomID]domain.RoomID{lid: draftRoom}); err != nil {
		t.Fatal(err)
	}
	got := a.meshDraft().Draft.Body
	if !strings.Contains(got, "joined room's") || !strings.Contains(got, "merged room's") {
		t.Errorf("the joined room's draft is %q, want both rooms' words", got)
	}
	for _, d := range a.store.Drafts() {
		if d.Room == lid {
			t.Errorf("the merged room still holds a draft: %+v", d)
		}
	}
}

// A write waits while the mesh holds versions the cache has not taken: it would write
// over them unseen. The cache takes them first, and the next write is over them.
func TestAWriteWaitsForVersionsNotTaken(t *testing.T) {
	t.Parallel()
	a, b := draftMesh(t)
	heldOnBoth(t, a, b, "both")
	b.text = "both beta"
	b.save()
	read := b.meshDraft()
	a.text = "both alpha"
	a.save()
	a.rest()
	syncBridges(t, a, b)
	wrote, err := b.store.WriteDraft(t.Context(), b.key, read, b.stored())
	if err != nil || wrote {
		t.Fatalf("a write over versions not taken: wrote %v, %v", wrote, err)
	}
	b.rest()
	if got := b.stored().Body; !strings.Contains(got, "alpha") || !strings.Contains(got, "beta") {
		t.Fatalf("b holds %q, want both writes", got)
	}
}

// A draft the cache held before drafts crossed (written by a kithd before this one) is
// owed to the mesh, and crosses.
func TestADraftFromBeforeDraftsCrossedCrosses(t *testing.T) {
	t.Parallel()
	a, b := draftMesh(t)
	if _, _, err := a.back.cache.RebaseDraft(t.Context(), domain.StoredDraft{RoomID: draftRoom, Body: "older"}, domain.StoredDraft{}, nil); err != nil {
		t.Fatal(err)
	}
	a.rest()
	a.rest()
	syncBridges(t, a, b)
	b.rest()
	if got := b.stored().Body; got != "older" {
		t.Errorf("b holds %q, want the draft a's cache held", got)
	}
}

// A save made while a write of the draft before it is under way leaves the place owed,
// so the newer draft crosses too.
func TestASaveDuringAWriteStaysOwed(t *testing.T) {
	t.Parallel()
	a, b := draftMesh(t)
	a.text = "first"
	a.save()
	read, first := a.meshDraft(), a.stored()
	a.text = "first second"
	a.save()
	if wrote, err := a.store.WriteDraft(t.Context(), a.key, read, first); err != nil || !wrote {
		t.Fatalf("write: %v, %v", wrote, err)
	}
	if !a.meshDraft().Owed {
		t.Fatal("a save made during a write is not owed")
	}
	a.rest()
	syncBridges(t, a, b)
	b.rest()
	if got := b.stored().Body; got != "first second" {
		t.Errorf("b holds %q, want the save made during the write", got)
	}
}
