package mediaarchive

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// cache is the cache's half over a list the test changes.
type cache struct {
	mu   sync.Mutex
	refs []domain.AttachmentRef
}

func (c *cache) Attachments(_ context.Context, room domain.RoomID, _ time.Time) ([]domain.AttachmentRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []domain.AttachmentRef
	for _, r := range c.refs {
		if room == "" || r.Room == room {
			out = append(out, r)
		}
	}
	return out, nil
}

func (c *cache) Attachment(_ context.Context, room domain.RoomID, event domain.EventID) (domain.AttachmentRef, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.refs {
		if r.Room == room && r.Event == event {
			return r, true, nil
		}
	}
	return domain.AttachmentRef{}, false, nil
}

// set replaces event's ref (an edit, a deletion: ok false).
func (c *cache) set(event domain.EventID, ref domain.AttachmentRef, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refs = slices.DeleteFunc(c.refs, func(r domain.AttachmentRef) bool { return r.Event == event })
	if ok {
		c.refs = append(c.refs, ref)
	}
}

// network serves each attachment as its event and revision, counting downloads.
type network struct {
	mu      sync.Mutex
	fetched map[domain.EventID]int
	cache   *cache
	during  func(event domain.EventID) // runs mid-download
	fail    bool
}

func (n *network) fetch(_ context.Context, room domain.RoomID, event domain.EventID) ([]byte, error) {
	n.mu.Lock()
	n.fetched[event]++
	fail, during := n.fail, n.during
	n.mu.Unlock()
	if fail {
		return nil, errors.New("offline")
	}
	ref, _, _ := n.cache.Attachment(context.Background(), room, event)
	if during != nil {
		during(event)
	}
	return []byte("bytes of " + string(event) + "@" + string(ref.Revision)), nil
}

func (n *network) count(event domain.EventID) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.fetched[event]
}

const (
	kept    domain.RoomID = "whatsapp:me/kept"
	skipped domain.RoomID = "whatsapp:me/skipped"
)

func ref(room domain.RoomID, event, revision domain.EventID) domain.AttachmentRef {
	return domain.AttachmentRef{Room: room, Event: event, Revision: revision, Name: "photo.jpg", Mime: "image/jpeg"}
}

// fixture is an archive in a fresh folder keeping kept's attachments, and what it warned.
func fixture(t *testing.T, refs ...domain.AttachmentRef) (*Archive, *cache, *network, *[]string) {
	t.Helper()
	c := &cache{refs: refs}
	n := &network{fetched: map[domain.EventID]int{}, cache: c}
	a := New(filepath.Join(t.TempDir(), "media"), c, n.fetch, slog.New(slog.DiscardHandler))
	var warned []string
	a.Use(func(r domain.RoomID) bool { return r == kept }, func() int64 { return -1 }, func(body string) { warned = append(warned, body) })
	if err := a.open(); err != nil {
		t.Fatal(err)
	}
	return a, c, n, &warned
}

// files is the archive folder's names.
func files(t *testing.T, a *Archive) []string {
	t.Helper()
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// At start the worker keeps every attachment already cached in a kept room, once.
func TestRunKeepsWhatIsAlreadyCached(t *testing.T) {
	t.Parallel()
	a, _, n, _ := fixture(t, ref(kept, "$1", ""), ref(skipped, "$2", ""))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for !a.has(ref(kept, "$1", "")) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if !a.has(ref(kept, "$1", "")) {
		t.Fatal("the cached attachment was not archived")
	}
	if n.count("$2") != 0 || a.has(ref(skipped, "$2", "")) {
		t.Error("a room whose attachments are not kept was archived")
	}
	a.keepSince(context.Background(), "", time.Time{})
	if n.count("$1") != 1 {
		t.Errorf("downloaded %d times, want once", n.count("$1"))
	}
}

// Load serves the copy without the network, so an attachment the network removed (or
// one whose room stopped being archived) still opens; one not archived is fetched and
// kept.
func TestLoadServesTheCopyFirst(t *testing.T) {
	t.Parallel()
	a, _, n, _ := fixture(t, ref(kept, "$1", ""), ref(skipped, "$2", ""))
	ctx := context.Background()
	first, err := a.Load(ctx, kept, "$1")
	if err != nil || n.count("$1") != 1 || !a.has(ref(kept, "$1", "")) {
		t.Fatalf("first Load = %q, %v; fetched %d, kept %v", first, err, n.count("$1"), a.has(ref(kept, "$1", "")))
	}
	if _, err := a.Load(ctx, skipped, "$2"); err != nil || a.has(ref(skipped, "$2", "")) {
		t.Errorf("Load in a room whose attachments are not kept: %v, archived %v", err, a.has(ref(skipped, "$2", "")))
	}
	n.fail = true
	a.Use(func(domain.RoomID) bool { return false }, func() int64 { return -1 }, func(string) {})
	again, err := a.Load(ctx, kept, "$1")
	if err != nil || string(again) != string(first) || n.count("$1") != 1 {
		t.Errorf("Load from the archive = %q, %v; fetched %d", again, err, n.count("$1"))
	}
	if _, err := a.Load(ctx, skipped, "$2"); err == nil {
		t.Error("an attachment never archived loaded with the network down")
	}
}

// A message that changes while its attachment downloads (an edit replacing it, a
// deletion or a timer, which takes it out of the list alike) is not kept under what it
// was; the next pass keeps the new one.
func TestAChangeDuringTheDownloadIsNotKept(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(c *cache){
		"edited":  func(c *cache) { c.set("$1", ref(kept, "$1", "$edit"), true) },
		"deleted": func(c *cache) { c.set("$1", domain.AttachmentRef{}, false) },
	} {
		a, c, n, _ := fixture(t, ref(kept, "$1", ""))
		n.during = func(domain.EventID) { change(c); n.during = nil }
		a.keepSince(context.Background(), "", time.Time{})
		if got := files(t, a); len(got) != 0 {
			t.Errorf("%s: kept %v", name, got)
		}
		a.keepSince(context.Background(), "", time.Time{})
		now, ok, _ := c.Attachment(context.Background(), kept, "$1")
		if ok != a.has(now) || len(files(t, a)) != map[bool]int{true: 1, false: 0}[ok] {
			t.Errorf("%s: next pass kept %v, want the message as it is now", name, files(t, a))
		}
	}
}

// A request and the worker keeping the same attachments at once leave each file once,
// whole, counted once, and nothing half written.
func TestConcurrentKeepsCountEachFileOnce(t *testing.T) {
	t.Parallel()
	var refs []domain.AttachmentRef
	for _, e := range []domain.EventID{"$1", "$2", "$3", "$4"} {
		refs = append(refs, ref(kept, e, ""))
	}
	a, _, _, _ := fixture(t, refs...)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { a.keepSince(context.Background(), "", time.Time{}) })
		wg.Go(func() {
			for _, r := range refs {
				if _, err := a.Load(context.Background(), r.Room, r.Event); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	var size int64
	for _, name := range files(t, a) {
		info, err := os.Stat(filepath.Join(a.dir, name))
		if err != nil {
			t.Fatal(err)
		}
		size += info.Size()
	}
	if got := files(t, a); len(got) != len(refs) {
		t.Errorf("files = %v, want one per attachment", got)
	}
	if a.used != size {
		t.Errorf("counted %d bytes, the folder holds %d", a.used, size)
	}
}

// Past the limit it warns once, deleting nothing; a limit raised above it re-arms the
// warning, and at start a folder already past it warns.
func TestPastTheLimitItWarnsOnceAndDeletesNothing(t *testing.T) {
	t.Parallel()
	a, _, _, warned := fixture(t, ref(kept, "$1", ""), ref(kept, "$2", ""), ref(kept, "$3", ""))
	limit := int64(30)
	a.Use(func(domain.RoomID) bool { return true }, func() int64 { return limit }, func(body string) { *warned = append(*warned, body) })
	a.keepSince(context.Background(), "", time.Time{})
	if len(*warned) != 1 || len(files(t, a)) != 3 {
		t.Fatalf("warned %q with %v kept, want one warning and every file", *warned, files(t, a))
	}
	limit = 1 << 20
	a.checkSize()
	limit = 30
	a.checkSize()
	if len(*warned) != 2 {
		t.Errorf("after the limit was raised and lowered: warned %d times, want 2", len(*warned))
	}
	restarted := New(a.dir, &cache{}, nil, a.log)
	restarted.Use(func(domain.RoomID) bool { return true }, func() int64 { return limit }, func(body string) { *warned = append(*warned, body) })
	if err := restarted.open(); err != nil || len(*warned) != 3 {
		t.Errorf("a start past the limit warned %d times (%v), want 3", len(*warned), err)
	}
}

// A file a crash left half written is removed at the next start, and not counted.
func TestAStartRemovesWhatACrashLeft(t *testing.T) {
	t.Parallel()
	a, _, _, _ := fixture(t)
	if err := os.WriteFile(filepath.Join(a.dir, ".tmp-123"), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.open(); err != nil {
		t.Fatal(err)
	}
	if got := files(t, a); len(got) != 0 || a.used != 0 {
		t.Errorf("after a start: %v, %d bytes counted", got, a.used)
	}
}
