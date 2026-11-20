package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/local"
)

// The rooms and the person of these tests: Telegram's, which keeps no verdicts of its
// own. draftRoom is held on both bridges, otherRoom too, aloneRoom on a only.
const (
	otherRoom   = domain.RoomID("tg:-100778")
	aloneRoom   = domain.RoomID("tg:-100779")
	notedPerson = "tg:555"
)

// verdictBackend is a cache's verdicts as kithd's backend keeps them (local.Service,
// the router's store keeper).
type verdictBackend struct {
	apitest.Nop
	cache *db.Cache
}

func (b *verdictBackend) KeptVerdicts(ctx context.Context) (domain.MeshVerdicts, error) {
	return local.New(b.cache, nil).KeptVerdicts(ctx)
}

func (b *verdictBackend) KeepVerdicts(ctx context.Context, mv domain.MeshVerdicts) (bool, error) {
	return local.New(b.cache, nil).KeepVerdicts(ctx, mv)
}

func (b *verdictBackend) RetagVerdicts(ctx context.Context, from, to string) error {
	return b.cache.RetagVerdicts(ctx, from, to, nil)
}

// A verdict bridge is a draft bridge's cache and mesh store, with its verdicts' server.
type verdictBridge struct {
	*draftBridge
	back *verdictBackend
	srv  daemon.VerdictsServer
}

func newVerdictBridge(t *testing.T, d *draftBridge, rooms ...domain.RoomID) *verdictBridge {
	t.Helper()
	held := map[domain.RoomID]domain.Protocol{}
	for _, r := range rooms {
		held[r] = domain.ProtocolTelegram
	}
	if err := d.back.cache.KeepRoomNetworks(t.Context(), held); err != nil {
		t.Fatal(err)
	}
	b := &verdictBridge{draftBridge: d, back: &verdictBackend{cache: d.back.cache}}
	notes, err := daemon.NewNotifications(asStarted(config.Config{}), world())
	if err != nil {
		t.Fatal(err)
	}
	paired := daemon.NewDevices(filepath.Join(t.TempDir(), "devices.toml"), func() (daemon.RosterStore, error) { return d.store, nil },
		identityOf(t, d.key), slog.New(slog.DiscardHandler))
	b.srv = daemon.NewVerdictsServer(&daemon.Daemon{
		Backend: b.back, Streams: daemon.NewStreams(), Notifications: notes, Paired: paired, Log: slog.New(slog.DiscardHandler),
	})
	return b
}

// verdictMesh is two bridges of one mesh whose config holds the tags spam and work.
func verdictMesh(t *testing.T) (a, b *verdictBridge) {
	t.Helper()
	da, db := draftMesh(t)
	a, b = newVerdictBridge(t, da, otherRoom, aloneRoom), newVerdictBridge(t, db, otherRoom)
	a.setTags("spam", "work")
	a.sync(b)
	return a, b
}

func (b *verdictBridge) sync(o *verdictBridge) { syncBridges(b.t, b.draftBridge, o.draftBridge) }

// setTags writes the config's tags into the mesh, as a settings write does.
func (b *verdictBridge) setTags(names ...string) {
	b.t.Helper()
	var cfg config.Config
	for _, n := range names {
		cfg.Tags = append(cfg.Tags, config.Tag{Name: n})
	}
	leaves, err := config.Leaves(cfg)
	if err != nil {
		b.t.Fatal(err)
	}
	if err := b.store.WriteConfig(b.t.Context(), b.key, map[string][]byte{"tag": leaves["tag"]}); err != nil {
		b.t.Fatal(err)
	}
}

// has reports whether the config the bridge's mesh holds has the tag.
func (b *verdictBridge) has(tag string) bool {
	mesh, _ := b.store.Config()
	cfg, err := config.FromLeaves(map[string][]byte{"tag": mesh["tag"]})
	if err != nil {
		b.t.Fatal(err)
	}
	return slices.ContainsFunc(cfg.Tags, func(t config.Tag) bool { return domain.SameTag(t.Name, tag) })
}

// spam is the tag a filter moving into spam names here: junk once the rename is here,
// none once its deletion is.
func (b *verdictBridge) spam() string {
	switch {
	case b.has("spam"):
		return "spam"
	case b.has("junk"):
		return "junk"
	}
	return ""
}

// moveRoom is b's filter moving room into tag at at; false when a release refuses it.
// Each bridge's filter moves by its own rule, so a move made on both differs.
func (b *verdictBridge) moveRoom(room domain.RoomID, tag string, at time.Time) bool {
	b.t.Helper()
	rule := domain.MoveAlways
	if b.name == "b" {
		rule = domain.MoveMostly
	}
	err := b.back.cache.MarkTag(b.t.Context(), domain.TagVerdict{Room: room, Tag: tag, Rule: rule, Filter: b.name, At: at})
	if errors.Is(err, domain.ErrReleased) {
		return false
	}
	if err != nil {
		b.t.Fatal(err)
	}
	return true
}

func (b *verdictBridge) releaseRoom(room domain.RoomID, tag string) {
	b.t.Helper()
	if err := b.back.cache.MarkTag(b.t.Context(), domain.TagVerdict{Room: room, Tag: tag, Released: true, At: time.Now()}); err != nil {
		b.t.Fatal(err)
	}
}

func (b *verdictBridge) markPerson(tag string) bool {
	b.t.Helper()
	rule := domain.MoveSenderFirst
	if b.name == "b" {
		rule = domain.MoveSenderMostly
	}
	err := b.back.cache.MarkPerson(b.t.Context(), domain.PersonVerdict{Person: notedPerson, Tag: tag, Rule: rule, Filter: b.name, At: time.Now()})
	if errors.Is(err, domain.ErrPersonReleased) {
		return false
	}
	if err != nil {
		b.t.Fatal(err)
	}
	return true
}

func (b *verdictBridge) releasePerson(tag string) {
	b.t.Helper()
	if err := b.back.cache.MarkPerson(b.t.Context(), domain.PersonVerdict{Person: notedPerson, Tag: tag, Released: true, At: time.Now()}); err != nil {
		b.t.Fatal(err)
	}
}

// rename is the tag editor renaming from to to ("" deleting it): the config written,
// then the retag.
func (b *verdictBridge) rename(from, to string) {
	b.t.Helper()
	if to == "" {
		b.setTags("work")
	} else {
		b.setTags(to, "work")
	}
	if err := b.srv.Retag(b.t.Context(), from, to); err != nil {
		b.t.Fatal(err)
	}
}

func (b *verdictBridge) follow() { b.srv.Follow(b.t.Context()) }

// verdictKey is a room's or person's verdict on a tag, as both hold it.
type verdictKey struct{ id, tag string }

// verdictWas is a verdict as the model compares it: a release, or a move.
type verdictWas struct{ released, in bool }

// cached is every verdict b's cache keeps for networks with nowhere else to keep them.
func (b *verdictBridge) cached() map[verdictKey]verdictWas {
	b.t.Helper()
	mv, err := b.back.KeptVerdicts(b.t.Context())
	if err != nil {
		b.t.Fatal(err)
	}
	return verdictMap(mv)
}

func verdictMap(mv domain.MeshVerdicts) map[verdictKey]verdictWas {
	out := map[verdictKey]verdictWas{}
	for _, v := range mv.Tags {
		out[verdictKey{string(v.Room), strings.ToLower(v.Tag)}] = verdictWas{v.Released, v.In()}
	}
	for _, v := range mv.People {
		out[verdictKey{v.Person, strings.ToLower(v.Tag)}] = verdictWas{v.Released, v.In()}
	}
	return out
}

// verdictScenario is a mesh as steps leave it, and every verdict made.
type verdictScenario struct {
	b [2]*verdictBridge
	// current is what spam is named now: junk renamed, spam renamed back, "" deleted.
	current string
	// moved and released are each verdict made, by its tag as named then.
	moved, released map[verdictKey]bool
}

// now is what k names once every bridge holds the renames; "" its tag deleted.
func (s *verdictScenario) now(k verdictKey) verdictKey {
	if k.tag == "spam" || k.tag == "junk" {
		k.tag = s.current
	}
	return k
}

// A verdict step is one thing that happens to a mesh of two bridges.
type verdictStep struct {
	name string
	do   func(s *verdictScenario)
}

// verdictSteps are every step, for each bridge where it is one bridge's.
func verdictSteps() []verdictStep {
	steps := []verdictStep{{"sync", func(s *verdictScenario) { s.b[0].sync(s.b[1]) }}}
	for i, side := range []string{"a", "b"} {
		on := func(name string, do func(s *verdictScenario, b *verdictBridge)) verdictStep {
			return verdictStep{name + "(" + side + ")", func(s *verdictScenario) { do(s, s.b[i]) }}
		}
		steps = append(steps,
			on("move", func(s *verdictScenario, b *verdictBridge) { s.move(b, draftRoom, time.Now()) }),
			on("moveOther", func(s *verdictScenario, b *verdictBridge) { s.move(b, otherRoom, time.Now()) }),
			// A filter on a bridge whose clock is an hour ahead.
			on("moveAhead", func(s *verdictScenario, b *verdictBridge) { s.move(b, draftRoom, time.Now().Add(time.Hour)) }),
			// A room only a holds.
			on("moveAlone", func(s *verdictScenario, b *verdictBridge) {
				if b == s.b[0] {
					s.move(b, aloneRoom, time.Now())
				}
			}),
			on("release", func(s *verdictScenario, b *verdictBridge) {
				if tag := b.spam(); tag != "" {
					b.releaseRoom(draftRoom, tag)
					s.released[verdictKey{string(draftRoom), tag}] = true
				}
			}),
			on("mark", func(s *verdictScenario, b *verdictBridge) {
				if tag := b.spam(); tag != "" && b.markPerson(tag) {
					s.moved[verdictKey{notedPerson, tag}] = true
				}
			}),
			on("unmark", func(s *verdictScenario, b *verdictBridge) {
				if tag := b.spam(); tag != "" {
					b.releasePerson(tag)
					s.released[verdictKey{notedPerson, tag}] = true
				}
			}),
			on("rename", func(s *verdictScenario, b *verdictBridge) { s.rename(b, "spam", "junk") }),
			on("renameBack", func(s *verdictScenario, b *verdictBridge) { s.rename(b, "junk", "spam") }),
			on("delete", func(s *verdictScenario, b *verdictBridge) { s.rename(b, "spam", "") }),
			on("follow", func(_ *verdictScenario, b *verdictBridge) { b.follow() }),
		)
	}
	return steps
}

// move is a filter on b moving room into spam (as b names it) at at.
func (s *verdictScenario) move(b *verdictBridge, room domain.RoomID, at time.Time) {
	if tag := b.spam(); tag != "" && b.moveRoom(room, tag, at) {
		s.moved[verdictKey{string(room), tag}] = true
	}
}

// rename renames from on b to to ("" deleting it), when both b and the scenario name
// it so.
func (s *verdictScenario) rename(b *verdictBridge, from, to string) {
	if s.current == from && b.has(from) {
		s.current = to
		b.rename(from, to)
	}
}

// verdictStarts are where the steps start from.
var verdictStarts = []struct {
	name  string
	steps []string
}{
	{"none", nil},
	// Each bridge's filter fires on one message while they are cut off.
	{"moved on both", []string{"move(a)", "move(b)"}},
	{"a release over a move both hold", []string{"move(a)", "follow(a)", "sync", "follow(b)", "release(a)"}},
	{"b's filter while a releases", []string{"move(a)", "follow(a)", "sync", "follow(b)", "release(a)", "move(b)", "mark(b)"}},
	{"a renamed over verdicts both hold", []string{"move(a)", "mark(a)", "follow(a)", "sync", "follow(b)", "rename(a)"}},
	{"a renamed, b still moving under spam", []string{"move(a)", "follow(a)", "sync", "follow(b)", "rename(a)", "moveOther(b)", "move(b)"}},
	{"a renamed, b releasing under spam", []string{"move(a)", "mark(a)", "follow(a)", "sync", "follow(b)", "rename(a)", "release(b)", "unmark(b)"}},
	{"a renamed and moved, b moved, cut off", []string{"rename(a)", "move(a)", "moveOther(b)", "follow(b)"}},
	{"a renamed, b's clock ahead", []string{"move(a)", "follow(a)", "sync", "follow(b)", "moveAhead(b)", "rename(a)"}},
	{"a deleted the tag, b moving under it", []string{"move(a)", "mark(a)", "follow(a)", "sync", "follow(b)", "delete(a)", "moveOther(b)", "release(b)"}},
	{"a room only a holds", []string{"moveAlone(a)", "follow(a)", "sync", "follow(b)"}},
	{"a renamed and back, b cut off", []string{"move(a)", "mark(a)", "follow(a)", "sync", "follow(b)", "rename(a)", "follow(a)", "renameBack(a)", "move(b)"}},
}

// TestVerdictsHoldOverEveryInterleaving is verdicts' class test across bridges (Plan
// step 7's failure review: merges writers): two bridges, a filter firing on both for
// one message, a release concurrent with a move, a rename concurrent with a move or a
// release under the old name, from each named start every further step, then quiet.
// Every bridge's cache then holds what the mesh holds, nothing is left under the old
// name, no release is undone, no move made is lost, and a further look writes nothing.
func TestVerdictsHoldOverEveryInterleaving(t *testing.T) {
	t.Parallel()
	steps := verdictSteps()
	byName := map[string]verdictStep{}
	for _, st := range steps {
		byName[st.name] = st
	}
	for _, start := range verdictStarts {
		var runs [][]string
		for _, x := range steps {
			runs = append(runs, append(slices.Clone(start.steps), x.name))
		}
		t.Run(start.name, func(t *testing.T) {
			t.Parallel()
			for _, run := range runs {
				a, b := verdictMesh(t)
				s := &verdictScenario{b: [2]*verdictBridge{a, b}, current: "spam", moved: map[verdictKey]bool{}, released: map[verdictKey]bool{}}
				for _, name := range run {
					byName[name].do(s)
				}
				s.checkQuiet(t, strings.Join(run, " "))
				if t.Failed() {
					return
				}
			}
		})
	}
}

// settle syncs and follows on both until a round writes nothing into either mesh.
func (s *verdictScenario) settle(t *testing.T, trail string) {
	t.Helper()
	for range 8 {
		s.b[0].sync(s.b[1])
		changed := [2]<-chan struct{}{s.b[0].store.Changed(), s.b[1].store.Changed()}
		s.b[0].follow()
		s.b[1].follow()
		if !closed(changed[0]) && !closed(changed[1]) {
			return
		}
	}
	t.Errorf("%s: the bridges never stopped writing verdicts", trail)
}

func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// checkQuiet settles the mesh and checks what holds once it is quiet.
func (s *verdictScenario) checkQuiet(t *testing.T, trail string) {
	t.Helper()
	s.settle(t, trail)
	a, b := s.b[0], s.b[1]
	mesh := verdictMap(a.store.Verdicts())
	if other := verdictMap(b.store.Verdicts()); fmt.Sprint(other) != fmt.Sprint(mesh) {
		t.Errorf("%s: the meshes differ:\na %v\nb %v", trail, mesh, other)
	}
	for _, br := range s.b {
		got := br.cached()
		for k, v := range mesh {
			if k.id == string(aloneRoom) && br == b {
				continue // b runs no account holding it, so keeps none of its verdicts
			}
			if got[k] != v {
				t.Errorf("%s: %s's cache holds %v of %v, the mesh %v", trail, br.name, got[k], k, v)
			}
		}
		for k := range got {
			if _, ok := mesh[k]; !ok {
				t.Errorf("%s: %s's cache holds %v, which the mesh does not", trail, br.name, k)
			}
		}
	}
	for k := range mesh {
		if (k.tag == "spam" || k.tag == "junk") && k.tag != s.current {
			t.Errorf("%s: %v is left under an old name", trail, k)
		}
	}
	for k := range s.released {
		if now := s.now(k); now.tag != "" && !mesh[now].released {
			t.Errorf("%s: %v was released, but the mesh holds %v", trail, now, mesh[now])
		}
	}
	for k := range s.moved {
		if now := s.now(k); now.tag != "" && !s.releasedAs(now) && !mesh[now].in {
			t.Errorf("%s: %v was moved, but the mesh holds %v", trail, now, mesh[now])
		}
	}
	if b.holdsRoom(aloneRoom) {
		t.Errorf("%s: b's cache holds a room of an account it does not run", trail)
	}
}

// holdsRoom reports whether b's cache holds room.
func (b *verdictBridge) holdsRoom(room domain.RoomID) bool {
	b.t.Helper()
	rooms, err := b.back.cache.Rooms(b.t.Context())
	if err != nil {
		b.t.Fatal(err)
	}
	return slices.ContainsFunc(rooms, func(r domain.Room) bool { return r.ID == room })
}

// releasedAs reports whether a release made under any name is now k's.
func (s *verdictScenario) releasedAs(k verdictKey) bool {
	for r := range s.released {
		if s.now(r) == k {
			return true
		}
	}
	return false
}

// A merged chat's verdict moves under the chat it joined, and its person's under the
// joined chat's person, and both stay there: the cache still holding the merged chat's
// (its network has not merged it yet) does not write them back under the merged
// chat's keys. The other bridge takes them under the chat joined, and its person.
func TestAMergedRoomsVerdictStaysUnderTheRoomItJoined(t *testing.T) {
	t.Parallel()
	a, b := verdictMesh(t)
	a.moveRoom(draftRoom, "spam", time.Now())
	const lid, number = domain.RoomID("whatsapp:1/88000@lid"), domain.RoomID("whatsapp:1/15550@s.whatsapp.net")
	merged := domain.PersonVerdict{Person: "whatsapp:88000@lid", Tag: "spam", Rule: domain.MoveSenderFirst, Filter: "a", At: time.Now()}
	if err := a.back.cache.MarkPerson(t.Context(), merged); err != nil {
		t.Fatal(err)
	}
	a.follow()
	if err := a.store.PublishAliases(t.Context(), a.key, map[domain.RoomID]domain.RoomID{draftRoom: otherRoom, lid: number}); err != nil {
		t.Fatal(err)
	}
	a.follow()
	changed := a.store.Changed()
	if err := a.store.PublishAliases(t.Context(), a.key, nil); err != nil {
		t.Fatal(err)
	}
	if closed(changed) {
		t.Error("the merged chat's verdict was written back under its key")
	}
	a.sync(b)
	b.follow()
	got := b.cached()
	if v := got[verdictKey{string(otherRoom), "spam"}]; !v.in {
		t.Errorf("b holds %v of the chat joined, want the move", v)
	}
	if v := got[verdictKey{"whatsapp:15550@s.whatsapp.net", "spam"}]; !v.in {
		t.Errorf("b holds %v of the joined chat's person, want the move", v)
	}
	if v, ok := got[verdictKey{merged.Person, "spam"}]; ok {
		t.Errorf("b holds %v of the merged chat's person, want none", v)
	}
}

// A cache rebuilt under the mesh takes its verdicts back from it.
func TestARebuiltCacheTakesItsVerdictsBack(t *testing.T) {
	t.Parallel()
	a, _ := verdictMesh(t)
	a.moveRoom(otherRoom, "spam", time.Now())
	a.releaseRoom(draftRoom, "spam")
	a.markPerson("spam")
	a.follow()
	before := a.cached()
	a.openCache()
	a.back.cache = a.draftBridge.back.cache
	if err := a.back.cache.KeepRoomNetworks(t.Context(), map[domain.RoomID]domain.Protocol{otherRoom: domain.ProtocolTelegram}); err != nil {
		t.Fatal(err)
	}
	a.follow()
	if got := a.cached(); fmt.Sprint(got) != fmt.Sprint(before) || len(got) != 3 {
		t.Errorf("the rebuilt cache holds %v, want %v", got, before)
	}
}

// A bridge joining a mesh brings the verdicts it made alone: each crosses where it
// wins, a release over the mesh's move whichever was made later.
func TestAJoiningBridgeBringsItsVerdicts(t *testing.T) {
	t.Parallel()
	da, db := newDraftBridge(t, "a"), newDraftBridge(t, "b")
	a, b := newVerdictBridge(t, da, otherRoom), newVerdictBridge(t, db, otherRoom)
	b.releaseRoom(draftRoom, "spam")
	b.markPerson("spam")
	b.follow()
	a.moveRoom(draftRoom, "spam", time.Now())
	a.moveRoom(otherRoom, "spam", time.Now())
	a.follow()
	joinDrafts(t, da, db)
	s := &verdictScenario{b: [2]*verdictBridge{a, b}}
	s.settle(t, "join")
	want := map[verdictKey]verdictWas{
		{string(draftRoom), "spam"}: {released: true},
		{string(otherRoom), "spam"}: {in: true},
		{notedPerson, "spam"}:       {in: true},
	}
	for _, br := range s.b {
		if got := br.cached(); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s holds %v, want %v", br.name, got, want)
		}
	}
}
