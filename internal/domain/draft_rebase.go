package domain

import "strings"

// A room's draft has two writers: the window holding the seat (api.Seat), which
// rewrites it as the person types, and kith-mcp, which only appends the assistant's
// words. A window's write is made over the version it last read; kithd rebases it onto
// what is stored now, in one step, so every client keeps the assistant's words the same
// way and none of them is dropped or doubled.

// RebaseDraft is what to store when a window writes draft over base, the version it last
// read (zero: none), and the store holds current; and the words another writer added
// since, which the window has yet to see. They go after the window's words: in the
// words, or while a message is corrected, in the draft the edit returns to. A stored
// draft that no longer holds base (its room was left elsewhere, or it was begun on both
// sides) is kept whole beside the window's words, so neither is lost.
//
// unanswered are the window's own earlier writes whose answer it never heard: one may
// be stored, the assistant's words after it or not. current is read as descending from
// the longest of them it extends, so a write that landed unheard is not taken for
// another writer's words and written twice.
func RebaseDraft(draft, base, current StoredDraft, unanswered []StoredDraft) (StoredDraft, string) {
	over := descentOf(current, base, unanswered)
	mine := draftWords(draft)
	theirs := addedOver(draftWords(current), draftWords(over), mine)
	return withDraftWords(draft, JoinDraft(mine, theirs)), theirs
}

// descentOf is the version current was written over, as far as the window can tell: one
// of its own unanswered writes when current is that write or that write plus the
// assistant's words (the longest such), else base. The assistant only appends words and
// never changes the targets, and only the seated window sets them, so a version current
// does not extend, or whose targets differ, is not its parent.
func descentOf(current, base StoredDraft, own []StoredDraft) StoredDraft {
	for i := range own {
		if sameVersion(own[i], current) {
			return own[i]
		}
	}
	under := func(v StoredDraft) bool {
		return sameTargets(v, current) && extends(draftWords(current), draftWords(v))
	}
	// The longest words; on a tie the window's write, which came after the base.
	best, longest := base, -1
	if under(base) {
		longest = len(draftWords(base))
	}
	for i := range own {
		if v := own[i]; draftWords(v) != "" && under(v) && len(draftWords(v)) >= longest {
			best, longest = v, len(draftWords(v))
		}
	}
	return best
}

// sameVersion reports whether two drafts are one stored version: the same composition,
// written in the same millisecond.
func sameVersion(a, b StoredDraft) bool {
	return sameTargets(a, b) && a.Body == b.Body && a.EditSaved == b.EditSaved &&
		a.Updated.UnixMilli() == b.Updated.UnixMilli()
}

// sameTargets reports whether two drafts agree on everything but their words: reply
// target, thread, and the message being corrected with its correction.
func sameTargets(a, b StoredDraft) bool {
	return a.ReplyTo == b.ReplyTo && a.ThreadRoot == b.ThreadRoot && a.Editing == b.Editing &&
		(a.Editing == "" || a.Body == b.Body)
}

// draftWords is a draft's words: the body, or while a message is corrected, the draft
// the edit returns to (the body is then the correction). withDraftWords sets them.
func draftWords(d StoredDraft) string {
	if d.Editing != "" {
		return d.EditSaved
	}
	return d.Body
}

func withDraftWords(d StoredDraft, text string) StoredDraft {
	if d.Editing != "" {
		d.EditSaved = text
	} else {
		d.Body = text
	}
	return d
}

// addedOver is what another writer appended to a stored draft after base (kith-mcp only
// appends). A draft rewritten otherwise, or written where there was none, is kept whole;
// one equal to what is being written adds nothing.
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

// extends reports whether text is base, or base followed by more after a space or line
// break: "a10" does not extend "a1".
func extends(text, base string) bool {
	if !strings.HasPrefix(text, base) {
		return false
	}
	rest := text[len(base):]
	return rest == "" || base == "" || rest[0] == ' ' || rest[0] == '\n' ||
		strings.HasSuffix(base, " ") || strings.HasSuffix(base, "\n")
}

// JoinDraft puts more after text, a blank line between, as kith-mcp appends.
func JoinDraft(text, more string) string {
	switch {
	case more == "":
		return text
	case text == "":
		return more
	default:
		return text + "\n\n" + more
	}
}
