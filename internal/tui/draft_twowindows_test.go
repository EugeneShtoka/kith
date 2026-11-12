package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Any number of windows write one draft, beside kith-mcp; each writes over the version
// it last read and kithd rebases it, and kithd says drafts changed (CHANGE_DRAFTS) to
// every window after each write. Two windows and kith-mcp each follow a short script of
// steps, and every interleaving of the scripts is walked, from each named start: two
// windows typing over the same base; one window sending (deleting over its version)
// while the other types; a write whose answer was lost, retried while the other window
// wrote; a write that failed, retried while the other wrote; a write that failed as its
// window left the room, retried only by the poll when nothing else changes; a window
// writing over a version the other replaced; a drafts change read while a window holds
// unsaved typing; kith-mcp appending in between each. A
// window's step is one of: type a word, pause (the debounce fires), send, replace its
// words, switch to another room, run its commands (its calls reach the store; a write may
// fail before it commits, or commit and lose its answer), or take its answers. At rest:
//
//   - every word typed or appended is in a sent message or the stored draft, unless the
//     person replaced it;
//   - every window holds the draft stored;
//   - a window that took in another writer's words says so;
//   - the windows come to rest: none answers a drafts change with a write that changes
//     the draft again, round and round (each would grow it, or never end).
func TestTwoWindowsKeepEveryWordInAnyOrder(t *testing.T) {
	t.Parallel()
	starts := map[string]domain.StoredDraft{
		"hi stored":      {RoomID: mx + "!a:x", Body: "hi", Updated: time.UnixMilli(1000)},
		"nothing stored": {},
	}
	type scripts struct{ a, b string }
	cases := map[string]scripts{
		"both type over one base":           {a: "type pause run take", b: "type pause run take"},
		"one sends while the other types":   {a: "type send run take", b: "type pause run take"},
		"an answer lost, retried meanwhile": {a: "type pause lose take", b: "type pause run take"},
		"a write failed, retried meanwhile": {a: "type pause fail take", b: "type pause run take"},
		"a write failed, its room left":     {a: "take type switch fail take", b: "type pause run take"},
		"one replaces, the other types":     {a: "replace pause run take", b: "type pause run take"},
		"a change read while one types":     {a: "type take run take pause run take", b: "type pause run"},
	}
	for start, stored := range starts {
		for name, s := range cases {
			t.Run(start+"/"+name, func(t *testing.T) {
				t.Parallel()
				for _, order := range interleavings([]string{"A " + s.a, "B " + s.b, "mcp append"}) {
					w := newTwoWindows(t, stored)
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

// twoWindows is two windows on one store, and kith-mcp.
type twoWindows struct {
	t       *testing.T
	store   *sharedDrafts
	windows map[string]*aWindow
	log     []string
	words   int
	clock   int64
	// typed and added are every word the windows typed and the assistant appended; sent
	// the messages sent; replaced the words a person replaced.
	typed, added, sent []string
	replaced           map[string]bool
	// silent is a window that took in another writer's words without saying so.
	silent string
	// updates counts the messages the windows took; runaway says they never came to
	// rest: a write answering a change with another change, round and round.
	updates int
	runaway string
}

// aWindow is one window: its model, the commands it has yet to run and the answers it
// has yet to take.
type aWindow struct {
	name    string
	m       Model
	drafts  *windowDrafts
	cmds    []tea.Cmd
	answers []tea.Msg
}

// windowDrafts is the store as one window reaches it through kithd: every write tells
// every window drafts changed, and a write can commit and lose its answer.
type windowDrafts struct {
	*sharedDrafts
	world *twoWindows
	who   string
	// fail: the next write fails before it commits; lose: it commits and its answer is
	// lost.
	fail, lose bool
}

func (d *windowDrafts) RebaseDraft(ctx context.Context, draft, base domain.StoredDraft, unanswered []domain.StoredDraft) (domain.StoredDraft, string, error) {
	if d.fail {
		d.fail = false
		d.world.note("    %s's write of %q fails before it commits", d.who, draft.Body)
		return domain.StoredDraft{}, "", errDaemonGone
	}
	stored, added, err := d.sharedDrafts.RebaseDraft(ctx, draft, base, unanswered)
	d.world.note("    %s writes %q over %q: stored %q", d.who, draft.Body, base.Body, stored.Body)
	d.world.draftsChanged()
	if d.lose {
		d.lose = false
		d.world.note("    its answer is lost")
		return domain.StoredDraft{}, "", errDaemonGone
	}
	return stored, added, err
}

func (d *windowDrafts) Send(_ context.Context, room domain.RoomID, draft domain.Draft) error {
	d.world.note("    %s sends %q in %s", d.who, draft.Body, room)
	d.world.sent = append(d.world.sent, draft.Body)
	return nil
}

func newTwoWindows(t *testing.T, stored domain.StoredDraft) *twoWindows {
	t.Helper()
	w := &twoWindows{t: t, clock: 5000, windows: map[string]*aWindow{}, replaced: map[string]bool{}}
	w.store = &sharedDrafts{drafts: map[domain.RoomID]domain.StoredDraft{}}
	if !stored.Empty() {
		w.store.drafts[stored.RoomID] = stored
	}
	for _, name := range []string{"A", "B"} {
		drafts := &windowDrafts{sharedDrafts: w.store, world: w, who: name}
		m := update(t, starterNew(drafts, config.Display{}),
			roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}, {ID: mx + "!b:x", Name: "Bravo"}}})
		m = sized(t, m)
		m, _ = m.selectRoom(m.filteredRooms()[0])
		m = deliver(t, m, m.loadDraftsCmd())
		w.windows[name] = &aWindow{name: name, m: m.clearStatus(), drafts: drafts}
	}
	return w
}

func (w *twoWindows) note(format string, args ...any) {
	w.log = append(w.log, fmt.Sprintf(format, args...))
}

// draftsChanged is kithd's CHANGE_DRAFTS, to every window.
func (w *twoWindows) draftsChanged() {
	for _, win := range w.windows {
		win.answers = append(win.answers, changedMsg{change: domain.ChangeDrafts})
	}
}

// update gives a window one message and keeps the commands it asks for.
func (w *twoWindows) update(win *aWindow, msg tea.Msg) {
	next, cmd := asModel(win.m.Update(msg))
	if saved, ok := msg.(draftSavedMsg); ok && saved.err == nil && saved.added != "" && saved.room == next.openRoom &&
		!strings.Contains(next.status(), "added to this draft") {
		w.silent = fmt.Sprintf("%s took in %q without saying so (status %q)", win.name, saved.added, next.status())
	}
	win.m = next
	w.updates++
	if n := len(localOf(next, mx+"!a:x").Body); n > maxRestingDraft && w.runaway == "" {
		w.runaway = fmt.Sprintf("%s's draft grew to %d bytes: the windows never come to rest", win.name, n)
	}
	if cmd != nil {
		win.cmds = append(win.cmds, cmd)
	}
}

// do takes one step of the walk.
func (w *twoWindows) do(step string) {
	w.note("%s", step)
	who, what, _ := strings.Cut(step, " ")
	if who == "mcp" {
		w.assistantAppends(mx + "!a:x")
		return
	}
	win := w.windows[who]
	win.m.focus, win.m.compose.insertMode = paneTimeline, true
	switch what {
	case "type":
		word := fmt.Sprintf("%s%d", strings.ToLower(who), w.next())
		w.typed = append(w.typed, word)
		win.m.compose.caret = caret{owner: fieldComposer, at: len(win.m.compose.input)}
		for _, r := range " " + word {
			w.update(win, keyText(string(r)))
		}
	case "pause":
		w.update(win, draftTickMsg{gen: win.m.draftSync.gen})
	case "switch":
		room, _ := win.m.rooms.byID(mx + "!b:x")
		next, cmd := win.m.selectRoom(room)
		win.m = next
		if cmd != nil {
			win.cmds = append(win.cmds, cmd)
		}
		w.update(win, draftTickMsg{gen: -1}) // any message: Update arms the left room's write
	case "send":
		if win.m.compose.input != "" {
			w.update(win, sendKey())
		}
	case "replace":
		word := fmt.Sprintf("%s%d", strings.ToLower(who), w.next())
		w.typed = append(w.typed, word)
		for f := range strings.FieldsSeq(win.m.compose.input) {
			w.replaced[f] = true
		}
		win.m = win.m.store(fieldComposer, newEditor(word).end())
		w.update(win, draftTickMsg{gen: -1}) // any message: Update arms the write
	case "run", "fail", "lose":
		win.drafts.fail, win.drafts.lose = what == "fail", what == "lose"
		w.runAll(win)
		win.drafts.fail, win.drafts.lose = false, false
	case "take":
		w.takeAll(win)
	}
}

func (w *twoWindows) next() int {
	w.words++
	return w.words
}

// runAll runs a window's commands to their end, keeping their answers; batches open
// into more commands, and timers and stream listeners are left (the walk fires the
// debounce itself).
func (w *twoWindows) runAll(win *aWindow) {
	for len(win.cmds) > 0 {
		cmd := win.cmds[0]
		win.cmds = win.cmds[1:]
		if cmd == nil || isTimer(cmd) || isListener(cmd) {
			continue
		}
		switch msg := cmd().(type) {
		case nil:
		case tea.BatchMsg:
			win.cmds = append(win.cmds, msg...)
		default:
			if seq, ok := sequenceOf(msg); ok {
				win.cmds = append(win.cmds, seq...)
				continue
			}
			win.answers = append(win.answers, msg)
		}
	}
}

// takeAll gives a window every answer waiting for it, in order.
func (w *twoWindows) takeAll(win *aWindow) {
	answers := win.answers
	win.answers = nil
	for _, msg := range answers {
		w.update(win, msg)
	}
}

// assistantAppends is kith-mcp's send_message in draft mode: an append to the stored
// draft, which kithd tells every window of.
func (w *twoWindows) assistantAppends(room domain.RoomID) {
	s := w.store
	s.mu.Lock()
	cur := s.drafts[room]
	word := fmt.Sprintf("m%d", w.next())
	w.added = append(w.added, word)
	w.clock++
	cur.RoomID, cur.Body, cur.Author = room, domain.JoinDraft(cur.Body, word), "claude-code"
	cur.Updated = time.UnixMilli(w.clock)
	s.drafts[room] = cur
	s.mu.Unlock()
	w.note("    the assistant appends %q: stored %q", word, cur.Body)
	w.draftsChanged()
}

// rest lets everything finish: every command and answer, pauses and the poll, until
// nothing changes.
func (w *twoWindows) rest() {
	w.note("— rest —")
	for range 12 {
		before := w.snapshot()
		for busy, budget := true, w.updates+restUpdates; busy && w.runaway == ""; {
			if w.updates > budget {
				w.runaway = fmt.Sprintf("the windows took over %d messages at rest: they never come to rest", restUpdates)
				return
			}
			busy = false
			for _, name := range []string{"A", "B"} {
				win := w.windows[name]
				if len(win.cmds) > 0 || len(win.answers) > 0 {
					busy = true
					w.runAll(win)
					w.takeAll(win)
				}
			}
		}
		for _, name := range []string{"A", "B"} {
			win := w.windows[name]
			w.update(win, draftTickMsg{gen: win.m.draftSync.gen})
			w.update(win, pollTickMsg{})
		}
		if w.snapshot() == before {
			return
		}
	}
}

func (w *twoWindows) snapshot() string {
	return fmt.Sprintf("%+v|%+v|%+v|%d %d", w.storedDraft(), localOf(w.windows["A"].m, mx+"!a:x"),
		localOf(w.windows["B"].m, mx+"!a:x"), len(w.windows["A"].cmds)+len(w.windows["A"].answers),
		len(w.windows["B"].cmds)+len(w.windows["B"].answers))
}

func (w *twoWindows) storedDraft() domain.StoredDraft {
	w.store.mu.Lock()
	defer w.store.mu.Unlock()
	d := w.store.drafts[mx+"!a:x"]
	d.Updated = time.Time{}
	return d
}

// check is the properties at rest, or "" when they hold.
func (w *twoWindows) check() string {
	if w.runaway != "" {
		return w.runaway
	}
	stored := w.storedDraft()
	where := fmt.Sprintf("stored %q, sent %q", stored.Body, w.sent)
	for _, name := range []string{"A", "B"} {
		if local := localOf(w.windows[name].m, mx+"!a:x"); !sameComposition(stored, local) {
			return fmt.Sprintf("window %s holds %q, not the draft stored (%s)", name, local.Body, where)
		}
	}
	held := strings.Fields(stored.Body + " " + strings.Join(w.sent, " "))
	for _, word := range append(slices.Clone(w.typed), w.added...) {
		if !w.replaced[word] && !slices.Contains(held, word) {
			return fmt.Sprintf("%q is lost (%s)", word, where)
		}
	}
	return w.silent
}

// maxRestingDraft is far longer than any script's words make a draft, and restUpdates
// far more messages than a round at rest takes: past either, the windows are in a loop.
// Bounded so a loop fails the test instead of growing a draft without end.
const maxRestingDraft, restUpdates = 4096, 2000
