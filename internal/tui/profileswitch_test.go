package tui

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A window naming a profile reaches only that profile's rooms: kithd refuses any call
// naming another (#444). A seeded walk switches a window between three profiles while
// their rooms load, the commands finishing and their answers arriving in any order, and
// interleaves typing, pauses, opening rooms, room loads, threads, reply and edit
// targets, a search pane's jumps, a link opening a chat no listing holds yet, room lists
// asked again (answering after a switch), a switch back before the last one's rooms
// answer, a switch asked while one is under way, and draft writes failing. kithd here
// holds work's rooms, home's, one room the two share and a third profile holds alone,
// an invitation of work and of home, and the chat each link opened. Checked:
//
//   - as a switch lands, every word typed before it was asked, in a room the new profile
//     does not hold, is stored, unless its write failed since (kithd takes that draft in
//     no other profile; a failed write is not waited for);
//   - a list taken is the profile's, and one moving the window off a room the profile
//     does not hold says why;
//   - at rest the window runs in the profile last asked for, as kithd runs it, lists
//     exactly that profile's rooms and invitations, and has none of another's open;
//   - using the room open at rest (its loads, typing, the draft's write) is refused
//     nowhere, and the window settles.
//
// A failure prints the schedule; KITH_SWITCH_SEED=<seed> replays it alone, and
// KITH_SWITCH_TALLY=1 sweeps on past failures and counts them by kind.
func TestAProfileSwitchLeavesTheRoomsOfTheProfileLeft(t *testing.T) {
	t.Parallel()
	seeds, steps := 400, 40
	if testing.Short() {
		seeds = 100
	}
	first := 0
	if one, err := strconv.Atoi(os.Getenv("KITH_SWITCH_SEED")); err == nil {
		first, seeds = one, one+1
	}
	var tally map[string][]int
	if os.Getenv("KITH_SWITCH_TALLY") != "" {
		tally = map[string][]int{}
	}
	for seed := first; seed < seeds; seed++ {
		w := newSwitchWalk(t, uint64(seed))
		for range steps {
			w.step()
			if w.failure != "" {
				break
			}
		}
		if w.failure == "" {
			w.rest()
			w.useTheOpenRoom()
			w.check()
		}
		if w.failure != "" && tally != nil {
			kind := w.failure
			for _, k := range []string{"not stored", "saying", "runs in", "refused", "took a list", "lists the invitations", "lists", "open"} {
				if strings.Contains(w.failure, k) {
					kind = k
					break
				}
			}
			tally[kind] = append(tally[kind], seed)
		} else if w.failure != "" {
			t.Fatalf("seed %d: %s\nschedule:\n  %s", seed, w.failure, strings.Join(w.log, "\n  "))
		}
	}
	for kind, seeds := range tally {
		t.Errorf("%d seeds: %s (first %v)", len(seeds), kind, seeds[:min(5, len(seeds))])
	}
}

var (
	swWork   = domain.Room{ID: mx + "!work:x", Name: "Standup"}
	swHome   = domain.Room{ID: mx + "!home:x", Name: "Family"}
	swShared = domain.Room{ID: mx + "!both:x", Name: "Street"}
	// swJoined and swInvites are each profile's, as kithd lists them; swFresh is the chat
	// a link resolves to in each, which kithd holds there but lists nowhere yet.
	swJoined  = map[string][]domain.Room{"work": {swWork, swShared}, "home": {swHome, swShared}, "play": {swShared}}
	swInvites = map[string][]domain.Room{
		"work": {invited(mx+"!offsite:x", "Offsite", "matrix:@boss:x")},
		"home": {invited(mx+"!party:x", "Party", "matrix:@aunt:x")},
	}
	swFresh = map[string]domain.Room{
		"work": {ID: "telegram:1/300", Name: "Supplier", IsDirect: true},
		"home": {ID: "telegram:1/301", Name: "Plumber", IsDirect: true},
		"play": {ID: "telegram:1/302", Name: "Coach", IsDirect: true},
	}
)

// swProfile is a profile by its name in the config: "" is the first, work.
func swProfile(name string) string {
	if name == "" {
		return "work"
	}
	return strings.ToLower(name)
}

// swHolds reports whether kithd holds room in profile: listed, invited or resolved there.
func swHolds(profile string, room domain.RoomID) bool {
	is := func(r domain.Room) bool { return r.ID == room }
	return slices.ContainsFunc(swJoined[profile], is) || slices.ContainsFunc(swInvites[profile], is) ||
		swFresh[profile].ID == room
}

// profiledKithd is kithd serving one window in a profile: lists narrowed to it, and any
// call naming a room outside it refused (recorded).
type profiledKithd struct {
	*sharedDrafts
	mu sync.Mutex
	in string
	// refused is every call refused: the call and the room it named.
	refused []string
	// landed is the walk's check as a switch lands, before any call is served in it.
	landed func(profile string)
	// fails is the walk's say whether a draft's write fails before it commits.
	fails func(room domain.RoomID) bool
}

func (k *profiledKithd) profile() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return swProfile(k.in)
}

func (k *profiledKithd) refuse(call string, room domain.RoomID) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if swHolds(swProfile(k.in), room) {
		return nil
	}
	k.refused = append(k.refused, call+" "+string(room))
	return fmt.Errorf("not found: the profile %s holds no room %s", swProfile(k.in), room)
}

func (k *profiledKithd) SwitchProfile(name string) error {
	k.landed(swProfile(name))
	k.mu.Lock()
	defer k.mu.Unlock()
	k.in = name
	return nil
}

func (k *profiledKithd) Rooms(context.Context) ([]domain.Room, error) {
	return slices.Clone(swJoined[k.profile()]), nil
}

func (k *profiledKithd) RefreshRooms(ctx context.Context) ([]domain.Room, error) { return k.Rooms(ctx) }

func (k *profiledKithd) CachedInvites(context.Context) ([]domain.Room, error) {
	return slices.Clone(swInvites[k.profile()]), nil
}

func (k *profiledKithd) CachedTimeline(_ context.Context, room domain.RoomID) ([]domain.Message, error) {
	return nil, k.refuse("CachedTimeline", room)
}

func (k *profiledKithd) Timeline(_ context.Context, room domain.RoomID, _ string, _ int) (domain.TimelinePage, error) {
	return domain.TimelinePage{}, k.refuse("Timeline", room)
}

func (k *profiledKithd) ThreadPage(_ context.Context, room domain.RoomID, _ domain.EventID, _ string, _ int) (domain.TimelinePage, error) {
	return domain.TimelinePage{}, k.refuse("ThreadPage", room)
}

func (k *profiledKithd) Members(_ context.Context, room domain.RoomID, _ int) ([]domain.Member, error) {
	return nil, k.refuse("Members", room)
}

func (k *profiledKithd) MarkRead(_ context.Context, room domain.RoomID, _ domain.EventID, _ bool) error {
	return k.refuse("MarkRead", room)
}

func (k *profiledKithd) SendTyping(_ context.Context, room domain.RoomID, _ bool, _ time.Duration) error {
	return k.refuse("SendTyping", room)
}

func (k *profiledKithd) Drafts(ctx context.Context) ([]domain.StoredDraft, error) {
	all, err := k.sharedDrafts.Drafts(ctx)
	profile := k.profile()
	return slices.DeleteFunc(all, func(d domain.StoredDraft) bool { return !swHolds(profile, d.RoomID) }), err
}

func (k *profiledKithd) RebaseDraft(ctx context.Context, draft, base domain.StoredDraft, unanswered []domain.StoredDraft) (domain.StoredDraft, string, error) {
	if err := k.refuse("RebaseDraft", draft.RoomID); err != nil {
		return domain.StoredDraft{}, "", err
	}
	if k.fails(draft.RoomID) {
		return domain.StoredDraft{}, "", errDaemonGone
	}
	return k.sharedDrafts.RebaseDraft(ctx, draft, base, unanswered)
}

// switchWalk is one window over profiledKithd under a seeded scheduler.
type switchWalk struct {
	rng  *rand.Rand
	m    Model
	k    *profiledKithd
	cmds []tea.Cmd // not yet run
	msgs []tea.Msg // finished, not yet delivered (in finishing order)
	log  []string
	// typed is every word typed where kithd took its draft, per room; asked is typed as
	// the last switch was asked.
	typed, asked map[domain.RoomID][]string
	words        int
	// failed are the rooms the window heard a draft's write fail in since the last switch
	// was asked; calm is the rest, where nothing fails.
	failed map[domain.RoomID]bool
	calm   bool
	// wanted is the profile last asked for and not refused.
	wanted  string
	failure string
}

// fails fails one draft write in eight before it commits (the daemon going away), never
// at rest.
func (w *switchWalk) fails(room domain.RoomID) bool {
	if w.calm || w.rng.IntN(8) != 0 {
		return false
	}
	w.note("    the write of %s's draft fails", room)
	return true
}

func newSwitchWalk(t *testing.T, seed uint64) *switchWalk {
	t.Helper()
	w := &switchWalk{rng: rand.New(rand.NewPCG(seed, 0x7377)), typed: map[domain.RoomID][]string{}, failed: map[domain.RoomID]bool{}}
	w.k = &profiledKithd{sharedDrafts: &sharedDrafts{drafts: map[domain.RoomID]domain.StoredDraft{}}, landed: w.landed, fails: w.fails}
	m := sized(t, starterNew(w.k, config.Display{}))
	m = m.WithConfigFile("", config.Config{Profiles: []config.Profile{
		{Name: "work", Include: []string{"*"}}, {Name: "home", Include: []string{"*"}}, {Name: "play", Include: []string{"*"}},
	}}).WithProfiles(w.k, "").closePicker()
	w.m = m
	// The start's loads, answering whenever they do.
	w.enqueue(m.loadRoomsCmd())
	w.enqueue(m.loadInvitesCmd())
	w.enqueue(m.loadDraftsCmd())
	return w
}

func (w *switchWalk) note(format string, args ...any) {
	w.log = append(w.log, fmt.Sprintf(format, args...))
}

func (w *switchWalk) enqueue(cmd tea.Cmd) {
	if cmd != nil {
		w.cmds = append(w.cmds, cmd)
	}
}

// update delivers msg and keeps what Update asks to run; a room or invitation list that
// moves the window off a room its profile does not hold must say why.
func (w *switchWalk) update(msg tea.Msg) {
	_, rooms := msg.(roomsMsg)
	_, invites := msg.(invitesMsg)
	listed := func() []domain.RoomID {
		if invites {
			return swIDs(w.m.rooms.invites)
		}
		return swIDs(w.m.rooms.joined)
	}
	if saved, ok := msg.(draftSavedMsg); ok && saved.err != nil {
		w.failed[saved.room] = true
	}
	was, before := w.m.openRoom, listed()
	next, cmd := asModel(w.m.Update(msg))
	w.m = next
	w.enqueue(cmd)
	// Between a switch landing in kithd and the window hearing of it, the window cannot
	// know whose lists it is given.
	profile := w.k.profile()
	if !rooms && !invites || profile != swProfile(w.m.currentProfile()) {
		return
	}
	// A list taken is the profile's.
	if after := listed(); !slices.Equal(before, after) {
		for _, id := range after {
			if !swHolds(profile, id) && w.failure == "" {
				w.failure = fmt.Sprintf("in %s the window took a list holding %s, another profile's", profile, id)
			}
		}
	}
	if was != "" && w.m.openRoom != was && !swHolds(profile, was) && !strings.Contains(w.m.status(), "is not in it") {
		w.failure = fmt.Sprintf("the window left %s, which %s does not hold, saying %q", was, profile, w.m.status())
	}
}

// swIDs is the rooms' IDs, sorted.
func swIDs(rooms []domain.Room) []domain.RoomID {
	out := make([]domain.RoomID, 0, len(rooms))
	for i := range rooms {
		out = append(out, rooms[i].ID)
	}
	slices.Sort(out)
	return out
}

// run runs one command to its end, as the runtime would on its own goroutine.
func (w *switchWalk) run(cmd tea.Cmd) {
	if cmd == nil || isTimer(cmd) || isListener(cmd) {
		return
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		w.cmds = append(w.cmds, msg...)
	default:
		if seq, ok := sequenceOf(msg); ok {
			for _, c := range seq {
				w.run(c) // in order, each to its end
			}
			return
		}
		w.msgs = append(w.msgs, msg)
	}
}

// landed is kithd moving the window to profile: what was typed before the switch was
// asked, in a room profile does not hold, must be stored by now.
func (w *switchWalk) landed(profile string) {
	for room, words := range w.asked {
		if swHolds(profile, room) || w.failed[room] {
			continue // a write that failed is retried, as any is; the switch does not wait on it
		}
		w.k.sharedDrafts.mu.Lock()
		stored := w.k.drafts[room]
		w.k.sharedDrafts.mu.Unlock()
		for _, word := range words {
			if !slices.Contains(strings.Fields(stored.Body+" "+stored.EditSaved), word) && w.failure == "" {
				w.failure = fmt.Sprintf("switched to %s with %q, typed in %s before, not stored (stored %q, editing over %q)",
					profile, word, room, stored.Body, stored.EditSaved)
			}
		}
	}
}

func (w *switchWalk) step() {
	m := w.m
	open, joined := m.currentRoom()
	joined = joined && !open.IsInvite()
	switch r := w.rng.IntN(100); {
	case r < 14:
		w.typeWord()
	case r < 19:
		w.note("pause (the draft's debounce fires)")
		w.update(draftTickMsg{gen: m.draftSync.gen})
	case r < 25:
		if len(m.rooms.all) == 0 {
			return
		}
		room := m.rooms.all[w.rng.IntN(len(m.rooms.all))]
		w.note("open %s", room.ID)
		next, cmd := m.selectRoom(room)
		w.m = next
		w.enqueue(cmd)
		w.update(draftTickMsg{gen: -1}) // any message: Update arms the stashed room's write
	case r < 29:
		w.note("the open room's loads fire (%s)", m.openRoom)
		w.update(roomLoadMsg{roomID: m.openRoom, armed: m.loadArmed})
	case r < 32 && joined:
		w.note("open a thread in %s", m.openRoom)
		next, cmd := m.enterThread(mx+"$root", "")
		w.m = next
		w.enqueue(cmd)
		w.update(draftTickMsg{gen: -1}) // the rest of the Update the key arrived in
	case r < 34 && joined && !m.compose.isEditing():
		w.note("reply in %s", m.openRoom)
		w.m.compose.replyTo = mx + "$asked"
		w.update(draftTickMsg{gen: -1})
	case r < 36 && joined:
		w.note("start or end an edit in %s", m.openRoom)
		w.m.focus, w.m.compose.insertMode = paneTimeline, true
		if m.compose.isEditing() {
			w.m = w.m.cancelEdit()
		} else {
			w.m.compose.editSaved = w.m.editorFor(fieldComposer).text
			w.m.compose.editing = mx + "$mine"
			w.m = w.m.store(fieldComposer, newEditor("corrected").end())
		}
		w.update(draftTickMsg{gen: -1})
	case r < 39:
		hits := []domain.SearchHit{{RoomID: swWork.ID, EventID: mx + "$w"}, {RoomID: swHome.ID, EventID: mx + "$h"}, {RoomID: swShared.ID, EventID: mx + "$s"}}
		cursor := w.rng.IntN(len(hits))
		w.note("a search pane lists work's, home's and the shared room's hits; jump to %s", hits[cursor].RoomID)
		w.m.search = searchState{active: true, hits: hits, cursor: cursor}
		next, cmd := w.m.jumpToHit()
		w.m = next
		w.enqueue(cmd)
		w.m.search = searchState{}      // a jump refused: the pane is closed, as esc does
		w.update(draftTickMsg{gen: -1}) // the rest of the Update the key arrived in
	case r < 41:
		fresh := swFresh[w.k.profile()]
		w.note("a link opens %s, held but not listed yet", fresh.ID)
		w.update(linkResolvedMsg{uri: "tg://resolve?domain=x", target: domain.LinkTarget{Room: fresh}})
	case r < 45:
		w.note("the rooms and invitations are asked for again")
		w.enqueue(m.refreshRoomsCmd())
		w.enqueue(m.loadInvitesCmd())
	case r < 51:
		to := []string{"work", "home", "play"}[w.rng.IntN(3)]
		w.note("switch to %s (in %s)", to, m.currentProfile())
		next, cmd := m.switchProfile(to)
		w.m = next
		w.enqueue(cmd)
		w.update(draftTickMsg{gen: -1}) // the rest of the Update the pick arrived in
		if said := next.status(); strings.HasPrefix(said, "already in") || strings.HasPrefix(said, "still switching") {
			w.note("  (%s)", said)
			return // asked nothing: what was typed since the switch under way was asked is not checked
		}
		w.asked, w.failed, w.wanted = map[domain.RoomID][]string{}, map[domain.RoomID]bool{}, to
		// What can still be written: the rooms of the profile kithd runs the window in.
		for room, words := range w.typed {
			if swHolds(w.k.profile(), room) {
				w.asked[room] = slices.Clone(words)
			}
		}
	case r < 76:
		if len(w.cmds) > 0 {
			i := w.rng.IntN(len(w.cmds))
			cmd := w.cmds[i]
			w.cmds = slices.Delete(w.cmds, i, i+1)
			w.note("a command finishes (%s)", cmdName(cmd))
			w.run(cmd)
		}
	default:
		if len(w.msgs) > 0 {
			// Mostly in finishing order; any of them, as a call asked before a switch
			// can answer after one asked since.
			i := 0
			if w.rng.IntN(3) == 0 {
				i = w.rng.IntN(len(w.msgs))
			}
			msg := w.msgs[i]
			w.msgs = slices.Delete(w.msgs, i, i+1)
			w.note("deliver %T", msg)
			w.update(msg)
		}
	}
}

// typeWord types one word into the open room's composer (not while editing, and not in
// an invitation).
func (w *switchWalk) typeWord() {
	open, ok := w.m.currentRoom()
	if !ok || open.IsInvite() || w.m.compose.isEditing() {
		return
	}
	w.words++
	word := fmt.Sprintf("w%d", w.words)
	w.note("type %q in %s", word, open.ID)
	if swHolds(w.k.profile(), open.ID) {
		// Typed where kithd takes the draft; not in a room the window still shows after
		// kithd moved to a profile without it (before its lists answer), which stays in
		// the window only.
		w.typed[open.ID] = append(w.typed[open.ID], word)
	}
	w.m.focus, w.m.compose.insertMode = paneTimeline, true
	w.m.compose.caret = caret{owner: fieldComposer, at: len(w.m.compose.input)}
	for _, r := range " " + word {
		w.update(keyText(string(r)))
	}
}

// rest lets everything finish, in any order: every command and answer, then a pause and
// a poll, a few times over.
func (w *switchWalk) rest() {
	w.note("— rest —")
	w.calm = true
	for range 4 {
		for turns := 0; len(w.cmds) > 0 || len(w.msgs) > 0; turns++ {
			if turns == 5000 {
				w.failure = fmt.Sprintf("never settles: %d commands and %d answers still going, the last %s",
					len(w.cmds), len(w.msgs), w.log[len(w.log)-1])
				return
			}
			if len(w.cmds) > 0 && (len(w.msgs) == 0 || w.rng.IntN(2) == 0) {
				i := w.rng.IntN(len(w.cmds))
				cmd := w.cmds[i]
				w.cmds = slices.Delete(w.cmds, i, i+1)
				w.run(cmd)
				continue
			}
			i := w.rng.IntN(len(w.msgs))
			msg := w.msgs[i]
			w.msgs = slices.Delete(w.msgs, i, i+1)
			w.note("  deliver %T", msg)
			w.update(msg)
		}
		w.update(draftTickMsg{gen: w.m.draftSync.gen})
		w.enqueue(w.m.loadDraftsCmd())
	}
}

// useTheOpenRoom loads the room open at rest and types in it: kithd refuses none of it.
func (w *switchWalk) useTheOpenRoom() {
	open := w.m.openRoom
	if open == "" || w.failure != "" {
		return
	}
	w.note("— use %s —", open)
	before := len(w.k.refused)
	w.update(roomLoadMsg{roomID: open, armed: w.m.loadArmed})
	w.typeWord()
	w.update(draftTickMsg{gen: w.m.draftSync.gen})
	w.rest()
	for _, call := range w.k.refused[before:] {
		if strings.HasSuffix(call, " "+string(open)) {
			w.failure = fmt.Sprintf("using the open room at rest: %s refused in %s", call, w.k.profile())
			return
		}
	}
}

// check is the state at rest.
func (w *switchWalk) check() {
	if w.failure != "" {
		return
	}
	profile := w.k.profile()
	if got := swProfile(w.m.currentProfile()); got != profile {
		w.failure = fmt.Sprintf("the window runs in %s, kithd in %s", got, profile)
		return
	}
	if w.wanted != "" && profile != w.wanted {
		w.failure = fmt.Sprintf("asked for %s, the window stays in %s", w.wanted, profile)
		return
	}
	if got, want := swIDs(w.m.rooms.joined), swIDs(swJoined[profile]); !slices.Equal(got, want) {
		w.failure = fmt.Sprintf("in %s the window lists %v, want %v", profile, got, want)
		return
	}
	if got, want := swIDs(w.m.rooms.invites), swIDs(swInvites[profile]); !slices.Equal(got, want) {
		w.failure = fmt.Sprintf("in %s the window lists the invitations %v, want %v", profile, got, want)
		return
	}
	for _, id := range swIDs(w.m.rooms.all) {
		if !swHolds(profile, id) {
			w.failure = fmt.Sprintf("in %s the window lists %s, another profile's", profile, id)
			return
		}
	}
	if open := w.m.openRoom; open != "" && !swHolds(profile, open) {
		w.failure = fmt.Sprintf("in %s the window has %s open, another profile's", profile, open)
	}
}
