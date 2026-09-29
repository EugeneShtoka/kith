package tui

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Two writers share every stored draft: the one window the daemon serves (api.Seat)
// and kith-mcp appending the assistant's words; and every window is followed by the next
// one, on the same store, as when another takes the seat or kith is started again.
// Each write is a compare-and-set, each reply can come back late, and the runtime runs
// commands in whatever order they finish. The happy-path tests
// in draft_merge_test.go fix one order each; this plays out thousands. A scheduler
// holds every command and every reply, and a seeded walk interleaves them with
// typing, pauses, sends, room switches, edits, polls, the assistant's appends (also
// between the RPCs of one save), deletions (a room left elsewhere deletes its draft)
// and quitting. The daemon fails
// too: a write refused before it commits, a write that commits with its reply lost,
// a read that fails. Quitting is either route out: quit (which waits for the saves; so
// does stepping aside for another window), or
// the program ending at once (the interrupt key, a signal, the terminal closing), after
// which only tui.Run's last write (FlushDrafts) runs, and what it cannot write is kept
// in a file. When it all comes to rest, the words must add up:
//
//   - every word typed or appended is, exactly once, in a sent message or a stored
//     draft (never lost, never doubled: the sent words never come back);
//   - a deleted draft stays deleted, unless this client held unsaved changes to it
//     then or typed there after, which are written back whole;
//   - the store and the window agree on every draft;
//   - a reply or edit target the person changed, in a room not sent from or deleted
//     in, is the one stored;
//   - after quit, everything typed is stored or sent; what the last write kept in the
//     file is, once the next run has restored it.
//
// A failure prints the schedule; KITH_DRAFT_SEED=<seed> replays that one alone, and
// KITH_DRAFT_TRACE=1 adds the store and this client's copy after every step.
func TestDraftsSurviveEveryInterleaving(t *testing.T) {
	t.Parallel()
	seeds, steps := 1500, 28
	if testing.Short() {
		seeds = 200
	}
	if n, err := strconv.Atoi(os.Getenv("KITH_DRAFT_SEEDS")); err == nil {
		seeds, steps = n, 40 // a deeper sweep, by hand
	}
	first := 0
	if one, err := strconv.Atoi(os.Getenv("KITH_DRAFT_SEED")); err == nil {
		first, seeds = one, one+1 // replay one schedule
	}
	// KITH_DRAFT_TALLY=1 sweeps on past failures and counts them by kind.
	var tally map[string][]int
	if os.Getenv("KITH_DRAFT_TALLY") != "" {
		tally = map[string][]int{}
	}
	for seed := first; seed < seeds; seed++ {
		w := newDraftWorld(t, uint64(seed))
		w.trace = os.Getenv("KITH_DRAFT_TRACE") != ""
		for range steps {
			if w.quit {
				break
			}
			w.step()
			if w.trace {
				w.note("    = %s", w.snapshot())
			}
		}
		w.rest()
		if msg := w.check(); msg != "" {
			if tally != nil {
				kind := msg
				for _, k := range []string{"times", "is lost", "came back", "target", "holds"} {
					if strings.Contains(msg, k) {
						kind = k
					}
				}
				tally[kind] = append(tally[kind], seed)
				continue
			}
			t.Fatalf("seed %d: %s\nschedule:\n  %s", seed, msg, strings.Join(w.log, "\n  "))
		}
	}
	for kind, seeds := range tally {
		t.Errorf("%d seeds: %s (first %v)", len(seeds), kind, seeds[:min(5, len(seeds))])
	}
}

// draftWorld is one client, its store and the assistant, under a seeded scheduler.
type draftWorld struct {
	t     *testing.T
	rng   *rand.Rand
	m     Model
	store *racyDrafts
	cmds  []tea.Cmd // not yet run
	msgs  []tea.Msg // finished, not yet delivered (in finishing order)
	log   []string
	quit  bool
	// leaving: quit was asked for; the user does nothing more while it finishes.
	leaving bool
	// calm: at rest, the assistant writes no more either.
	calm bool
	// trace: note the state after every step (KITH_DRAFT_TRACE).
	trace bool
	words int
	clock int64
	// typed and added are every word this client typed and the assistant appended.
	typed, added []string
	// sent is the windows' sent messages.
	sent []string
	// deleted are the words a deletion elsewhere removed, per room; touched rooms held
	// unsaved changes then, or were typed in after, which may bring them back.
	deleted map[domain.RoomID][]string
	touched map[domain.RoomID]bool
	// end cancels the program's context, as tui.Run does before its last write.
	end context.CancelFunc
	// targets records, per room and field ("reply", "edit"), what the person set it to
	// last; settled rooms (no send, no deletion) are checked against it at rest.
	targets   map[domain.RoomID]map[string]string
	unsettled map[domain.RoomID]bool
	serial    int
	// exits counts the programs ended (then followed by the next one: nextRun).
	exits int
	// givenUp are the words of rooms the last write at an exit could not reach: they
	// may be missing, or back in a draft after they were sent.
	givenUp map[string]bool
}

// setTarget records the person setting a target in a room.
func (w *draftWorld) setTarget(room domain.RoomID, field, value string) {
	if w.targets[room] == nil {
		w.targets[room] = map[string]string{}
	}
	w.targets[room][field] = value
}

var draftRooms = []domain.RoomID{"!a:x", "!b:x"}

func newDraftWorld(t *testing.T, seed uint64) *draftWorld {
	t.Helper()
	m, shared := sharing(t)
	w := &draftWorld{
		t: t, rng: rand.New(rand.NewPCG(seed, 0x6d78)), clock: 5000,
		deleted: map[domain.RoomID][]string{}, touched: map[domain.RoomID]bool{},
		typed: []string{"hi"}, // the draft sharing() starts with
	}
	w.store = &racyDrafts{sharedDrafts: shared, world: w, who: "this client"}
	m.backend = w.store
	m.ctx, w.end = context.WithCancel(context.Background())
	w.targets, w.unsettled = map[domain.RoomID]map[string]string{}, map[domain.RoomID]bool{}
	w.givenUp = map[string]bool{}
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	w.m = m
	return w
}

func (w *draftWorld) note(format string, args ...any) {
	w.log = append(w.log, fmt.Sprintf(format, args...))
}

func (w *draftWorld) word(prefix string) string {
	w.words++
	return fmt.Sprintf("%s%d", prefix, w.words)
}

// update delivers msg and keeps what Update asks to run.
func (w *draftWorld) update(msg tea.Msg) {
	next, cmd := asModel(w.m.Update(msg))
	w.m = next
	w.enqueue(cmd)
}

func (w *draftWorld) enqueue(cmd tea.Cmd) {
	if cmd != nil {
		w.cmds = append(w.cmds, cmd)
	}
}

// cmdName is the function a command is, for the schedule.
func cmdName(cmd tea.Cmd) string {
	if fn := runtime.FuncForPC(reflect.ValueOf(cmd).Pointer()); fn != nil {
		return strings.TrimPrefix(fn.Name(), "github.com/EugeneShtoka/kith/internal/tui.")
	}
	return "?"
}

// run runs one command to its end, as the runtime would on its own goroutine.
func (w *draftWorld) run(cmd tea.Cmd) {
	if isTimer(cmd) || isListener(cmd) {
		return
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		w.cmds = append(w.cmds, msg...)
	case tea.QuitMsg:
		w.quit = true
	default:
		if seq, ok := sequenceOf(msg); ok {
			// A sequence runs its commands in order, each to its end.
			for _, c := range seq {
				if c != nil && !w.quit {
					w.run(c)
				}
			}
			return
		}
		w.msgs = append(w.msgs, msg)
	}
}

// sequenceOf reads tea.Sequence's (unexported) message: a slice of commands.
func sequenceOf(msg tea.Msg) ([]tea.Cmd, bool) {
	v := reflect.ValueOf(msg)
	if !v.IsValid() || v.Kind() != reflect.Slice || v.Type().Elem() != reflect.TypeFor[tea.Cmd]() {
		return nil, false
	}
	out := make([]tea.Cmd, v.Len())
	for i := range out {
		out[i], _ = reflect.TypeAssert[tea.Cmd](v.Index(i))
	}
	return out, true
}

// step takes one random action.
func (w *draftWorld) step() {
	r := w.rng.IntN(100)
	if w.leaving && r < 41 {
		return // no typing, pausing, sending, switching or editing after quit
	}
	switch {
	case r < 16:
		w.typeWord()
	case r < 26:
		w.note("pause (the debounce fires)")
		w.update(draftTickMsg{gen: w.m.draftSync.gen})
	case r < 32:
		w.send()
	case r < 37:
		w.switchRoom()
	case r < 39:
		w.edit()
	case r < 41:
		w.reply()
	case r < 49:
		w.note("poll")
		w.enqueue(w.m.loadDraftsCmd())
	case r < 53:
		w.assistantAppends(draftRooms[w.rng.IntN(len(draftRooms))])
	case r < 59:
		w.deleteElsewhere(draftRooms[w.rng.IntN(len(draftRooms))])
	case r < 80:
		if len(w.cmds) > 0 {
			i := w.rng.IntN(len(w.cmds))
			cmd := w.cmds[i]
			w.cmds = slices.Delete(w.cmds, i, i+1)
			w.note("a command finishes (%s)", cmdName(cmd))
			w.run(cmd)
		}
	case r < 99:
		if len(w.msgs) > 0 {
			msg := w.msgs[0]
			w.msgs = w.msgs[1:]
			w.note("deliver %T", msg)
			w.update(msg)
		}
	case w.rng.IntN(3) == 0:
		w.note("another window takes the seat: this one saves its drafts and quits")
		w.leaving = true
		w.update(seatLostMsg{})
	case w.rng.IntN(2) == 0:
		w.note("quit (esc, q)")
		w.leaving = true
		room, reply, editing := w.m.openRoom, w.m.compose.replyTo, w.m.compose.editing
		w.update(tea.KeyPressMsg{Code: tea.KeyEscape})
		// Esc in the composer can drop a reply or an edit: the person's change.
		if w.m.compose.replyTo != reply {
			w.setTarget(room, "reply", string(w.m.compose.replyTo))
		}
		if w.m.compose.editing != editing {
			w.setTarget(room, "edit", string(w.m.compose.editing))
		}
		w.update(keyText("q"))
	default:
		w.note("the program ends at once (interrupt, a signal, the terminal closing)")
		w.leaving, w.quit = true, true
	}
}

// typeWord types one word into the open room's composer (not while editing: those
// words are the correction, which this model does not send).
func (w *draftWorld) typeWord() {
	if w.m.compose.isEditing() {
		return
	}
	word := w.word("w")
	w.note("type %q in %s", word, w.m.openRoom)
	w.typed = append(w.typed, word)
	w.touched[w.m.openRoom] = true
	// Always after a space: typing that waits on a hold lands on the draft as it is when
	// the hold arrives, which may no longer be empty.
	word = " " + word
	w.m.focus, w.m.compose.insertMode = paneTimeline, true
	w.m.compose.caret = caret{owner: fieldComposer, at: len(w.m.compose.input)}
	for _, r := range word {
		w.update(keyText(string(r)))
	}
}

func (w *draftWorld) send() {
	body := w.m.compose.input
	if body == "" || w.m.compose.isEditing() {
		return
	}
	w.note("send %q", body)
	w.m.focus, w.m.compose.insertMode = paneTimeline, true
	w.update(sendKey()) // counted when it goes out (racyDrafts.Send): it may wait on a hold
}

func (w *draftWorld) switchRoom() {
	to := draftRooms[0]
	if w.m.openRoom == to {
		to = draftRooms[1]
	}
	room, _ := w.m.rooms.byID(to)
	w.note("switch to %s", to)
	next, cmd := w.m.selectRoom(room)
	w.m = next
	w.enqueue(cmd)
	w.update(draftTickMsg{gen: -1}) // any message: Update arms the stashed room's write
}

// reply chooses a message to answer (not while editing).
func (w *draftWorld) reply() {
	if w.m.compose.isEditing() {
		return
	}
	w.serial++
	target := domain.EventID(fmt.Sprintf("$t%d", w.serial))
	w.note("reply to %s in %s", target, w.m.openRoom)
	w.m.compose.replyTo = target
	w.setTarget(w.m.openRoom, "reply", string(target))
	w.touched[w.m.openRoom] = true
	w.update(draftTickMsg{gen: -1})
}

// edit starts correcting one of this client's messages, or cancels the correction.
func (w *draftWorld) edit() {
	w.touched[w.m.openRoom] = true // the person acts in the room, as typing does
	w.m.focus, w.m.compose.insertMode = paneTimeline, true
	if w.m.compose.isEditing() {
		w.note("cancel the edit")
		w.m = w.m.cancelEdit()
		w.setTarget(w.m.openRoom, "edit", "")
	} else {
		w.note("start an edit")
		w.m.compose.editSaved = w.m.editorFor(fieldComposer).text
		w.m.compose.editing = "$mine"
		w.m = w.m.store(fieldComposer, newEditor("corrected").end())
		w.setTarget(w.m.openRoom, "edit", "$mine")
	}
	w.update(draftTickMsg{gen: -1})
}

// assistantAppends is kith-mcp's send_message with draft mode: an append to the stored
// draft, refused while a message is being corrected there.
func (w *draftWorld) assistantAppends(room domain.RoomID) {
	s := w.store.sharedDrafts
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.drafts[room]
	if cur.Editing != "" {
		return
	}
	word := w.word("a")
	w.note("assistant appends %q to %s", word, room)
	w.added = append(w.added, word)
	w.clock++
	cur.RoomID, cur.Body, cur.Author = room, joinDraft(cur.Body, word), "claude-code"
	cur.Updated = time.UnixMilli(w.clock)
	s.drafts[room] = cur
}

// deleteElsewhere is the room left on another device: the daemon deletes its draft.
func (w *draftWorld) deleteElsewhere(room domain.RoomID) {
	s := w.store.sharedDrafts
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, held := s.drafts[room]
	if !held {
		return
	}
	w.note("deleted elsewhere: %s (%q, edit returns to %q)", room, cur.Body, cur.EditSaved)
	w.deleted[room] = append(w.deleted[room], strings.Fields(cur.Body+" "+cur.EditSaved)...)
	// Held here unsaved, a different composition may be kept whole (it is what the
	// person sees and went on with); held as stored, the deletion must win. A write of
	// this client's that went unanswered is unsaved as far as it knows: whether the
	// deletion took its words or older ones cannot be told, and keeping them is safer.
	w.touched[room] = w.touched[room] || !sameComposition(cur, w.local(room)) ||
		len(w.m.draftSync.unsure[room]) > 0
	w.unsettled[room] = true
	delete(s.drafts, room)
}

// rest lets everything finish: every command and reply, then pauses and polls until
// nothing changes. After quit, nothing is delivered and nothing more happens: the
// program is gone, and only what reached the store counts.
func (w *draftWorld) rest() {
	w.note("— rest —")
	w.calm = true
	if w.quit {
		w.exit()
		return
	}
	for round := range 12 {
		before := w.snapshot()
		for len(w.cmds) > 0 || (len(w.msgs) > 0 && !w.quit) {
			if len(w.cmds) > 0 {
				cmd := w.cmds[0]
				w.cmds = w.cmds[1:]
				if w.trace {
					w.note("  runs %s", cmdName(cmd))
				}
				w.run(cmd)
				continue
			}
			msg := w.msgs[0]
			w.msgs = w.msgs[1:]
			if w.trace {
				w.note("  delivers %T", msg)
			}
			w.update(msg)
			if w.trace {
				w.note("    = %s", w.snapshot())
			}
		}
		if w.quit {
			w.exit()
			return
		}
		w.update(draftTickMsg{gen: w.m.draftSync.gen})
		w.enqueue(w.m.loadDraftsCmd())
		if round > 1 && w.snapshot() == before && len(w.cmds) == 0 && len(w.msgs) == 0 {
			return
		}
	}
}

// exit is the program ending: a command still running either finished before the end
// or was cut off (Run cancels their context), no reply is delivered, and tui.Run's last
// write takes what is left. What it cannot store is kept.
func (w *draftWorld) exit() {
	w.note("— exit —")
	w.exits++
	var done, cut []tea.Cmd
	// A batch is its commands running side by side, each finishing or cut on its own.
	for pending := w.cmds; len(pending) > 0; {
		cmd := pending[0]
		pending = pending[1:]
		switch {
		case cmd == nil || isTimer(cmd) || isListener(cmd):
		case isBatch(cmd):
			batch, _ := cmd().(tea.BatchMsg)
			pending = append(pending, batch...)
		case w.rng.IntN(2) == 0:
			done = append(done, cmd)
		default:
			cut = append(cut, cmd)
		}
	}
	// Finished before the end: the runtime took their replies. What those ask to run
	// next is too late.
	w.cmds = nil
	for _, cmd := range done {
		w.run(cmd)
	}
	for _, msg := range w.msgs {
		w.update(msg)
	}
	// A send those replies asked for (typing that waited on a hold, then its send) is
	// counted as sent: what becomes of a send cut off by the end is not the drafts'
	// business, and this walk asks only where the words of drafts go.
	w.runSends(w.cmds)
	w.end()
	for _, cmd := range cut {
		w.detach(cmd) // its call in flight may land; it goes no further
	}
	w.cmds, w.msgs = nil, nil
	// A room the last write could not reach is given up (unsaved.go): its unsaved words
	// may be gone, and a send whose clear did not land may be offered again.
	for _, room := range w.m.flushDrafts(context.Background()) {
		local, stored := w.local(room), w.stored(room)
		w.unsettled[room] = true // its targets as much as its words
		w.note("given up at exit: %s %q (stored %q)", room, local.Body+" "+local.EditSaved, stored.Body+" "+stored.EditSaved)
		for f := range strings.FieldsSeq(local.Body + " " + local.EditSaved + " " + stored.Body + " " + stored.EditSaved) {
			w.givenUp[f] = true
		}
	}
	w.nextRun()
}

// nextRun is the next window, on the same store with the daemon back, and everything
// comes to rest.
func (w *draftWorld) nextRun() {
	w.note("— next run —")
	w.quit, w.leaving = false, false
	w.m, w.cmds, w.msgs = New(context.Background(), w.store, config.Display{}), nil, nil
	w.enqueue(w.m.loadDraftsCmd())
	w.update(roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}, {ID: "!b:x", Name: "Bravo"}}})
	w.m = sized(w.t, w.m) // the rooms opened the first one, as at a cold start
	w.update(draftTickMsg{gen: -1})
	w.rest()
}

// runSends runs the sends among cmds, batches opened, and nothing else.
func (w *draftWorld) runSends(cmds []tea.Cmd) {
	for _, cmd := range cmds {
		switch {
		case cmd == nil || isTimer(cmd) || isListener(cmd):
		case isBatch(cmd):
			batch, _ := cmd().(tea.BatchMsg)
			w.runSends(batch)
		case strings.HasSuffix(cmdName(cmd), "Model.sendCmd.func1"):
			cmd()
		}
	}
}

// detach runs a command whose answer nobody reads any more.
func (w *draftWorld) detach(cmd tea.Cmd) {
	if cmd == nil || isTimer(cmd) || isListener(cmd) {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			w.detach(c)
		}
	} else if seq, ok := sequenceOf(msg); ok {
		for _, c := range seq {
			w.detach(c)
		}
	}
}

func (w *draftWorld) snapshot() string {
	var b strings.Builder
	for _, room := range draftRooms {
		fmt.Fprintf(&b, "%s|%+v|%+v;", room, w.stored(room), w.local(room))
	}
	return b.String()
}

func (w *draftWorld) stored(room domain.RoomID) domain.StoredDraft {
	s := w.store.sharedDrafts
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.drafts[room]
	d.Updated = time.Time{}
	return d
}

// check is the invariants at rest, or "" when they hold.
func (w *draftWorld) check() string {
	// Sent twice, or sent and still in a draft, is the sync bringing sent words back.
	count := map[string]int{}
	var where []string
	for _, body := range w.sent {
		for f := range strings.FieldsSeq(body) {
			count[f]++
		}
		where = append(where, fmt.Sprintf("sent %q", body))
	}
	for _, room := range draftRooms {
		d := w.stored(room)
		for f := range strings.FieldsSeq(d.Body + " " + d.EditSaved) {
			count[f]++
		}
		where = append(where, fmt.Sprintf("stored %s %q (edit returns to %q)", room, d.Body, d.EditSaved))
		if local := w.local(room); !w.quit && !sameComposition(d, local) {
			return fmt.Sprintf("%s: stored %+v but this client holds %+v", room, d, local)
		}
		if w.unsettled[room] {
			continue
		}
		for field, want := range w.targets[room] {
			got := string(d.ReplyTo)
			if field == "edit" {
				got = string(d.Editing)
			}
			if got != want {
				return fmt.Sprintf("%s: the %s target %q the person set is stored as %q (%s)",
					room, field, want, got, strings.Join(where, "; "))
			}
		}
	}
	gone := map[string]bool{}
	for room, words := range w.deleted {
		for _, word := range words {
			gone[word] = true
			if !w.touched[room] && strings.Contains(" "+w.stored(room).Body+" ", " "+word+" ") {
				return fmt.Sprintf("%q came back in %s after it was deleted elsewhere (%s)", word, room, strings.Join(where, "; "))
			}
		}
	}
	for _, word := range append(append([]string{}, w.typed...), w.added...) {
		if w.givenUp[word] {
			continue
		}
		switch n := count[word]; {
		case n > 1:
			return fmt.Sprintf("%q is there %d times (%s)", word, n, strings.Join(where, "; "))
		case n == 0 && !gone[word]:
			return fmt.Sprintf("%q is lost (%s)", word, strings.Join(where, "; "))
		}
	}
	return ""
}

// local is room's draft as this client holds it: the composer for the open room, the
// stashed copy for another.
func (w *draftWorld) local(room domain.RoomID) domain.StoredDraft { return localOf(w.m, room) }

func localOf(m Model, room domain.RoomID) domain.StoredDraft {
	d := m.drafts[room]
	if room == m.openRoom {
		c := m.compose
		d = draft{input: c.input, replyTo: c.replyTo, editing: c.editing, editSaved: c.editSaved}
	}
	return domain.StoredDraft{RoomID: room, Body: d.input, ReplyTo: d.replyTo, Editing: d.editing, EditSaved: d.editSaved}
}

// sameComposition compares what a person would see of two drafts.
func sameComposition(a, b domain.StoredDraft) bool {
	return a.Body == b.Body && a.ReplyTo == b.ReplyTo && a.Editing == b.Editing && a.EditSaved == b.EditSaved
}

// racyDrafts lets the assistant write between the RPCs of one save, as it can when
// kith-mcp runs beside the client.
type racyDrafts struct {
	*sharedDrafts
	world *draftWorld
	// who writes through it, for the trace.
	who string
}

// Send is a message going out: its words leave the draft for good.
func (r *racyDrafts) Send(_ context.Context, room domain.RoomID, d domain.Draft) error {
	w := r.world
	w.note("    %s sends %q in %s", r.who, d.Body, room)
	w.sent = append(w.sent, d.Body)
	w.unsettled[room] = true
	return nil
}

func (r *racyDrafts) meddle() {
	if !r.world.calm && r.world.rng.IntN(5) == 0 {
		r.world.assistantAppends(draftRooms[r.world.rng.IntN(len(draftRooms))])
	}
}

// fails reports whether the daemon fails this call: 1 in 10 while things happen, and
// while quitting (the daemon may be the reason), never at rest.
func (r *racyDrafts) fails() bool {
	return (!r.world.calm || r.world.leaving) && r.world.rng.IntN(10) == 0
}

var errDaemonGone = errors.New("unavailable: connection reset")

func (r *racyDrafts) Drafts(ctx context.Context) ([]domain.StoredDraft, error) {
	r.meddle()
	if r.fails() {
		r.world.note("    the read fails")
		return nil, errDaemonGone
	}
	return r.sharedDrafts.Drafts(ctx)
}

func (r *racyDrafts) ReplaceDraft(ctx context.Context, draft, over domain.StoredDraft) (bool, error) {
	r.meddle()
	if r.fails() {
		r.world.note("    the write fails before it commits")
		return false, errDaemonGone
	}
	ok, err := r.sharedDrafts.ReplaceDraft(ctx, draft, over)
	if r.world.trace {
		r.world.note("    %s writes %s {%q editing=%q returns to %q reply=%q} over %q: %v", r.who, draft.RoomID, draft.Body, draft.Editing, draft.EditSaved, draft.ReplyTo, over.Body, ok)
	}
	if ok && r.fails() {
		r.world.note("    the write commits, and its reply is lost")
		return false, errDaemonGone
	}
	return ok, err
}
