package tui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A half-written message belongs to the room it was written for: the composer is
// stashed under the room being left and restored when it is opened again. Everything
// that decides what enter means travels together (words, caret, mentions, reply and
// edit targets, undo history). The daemon stores all but the undo history
// (db/drafts.go); a draft restored from disk shows who wrote it and when.

// draft is one room's unsent composition.
type draft struct {
	input string
	caret int
	// author (empty for the person at the keyboard) and written are set only for a
	// draft loaded from disk.
	author    string
	written   time.Time
	drafted   []domain.Mention
	replyTo   domain.EventID
	editing   domain.EventID
	editSaved string
	// edits is the composer's undo history, kept in memory only.
	edits editHistory
}

// draftSaver debounces the open room's draft writes: saved is the composition last
// written or loaded for room (words, reply and edit targets: a reply chosen alone is
// written too), gen discards stale ticks, and pending are rooms stashed since the last
// pass whose drafts still need writing (copied on append, so Model copies never share it).
type draftSaver struct {
	saved composerKey
	room  domain.RoomID
	gen   int
	// since is when the open room's composer first changed after its last save:
	// typing that goes on without a pause is saved every draftSaveEvery all the same.
	since   time.Time
	pending []domain.RoomID
	// failing: the last write failed and the reader has been told.
	failing bool
	// bases are the stored versions this client's drafts are based on, per room: set
	// on load, adoption and save. A stored draft that differs was written elsewhere
	// (kith-mcp); a local copy that differs holds typing not yet saved. Copied on write.
	bases map[domain.RoomID]draftStamp
	// inflight are rooms with a save on the wire, and dirty the ones asked to save again
	// meanwhile. One save per room at a time, each based on what the one before it
	// stored, so this client never takes its own write for another writer's addition.
	inflight map[domain.RoomID]bool
	dirty    map[domain.RoomID]bool
	// epoch counts save starts and ends, and touched is each room's last. A poll
	// carries the epoch it was read at and is not taken for a room touched since: it
	// may predate a save that already answered.
	epoch   int
	touched map[domain.RoomID]int
	// retry are rooms whose last save failed (the daemon did not answer, or the store
	// kept changing under it). The next poll that succeeds writes them again.
	retry map[domain.RoomID]bool
	// unsure are the versions this client wrote whose outcome it never heard, per room
	// and oldest first: a write can land and its reply be lost. A stored draft that is
	// one of them, or extends one, descends from this client's own write, so none of it
	// is another writer's (see descentOf). Cleared when a save of the room answers.
	unsure map[domain.RoomID][]ownWrite
	// lastMs is the newest stamp this client gave a write. Stamps only go up, so no two
	// of its writes look alike: one turned down must not be mistaken for one that landed.
	lastMs int64
	// quitting: quit is waiting for the drafts still being written; quitRetries
	// counts the failed saves it tried again.
	quitting    bool
	quitRetries int
}

// ownWrite is a version this client wrote without hearing back, and the assistant's
// words that write folded in, which the local copy never learnt.
type ownWrite struct {
	v     draftStamp
	added string
}

// composerKey is what the debounce watches in the open room's composer.
type composerKey struct {
	text, editSaved string
	reply, editing  domain.EventID
}

func (m Model) composerKey() composerKey {
	c := m.compose
	return composerKey{text: c.input, editSaved: c.editSaved, reply: c.replyTo, editing: c.editing}
}

// touch records a save starting or ending for room.
func (s draftSaver) touch(room domain.RoomID) draftSaver {
	s.epoch++
	s.touched = withEntry(s.touched, room, s.epoch)
	return s
}

// settled reports whether a poll read at epoch issued still describes room: no save
// of it is on the wire, owed, or has started or answered since.
func (s draftSaver) settled(room domain.RoomID, issued int) bool {
	return !s.inflight[room] && !s.dirty[room] && !s.retry[room] && s.touched[room] <= issued &&
		!slices.Contains(s.pending, room)
}

// draftStamp identifies one stored version of a draft, and what it composes: the
// words, the reply and edit targets, and the draft an edit returns to. The zero stamp
// is none stored.
type draftStamp struct {
	body    string
	saved   string
	editing domain.EventID
	reply   domain.EventID
	ms      int64
}

// words is the stamp's draft words: the body, or, while a message was being
// corrected, the draft the edit returns to (the body is the correction then).
func (st draftStamp) words() string {
	if st.editing != "" {
		return st.saved
	}
	return st.body
}

// stampOf is the stored version s is.
func stampOf(s domain.StoredDraft) draftStamp {
	if s.Empty() {
		return draftStamp{}
	}
	return draftStamp{body: s.Body, saved: s.EditSaved, editing: s.Editing, reply: s.ReplyTo, ms: s.Updated.UnixMilli()}
}

// over is the stamp as the draft ReplaceDraft compares against.
func (st draftStamp) over(room domain.RoomID) domain.StoredDraft {
	if st == (draftStamp{}) {
		return domain.StoredDraft{}
	}
	return domain.StoredDraft{
		RoomID: room, Body: st.body, ReplyTo: st.reply, Editing: st.editing, EditSaved: st.saved,
		Updated: time.UnixMilli(st.ms),
	}
}

// withBase is bases with room's set, copied because the Model is copied by value.
func withBase(bases map[domain.RoomID]draftStamp, room domain.RoomID, st draftStamp) map[domain.RoomID]draftStamp {
	out := make(map[domain.RoomID]draftStamp, len(bases)+1)
	maps.Copy(out, bases)
	out[room] = st
	return out
}

// empty reports whether there is nothing worth keeping.
func (d draft) empty() bool {
	return d.input == "" && d.replyTo == "" && d.editing == "" && d.editSaved == ""
}

// stashDraft puts the composer aside under the room being left (before openRoom
// moves) and empties it.
func (m Model) stashDraft(from domain.RoomID) Model {
	if from == "" {
		return m
	}
	held := m.composerDraft()
	held.edits = m.edits[fieldComposer]
	prev := m.drafts[from]
	if held.input == prev.input {
		// Visiting a draft is not writing it: it stays whoever drafted it.
		held.author, held.written = prev.author, prev.written
	}
	m.drafts = setDraft(m.drafts, from, held)
	// The write is deferred to armDraftSave via draftSync.pending, since callers are
	// mid-transition. A draft that matches what is stored writes nothing.
	if m.draftSync.differs(from, held) {
		m.draftSync.pending = append(append([]domain.RoomID{}, m.draftSync.pending...), from)
	}
	return m.withDraft(draft{})
}

// differs reports whether held composes something other than the stored version this
// client is based on: only then is there anything of this client's to write.
func (s draftSaver) differs(room domain.RoomID, held draft) bool {
	base := s.bases[room]
	if held.empty() {
		return base != (draftStamp{})
	}
	return held.input != base.body || held.editSaved != base.saved ||
		held.editing != base.editing || held.replyTo != base.reply
}

// restoreDraft puts a room's stashed draft back in the composer, or empties it.
func (m Model) restoreDraft(to domain.RoomID) Model {
	return m.withDraft(m.drafts[to])
}

// withDraft is the composer set from a draft, empty or not.
func (m Model) withDraft(d draft) Model {
	m.compose.input = d.input
	m.compose.drafted = d.drafted
	m.compose.replyTo = d.replyTo
	m.compose.editing = d.editing
	m.compose.editSaved = d.editSaved
	m.edits[fieldComposer] = d.edits
	// The caret is shared by all fields, so it is claimed only when there is text.
	if d.input != "" {
		m.compose.caret = caret{owner: fieldComposer, at: clampOffset(d.input, d.caret)}
	}
	return m
}

// hasDraft reports whether a room holds an unsent message.
func (m Model) hasDraft(id domain.RoomID) bool {
	if id == m.openRoom {
		return m.compose.input != ""
	}
	_, held := m.drafts[id]
	return held
}

// stored is this draft as it survives a restart, for the room it belongs to.
func (d draft) stored(room domain.RoomID) domain.StoredDraft {
	return domain.StoredDraft{
		RoomID:    room,
		Body:      d.input,
		Caret:     d.caret,
		Mentions:  d.drafted,
		ReplyTo:   d.replyTo,
		Editing:   d.editing,
		EditSaved: d.editSaved,
		Author:    d.author,
		Updated:   time.Now(),
	}
}

// draftFrom is one stored draft read back, with its provenance and no undo history.
func draftFrom(stored domain.StoredDraft) draft {
	return draft{
		input:     stored.Body,
		caret:     stored.Caret,
		drafted:   stored.Mentions,
		replyTo:   stored.ReplyTo,
		editing:   stored.Editing,
		editSaved: stored.EditSaved,
		author:    stored.Author,
		written:   stored.Updated,
	}
}

// draftSaveDelay is the debounce before a draft is written; draftSaveEvery bounds how
// long typing without a pause goes unsaved, which is what an exit that cannot reach the
// daemon loses.
const (
	draftSaveDelay = 900 * time.Millisecond
	draftSaveEvery = 5 * time.Second
)

// draftTickMsg is the debounce firing.
type draftTickMsg struct{ gen int }

// draftsLoadedMsg carries the stored drafts, and the save epoch they were read at.
type draftsLoadedMsg struct {
	drafts []domain.StoredDraft
	issued int
}

// draftTickCmd fires the draft-save debounce.
func draftTickCmd(gen int) tea.Cmd {
	return tea.Tick(draftSaveDelay, func(time.Time) tea.Msg { return draftTickMsg{gen: gen} })
}

// loadDraftsCmd reads every stored draft (at startup and on the poll).
func (m Model) loadDraftsCmd() tea.Cmd {
	ctx, backend, log, issued := m.ctx, m.backend, m.log, m.draftSync.epoch
	return func() tea.Msg {
		drafts, err := backend.Drafts(ctx)
		if err != nil {
			// The composers keep what they hold; the stored drafts are still there.
			log.Warn("load drafts failed", "err", err)
			return nil
		}
		return draftsLoadedMsg{drafts: drafts, issued: issued}
	}
}

// handleDraftsLoaded files stored drafts (at startup and on the poll). A stored draft
// written elsewhere (kith-mcp) since this client's version of it is taken when this
// client holds no unsaved typing there; otherwise the typing stays, and the next save
// folds the other writer's words in (see saveDraftCmd). A draft this client knows
// that is no longer stored was deleted elsewhere, and goes the same way. A room with
// a save on the wire, or one since the poll was read, is left alone: the poll may
// predate it.
func (m Model) handleDraftsLoaded(msg draftsLoadedMsg) (Model, tea.Cmd) {
	stored := make(map[domain.RoomID]bool, len(msg.drafts))
	for i := range msg.drafts {
		stored[msg.drafts[i].RoomID] = true
		m = m.adoptStored(msg.drafts[i], msg.issued)
	}
	for room, base := range m.draftSync.bases {
		if !stored[room] && base != (draftStamp{}) {
			m = m.adoptStored(domain.StoredDraft{RoomID: room}, msg.issued)
		}
	}
	// The daemon answers again: write what it failed to take.
	m, retries := m.saveRetries()
	// The rail's Drafts group must show what arrived.
	return m.rebuiltRail(), batched(retries)
}

// saveRetries asks again for the saves that failed, in room order.
func (m Model) saveRetries() (Model, []tea.Cmd) {
	rooms := slices.Sorted(maps.Keys(m.draftSync.retry))
	m.draftSync.retry = nil
	var writes []tea.Cmd
	for _, room := range rooms {
		var write tea.Cmd
		m, write = m.requestSave(room)
		if write != nil {
			writes = append(writes, write)
		}
	}
	return m, writes
}

// adoptStored takes one room's stored version (empty: deleted) as its draft, unless
// the poll is stale for the room or this client holds unsaved typing there.
func (m Model) adoptStored(stored domain.StoredDraft, issued int) Model {
	room := stored.RoomID
	base := m.draftSync.bases[room]
	if stampOf(stored) == base || !m.draftSync.settled(room, issued) {
		return m
	}
	if m.draftSync.differs(room, m.localDraft(room)) {
		return m // changes not yet saved: they win here, and the save merges
	}
	adopted := draftFrom(stored)
	// Another writer's caret means nothing here: the same words keep this client's
	// caret, and new ones are carried on from the end.
	if local := m.localDraft(room); adopted.input == local.input {
		adopted.caret = local.caret
	} else {
		adopted.caret = len(adopted.input)
	}
	// The composer's undo history stays, so the adoption can be undone.
	if room == m.openRoom {
		adopted.edits = m.edits[fieldComposer]
	} else {
		adopted.edits = m.drafts[room].edits
	}
	m.drafts = setDraft(m.drafts, room, adopted)
	m.draftSync.bases = withBase(m.draftSync.bases, room, stampOf(stored))
	if room == m.openRoom {
		m = m.withDraft(adopted)
		m.draftSync.room, m.draftSync.saved = room, m.composerKey()
	}
	return m
}

// armDraftSave notices the composer has changed and schedules a write. Called from
// Update after every message, since many things change the composer.
func (m Model) armDraftSave() (Model, tea.Cmd) {
	// Rooms stashed since the last pass are written first, open room or not.
	m, writes := m.savePending()
	if m.openRoom == "" {
		return m, batched(writes)
	}

	now := m.composerKey()
	if m.draftSync.room != m.openRoom {
		// Arriving in a room changes nothing stored: re-sync without a write.
		m.draftSync.room, m.draftSync.saved, m.draftSync.since = m.openRoom, now, time.Time{}
		return m, batched(writes)
	}
	if now == m.draftSync.saved {
		return m, batched(writes)
	}
	emptied := now.text == "" && m.draftSync.saved.text != ""
	m.draftSync.saved = now
	m.draftSync.gen++
	m = m.trackComposerDraft()
	if m.draftSync.since.IsZero() {
		m.draftSync.since = time.Now()
	}
	// Emptying is written at once, or the poll could restore the cleared draft in the
	// meantime; so is typing that has gone on unsaved for draftSaveEvery. The
	// generation bump discards any tick in flight.
	if emptied || time.Since(m.draftSync.since) >= draftSaveEvery {
		m.draftSync.since = time.Time{}
		var write tea.Cmd
		m, write = m.requestSave(m.openRoom)
		return m, batched(append(writes, write))
	}
	return m, batched(append(writes, draftTickCmd(m.draftSync.gen)))
}

// savePending asks for the writes of the rooms stashed since the last pass.
func (m Model) savePending() (Model, []tea.Cmd) {
	var writes []tea.Cmd
	pending := m.draftSync.pending
	m.draftSync.pending = nil
	for _, room := range pending {
		var write tea.Cmd
		m, write = m.requestSave(room)
		if write != nil {
			writes = append(writes, write)
		}
	}
	return m, writes
}

// requestSave writes room's draft as this client holds it now, or, while a save of the
// room is on the wire, marks it to be written when that one answers (handleDraftSaved).
func (m Model) requestSave(room domain.RoomID) (Model, tea.Cmd) {
	if m.draftSync.inflight[room] {
		m.draftSync.dirty = withEntry(m.draftSync.dirty, room, true)
		return m, nil
	}
	held := m.localDraft(room)
	if !m.draftSync.differs(room, held) && len(m.draftSync.unsure[room]) == 0 {
		// Nothing of this client's to add: whatever is stored now (an addition, or a
		// deletion elsewhere) stands, and the poll brings it here. A write that went
		// unanswered may have stored something else, so that room is written again.
		return m, nil
	}
	stored := held.stored(room)
	m.draftSync, stored.Updated = m.draftSync.stamped(stored.Updated)
	m.draftSync.inflight = withEntry(m.draftSync.inflight, room, true)
	m.draftSync = m.draftSync.touch(room).mayHaveWritten(room, ownWrite{v: stampOf(stored)})
	return m, m.saveDraftCmd(room, stored)
}

// stamped is now as a write's stamp, after every stamp this client gave before.
func (s draftSaver) stamped(now time.Time) (draftSaver, time.Time) {
	ms := max(now.UnixMilli(), s.lastMs+1)
	s.lastMs = ms
	return s, time.UnixMilli(ms)
}

// unsureKept bounds the writes remembered per room while the daemon does not answer.
const unsureKept = 16

// notWritten forgets versions of room the store turned down.
func (s draftSaver) notWritten(room domain.RoomID, refused []draftStamp) draftSaver {
	if len(refused) == 0 {
		return s
	}
	list := slices.DeleteFunc(slices.Clone(s.unsure[room]), func(w ownWrite) bool {
		return slices.Contains(refused, w.v)
	})
	s.unsure = withEntry(s.unsure, room, list)
	return s
}

// mayHaveWritten records versions of room that may be stored now, reply or not.
func (s draftSaver) mayHaveWritten(room domain.RoomID, writes ...ownWrite) draftSaver {
	list := slices.Clone(s.unsure[room])
	for i := range writes {
		if !slices.ContainsFunc(list, func(had ownWrite) bool { return had.v == writes[i].v }) {
			list = append(list, writes[i])
		}
	}
	if len(list) > unsureKept {
		list = list[len(list)-unsureKept:]
	}
	out := make(map[domain.RoomID][]ownWrite, len(s.unsure)+1)
	maps.Copy(out, s.unsure)
	out[room] = list
	s.unsure = out
	return s
}

// localDraft is room's draft as this client holds it: the composer for the open room
// (keeping its provenance while its words are the loaded ones), else the stashed copy.
func (m Model) localDraft(room domain.RoomID) draft {
	if room != m.openRoom {
		return m.drafts[room]
	}
	held := m.composerDraft()
	if was := m.drafts[room]; was.input == held.input {
		held.author, held.written = was.author, was.written
	}
	return held
}

// trackComposerDraft keeps m.drafts in step with the composer for the open room, so
// the Drafts group clears and the poll cannot refill a just-sent message.
func (m Model) trackComposerDraft() Model {
	held := m.composerDraft()
	held.edits = m.edits[fieldComposer]
	was, had := m.drafts[m.openRoom]
	if held.empty() && !had {
		return m
	}
	// Restoring a draft is not editing it: keep its provenance.
	if had && was.input == held.input {
		return m
	}
	m.drafts = setDraft(m.drafts, m.openRoom, held)
	// Rebuild the rail only when presence in the Drafts group changes.
	if had == held.empty() {
		m = m.rebuiltRail()
	}
	return m
}

// setDraft is drafts with room set to d (removed when empty), copied rather than
// mutated because the Model is copied by value.
func setDraft(drafts map[domain.RoomID]draft, room domain.RoomID, d draft) map[domain.RoomID]draft {
	out := make(map[domain.RoomID]draft, len(drafts)+1)
	maps.Copy(out, drafts)
	if d.empty() {
		delete(out, room)
	} else {
		out[room] = d
	}
	return out
}

// batched is tea.Batch that answers nil for nothing.
func batched(cmds []tea.Cmd) tea.Cmd {
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// handleDraftTick writes, if the composer it was armed for is still the composer.
func (m Model) handleDraftTick(msg draftTickMsg) (Model, tea.Cmd) {
	room := m.draftSync.room
	if msg.gen != m.draftSync.gen || room == "" {
		return m, nil
	}
	m.draftSync.since = time.Time{}
	if !m.draftSync.differs(room, m.composerDraft()) {
		return m, nil // typed back to what is stored: nothing to write
	}
	return m.requestSave(room)
}

// composerDraft is what the composer holds right now, as a draft with no author: a
// draft typed into is the typist's.
func (m Model) composerDraft() draft {
	return draft{
		input:     m.compose.input,
		caret:     m.compose.caret.at,
		drafted:   m.compose.drafted,
		replyTo:   m.compose.replyTo,
		editing:   m.compose.editing,
		editSaved: m.compose.editSaved,
	}
}

// draftSavedMsg is a draft write's outcome: the version now stored, and the words
// the assistant had added that the write folded in, for the room's local copy.
type draftSavedMsg struct {
	err  error
	room domain.RoomID
	// wrote is what the save stored, the assistant's words included.
	wrote domain.StoredDraft
	added string
	// unsure is the version a failed write may have stored, and refused the ones the
	// store turned down (so they are known not to be stored).
	unsure  []ownWrite
	refused []draftStamp
}

// draftAttempts bounds how often a save folds in the assistant's words and retries.
const draftAttempts = 3

// errDraftContended is a save that lost to other writers every time.
var errDraftContended = errors.New("the stored draft kept changing under this write")

// saveDraftCmd writes one room's draft, or clears it, over the version this client
// holds. If the assistant (kith-mcp) appended to it since, its words go after these and
// the write is retried, so neither side's words are lost. A stored version this client
// wrote without hearing back is its own, not the assistant's (descentOf).
func (m Model) saveDraftCmd(room domain.RoomID, stored domain.StoredDraft) tea.Cmd {
	base := m.draftSync.bases[room]
	own := slices.Clone(m.draftSync.unsure[room])
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		var added string
		var refused []draftStamp
		for range draftAttempts {
			saved, err := backend.ReplaceDraft(ctx, stored, base.over(room))
			if ctx.Err() != nil {
				return nil // the program ended (tui.Run cancels its commands); its last write redoes this
			}
			if err != nil {
				return failedSave(err, room, ownWrite{v: stampOf(stored), added: added}, refused)
			}
			if saved {
				return draftSavedMsg{room: room, wrote: stored, added: added}
			}
			// Turned down: this version is not stored, so it is not this client's to claim.
			refused = append(refused, stampOf(stored))
			own = slices.DeleteFunc(own, func(w ownWrite) bool { return w.v == stampOf(stored) })
			current, err := storedDraftOf(ctx, backend, room)
			if err != nil {
				return draftSavedMsg{err: err, room: room, refused: refused}
			}
			over, mine := descentOf(current, base, own)
			own = slices.DeleteFunc(own, func(w ownWrite) bool { return w.v == mine.v })
			// A write of this client's that landed unheard already folded these in.
			stored = withWords(stored, joinDraft(words(stored), mine.added))
			var theirs string
			stored, theirs = rebaseOnto(stored, over, current)
			added = joinDraft(joinDraft(added, mine.added), theirs)
			base = stampOf(current) // the compare-and-set is against what is stored
		}
		return draftSavedMsg{err: errDraftContended, room: room, refused: refused}
	}
}

// failedSave is a save's answer when its write failed. A write the seat refused is
// known not to be stored (another window has it); any other failure may have landed
// with its answer lost, so mine may be stored.
func failedSave(err error, room domain.RoomID, mine ownWrite, refused []draftStamp) draftSavedMsg {
	if errors.Is(err, api.ErrSeatTaken) {
		return draftSavedMsg{err: err, room: room, refused: append(refused, mine.v)}
	}
	return draftSavedMsg{err: err, room: room, unsure: []ownWrite{mine}, refused: refused}
}

// descentOf is the version current was written over, as far as this client can tell:
// one of its own unanswered writes when current is that write, or that write with the
// assistant's words after it (the longest such), else base. The assistant appends
// words only, never mid-edit, and leaves the reply target: a version whose words
// current does not extend, or whose targets it does not hold, is not what current was
// written over. This window is the only one that sets them (api.Seat). Without this, a
// write that landed with its reply lost reads, on the retry, as the assistant's
// addition, and this client's words are doubled.
func descentOf(current domain.StoredDraft, base draftStamp, own []ownWrite) (draftStamp, ownWrite) {
	now := stampOf(current)
	if i := slices.IndexFunc(own, func(w ownWrite) bool { return w.v == now }); i >= 0 {
		return now, own[i]
	}
	under := func(v draftStamp) bool {
		return sameTargets(v, now) && extends(words(current), v.words())
	}
	// The longest words; on a tie this client's write, which came after the base.
	best, mine, longest := base, ownWrite{}, -1
	if under(base) {
		longest = len(base.words())
	}
	for i := range own {
		if v := own[i].v; v.words() != "" && under(v) && len(v.words()) >= longest {
			best, mine, longest = v, own[i], len(v.words())
		}
	}
	return best, mine
}

// sameTargets reports whether two versions mean the same by enter besides their
// words: the reply target, and the message being corrected with its correction.
func sameTargets(a, b draftStamp) bool {
	return a.reply == b.reply && a.editing == b.editing && (a.editing == "" || a.body == b.body)
}

// words is a stored draft's draft words: the body, or, while a message is corrected,
// the draft the edit returns to. withWords sets them.
func words(d domain.StoredDraft) string {
	if d.Editing != "" {
		return d.EditSaved
	}
	return d.Body
}

func withWords(d domain.StoredDraft, text string) domain.StoredDraft {
	if d.Editing != "" {
		d.EditSaved = text
	} else {
		d.Body = text
	}
	return d
}

// rebaseOnto is what this client writes when the stored draft moved from base to
// current under it, and the words the assistant added. Its words go after this
// client's: in the words, or, while a message is corrected, in the draft the edit
// returns to (a correction is not draft words, and is kept). A stored draft that no
// longer holds base (its room was left elsewhere, which deletes it) is kept whole
// beside this client's words, so neither is lost.
func rebaseOnto(stored domain.StoredDraft, base draftStamp, current domain.StoredDraft) (domain.StoredDraft, string) {
	mine := words(stored)
	theirs := addedOver(words(current), base.words(), mine)
	return withWords(stored, joinDraft(mine, theirs)), theirs
}

// storedDraftOf is the room's stored draft, or the empty one.
func storedDraftOf(ctx context.Context, backend api.Drafts, room domain.RoomID) (domain.StoredDraft, error) {
	drafts, err := backend.Drafts(ctx)
	if err != nil {
		return domain.StoredDraft{}, fmt.Errorf("read the stored draft: %w", err)
	}
	for i := range drafts {
		if drafts[i].RoomID == room {
			return drafts[i], nil
		}
	}
	return domain.StoredDraft{}, nil
}

// addedOver is what another writer put in a stored draft: what follows the version
// this client had, since kith-mcp only appends. A draft rewritten some other way, or
// written where this client had none, is kept whole; one equal to what this client
// is writing adds nothing.
func addedOver(stored, base, writing string) string {
	switch {
	case stored == writing:
		return ""
	case base != "" && extends(stored, base):
		return strings.TrimLeft(stored[len(base):], " \n")
	default:
		return stored
	}
}

// extends reports whether text is base, or base and then more after a space or line
// break: "a10" does not extend "a1".
func extends(text, base string) bool {
	if !strings.HasPrefix(text, base) {
		return false
	}
	rest := text[len(base):]
	return rest == "" || base == "" || rest[0] == ' ' || rest[0] == '\n' ||
		strings.HasSuffix(base, " ") || strings.HasSuffix(base, "\n")
}

// joinDraft puts more after text, a blank line between, as kith-mcp appends.
func joinDraft(text, more string) string {
	switch {
	case more == "":
		return text
	case text == "":
		return more
	default:
		return text + "\n\n" + more
	}
}

// handleDraftSaved ends a save: the room's base moves to what it stored, words the
// assistant had added come back to the local copy, and a save owed meanwhile (or one the
// local copy now needs) starts over the new base. It says when drafts stop being kept,
// once per run of failures (the next pause retries, so each retry would repeat it),
// and when they are kept again. A failed clear matters as much: the poll would bring
// the sent draft back.
func (m Model) handleDraftSaved(msg draftSavedMsg) (Model, tea.Cmd) {
	room := msg.room
	if errors.Is(msg.err, api.ErrSeatTaken) {
		// Another window has the seat: this one steps aside, as if it had been told.
		m.draftSync.inflight = withoutEntry(m.draftSync.inflight, room)
		m.draftSync = m.draftSync.touch(room).notWritten(room, msg.refused)
		if m.link.seatLost {
			return m, m.quitWhenDrafted()
		}
		return m.handleSeatLost()
	}
	m.draftSync.inflight = withoutEntry(m.draftSync.inflight, room)
	m.draftSync = m.draftSync.touch(room)
	owed := m.draftSync.dirty[room]
	m.draftSync.dirty = withoutEntry(m.draftSync.dirty, room)
	if msg.err == nil {
		m.draftSync.unsure = withoutEntry(m.draftSync.unsure, room)
	} else {
		m.draftSync = m.draftSync.notWritten(room, msg.refused).mayHaveWritten(room, msg.unsure...)
	}
	if msg.err != nil && !owed {
		if m.draftSync.quitting && m.draftSync.quitRetries < quitSaveRetries {
			// No next poll after quit: try again now, a few times.
			m.draftSync.quitRetries++
			owed = true
		} else {
			m.draftSync.retry = withEntry(m.draftSync.retry, room, true)
		}
	}
	if msg.err == nil {
		m.draftSync.bases = withBase(m.draftSync.bases, room, stampOf(msg.wrote))
		if msg.added != "" {
			m = m.foldInDraft(room, msg.added)
		}
		// Typing (or the fold) since the write is not stored yet.
		owed = owed || !sameStored(m.localDraft(room).stored(room), msg.wrote)
		if room == m.openRoom && sameStored(m.composerDraft().stored(room), msg.wrote) {
			m.draftSync.saved = m.composerKey() // stored already: nothing to arm
		}
	}
	var next tea.Cmd
	if owed {
		m, next = m.requestSave(room)
	}
	m = m.reportDraftSave(msg.err)
	return m, batched([]tea.Cmd{next, m.quitWhenDrafted()})
}

// reportDraftSave tells the reader when drafts stop and start being kept.
func (m Model) reportDraftSave(err error) Model {
	switch {
	case err != nil && !m.draftSync.failing:
		m.draftSync.failing = true
		return m.sayErr("draft not saved", err)
	case err != nil:
		m.logErr(slog.LevelWarn, "save draft", err)
	case m.draftSync.failing:
		m.draftSync.failing = false
		return m.say("drafts are being saved again")
	}
	return m
}

// sameStored reports whether two stored drafts hold the same composition.
func sameStored(a, b domain.StoredDraft) bool {
	return a.Body == b.Body && a.ReplyTo == b.ReplyTo && a.Editing == b.Editing && a.EditSaved == b.EditSaved
}

// foldInDraft appends words another writer added, which a save already stored after
// this client's, to the room's local copy: to the words, or, while a message is being
// corrected, to the draft the composer returns to afterwards.
func (m Model) foldInDraft(room domain.RoomID, added string) Model {
	if room != m.openRoom {
		held := m.drafts[room]
		if held.editing != "" {
			held.editSaved = joinDraft(held.editSaved, added)
		} else {
			held.input = joinDraft(held.input, added)
		}
		m.drafts = setDraft(m.drafts, room, held)
		return m.rebuiltRail()
	}
	if m.compose.isEditing() {
		m.compose.editSaved = joinDraft(m.compose.editSaved, added)
		return m.say("the assistant added to this draft; it is back when the edit ends")
	}
	if m.compose.input == "" {
		// Nothing of this client's to carry on from: carry on after theirs.
		m = m.store(fieldComposer, newEditor(added).end())
	} else {
		m.compose.input = joinDraft(m.compose.input, added)
	}
	m = m.trackComposerDraft()
	return m.say("the assistant added to this draft")
}

// quitDraftWait bounds how long quitting waits for drafts still being written, and
// quitSaveRetries how often it tries a failed one again.
const (
	quitDraftWait   = 2 * time.Second
	quitSaveRetries = 3
)

// quitNowMsg ends the wait for drafts at quit.
type quitNowMsg struct{}

// quitAfterDrafts quits once every draft is written: the ones stashed, the open
// room's if it differs from what is stored, and any save already on the wire. first
// runs before anything else (the open room's colors). A daemon that does not answer
// holds the quit for quitDraftWait at most.
func (m Model) quitAfterDrafts(first tea.Cmd) (Model, tea.Cmd) {
	m.draftSync.quitting = true
	m, writes := m.savePending()
	m, retries := m.saveRetries()
	writes = append(writes, retries...)
	if room := m.openRoom; room != "" && m.draftSync.differs(room, m.composerDraft()) {
		var write tea.Cmd
		m, write = m.requestSave(room)
		writes = append(writes, write)
	}
	if len(m.draftSync.inflight) == 0 {
		return m, tea.Sequence(first, tea.Quit)
	}
	wait := tea.Tick(quitDraftWait, func(time.Time) tea.Msg { return quitNowMsg{} })
	return m, tea.Sequence(first, batched(append(writes, wait)))
}

// quitWhenDrafted is tea.Quit once a quit waiting on drafts has none left in flight.
func (m Model) quitWhenDrafted() tea.Cmd {
	if m.draftSync.quitting && len(m.draftSync.inflight) == 0 {
		return tea.Quit
	}
	return nil
}

// draftNote says who wrote a restored draft and how long ago, or "" for words typed
// or edited in this session.
func (m Model) draftNote() string {
	held, ok := m.drafts[m.openRoom]
	if !ok || held.written.IsZero() || m.compose.input != held.input {
		return ""
	}
	who := "drafted"
	if held.author != "" {
		who = "drafted by " + isolate(held.author)
	}
	return who + " " + shortAgo(time.Since(held.written))
}

// shortAgo is a coarse age for a gutter note ("2h ago").
func shortAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
