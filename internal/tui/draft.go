package tui

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A half-written message belongs to its room: the composer is stashed under the room
// being left and restored on return. Everything that decides what enter means travels
// together (words, caret, mentions, reply and edit targets, undo history). The daemon
// stores all but undo history (db/drafts.go); a draft restored from disk shows who
// wrote it and when.

// draft is one room's unsent composition.
type draft struct {
	input string
	caret int
	// author (empty for the person at the keyboard) and written are set only for a draft
	// loaded from disk.
	author    string
	written   time.Time
	drafted   []domain.Mention
	replyTo   domain.EventID
	editing   domain.EventID
	editSaved string
	// thread is the thread it is written into ("" for the main timeline).
	thread domain.EventID
	// edits is the composer's undo history, kept in memory only.
	edits editHistory
}

// draftSaver debounces the open room's draft writes: saved is the composition last
// written or loaded for room (words and reply/edit targets), gen discards stale ticks,
// and pending are rooms stashed since the last pass that still need writing (copied on
// append, so Model copies never share it).
type draftSaver struct {
	saved composerKey
	room  domain.RoomID
	gen   int
	// since is when the open room's composer first changed after its last save: unbroken
	// typing is still saved every draftSaveEvery.
	since   time.Time
	pending []domain.RoomID
	// failing: the last write failed and the reader has been told.
	failing bool
	// bases are the stored versions this client's drafts are based on, per room, set on
	// load, adoption and save. A differing stored draft was written elsewhere (kith-mcp);
	// a differing local copy holds unsaved typing. Copied on write.
	bases map[domain.RoomID]draftStamp
	// inflight are rooms with a save on the wire, dirty those asked to save again
	// meanwhile. One save per room at a time, each based on what the previous stored, so
	// this client never mistakes its own write for another writer's addition.
	inflight map[domain.RoomID]bool
	dirty    map[domain.RoomID]bool
	// epoch counts save starts and ends, touched is each room's last. A poll carries the
	// epoch it was read at and is ignored for rooms touched since: it may predate a save
	// that already answered.
	epoch   int
	touched map[domain.RoomID]int
	// retry are rooms whose last save failed. The next successful poll writes them again.
	retry map[domain.RoomID]bool
	// unsure are this client's writes whose outcome it never heard, per room, oldest
	// first: a write can land and its reply be lost. Each save hands them to kithd, which
	// reads a stored draft equal to or extending one as descending from this client's own
	// write, so none of it is another writer's (domain.RebaseDraft). Cleared when a save
	// of the room answers.
	unsure map[domain.RoomID][]ownWrite
	// lastMs is the newest stamp this client gave a write. Stamps only increase, so a
	// write turned down is never mistaken for one that landed.
	lastMs int64
	// quitting: quit waits for drafts still being written; quitRetries counts failed
	// saves retried.
	quitting    bool
	quitRetries int
}

// ownWrite is a version this client wrote without hearing back.
type ownWrite struct {
	v draftStamp
}

// composerKey is what the debounce watches in the open room's composer.
type composerKey struct {
	text, editSaved        string
	reply, editing, thread domain.EventID
}

func (m Model) composerKey() composerKey {
	c := m.compose
	return composerKey{text: c.input, editSaved: c.editSaved, reply: c.replyTo, editing: c.editing, thread: m.thread.root}
}

func (s draftSaver) touch(room domain.RoomID) draftSaver {
	s.epoch++
	s.touched = withEntry(s.touched, room, s.epoch)
	return s
}

// settled reports whether a poll read at epoch issued still describes room: no save of
// it is on the wire, owed, or started or answered since.
func (s draftSaver) settled(room domain.RoomID, issued int) bool {
	return !s.inflight[room] && !s.dirty[room] && !s.retry[room] && s.touched[room] <= issued &&
		!slices.Contains(s.pending, room)
}

// draftStamp identifies one stored draft version and what it composes: words, reply
// and edit targets, thread, and the draft an edit returns to. Zero means none stored.
type draftStamp struct {
	body    string
	saved   string
	editing domain.EventID
	reply   domain.EventID
	thread  domain.EventID
	ms      int64
}

func stampOf(s domain.StoredDraft) draftStamp {
	if s.Empty() {
		return draftStamp{}
	}
	return draftStamp{
		body: s.Body, saved: s.EditSaved, editing: s.Editing, reply: s.ReplyTo, thread: s.ThreadRoot,
		ms: s.Updated.UnixMilli(),
	}
}

// over is the stamp as the draft ReplaceDraft compares against.
func (st draftStamp) over(room domain.RoomID) domain.StoredDraft {
	if st == (draftStamp{}) {
		return domain.StoredDraft{}
	}
	return domain.StoredDraft{
		RoomID: room, Body: st.body, ReplyTo: st.reply, Editing: st.editing, EditSaved: st.saved,
		ThreadRoot: st.thread, Updated: time.UnixMilli(st.ms),
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
		// Visiting a draft is not writing it: it keeps its author.
		held.author, held.written = prev.author, prev.written
	}
	m.drafts = setDraft(m.drafts, from, held)
	// The write is deferred to armDraftSave via draftSync.pending, since callers are
	// mid-transition. A draft matching what is stored writes nothing.
	if m.draftSync.differs(from, held) {
		m.draftSync.pending = append(append([]domain.RoomID{}, m.draftSync.pending...), from)
	}
	return m.withDraft(draft{})
}

// differs reports whether held differs from the stored version this client is based
// on: only then is there anything to write.
func (s draftSaver) differs(room domain.RoomID, held draft) bool {
	base := s.bases[room]
	if held.empty() {
		return base != (draftStamp{})
	}
	return held.input != base.body || held.editSaved != base.saved ||
		held.editing != base.editing || held.replyTo != base.reply || held.thread != base.thread
}

// restoreDraft puts a room's stashed draft back in the composer, or empties it. A
// draft written in a thread opens that thread, where it will be sent.
func (m Model) restoreDraft(to domain.RoomID) (Model, tea.Cmd) {
	d := m.drafts[to]
	m, cmd := m.aimAt(d.thread)
	return m.withDraft(d), cmd
}

// aimAt shows the draft's thread (the main timeline for none) so the composer sends
// where the draft is aimed. The composer's mode is kept: the draft is put in
// afterwards, and changing thread drops the reply target it holds.
func (m Model) aimAt(thread domain.EventID) (Model, tea.Cmd) {
	if thread == m.thread.root {
		return m, nil
	}
	mode := m.compose.insertMode
	var cmd tea.Cmd
	if thread == "" {
		m, cmd = m.closeThread()
	} else {
		m, cmd = m.enterThread(thread, "")
	}
	m.compose.insertMode = mode
	return m, cmd
}

// withDraft is the composer set from a draft, empty or not.
func (m Model) withDraft(d draft) Model {
	m.compose.input = d.input
	m.compose.drafted = d.drafted
	m.compose.replyTo = d.replyTo
	m.compose.editing = d.editing
	m.compose.editSaved = d.editSaved
	m.edits[fieldComposer] = d.edits
	// The caret is shared by all fields, so claim it only when there is text.
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

// stored is this draft as it survives a restart.
func (d draft) stored(room domain.RoomID) domain.StoredDraft {
	return domain.StoredDraft{
		RoomID:     room,
		Body:       d.input,
		Caret:      d.caret,
		Mentions:   d.drafted,
		ReplyTo:    d.replyTo,
		Editing:    d.editing,
		EditSaved:  d.editSaved,
		ThreadRoot: d.thread,
		Author:     d.author,
		Updated:    time.Now(),
	}
}

// draftFrom is a stored draft read back, with its provenance and no undo history.
func draftFrom(stored domain.StoredDraft) draft {
	return draft{
		input:     stored.Body,
		caret:     stored.Caret,
		drafted:   stored.Mentions,
		replyTo:   stored.ReplyTo,
		editing:   stored.Editing,
		editSaved: stored.EditSaved,
		thread:    stored.ThreadRoot,
		author:    stored.Author,
		written:   stored.Updated,
	}
}

// draftSaveDelay is the debounce before a write; draftSaveEvery bounds how long
// unbroken typing goes unsaved, which is what an exit that cannot reach the daemon loses.
const (
	draftSaveDelay = 900 * time.Millisecond
	draftSaveEvery = 5 * time.Second
)

type draftTickMsg struct{ gen int }

// draftsLoadedMsg carries the stored drafts, and the save epoch they were read at.
type draftsLoadedMsg struct {
	drafts []domain.StoredDraft
	issued int
}

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

// handleDraftsLoaded files stored drafts (startup and poll). A draft written elsewhere
// (kith-mcp) since this client's version is taken when this client has no unsaved
// typing there; otherwise the typing stays and the next save folds the other words in
// (saveDraftCmd). A known draft no longer stored was deleted elsewhere and goes too.
// Rooms with a save on the wire, or one since the poll was read, are skipped: the
// poll may predate it.
func (m Model) handleDraftsLoaded(msg draftsLoadedMsg) (Model, tea.Cmd) {
	stored := make(map[domain.RoomID]bool, len(msg.drafts))
	var cmds []tea.Cmd
	for i := range msg.drafts {
		stored[msg.drafts[i].RoomID] = true
		var aimed tea.Cmd
		m, aimed = m.adoptStored(msg.drafts[i], msg.issued)
		cmds = append(cmds, aimed)
	}
	for room, base := range m.draftSync.bases {
		if !stored[room] && base != (draftStamp{}) {
			var aimed tea.Cmd
			m, aimed = m.adoptStored(domain.StoredDraft{RoomID: room}, msg.issued)
			cmds = append(cmds, aimed)
		}
	}
	// The daemon answers again: rewrite what it failed to take.
	m, retries := m.saveRetries()
	// The rail's Drafts group must show what arrived.
	return m.rebuiltRail(), batched(slices.DeleteFunc(append(cmds, retries...), func(c tea.Cmd) bool { return c == nil }))
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
// the poll is stale for the room or this client has unsaved typing there.
func (m Model) adoptStored(stored domain.StoredDraft, issued int) (Model, tea.Cmd) {
	room := stored.RoomID
	base := m.draftSync.bases[room]
	if stampOf(stored) == base || !m.draftSync.settled(room, issued) {
		return m, nil
	}
	if m.draftSync.differs(room, m.localDraft(room)) {
		return m, nil // changes not yet saved: they win here, and the save merges
	}
	adopted := draftFrom(stored)
	// Another writer's caret means nothing here: same words keep this caret, new ones put
	// it at the end.
	if local := m.localDraft(room); adopted.input == local.input {
		adopted.caret = local.caret
	} else {
		adopted.caret = len(adopted.input)
	}
	// The undo history stays, so the adoption can be undone.
	if room == m.openRoom {
		adopted.edits = m.edits[fieldComposer]
	} else {
		adopted.edits = m.drafts[room].edits
	}
	m.drafts = setDraft(m.drafts, room, adopted)
	m.draftSync.bases = withBase(m.draftSync.bases, room, stampOf(stored))
	if room != m.openRoom {
		return m, nil
	}
	// The composer shows the draft where it will be sent: a draft written into another
	// thread (by the assistant, into an idle composer) opens that thread. An emptied
	// draft moves nothing.
	var aimed tea.Cmd
	if !adopted.empty() && adopted.thread != m.thread.root {
		m, aimed = m.aimAt(adopted.thread)
		where := "the main timeline"
		if adopted.thread != "" {
			where = "a thread"
		}
		m = m.say("a draft is waiting in " + where + " — showing it")
	}
	m = m.withDraft(adopted)
	m.draftSync.room, m.draftSync.saved = room, m.composerKey()
	return m, aimed
}

// armDraftSave schedules a write when the composer changed. Called after every Update
// message, since many things change the composer.
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
	// Emptying is written at once, or the poll could restore the cleared draft; so is
	// typing unsaved for draftSaveEvery. The generation bump discards any tick in flight.
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

// requestSave writes room's draft as held now or, while a save of the room is on the
// wire, marks it to be written when that answers (handleDraftSaved).
func (m Model) requestSave(room domain.RoomID) (Model, tea.Cmd) {
	if m.draftSync.inflight[room] {
		m.draftSync.dirty = withEntry(m.draftSync.dirty, room, true)
		return m, nil
	}
	held := m.localDraft(room)
	if !m.draftSync.differs(room, held) && len(m.draftSync.unsure[room]) == 0 {
		// Nothing of this client's to add: whatever is stored (an addition, or a deletion
		// elsewhere) stands and the poll brings it. An unanswered write may have stored
		// something else, so that room is written again.
		return m, nil
	}
	stored := held.stored(room)
	m.draftSync, stored.Updated = m.draftSync.stamped(stored.Updated)
	m.draftSync.inflight = withEntry(m.draftSync.inflight, room, true)
	m.draftSync = m.draftSync.touch(room).mayHaveWritten(room, ownWrite{v: stampOf(stored)})
	return m, m.saveDraftCmd(room, stored)
}

// stamped is now as a write stamp, after every stamp this client gave before.
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
// (keeping provenance while its words are the loaded ones), else the stashed copy.
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

// trackComposerDraft keeps m.drafts in step with the open room's composer, so the
// Drafts group clears and the poll cannot refill a just-sent message.
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

// setDraft is drafts with room set to d (removed when empty), copied because the
// Model is copied by value.
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

// batched is tea.Batch, nil for no commands.
func batched(cmds []tea.Cmd) tea.Cmd {
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// handleDraftTick writes if the composer it was armed for is unchanged.
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

// composerDraft is the composer's current content as a draft with no author: a draft
// typed into is the typist's.
func (m Model) composerDraft() draft {
	return draft{
		input:     m.compose.input,
		caret:     m.compose.caret.at,
		drafted:   m.compose.drafted,
		replyTo:   m.compose.replyTo,
		editing:   m.compose.editing,
		editSaved: m.compose.editSaved,
		thread:    m.thread.root,
	}
}

// draftSavedMsg is a draft write's outcome: the version now stored, and the
// assistant's words kithd folded in, for the room's local copy.
type draftSavedMsg struct {
	err  error
	room domain.RoomID
	// wrote is what the save stored, the assistant's words included.
	wrote domain.StoredDraft
	added string
	// unsure is the version a failed write may have stored; refused are those the store
	// turned down (known not stored).
	unsure  []ownWrite
	refused []draftStamp
}

// saveDraftCmd writes or clears one room's draft over the version this client holds.
// kithd rebases it (api.Drafts.RebaseDraft): if the assistant (kith-mcp) appended
// since, its words go after these, so neither side loses words, and come back to be
// folded into the local copy. This client's writes it never heard back about go along,
// so a stored version it wrote is read as its own, not the assistant's.
func (m Model) saveDraftCmd(room domain.RoomID, stored domain.StoredDraft) tea.Cmd {
	base := m.draftSync.bases[room].over(room)
	var unanswered []domain.StoredDraft
	for _, w := range m.draftSync.unsure[room] {
		if w.v != stampOf(stored) { // this write itself is not stored yet
			unanswered = append(unanswered, w.v.over(room))
		}
	}
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		wrote, added, err := backend.RebaseDraft(ctx, stored, base, unanswered)
		if ctx.Err() != nil {
			return nil // the program ended (tui.Run cancels its commands); its last write redoes this
		}
		if err != nil {
			return failedSave(err, room, ownWrite{v: stampOf(stored)})
		}
		return draftSavedMsg{room: room, wrote: wrote, added: added}
	}
}

// failedSave is a save's answer when its write failed. A write the seat refused is
// known not stored (another window has it); any other failure may have landed with
// its answer lost, so mine may be stored.
func failedSave(err error, room domain.RoomID, mine ownWrite) draftSavedMsg {
	if errors.Is(err, api.ErrSeatTaken) {
		return draftSavedMsg{err: err, room: room, refused: []draftStamp{mine.v}}
	}
	return draftSavedMsg{err: err, room: room, unsure: []ownWrite{mine}}
}

// handleDraftSaved ends a save: the room's base moves to what was stored, the
// assistant's added words come back to the local copy, and a save owed meanwhile (or
// now needed) starts over the new base. It reports once per run of failures when
// drafts stop being kept, and when they are kept again. A failed clear matters too:
// the poll would bring the sent draft back.
func (m Model) handleDraftSaved(msg draftSavedMsg) (Model, tea.Cmd) {
	room := msg.room
	if errors.Is(msg.err, api.ErrSeatTaken) {
		// Another window has the seat: step aside.
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
			// No poll after quit: retry now, a few times.
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
	m, switching := m.switchWhenDrafted()
	return m, batched([]tea.Cmd{next, m.quitWhenDrafted(), switching})
}

// reportDraftSave says when drafts stop and start being kept.
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
	return a.Body == b.Body && a.ReplyTo == b.ReplyTo && a.Editing == b.Editing && a.EditSaved == b.EditSaved &&
		a.ThreadRoot == b.ThreadRoot
}

// foldInDraft appends words another writer added, already stored after this client's,
// to the room's local copy: to the words or, while a message is corrected, to the
// draft the composer returns to.
func (m Model) foldInDraft(room domain.RoomID, added string) Model {
	if room != m.openRoom {
		held := m.drafts[room]
		if held.editing != "" {
			held.editSaved = domain.JoinDraft(held.editSaved, added)
		} else {
			held.input = domain.JoinDraft(held.input, added)
		}
		m.drafts = setDraft(m.drafts, room, held)
		return m.rebuiltRail()
	}
	if m.compose.isEditing() {
		m.compose.editSaved = domain.JoinDraft(m.compose.editSaved, added)
		return m.say("the assistant added to this draft; it is back when the edit ends")
	}
	if m.compose.input == "" {
		// Nothing of this client's to continue from: continue after theirs.
		m = m.store(fieldComposer, newEditor(added).end())
	} else {
		m.compose.input = domain.JoinDraft(m.compose.input, added)
	}
	m = m.trackComposerDraft()
	return m.say("the assistant added to this draft")
}

// quitDraftWait bounds how long quitting waits for drafts being written;
// quitSaveRetries how often it retries a failed one.
const (
	quitDraftWait   = 2 * time.Second
	quitSaveRetries = 3
)

// quitNowMsg ends the wait for drafts at quit.
type quitNowMsg struct{}

// quitAfterDrafts quits once every draft is written: those stashed, the open room's if
// it differs from what is stored, and any save on the wire. first runs before anything
// else (the open room's colors). An unresponsive daemon holds the quit at most
// quitDraftWait.
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

// quitWhenDrafted is tea.Quit once a quit waiting on drafts has none in flight.
func (m Model) quitWhenDrafted() tea.Cmd {
	if m.draftSync.quitting && len(m.draftSync.inflight) == 0 {
		return tea.Quit
	}
	return nil
}

// draftNote says who wrote a restored draft and how long ago, or "" for words typed
// or edited this session.
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
