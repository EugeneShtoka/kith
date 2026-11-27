package tui

import (
	"cmp"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A half-written message belongs to its place: a room's main timeline, or one of its
// threads. The composer is stashed under the place being left (another room, a thread
// entered or closed) and restored on return. Everything that decides what enter means
// travels together (words, caret, mentions, reply and edit targets, undo history). The
// daemon stores all but undo history (db/drafts.go); a draft restored from disk shows
// who wrote it and when.

// place is where a draft is written: a room's main timeline, or one of its threads.
type place struct {
	room   domain.RoomID
	thread domain.EventID
}

// placeOf is where a stored draft is written.
func placeOf(s domain.StoredDraft) place { return place{room: s.RoomID, thread: s.ThreadRoot} }

// here is the open room's place: the thread open in it, else its main timeline.
func (m Model) here() place { return place{room: m.openRoom, thread: m.thread.root} }

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
	// edits is the composer's undo history, kept in memory only.
	edits editHistory
}

// draftSaver debounces the open room's draft writes: saved is the composition last
// written or loaded for room (words and reply/edit targets), gen discards stale ticks,
// and pending are rooms stashed since the last pass that still need writing (copied on
// append, so Model copies never share it).
type draftSaver struct {
	saved composerKey
	at    place
	gen   int
	// since is when the open room's composer first changed after its last save: unbroken
	// typing is still saved every draftSaveEvery.
	since   time.Time
	pending []place
	// failing: the last write failed and the reader has been told.
	failing bool
	// bases are the stored versions this client's drafts are based on, per room, set on
	// load, adoption and save. A differing stored draft was written elsewhere (another
	// window, kith-mcp); a differing local copy holds unsaved typing. Copied on write.
	bases map[place]draftStamp
	// inflight are rooms with a save on the wire, dirty those asked to save again
	// meanwhile. One save per room at a time, each based on what the previous stored, so
	// this client never mistakes its own write for another writer's addition.
	inflight map[place]bool
	dirty    map[place]bool
	// epoch counts save starts and ends, touched is each room's last. A poll carries the
	// epoch it was read at and is ignored for rooms touched since: it may predate a save
	// that already answered.
	epoch   int
	touched map[place]int
	// retry are rooms whose last save failed. The next successful poll writes them again.
	retry map[place]bool
	// unsure are this client's writes whose outcome it never heard, per room, oldest
	// first: a write can land and its reply be lost. Each save hands them to kithd, which
	// reads a stored draft equal to or extending one as descending from this client's own
	// write, so none of it is another writer's (domain.RebaseDraft). Cleared when a save
	// of the room answers.
	unsure map[place][]ownWrite
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

// composerKey is what the debounce watches in the open place's composer.
type composerKey struct {
	text, editSaved string
	reply, editing  domain.EventID
}

func (m Model) composerKey() composerKey {
	c := m.compose
	return composerKey{text: c.input, editSaved: c.editSaved, reply: c.replyTo, editing: c.editing}
}

func (s draftSaver) touch(at place) draftSaver {
	s.epoch++
	s.touched = withEntry(s.touched, at, s.epoch)
	return s
}

// settled reports whether a poll read at epoch issued still describes room: no save of
// it is on the wire, owed, or started or answered since.
func (s draftSaver) settled(at place, issued int) bool {
	return !s.inflight[at] && !s.dirty[at] && !s.retry[at] && s.touched[at] <= issued &&
		!slices.Contains(s.pending, at)
}

// draftStamp identifies one stored draft version of a place and what it composes:
// words, reply and edit targets, and the draft an edit returns to. Zero means none
// stored.
type draftStamp struct {
	body    string
	saved   string
	editing domain.EventID
	reply   domain.EventID
	ms      int64
}

func stampOf(s domain.StoredDraft) draftStamp {
	if s.Empty() {
		return draftStamp{}
	}
	return draftStamp{
		body: s.Body, saved: s.EditSaved, editing: s.Editing, reply: s.ReplyTo, ms: s.Updated.UnixMilli(),
	}
}

// over is the stamp as the draft ReplaceDraft compares against.
func (st draftStamp) over(at place) domain.StoredDraft {
	if st == (draftStamp{}) {
		return domain.StoredDraft{}
	}
	return domain.StoredDraft{
		RoomID: at.room, ThreadRoot: at.thread, Body: st.body, ReplyTo: st.reply, Editing: st.editing,
		EditSaved: st.saved, Updated: time.UnixMilli(st.ms),
	}
}

// withBase is bases with room's set, copied because the Model is copied by value.
func withBase(bases map[place]draftStamp, at place, st draftStamp) map[place]draftStamp {
	out := make(map[place]draftStamp, len(bases)+1)
	maps.Copy(out, bases)
	out[at] = st
	return out
}

// empty reports whether there is nothing worth keeping.
func (d draft) empty() bool {
	return d.input == "" && d.replyTo == "" && d.editing == "" && d.editSaved == ""
}

// stashDraft puts the composer aside under the place being left (before openRoom or the
// open thread moves) and empties it.
func (m Model) stashDraft(from place) Model {
	if from.room == "" {
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
		m.draftSync.pending = append(slices.Clone(m.draftSync.pending), from)
	}
	return m.withDraft(draft{})
}

// differs reports whether held differs from the stored version this client is based
// on: only then is there anything to write.
func (s draftSaver) differs(at place, held draft) bool {
	base := s.bases[at]
	if held.empty() {
		return base != (draftStamp{})
	}
	return held.input != base.body || held.editSaved != base.saved ||
		held.editing != base.editing || held.replyTo != base.reply
}

// restoreDraft puts a place's stashed draft back in the composer, or empties it.
func (m Model) restoreDraft(to place) Model {
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
	// The caret is shared by all fields, so claim it only when there is text.
	if d.input != "" {
		m.compose.caret = caret{owner: fieldComposer, at: clampOffset(d.input, d.caret)}
	}
	return m
}

// hasDraft reports whether a room holds an unsent message, in its main timeline or a
// thread.
func (m Model) hasDraft(id domain.RoomID) bool {
	if id == m.openRoom && m.compose.input != "" {
		return true
	}
	return roomDrafted(m.drafts, id)
}

// draftedThreads are the room's threads holding a draft, in place order.
func (m Model) draftedThreads(id domain.RoomID) []domain.EventID {
	var roots []domain.EventID
	for at := range m.drafts {
		if at.room == id && at.thread != "" {
			roots = append(roots, at.thread)
		}
	}
	slices.Sort(roots)
	return roots
}

// threadDrafted reports whether a thread holds an unsent message.
func (m Model) threadDrafted(id domain.RoomID, root domain.EventID) bool {
	if id == m.openRoom && root == m.thread.root && m.compose.input != "" {
		return true
	}
	_, ok := m.drafts[place{room: id, thread: root}]
	return ok
}

// roomDrafted reports whether drafts holds one of room's, in any of its places.
func roomDrafted(drafts map[place]draft, room domain.RoomID) bool {
	for at := range drafts {
		if at.room == room {
			return true
		}
	}
	return false
}

// stored is this draft as it survives a restart.
func (d draft) stored(at place) domain.StoredDraft {
	return domain.StoredDraft{
		RoomID:     at.room,
		ThreadRoot: at.thread,
		Body:       d.input,
		Caret:      d.caret,
		Mentions:   d.drafted,
		ReplyTo:    d.replyTo,
		Editing:    d.editing,
		EditSaved:  d.editSaved,
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

// loadDraftsCmd reads every stored draft (at startup, on reattaching, on CHANGE_DRAFTS,
// and on the poll while a save failed).
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

// handleDraftsLoaded files stored drafts (loadDraftsCmd). A draft written elsewhere
// (another window, kith-mcp) since this client's version is taken when this client has
// no unsaved typing there; otherwise the typing stays and the next save folds the other
// words in (saveDraftCmd). A known draft no longer stored was deleted elsewhere and goes
// too. Rooms with a save on the wire, or one since the read was asked for, are skipped:
// the read may predate it.
func (m Model) handleDraftsLoaded(msg draftsLoadedMsg) (Model, tea.Cmd) {
	stored := make(map[place]bool, len(msg.drafts))
	for i := range msg.drafts {
		stored[placeOf(msg.drafts[i])] = true
		m = m.adoptStored(msg.drafts[i], msg.issued)
	}
	for at, base := range m.draftSync.bases {
		if !stored[at] && base != (draftStamp{}) {
			m = m.adoptStored(domain.StoredDraft{RoomID: at.room, ThreadRoot: at.thread}, msg.issued)
		}
	}
	// The daemon answers again: rewrite what it failed to take.
	m, retries := m.saveRetries()
	// The rail's Drafts group must show what arrived.
	return m.rebuiltRail(), batched(retries)
}

// saveRetries asks again for the saves that failed, in place order.
func (m Model) saveRetries() (Model, []tea.Cmd) {
	rooms := slices.SortedFunc(maps.Keys(m.draftSync.retry), place.compare)
	m.draftSync.retry = nil
	var writes []tea.Cmd
	for _, at := range rooms {
		var write tea.Cmd
		m, write = m.requestSave(at)
		if write != nil {
			writes = append(writes, write)
		}
	}
	return m, writes
}

// adoptStored takes one place's stored version (empty: deleted) as its draft, unless
// the poll is stale for the place or this client has unsaved typing there.
func (m Model) adoptStored(stored domain.StoredDraft, issued int) Model {
	at := placeOf(stored)
	base := m.draftSync.bases[at]
	if stampOf(stored) == base || !m.draftSync.settled(at, issued) {
		return m
	}
	if m.draftSync.differs(at, m.localDraft(at)) {
		return m // changes not yet saved: they win here, and the save merges
	}
	adopted := draftFrom(stored)
	// Another writer's caret means nothing here: same words keep this caret, new ones put
	// it at the end.
	if local := m.localDraft(at); adopted.input == local.input {
		adopted.caret = local.caret
	} else {
		adopted.caret = len(adopted.input)
	}
	// The undo history stays, so the adoption can be undone.
	if at == m.here() {
		adopted.edits = m.edits[fieldComposer]
	} else {
		adopted.edits = m.drafts[at].edits
	}
	m.drafts = setDraft(m.drafts, at, adopted)
	m.draftSync.bases = withBase(m.draftSync.bases, at, stampOf(stored))
	if at != m.here() {
		return m
	}
	m = m.withDraft(adopted)
	m.draftSync.at, m.draftSync.saved = at, m.composerKey()
	return m
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
	if m.draftSync.at != m.here() {
		// Arriving in a room changes nothing stored: re-sync without a write.
		m.draftSync.at, m.draftSync.saved, m.draftSync.since = m.here(), now, time.Time{}
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
		m, write = m.requestSave(m.here())
		return m, batched(append(writes, write))
	}
	return m, batched(append(writes, draftTickCmd(m.draftSync.gen)))
}

// savePending asks for the writes of the rooms stashed since the last pass.
func (m Model) savePending() (Model, []tea.Cmd) {
	var writes []tea.Cmd
	pending := m.draftSync.pending
	m.draftSync.pending = nil
	for _, at := range pending {
		var write tea.Cmd
		m, write = m.requestSave(at)
		if write != nil {
			writes = append(writes, write)
		}
	}
	return m, writes
}

// requestSave writes room's draft as held now or, while a save of the room is on the
// wire, marks it to be written when that answers (handleDraftSaved).
func (m Model) requestSave(at place) (Model, tea.Cmd) {
	if m.draftSync.inflight[at] {
		m.draftSync.dirty = withEntry(m.draftSync.dirty, at, true)
		return m, nil
	}
	held := m.localDraft(at)
	if !m.draftSync.differs(at, held) && len(m.draftSync.unsure[at]) == 0 {
		// Nothing of this client's to add: whatever is stored (an addition, or a deletion
		// elsewhere) stands and the poll brings it. An unanswered write may have stored
		// something else, so that room is written again.
		return m, nil
	}
	stored := held.stored(at)
	m.draftSync, stored.Updated = m.draftSync.stamped(stored.Updated)
	m.draftSync.inflight = withEntry(m.draftSync.inflight, at, true)
	m.draftSync = m.draftSync.touch(at).mayHaveWritten(at, ownWrite{v: stampOf(stored)})
	return m, m.saveDraftCmd(at, stored)
}

// compare orders places by room, then thread, the main timeline first.
func (a place) compare(b place) int {
	return cmp.Or(cmp.Compare(a.room, b.room), cmp.Compare(a.thread, b.thread))
}

// stamped is now as a write stamp, after every stamp this client gave before.
func (s draftSaver) stamped(now time.Time) (draftSaver, time.Time) {
	ms := max(now.UnixMilli(), s.lastMs+1)
	s.lastMs = ms
	return s, time.UnixMilli(ms)
}

// unsureKept bounds the writes remembered per room while the daemon does not answer.
const unsureKept = 16

// mayHaveWritten records versions of room that may be stored now, reply or not.
func (s draftSaver) mayHaveWritten(at place, writes ...ownWrite) draftSaver {
	list := slices.Clone(s.unsure[at])
	for i := range writes {
		if !slices.ContainsFunc(list, func(had ownWrite) bool { return had.v == writes[i].v }) {
			list = append(list, writes[i])
		}
	}
	if len(list) > unsureKept {
		list = list[len(list)-unsureKept:]
	}
	out := make(map[place][]ownWrite, len(s.unsure)+1)
	maps.Copy(out, s.unsure)
	out[at] = list
	s.unsure = out
	return s
}

// localDraft is room's draft as this client holds it: the composer for the open room
// (keeping provenance while its words are the loaded ones), else the stashed copy.
func (m Model) localDraft(at place) draft {
	if at != m.here() {
		return m.drafts[at]
	}
	held := m.composerDraft()
	if was := m.drafts[at]; was.input == held.input {
		held.author, held.written = was.author, was.written
	}
	return held
}

// trackComposerDraft keeps m.drafts in step with the open room's composer, so the
// Drafts group clears and the poll cannot refill a just-sent message.
func (m Model) trackComposerDraft() Model {
	held := m.composerDraft()
	held.edits = m.edits[fieldComposer]
	was, had := m.drafts[m.here()]
	if held.empty() && !had {
		return m
	}
	// Restoring a draft is not editing it: keep its provenance.
	if had && was.input == held.input {
		return m
	}
	m.drafts = setDraft(m.drafts, m.here(), held)
	// Rebuild the rail only when presence in the Drafts group changes.
	if had == held.empty() {
		m = m.rebuiltRail()
	}
	return m
}

// setDraft is drafts with room set to d (removed when empty), copied because the
// Model is copied by value.
func setDraft(drafts map[place]draft, at place, d draft) map[place]draft {
	out := make(map[place]draft, len(drafts)+1)
	maps.Copy(out, drafts)
	if d.empty() {
		delete(out, at)
	} else {
		out[at] = d
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
	at := m.draftSync.at
	if msg.gen != m.draftSync.gen || at.room == "" {
		return m, nil
	}
	m.draftSync.since = time.Time{}
	if !m.draftSync.differs(at, m.composerDraft()) {
		return m, nil // typed back to what is stored: nothing to write
	}
	return m.requestSave(at)
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
	}
}

// draftSavedMsg is a draft write's outcome: the version now stored, and the
// assistant's words kithd folded in, for the room's local copy.
type draftSavedMsg struct {
	err error
	at  place
	// wrote is what the save stored, the assistant's words included.
	wrote domain.StoredDraft
	added string
	// unsure is the version a failed write may have stored.
	unsure []ownWrite
}

// saveDraftCmd writes or clears one room's draft over the version this client holds.
// kithd rebases it (api.Drafts.RebaseDraft): if another window or the assistant
// (kith-mcp) wrote since, its words go after these, so no side loses words, and come
// back to be folded into the local copy. This client's writes it never heard back about
// go along, so a stored version it wrote is read as its own, not another writer's.
func (m Model) saveDraftCmd(at place, stored domain.StoredDraft) tea.Cmd {
	base := m.draftSync.bases[at].over(at)
	var unanswered []domain.StoredDraft
	for _, w := range m.draftSync.unsure[at] {
		if w.v != stampOf(stored) { // this write itself is not stored yet
			unanswered = append(unanswered, w.v.over(at))
		}
	}
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		wrote, added, err := backend.RebaseDraft(ctx, stored, base, unanswered)
		if ctx.Err() != nil {
			return nil // the program ended (tui.Run cancels its commands); its last write redoes this
		}
		if err != nil {
			// It may have landed with its answer lost, so it may be stored.
			return draftSavedMsg{err: err, at: at, unsure: []ownWrite{{v: stampOf(stored)}}}
		}
		return draftSavedMsg{at: at, wrote: wrote, added: added}
	}
}

// handleDraftSaved ends a save: the room's base moves to what was stored, the
// assistant's added words come back to the local copy, and a save owed meanwhile (or
// now needed) starts over the new base. It reports once per run of failures when
// drafts stop being kept, and when they are kept again. A failed clear matters too:
// the poll would bring the sent draft back.
func (m Model) handleDraftSaved(msg draftSavedMsg) (Model, tea.Cmd) {
	at := msg.at
	m.draftSync.inflight = withoutEntry(m.draftSync.inflight, at)
	m.draftSync = m.draftSync.touch(at)
	owed := m.draftSync.dirty[at]
	m.draftSync.dirty = withoutEntry(m.draftSync.dirty, at)
	if msg.err == nil {
		m.draftSync.unsure = withoutEntry(m.draftSync.unsure, at)
	} else {
		m.draftSync = m.draftSync.mayHaveWritten(at, msg.unsure...)
	}
	if msg.err != nil && !owed {
		if m.draftSync.quitting && m.draftSync.quitRetries < quitSaveRetries {
			// No poll after quit: retry now, a few times.
			m.draftSync.quitRetries++
			owed = true
		} else {
			m.draftSync.retry = withEntry(m.draftSync.retry, at, true)
		}
	}
	// Said before the fold, which says another writer's words were added over it.
	m = m.reportDraftSave(msg.err)
	if msg.err == nil {
		m.draftSync.bases = withBase(m.draftSync.bases, at, stampOf(msg.wrote))
		if msg.added != "" {
			m = m.foldInDraft(at, msg.added)
		}
		// Typing (or the fold) since the write is not stored yet.
		owed = owed || !sameStored(m.localDraft(at).stored(at), msg.wrote)
		if at == m.here() && sameStored(m.composerDraft().stored(at), msg.wrote) {
			m.draftSync.saved = m.composerKey() // stored already: nothing to arm
		}
	}
	var next tea.Cmd
	if owed {
		m, next = m.requestSave(at)
	}
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

// foldInDraft appends words another writer (another window, the assistant) added,
// already stored after this client's, to the room's local copy: to the words or, while
// a message is corrected, to the draft the composer returns to. In the open room it says
// so, so nothing appears silently.
func (m Model) foldInDraft(at place, added string) Model {
	if at != m.here() {
		held := m.drafts[at]
		if held.editing != "" {
			held.editSaved = domain.JoinDraft(held.editSaved, added)
		} else {
			held.input = domain.JoinDraft(held.input, added)
		}
		m.drafts = setDraft(m.drafts, at, held)
		return m.rebuiltRail()
	}
	if m.compose.isEditing() {
		m.compose.editSaved = domain.JoinDraft(m.compose.editSaved, added)
		return m.say("another window or the assistant added to this draft; it is back when the edit ends")
	}
	if m.compose.input == "" {
		// Nothing of this client's to continue from: continue after theirs.
		m = m.store(fieldComposer, newEditor(added).end())
	} else {
		m.compose.input = domain.JoinDraft(m.compose.input, added)
	}
	m = m.trackComposerDraft()
	return m.say("another window or the assistant added to this draft")
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
	if at := m.here(); at.room != "" && m.draftSync.differs(at, m.composerDraft()) {
		var write tea.Cmd
		m, write = m.requestSave(at)
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
	held, ok := m.drafts[m.here()]
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
