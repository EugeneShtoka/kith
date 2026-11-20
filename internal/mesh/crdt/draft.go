package crdt

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Drafts: a room's (draft/<room>) and a thread's (threaddraft/<room>/<root>), a
// register keeping every concurrent version, each a JSON object naming its thread's
// root and the words it was written over (internal/mesh writes and reads the rest). A
// version that cleared the draft is a value too, so a version written concurrently
// reads as what it added; an empty value deletes the key (a merged room's, moved under
// the room it joined).
const (
	draftPrefix       = "draft/"
	threadDraftPrefix = "threaddraft/"
)

// DraftKey is the key of the draft of room, or of its thread root.
func DraftKey(room, root string) string {
	if root == "" {
		return draftPrefix + room
	}
	return threadDraftPrefix + room + "/" + root
}

// isDraftKey reports whether key is a draft's.
func isDraftKey(key string) bool {
	return strings.HasPrefix(key, draftPrefix) || strings.HasPrefix(key, threadDraftPrefix)
}

// DraftPlace is the room and thread root a draft value names under key, and whether it
// is a draft's.
func DraftPlace(key string, value []byte) (room, root string, ok bool) {
	var v struct {
		Root string `json:"root"`
	}
	if !isDraftKey(key) || json.Unmarshal(value, &v) != nil {
		return "", "", false
	}
	if rest, room := strings.CutPrefix(key, draftPrefix); room {
		return rest, "", rest != "" && v.Root == ""
	}
	rest, _ := strings.CutPrefix(key, threadDraftPrefix)
	room, ok = strings.CutSuffix(rest, "/"+v.Root)
	return room, v.Root, ok && room != "" && v.Root != ""
}

// readDraftWrite checks w as a draft's write, reporting whether it is one: a place's
// key, holding a version that names it, or nothing (deleted).
func readDraftWrite(w Write) (bool, error) {
	switch {
	case !isDraftKey(w.Key):
		return false, nil
	case len(w.Value) > 0:
		if _, _, ok := DraftPlace(w.Key, w.Value); !ok {
			return true, fmt.Errorf("%w: %s holds no draft", ErrBadWrite, w.Key)
		}
	case w.Key == draftPrefix || w.Key == threadDraftPrefix:
		return true, fmt.Errorf("%w: %s names no room", ErrBadWrite, w.Key)
	}
	return true, nil
}

// Drafts is each draft key's versions, oldest first by (clock, origin), as every
// replica orders them.
func (s State) Drafts() map[string][]Entry {
	out := map[string][]Entry{}
	for key, entries := range s.Entries {
		if !isDraftKey(key) || len(entries) == 0 {
			continue
		}
		sorted := slices.Clone(entries)
		slices.SortFunc(sorted, func(a, b Entry) int {
			if later(a, b) {
				return 1
			}
			return -1
		})
		out[key] = sorted
	}
	return out
}
