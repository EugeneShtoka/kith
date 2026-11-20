package main

import (
	"context"
	"log"
	"slices"
	"time"

	"gioui.org/widget"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A half-written message belongs to its place, a room's main timeline or one of its
// threads, each with a draft of its own kept in kithd (db/drafts.go), where every window
// and kith-mcp read it, and any number of windows write it. The main composer holds the
// open room's draft, the thread composer the open thread's. This window writes them as
// the desktop does (drafts.ts), in less: after a pause in typing, at once when emptied
// (sent or cleared), each write over the version it last read, which kithd rebases
// (RebaseDraft): words another writer added since go after this window's, and come back
// to be folded in here, saying so. What it reads, at start, when the window gets the
// focus back and whenever kithd says drafts changed (CHANGE_DRAFTS), it takes for places
// with nothing unsaved here and no write started or answered since the read. A thread or
// room left puts its draft aside, to be written and shown again on return.
// shortcut: the thread composer has no reply or edit of its own, so a thread's draft
// written elsewhere answering a message loses that when typed in here; and a failed
// write is tried again only at the next frame.

// draftDelay is the pause in typing before a write, the desktop's and the TUI's.
const draftDelay = 900 * time.Millisecond

// addedNote is what the window says when another writer's words were folded into an
// open draft, the desktop's words.
const addedNote = "another window or the assistant added to this draft"

// place is where a draft is written: a room's main timeline, or one of its threads.
type place struct {
	room   domain.RoomID
	thread domain.EventID
}

// placeOf is where a stored draft is written.
func placeOf(d domain.StoredDraft) place { return place{room: d.RoomID, thread: d.ThreadRoot} }

// drafts is this window's side of kithd's drafts.
type drafts struct {
	base    map[place]domain.StoredDraft   // the stored version each place's words are based on
	local   map[place]domain.StoredDraft   // places not open, with words not yet stored
	unsure  map[place][]domain.StoredDraft // writes whose answer never came; one may be stored
	busy    map[place]bool                 // a write on the wire
	touched map[place]uint64               // the epoch each place's write last started or was answered at
	epoch   uint64
	seen    map[place]domain.StoredDraft // each open composer as last looked at
	changed map[place]time.Time          // and when it last changed
	stamp   time.Time                    // the newest stamp given a write: stamps only go up
}

func newDrafts() drafts {
	return drafts{
		base: map[place]domain.StoredDraft{}, local: map[place]domain.StoredDraft{},
		unsure: map[place][]domain.StoredDraft{}, busy: map[place]bool{},
		touched: map[place]uint64{}, seen: map[place]domain.StoredDraft{}, changed: map[place]time.Time{},
	}
}

// sameComposition is whether two drafts say the same, whenever written.
func sameComposition(a, b domain.StoredDraft) bool {
	return a.Body == b.Body && a.ReplyTo == b.ReplyTo && a.Editing == b.Editing && a.EditSaved == b.EditSaved &&
		slices.Equal(a.Mentions, b.Mentions)
}

// opened are the places whose drafts the composers hold: the open room's, and the
// thread's open beside it.
func (u *ui) opened() []place {
	if u.current == "" {
		return nil
	}
	open := []place{{room: u.current}}
	if t := u.timeline.thread; t != "" {
		open = append(open, place{room: u.current, thread: t})
	}
	return open
}

// isOpen is whether a composer holds at's draft.
func (u *ui) isOpen(at place) bool {
	return at.room == u.current && at.room != "" && (at.thread == "" || at.thread == u.timeline.thread)
}

// editorOf is the composer that holds at's draft while it is open.
func (u *ui) editorOf(at place) *widget.Editor {
	if at.thread != "" {
		return &u.threadComposer
	}
	return &u.composer
}

// composition is the composer holding at as a draft of it.
func (u *ui) composition(at place) domain.StoredDraft {
	if at.thread != "" {
		return domain.StoredDraft{
			RoomID: at.room, ThreadRoot: at.thread, Body: u.threadComposer.Text(), Mentions: u.mentions[&u.threadComposer],
		}
	}
	return domain.StoredDraft{
		RoomID: at.room, Body: u.composer.Text(), ReplyTo: u.replyTo, Editing: u.editing,
		EditSaved: u.editSaved, Mentions: u.mentions[&u.composer],
	}
}

// compose puts d in the composer holding at.
func (u *ui) compose(at place, d domain.StoredDraft) {
	ed := u.editorOf(at)
	ed.SetText(d.Body)
	end := len([]rune(d.Body))
	ed.SetCaret(end, end)
	u.mentions[ed] = d.Mentions
	if at.thread == "" {
		u.replyTo, u.editing, u.editSaved = d.ReplyTo, d.Editing, d.EditSaved
	}
	u.drafts.seen[at] = u.composition(at)
}

// loadDrafts reads every stored draft and takes each where nothing here is unsaved and
// no write of the place started or was answered since the read was asked for (it may be
// older than what that write stored); a draft known here and no longer stored was sent
// or cleared elsewhere.
func (u *ui) loadDrafts(ctx context.Context) {
	u.mu.Lock()
	issued := u.drafts.epoch
	u.mu.Unlock()
	stored, err := u.backend.Drafts(ctx)
	if err != nil {
		log.Printf("drafts: %v", err)
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	byPlace := map[place]domain.StoredDraft{}
	for i := range stored {
		byPlace[placeOf(stored[i])] = stored[i]
	}
	for at := range u.drafts.base {
		if _, ok := byPlace[at]; !ok {
			byPlace[at] = domain.StoredDraft{RoomID: at.room, ThreadRoot: at.thread}
		}
	}
	for at, d := range byPlace {
		settled := !u.drafts.busy[at] && len(u.drafts.unsure[at]) == 0 && u.drafts.touched[at] <= issued
		if settled && !u.unsaved(at) {
			u.take(at, d)
		}
	}
	u.w.Invalidate()
}

// unsaved is whether at holds words here that kithd is not known to hold.
func (u *ui) unsaved(at place) bool {
	if u.isOpen(at) {
		return !sameComposition(u.composition(at), u.drafts.base[at])
	}
	local, ok := u.drafts.local[at]
	return ok && !sameComposition(local, u.drafts.base[at])
}

// take makes d at's stored version, and its words.
func (u *ui) take(at place, d domain.StoredDraft) {
	u.drafts.base[at] = d
	delete(u.drafts.local, at)
	if u.isOpen(at) && !sameComposition(u.composition(at), d) {
		u.compose(at, d)
	}
}

// saveDrafts writes each open composer when it changed and typing paused (at once when
// emptied), and every place left with words unstored.
func (u *ui) saveDrafts(ctx context.Context) {
	for _, at := range u.opened() {
		now := u.composition(at)
		if !sameComposition(now, u.drafts.seen[at]) {
			u.drafts.seen[at], u.drafts.changed[at] = now, time.Now()
			time.AfterFunc(draftDelay, u.w.Invalidate)
		}
		quiet := time.Since(u.drafts.changed[at]) >= draftDelay || now.Empty()
		if quiet && u.owed(now) {
			u.writeDraft(ctx, now)
		}
	}
	for _, d := range u.drafts.local {
		if u.owed(d) {
			u.writeDraft(ctx, d)
		}
	}
}

// owed is whether d is to be written: it says other than its place's stored version, or
// a write of the place was not answered (it may hold what d no longer does).
func (u *ui) owed(d domain.StoredDraft) bool {
	at := placeOf(d)
	return !sameComposition(d, u.drafts.base[at]) || len(u.drafts.unsure[at]) > 0
}

// writeDraft writes d over what its place's words are based on, unless a write of the
// place is on the wire; its answer is the place's new base, and the words another writer
// added are folded into the place's words here, typed since or not.
func (u *ui) writeDraft(ctx context.Context, d domain.StoredDraft) {
	at := placeOf(d)
	if u.drafts.busy[at] {
		return
	}
	u.drafts.busy[at] = true
	stamp := time.Now()
	if !stamp.After(u.drafts.stamp) {
		stamp = u.drafts.stamp.Add(time.Millisecond)
	}
	u.drafts.stamp, d.Updated = stamp, stamp
	base, unsure := u.drafts.base[at], slices.Clone(u.drafts.unsure[at])
	go func() {
		stored, added, err := u.backend.RebaseDraft(ctx, d, base, unsure)
		u.mu.Lock()
		defer u.mu.Unlock()
		delete(u.drafts.busy, at)
		u.touch(at)
		if err != nil {
			log.Printf("draft of %v: %v", at, err)
			// The write may have landed unheard: told to kithd next time, so it is not doubled.
			u.drafts.unsure[at] = append(u.drafts.unsure[at], d)[max(0, len(u.drafts.unsure[at])-3):]
			return
		}
		delete(u.drafts.unsure, at)
		u.drafts.base[at] = stored
		if added != "" {
			u.fold(at, added)
		}
		u.w.Invalidate()
	}()
}

// fold puts another writer's words, which kithd stored after this window's, after the
// place's words here too, or while a message is corrected, after the draft the composer
// returns to; and says so where it is open, so nothing appears silently.
func (u *ui) fold(at place, added string) {
	after := func(c domain.StoredDraft) domain.StoredDraft {
		if c.Editing != "" {
			c.EditSaved = domain.JoinDraft(c.EditSaved, added)
		} else {
			c.Body = domain.JoinDraft(c.Body, added)
		}
		return c
	}
	if u.isOpen(at) {
		ed := u.editorOf(at)
		start, end := ed.Selection()
		u.compose(at, after(u.composition(at)))
		ed.SetCaret(start, end) // the words go after: where the person types stays
		u.note, u.noteAt = addedNote, time.Now()
		return
	}
	if local, ok := u.drafts.local[at]; ok {
		u.drafts.local[at] = after(local)
	}
}

// touch marks a write of at answered (one on the wire is busy), which a read asked for
// earlier may be older than.
func (u *ui) touch(at place) {
	u.drafts.epoch++
	u.drafts.touched[at] = u.drafts.epoch
}

// setAside keeps an open place's words to be written as it closes, and its seen state
// goes: the composer will hold another's.
func (u *ui) setAside(at place) {
	if d := u.composition(at); u.owed(d) {
		u.drafts.local[at] = d
	}
	delete(u.drafts.seen, at)
	delete(u.drafts.changed, at)
}

// bringBack puts at's draft in its composer: its words not yet stored, else what kithd
// holds.
func (u *ui) bringBack(at place) {
	d, ok := u.drafts.local[at]
	if !ok {
		d = u.drafts.base[at]
	}
	delete(u.drafts.local, at) // the composer holds them now
	d.RoomID, d.ThreadRoot = at.room, at.thread
	u.compose(at, d)
}

// leaveThread closes the thread open beside the timeline, its draft set aside and its
// composer emptied.
func (u *ui) leaveThread() {
	t := &u.timeline
	if t.thread == "" {
		return
	}
	u.setAside(place{room: u.current, thread: t.thread})
	u.threadComposer.SetText("")
	delete(u.mentions, &u.threadComposer)
	t.thread = ""
}

// switchDraft keeps the room being left's words to be written, and puts the opened
// room's in the composer.
func (u *ui) switchDraft(from, to domain.RoomID) {
	if from != "" {
		u.setAside(place{room: from})
	}
	u.current = to
	u.bringBack(place{room: to})
}
