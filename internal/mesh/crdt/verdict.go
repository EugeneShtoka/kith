package crdt

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Verdicts of the networks that keep none of their own: a room's on a tag
// (tag/<room>/<tag>) and a person's (person/<person>/<tag>), each a register whose value
// is a JSON object naming its room or person (id) and lower-cased tag (internal/mesh
// writes and reads the rest); and each tag renamed or deleted (retag/<tag>), a JSON
// object naming the tag it became. An empty value deletes a verdict: a merged room's,
// moved under the room it joined, or one moved under its tag's new name.
const (
	tagVerdictPrefix    = "tag/"
	personVerdictPrefix = "person/"
	retagPrefix         = "retag/"
)

// TagVerdictKey is the key of room's verdict on tag (lower-cased).
func TagVerdictKey(room, tag string) string { return tagVerdictPrefix + room + "/" + tag }

// PersonVerdictKey is the key of person's verdict on tag (lower-cased).
func PersonVerdictKey(person, tag string) string { return personVerdictPrefix + person + "/" + tag }

// RetagKey is the key of the retag of tag (lower-cased).
func RetagKey(tag string) string { return retagPrefix + tag }

// isVerdictKey reports whether key is a verdict's or a retag's.
func isVerdictKey(key string) bool {
	return strings.HasPrefix(key, tagVerdictPrefix) || strings.HasPrefix(key, personVerdictPrefix) ||
		strings.HasPrefix(key, retagPrefix)
}

// VerdictPlace is the room or person (id) and tag a verdict value names under key,
// whether it is a person's, and whether it is a verdict's.
func VerdictPlace(key string, value []byte) (id, tag string, person, ok bool) {
	var v struct {
		ID  string `json:"id"`
		Tag string `json:"tag"`
	}
	if json.Unmarshal(value, &v) != nil || v.ID == "" || v.Tag == "" {
		return "", "", false, false
	}
	switch key {
	case TagVerdictKey(v.ID, v.Tag):
		return v.ID, v.Tag, false, true
	case PersonVerdictKey(v.ID, v.Tag):
		return v.ID, v.Tag, true, true
	}
	return "", "", false, false
}

// RetagOf is the tag a retag key renames and the value it became ("" deleted), and
// whether it is a retag's.
func RetagOf(key string, value []byte) (from, to string, ok bool) {
	var v struct {
		To *string `json:"to"`
	}
	from, ok = strings.CutPrefix(key, retagPrefix)
	if !ok || from == "" || json.Unmarshal(value, &v) != nil || v.To == nil {
		return "", "", false
	}
	return from, *v.To, true
}

// readVerdictWrite checks w as a verdict's or a retag's write, reporting whether it is
// one: a key holding a value that names it, or a verdict deleted.
func readVerdictWrite(w Write) (bool, error) {
	switch {
	case !isVerdictKey(w.Key):
		return false, nil
	case strings.HasPrefix(w.Key, retagPrefix):
		if _, _, ok := RetagOf(w.Key, w.Value); !ok {
			return true, fmt.Errorf("%w: %s holds no retag", ErrBadWrite, w.Key)
		}
	case len(w.Value) > 0:
		if _, _, _, ok := VerdictPlace(w.Key, w.Value); !ok {
			return true, fmt.Errorf("%w: %s holds no verdict", ErrBadWrite, w.Key)
		}
	case strings.Count(w.Key, "/") < 2:
		return true, fmt.Errorf("%w: %s names no verdict", ErrBadWrite, w.Key)
	}
	return true, nil
}

// Verdicts is each verdict key's values and each retag key's, in dot order.
func (s State) Verdicts() map[string][]Entry {
	out := map[string][]Entry{}
	for key, entries := range s.Entries {
		if isVerdictKey(key) && len(entries) > 0 {
			out[key] = entries
		}
	}
	return out
}
