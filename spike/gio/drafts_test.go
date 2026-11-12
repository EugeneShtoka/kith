package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"gioui.org/app"
	"gioui.org/widget"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Any number of windows write one draft, beside kith-mcp; each writes over the version
// it last read and kithd rebases it, and kithd says drafts changed (CHANGE_DRAFTS) to
// every window after each write. Two Gio windows, each over its own socket to one store,
// and kith-mcp follow short scripts, and every interleaving of the scripts is walked,
// from each named start: two windows typing over the same base; one window sending
// while the other types; a write whose answer was lost, or that failed, retried while
// the other wrote; a write that failed as its window left the room; a window writing
// over a version the other replaced; a window typing while its write is on the wire; a
// drafts change read before the window's own write was answered and delivered after; a
// room left with a failed write and opened again; one window alone losing an answer,
// or erasing a word after a lost or failed write (and leaving the room); kith-mcp appending in between each. A
// window's step is one of: type a word, erase its last, pause (the debounce fires),
// send, replace its words, switch to the other room, run its writes
// (each reaches the store; with lose it commits and its answer is lost, with fail it
// never commits), or take (its answers, then the drafts it read, then a read of the
// drafts if kithd said they changed). At rest:
//
//   - every word typed or appended is in a sent message or a stored draft, unless the
//     person replaced it;
//   - every window holds the drafts stored;
//   - no window took a read of the drafts older than its own write (it would show again
//     what it had sent or replaced);
//   - a window that took in another writer's words says so;
//   - the windows come to rest;
//   - with one window typing, no stored draft holds a word twice or one the person
//     erased (with two, kithd keeps a draft whole beside words written over another
//     version, which may repeat words: planning/design-windows.md, Open).
func TestTwoGioWindowsKeepEveryWordInAnyOrder(t *testing.T) {
	t.Parallel()
	starts := map[string]domain.StoredDraft{
		"hi stored":      {RoomID: roomA, Body: "hi", Updated: time.UnixMilli(1000)},
		"nothing stored": {},
	}
	type scripts struct{ a, b string }
	cases := map[string]scripts{
		"both type over one base":           {a: "type pause run take", b: "type pause run take"},
		"one sends while the other types":   {a: "type send run take", b: "type pause run take"},
		"an answer lost, retried meanwhile": {a: "type pause lose take", b: "type pause run take"},
		"a write failed, retried meanwhile": {a: "type pause fail take", b: "type pause run take"},
		"a write failed, its room left":     {a: "type switch fail take", b: "type pause run take"},
		"one replaces, the other types":     {a: "replace pause run take", b: "type pause run take"},
		"typed while its write is out":      {a: "type pause type run take", b: "type pause run take"},
		"a read outlives its own write":     {a: "type pause take run take", b: "type pause run"},
		"a room left and opened again":      {a: "type switch fail switch type take", b: "type pause run take"},
		// One window alone: no word comes twice, and none the person erased comes back.
		"an answer lost, retried alone":   {a: "type pause lose take type pause run take"},
		"an answer lost, its word erased": {a: "type pause lose take erase take"},
		"a write failed, its word erased": {a: "type pause fail take erase take"},
		"erased, then its room left":      {a: "type pause lose take erase switch take"},
	}
	for start, stored := range starts {
		for name, s := range cases {
			t.Run(start+"/"+name, func(t *testing.T) {
				t.Parallel()
				w := newWorld(t)
				for _, order := range interleavings([]string{"A " + s.a, "B " + s.b, "mcp append"}) {
					w.reset(stored)
					for _, step := range order {
						w.do(step)
					}
					w.rest()
					if msg := w.check(); msg != "" {
						t.Fatalf("%s\nsteps:\n  %s", msg, strings.Join(w.log, "\n  "))
					}
				}
			})
		}
	}
}

const (
	roomA domain.RoomID = "matrix:x/!a:x"
	roomB domain.RoomID = "matrix:x/!b:x"
)

// interleavings is every order of the actors' steps that keeps each actor's own order;
// each script is "actor step step ...", and a step comes back as "actor step".
func interleavings(scripts []string) [][]string {
	queues := make([][]string, len(scripts))
	for i, s := range scripts {
		fields := strings.Fields(s)
		for _, step := range fields[1:] {
			queues[i] = append(queues[i], fields[0]+" "+step)
		}
	}
	var out [][]string
	var walk func(done []string, queues [][]string)
	walk = func(done []string, queues [][]string) {
		left := false
		for i, q := range queues {
			if len(q) == 0 {
				continue
			}
			left = true
			next := slices.Clone(queues)
			next[i] = q[1:]
			walk(append(slices.Clone(done), q[0]), next)
		}
		if !left {
			out = append(out, done)
		}
	}
	walk(nil, queues)
	return out
}

// world is kithd's draft store, two windows on it, and the words each writer wrote.
type world struct {
	t       *testing.T
	mu      sync.Mutex
	drafts  map[domain.RoomID]domain.StoredDraft
	windows []*window
	log     []string
	words   int
	clock   int64
	// typed and added are every word the windows typed and the assistant appended; sent
	// the words of the messages sent; replaced the words a person replaced.
	typed, added, sent     []string
	replaced               map[string]bool
	silent, stale, runaway string
	// version counts the drafts stored, each write and append one more.
	version int
}

// window is one Gio window, the kithd it reaches (its socket), its writes held there,
// and the read of the drafts it has under way.
type window struct {
	name    string
	u       *ui
	remote  *daemon.Remote
	kithd   *kithd
	waiting []*write // reached kithd, not run yet
	ran     []*write // run, the answer held
	reading *read
	changed bool // kithd said drafts changed since the window last read them
	wrote   int  // the version its last write that reached the store stored
}

// kithd is one window's end of the store: its calls stop where the test steps them.
type kithd struct {
	backendv1connect.UnimplementedBackendServiceHandler
	world  *world
	window *window
	writes chan *write
	reads  chan *read
}

// write is a ReplaceDraft call held in kithd: run says how it goes (run, lose, fail),
// done that it went, answer that its answer may go back.
type write struct {
	how          chan string
	done, answer chan struct{}
}

// read is a Drafts call that took what was stored (version at) and holds its answer;
// over closes when the window took it in.
type read struct {
	release, over chan struct{}
	at            int
}

func (k *kithd) ReplaceDraft(_ context.Context, r *connect.Request[v1.ReplaceDraftRequest]) (*connect.Response[v1.ReplaceDraftResponse], error) {
	c := &write{how: make(chan string), done: make(chan struct{}), answer: make(chan struct{})}
	k.writes <- c
	how := <-c.how
	var answer v1.ReplaceDraftResponse
	if how != "fail" {
		stored, added := k.world.rebase(r.Msg)
		k.window.wrote = k.world.version
		answer.Saved, answer.Added = true, added
		if !stored.Empty() {
			answer.Stored = pc.StoredDraftToProto(stored)
		}
	}
	close(c.done)
	<-c.answer
	if how != "run" {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("the answer was "+how))
	}
	return connect.NewResponse(&answer), nil
}

func (k *kithd) Drafts(context.Context, *connect.Request[v1.DraftsRequest]) (*connect.Response[v1.DraftsResponse], error) {
	k.world.mu.Lock()
	var all []domain.StoredDraft
	for _, d := range k.world.drafts {
		all = append(all, d)
	}
	r := &read{release: make(chan struct{}), at: k.world.version}
	k.world.mu.Unlock()
	k.reads <- r
	<-r.release
	return connect.NewResponse(&v1.DraftsResponse{Drafts: pc.StoredDraftsToProto(all)}), nil
}

// rebase is kithd's RebaseDraft (db/drafts.go): in one step with reading what is
// stored, and every window told drafts changed.
func (w *world) rebase(r *v1.ReplaceDraftRequest) (domain.StoredDraft, string) {
	draft, base := pc.ProtoToStoredDraft(r.GetDraft()), pc.ProtoToStoredDraft(r.GetOver())
	w.mu.Lock()
	stored, added := domain.RebaseDraft(draft, base, w.drafts[draft.RoomID], pc.ProtoToStoredDrafts(r.GetUnanswered()))
	if stored.Empty() {
		delete(w.drafts, draft.RoomID)
	} else {
		w.drafts[draft.RoomID] = stored
	}
	w.version++
	w.mu.Unlock()
	w.draftsChanged()
	return stored, added
}

func (w *world) draftsChanged() {
	for _, win := range w.windows {
		win.changed = true
	}
}

// newWorld starts each window's kithd on a socket of its own; the windows are made anew
// for each walk (reset).
func newWorld(t *testing.T) *world {
	w := &world{t: t}
	for _, name := range []string{"A", "B"} {
		dir, err := os.MkdirTemp("", "gio")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		path := filepath.Join(dir, "s")
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		k := &kithd{world: w, writes: make(chan *write, 8), reads: make(chan *read, 8)}
		mux := http.NewServeMux()
		mux.Handle(backendv1connect.NewBackendServiceHandler(k))
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Minute}
		go func() { _ = srv.Serve(ln) }()
		t.Cleanup(func() { _ = srv.Close() })
		win := &window{name: name, remote: daemon.NewRemote(path), kithd: k}
		k.window = win
		w.windows = append(w.windows, win)
	}
	return w
}

// reset stores start and opens a new window on each socket, in room A, having read it.
func (w *world) reset(start domain.StoredDraft) {
	w.drafts = map[domain.RoomID]domain.StoredDraft{}
	if !start.Empty() {
		w.drafts[start.RoomID] = start
	}
	w.log, w.words, w.clock, w.version = nil, 0, 0, 0
	w.typed, w.added, w.sent, w.replaced = nil, nil, nil, map[string]bool{}
	w.silent, w.stale, w.runaway = "", "", ""
	for _, win := range w.windows {
		win.u = &ui{
			w: new(app.Window), backend: win.remote, drafts: newDrafts(),
			mentions: map[*widget.Editor][]domain.Mention{},
		}
		win.waiting, win.ran, win.reading, win.changed, win.wrote = nil, nil, nil, false, 0
		w.startRead(win)
		w.deliverRead(win)
		win.u.mu.Lock()
		win.u.switchDraft("", roomA)
		win.u.mu.Unlock()
	}
}

func (w *world) note(format string, args ...any) { w.log = append(w.log, fmt.Sprintf(format, args...)) }

func (w *world) next() int { w.words++; return w.words }

func (w *world) do(step string) {
	actor, what, _ := strings.Cut(step, " ")
	if actor == "mcp" {
		w.assistantAppends(roomA)
		return
	}
	win := w.windows[0]
	if actor == "B" {
		win = w.windows[1]
	}
	w.note("%s %s", actor, what)
	switch what {
	case "type":
		word := fmt.Sprintf("%s%d", strings.ToLower(actor), w.next())
		w.typed = append(w.typed, word)
		w.frame(win, func(u *ui) { u.composer.SetText(strings.TrimSpace(u.composer.Text() + " " + word)) })
	case "replace":
		word := fmt.Sprintf("r%d", w.next())
		w.typed = append(w.typed, word)
		w.frame(win, func(u *ui) {
			for _, gone := range strings.Fields(u.composer.Text()) {
				w.replaced[gone] = true
			}
			u.composer.SetText(word)
		})
	case "send":
		w.frame(win, func(u *ui) {
			w.sent = append(w.sent, strings.Fields(u.composer.Text())...)
			u.submit(context.Background(), &u.composer, "")
		})
	case "erase":
		w.frame(win, func(u *ui) {
			words := strings.Fields(u.composer.Text())
			w.replaced[words[len(words)-1]] = true
			u.composer.SetText(strings.Join(words[:len(words)-1], " "))
		})
	case "switch":
		w.frame(win, func(u *ui) {
			to := roomB
			if u.current == roomB {
				to = roomA
			}
			u.switchDraft(u.current, to)
		})
	case "pause":
		w.pause(win)
	case "run", "lose", "fail":
		w.run(win, what)
	case "take":
		w.take(win)
	}
}

// frame does change to the window as its frame would, then saves its drafts as the frame
// does, and waits for each write that starts to reach kithd.
func (w *world) frame(win *window, change func(*ui)) {
	u := win.u
	u.mu.Lock()
	change(u)
	before := len(u.drafts.busy)
	u.saveDrafts(context.Background())
	started := len(u.drafts.busy) - before
	u.mu.Unlock()
	for range started {
		select {
		case c := <-win.kithd.writes:
			win.waiting = append(win.waiting, c)
		case <-time.After(5 * time.Second):
			w.t.Fatalf("window %s: a write never reached kithd", win.name)
		}
	}
	w.note("    %s holds %q", win.name, u.composer.Text())
}

// pause is the debounce firing: the frame after typing stopped long enough.
func (w *world) pause(win *window) {
	w.frame(win, func(u *ui) { u.drafts.seen, u.drafts.changed = u.composition(), time.Time{} })
}

func (w *world) run(win *window, how string) {
	for _, c := range win.waiting {
		c.how <- how
		<-c.done
		win.ran = append(win.ran, c)
	}
	win.waiting = nil
	w.mu.Lock()
	w.note("    stored %q", w.drafts[roomA].Body)
	w.mu.Unlock()
}

// take hands the window its answers and waits until it took them in, then the drafts it
// read, then reads them again if kithd said they changed.
func (w *world) take(win *window) {
	u := win.u
	if len(win.ran) > 0 {
		u.mu.Lock()
		was, said := strings.Fields(u.composer.Text()), u.noteAt
		u.mu.Unlock()
		for _, c := range win.ran {
			close(c.answer)
		}
		win.ran = nil
		w.until(win, func() bool { return len(u.drafts.busy) == len(win.waiting) })
		u.mu.Lock()
		now := strings.Fields(u.composer.Text())
		if u.noteAt == said && slices.ContainsFunc(now, func(s string) bool { return !slices.Contains(was, s) }) {
			w.silent = fmt.Sprintf("window %s took in another writer's words without saying so", win.name)
		}
		u.mu.Unlock()
	}
	w.deliverRead(win)
	if win.changed {
		win.changed = false
		w.startRead(win)
	}
	w.note("    %s holds %q", win.name, u.composer.Text())
}

// until waits for the window's goroutines to bring it to done.
func (w *world) until(win *window, done func() bool) {
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Microsecond) {
		win.u.mu.Lock()
		ok := done()
		win.u.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("window %s never took its answers in", win.name)
		}
	}
}

// startRead has the window read the drafts as it does on CHANGE_DRAFTS: kithd takes what
// is stored now and holds the answer.
func (w *world) startRead(win *window) {
	over := make(chan struct{})
	go func() {
		win.u.loadDrafts(context.Background())
		close(over)
	}()
	r := <-win.kithd.reads
	r.over = over
	win.reading = r
}

func (w *world) deliverRead(win *window) {
	r := win.reading
	if r == nil {
		return
	}
	win.reading = nil
	u := win.u
	u.mu.Lock()
	before := u.drafts.base[roomA]
	u.mu.Unlock()
	close(r.release)
	<-r.over
	u.mu.Lock()
	after := u.drafts.base[roomA]
	u.mu.Unlock()
	if r.at < win.wrote && (before.Body != after.Body || !before.Updated.Equal(after.Updated)) {
		w.stale = fmt.Sprintf("window %s took %q, read before its own write stored %q", win.name, after.Body, before.Body)
	}
}

// assistantAppends is kith-mcp's send_message in draft mode: an append to the stored
// draft, which kithd tells every window of.
func (w *world) assistantAppends(room domain.RoomID) {
	word := fmt.Sprintf("m%d", w.next())
	w.added = append(w.added, word)
	w.mu.Lock()
	cur := w.drafts[room]
	w.clock++
	cur.RoomID, cur.Body, cur.Author = room, domain.JoinDraft(cur.Body, word), "claude-code"
	cur.Updated = time.UnixMilli(w.clock)
	w.drafts[room] = cur
	w.version++
	w.mu.Unlock()
	w.note("mcp appends %q: stored %q", word, cur.Body)
	w.draftsChanged()
}

// rest lets everything finish: pauses, writes, answers and reads, until nothing is left.
func (w *world) rest() {
	w.note("— rest —")
	for range 30 {
		for _, win := range w.windows {
			w.pause(win)
			w.run(win, "run")
			w.take(win)
		}
		if !slices.ContainsFunc(w.windows, func(win *window) bool {
			return win.reading != nil || win.changed || len(win.u.drafts.busy) > 0
		}) {
			return
		}
	}
	w.runaway = "the windows never come to rest"
}

// check is the properties at rest, or "" when they hold.
func (w *world) check() string {
	if w.runaway != "" {
		return w.runaway
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	where := fmt.Sprintf("stored %q / %q, sent %q", w.drafts[roomA].Body, w.drafts[roomB].Body, w.sent)
	var held []string
	for _, room := range []domain.RoomID{roomA, roomB} {
		stored := w.drafts[room]
		held = append(held, strings.Fields(stored.Body)...)
		for _, win := range w.windows {
			u := win.u
			has, ok := u.drafts.local[room]
			if room == u.current {
				has, ok = u.composition(), true
			}
			if !ok {
				has = u.drafts.base[room]
			}
			if has.Body != stored.Body {
				return fmt.Sprintf("window %s holds %q in %s, not the draft stored (%s)", win.name, has.Body, room, where)
			}
		}
	}
	alone := !slices.ContainsFunc(w.typed, func(s string) bool { return s[0] == 'b' })
	for _, room := range []domain.RoomID{roomA, roomB} {
		words := strings.Fields(w.drafts[room].Body)
		for i, word := range words {
			if alone && (slices.Contains(words[i+1:], word) || w.replaced[word]) {
				return fmt.Sprintf("%q came twice or back into the draft of %s (%s)", word, room, where)
			}
		}
	}
	held = append(held, w.sent...)
	for _, word := range append(slices.Clone(w.typed), w.added...) {
		if !w.replaced[word] && !slices.Contains(held, word) {
			return fmt.Sprintf("%q is lost (%s)", word, where)
		}
	}
	if w.stale != "" {
		return w.stale
	}
	return w.silent
}
