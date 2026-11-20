package domain

import "errors"

// Across bridges a draft is a register of the mesh that keeps every concurrent version
// (planning/design-mesh.md, Drafts): two windows on two bridges, or a window on one and
// kith-mcp on the other, can write one place's draft at once. Each version records the
// words it was written over, so a read keeps what each added; text may be repeated
// when both sides rewrote, and none is lost.

// DraftVersion is one version of a place's draft the mesh holds, and the words it was
// written over ("" when there were none).
type DraftVersion struct {
	Draft StoredDraft
	Over  string
}

// FoldDraft is the draft concurrent versions read as, given oldest first by the mesh's
// clock so every bridge reads the same: the newest whole (its targets: reply, thread,
// edit), and after it each other version's words, as RebaseDraft keeps another
// writer's: what it added over the words it was written over, when the newest had seen
// those (written over them too, or holding them); else the whole of it, as those words
// may survive nowhere else (a version its own bridge wrote and wrote over before the
// two met). A version that cleared the draft (it was sent) took its words with it: the
// newest then gives its targets only, and every version gives what it added over words
// a clearing version had seen, so an append made concurrently with a send comes back
// alone.
func FoldDraft(versions []DraftVersion) StoredDraft {
	if len(versions) == 0 {
		return StoredDraft{}
	}
	newest := len(versions) - 1
	var seers []DraftVersion // the versions whose seeing a base lets its words go
	for i := range versions {
		if versions[i].Draft.Empty() {
			seers = append(seers, versions[i])
		}
	}
	cleared := len(seers) > 0
	out := versions[newest].Draft
	if cleared {
		out = withDraftWords(out, "")
	} else {
		seers = versions[newest:]
	}
	for i := range versions {
		v := &versions[i]
		if v.Draft.Empty() || (i == newest && !cleared) {
			continue
		}
		base := ""
		for j := range seers {
			if s := &seers[j]; v.Over == s.Over || extends(draftWords(s.Draft), v.Over) || extends(s.Over, v.Over) {
				base = v.Over
			}
		}
		out = withDraftWords(out, JoinDraft(draftWords(out), addedOver(draftWords(v.Draft), base, draftWords(out))))
	}
	return out
}

// TakeDraft is what a store holding current keeps when the draft moved from from to to
// elsewhere: to, when current is still from; else current, with what to added over
// from after its words (RebaseDraft), so nothing written here and not yet passed on
// is lost.
func TakeDraft(current, from, to StoredDraft) StoredDraft {
	if SameDraft(current, from) {
		return to
	}
	taken, _ := RebaseDraft(current, from, to, nil)
	return taken
}

// SameDraft reports whether two drafts hold the same words and targets.
func SameDraft(a, b StoredDraft) bool {
	return sameTargets(a, b) && a.Body == b.Body && a.EditSaved == b.EditSaved
}

// DraftWords is a draft's words: the body, or while a message is corrected, the draft
// the edit returns to.
func DraftWords(d StoredDraft) string { return draftWords(d) }

// ErrDraftTooLarge is a draft too large for a mesh op: it stays on its bridge.
var ErrDraftTooLarge = errors.New("the draft is over 1 MiB, too large to cross to the other bridges; it stays on this one")

// MeshDraft is one place's draft as the mesh holds it, and what this bridge's cache
// last took of it.
type MeshDraft struct {
	Room RoomID
	Root EventID
	// Draft is the mesh's versions read as one (FoldDraft); empty when it holds none.
	Draft StoredDraft
	// Seen names the versions the mesh holds. Taken is the draft the cache last took
	// from the mesh or wrote into it; Moved is set when the mesh held others then.
	Seen  string
	Taken StoredDraft
	Moved bool
	// Owed is set while a save here changed the cache's draft since the mesh last took
	// it; Saves counts the saves made since kithd started.
	Owed  bool
	Saves uint64
}
