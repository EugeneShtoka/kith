package main

import (
	"context"
	"log"
	"slices"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A room's half-written message is kept in kithd (db/drafts.go), where every window and
// kith-mcp read it. This window writes the main composer's draft as the desktop does
// (drafts.ts), in less: after a pause in typing, at once when emptied (sent or cleared),
// each write over the version it last read, which kithd rebases (RebaseDraft) so words
// another writer added since are kept after this window's. What it reads, at start and
// when the window gets the focus back, it takes for rooms with nothing unsaved here.
// shortcut: the thread composer's draft is not kept, and writes are not retried until
// the next change or load.

// draftDelay is the pause in typing before a write, the desktop's and the TUI's.
const draftDelay = 900 * time.Millisecond

// drafts is this window's side of kithd's drafts.
type drafts struct {
	base    map[domain.RoomID]domain.StoredDraft   // the stored version each room's words are based on
	local   map[domain.RoomID]domain.StoredDraft   // rooms not open, with words not yet stored
	unsure  map[domain.RoomID][]domain.StoredDraft // writes whose answer never came; one may be stored
	busy    map[domain.RoomID]bool                 // a write on the wire
	seen    domain.StoredDraft                     // the open composer as last looked at
	changed time.Time                              // when it last changed
	stamp   time.Time                              // the newest stamp given a write: stamps only go up
}

func newDrafts() drafts {
	return drafts{
		base: map[domain.RoomID]domain.StoredDraft{}, local: map[domain.RoomID]domain.StoredDraft{},
		unsure: map[domain.RoomID][]domain.StoredDraft{}, busy: map[domain.RoomID]bool{},
	}
}

// sameComposition is whether two drafts say the same, whenever written.
func sameComposition(a, b domain.StoredDraft) bool {
	return a.Body == b.Body && a.ReplyTo == b.ReplyTo && a.Editing == b.Editing && a.EditSaved == b.EditSaved &&
		slices.Equal(a.Mentions, b.Mentions)
}

// composition is the main composer as a draft of the open room.
func (u *ui) composition() domain.StoredDraft {
	return domain.StoredDraft{
		RoomID: u.current, Body: u.composer.Text(), ReplyTo: u.replyTo, Editing: u.editing,
		EditSaved: u.editSaved, Mentions: u.mentions[&u.composer],
	}
}

// compose puts d in the main composer.
func (u *ui) compose(d domain.StoredDraft) {
	u.composer.SetText(d.Body)
	end := len([]rune(d.Body))
	u.composer.SetCaret(end, end)
	u.replyTo, u.editing, u.editSaved, u.mentions[&u.composer] = d.ReplyTo, d.Editing, d.EditSaved, d.Mentions
	u.drafts.seen = u.composition()
}

// loadDrafts reads every stored draft and takes each where nothing here is unsaved; a
// draft known here and no longer stored was sent or cleared elsewhere.
func (u *ui) loadDrafts(ctx context.Context) {
	stored, err := u.backend.Drafts(ctx)
	if err != nil {
		log.Printf("drafts: %v", err)
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	byRoom := map[domain.RoomID]domain.StoredDraft{}
	for _, d := range stored {
		if d.ThreadRoot == "" {
			byRoom[d.RoomID] = d
		}
	}
	for room := range u.drafts.base {
		if _, ok := byRoom[room]; !ok {
			byRoom[room] = domain.StoredDraft{RoomID: room}
		}
	}
	for room, d := range byRoom {
		if !u.unsaved(room) {
			u.take(room, d)
		}
	}
	u.w.Invalidate()
}

// unsaved is whether room holds words here that kithd is not known to hold.
func (u *ui) unsaved(room domain.RoomID) bool {
	if local, ok := u.drafts.local[room]; ok {
		return !sameComposition(local, u.drafts.base[room])
	}
	return room == u.current && !sameComposition(u.composition(), u.drafts.base[room])
}

// take makes d room's stored version, and its words where nothing there is unsaved.
func (u *ui) take(room domain.RoomID, d domain.StoredDraft) {
	if u.drafts.busy[room] {
		return // the write on the wire answers with what is stored
	}
	u.drafts.base[room] = d
	delete(u.drafts.local, room)
	if room == u.current {
		u.compose(d)
	}
}

// saveDrafts writes the open composer when it changed and typing paused (at once when
// emptied), and every room left with words unstored.
func (u *ui) saveDrafts(ctx context.Context) {
	if u.current == "" {
		return
	}
	now := u.composition()
	if !sameComposition(now, u.drafts.seen) {
		u.drafts.seen, u.drafts.changed = now, time.Now()
		time.AfterFunc(draftDelay, u.w.Invalidate)
	}
	quiet := time.Since(u.drafts.changed) >= draftDelay || now.Empty()
	if quiet && !sameComposition(now, u.drafts.base[u.current]) {
		u.writeDraft(ctx, now)
	}
	for room, d := range u.drafts.local {
		if !sameComposition(d, u.drafts.base[room]) {
			u.writeDraft(ctx, d)
		}
	}
}

// writeDraft writes d over what its room's words are based on, unless a write of the
// room is on the wire; its answer is the room's new base, and words another writer
// added come into the composer if nothing was typed since.
func (u *ui) writeDraft(ctx context.Context, d domain.StoredDraft) {
	room := d.RoomID
	if u.drafts.busy[room] {
		return
	}
	u.drafts.busy[room] = true
	stamp := time.Now()
	if !stamp.After(u.drafts.stamp) {
		stamp = u.drafts.stamp.Add(time.Millisecond)
	}
	u.drafts.stamp, d.Updated = stamp, stamp
	base, unsure := u.drafts.base[room], slices.Clone(u.drafts.unsure[room])
	go func() {
		stored, _, err := u.backend.RebaseDraft(ctx, d, base, unsure)
		u.mu.Lock()
		defer u.mu.Unlock()
		delete(u.drafts.busy, room)
		if err != nil {
			log.Printf("draft of %s: %v", room, err)
			// The write may have landed unheard: told to kithd next time, so it is not doubled.
			u.drafts.unsure[room] = append(u.drafts.unsure[room], d)[max(0, len(u.drafts.unsure[room])-3):]
			return
		}
		delete(u.drafts.unsure, room)
		u.drafts.base[room] = stored
		if local, ok := u.drafts.local[room]; ok && sameComposition(local, d) {
			delete(u.drafts.local, room)
		}
		if room == u.current && sameComposition(u.composition(), d) && !sameComposition(stored, d) {
			u.compose(stored) // another writer's words, after this window's
		}
		u.w.Invalidate()
	}()
}

// switchDraft keeps the room being left's words to be written, and puts the opened
// room's in the composer: its words not yet stored, else what kithd holds.
func (u *ui) switchDraft(from, to domain.RoomID) {
	if from != "" {
		if d := u.composition(); !sameComposition(d, u.drafts.base[from]) {
			u.drafts.local[from] = d
		}
	}
	d, ok := u.drafts.local[to]
	if !ok {
		d = u.drafts.base[to]
	}
	d.RoomID = to
	u.current = to
	u.compose(d)
}
