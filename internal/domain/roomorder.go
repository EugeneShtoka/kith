package domain

import "strings"

// What order the room list is in.

// The sort keys. Each is one comparator; a chain applies them in order.
const (
	// SortUnread puts the rooms with something unread first.
	SortUnread = "unread"
	// SortMentions puts the rooms where something unread *names you* first.
	SortMentions = "mentions"
	// SortDrafts puts the rooms holding an unsent message first — where you look when
	// you come back to a client you left mid-sentence.
	SortDrafts = "drafts"
	// SortRecent is newest activity first.
	SortRecent = "recent"
	// SortName is by the name the room is shown under here.
	SortName = "name"
)

// SortKey is one comparator in a chain, and whether it is applied backwards.
type SortKey struct {
	Key string
	// Rev flips this comparator only — written "~unread" in config.
	Rev bool
}

// String is how the key is spelled in config.
func (k SortKey) String() string {
	if k.Rev {
		return "~" + k.Key
	}
	return k.Key
}

var sortKeys = map[string]bool{
	SortUnread: true, SortMentions: true, SortDrafts: true, SortRecent: true, SortName: true,
}

// SortKeys is every key, for error messages.
func SortKeys() []string {
	return []string{SortUnread, SortMentions, SortDrafts, SortRecent, SortName}
}

// ParseSortKey reads one chain entry: a key, optionally prefixed "~" to reverse it.
// ok is false for anything unrecognized, so a typo can be reported.
func ParseSortKey(token string) (SortKey, bool) {
	tok := strings.ToLower(strings.TrimSpace(token))
	rev := strings.HasPrefix(tok, "~")
	tok = strings.TrimSpace(strings.TrimPrefix(tok, "~"))
	if sortKeys[tok] {
		return SortKey{Key: tok, Rev: rev}, true
	}
	return SortKey{}, false
}

// ParseSortChain reads a whole chain, returning the keys it understood and the entries
// it did not.
func ParseSortChain(tokens []string) (chain []SortKey, unknown []string) {
	seen := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		key, ok := ParseSortKey(token)
		if !ok {
			unknown = append(unknown, token)
			continue
		}
		if seen[key.Key] {
			continue
		}
		seen[key.Key] = true
		chain = append(chain, key)
	}
	return chain, unknown
}

// DefaultRoomChain is what an unconfigured client sorts by: what needs you, then
// what is happening, then a stable answer for everything else.
func DefaultRoomChain() []SortKey {
	return []SortKey{{Key: SortUnread}, {Key: SortRecent}, {Key: SortName}}
}

// RoomList is what applies to one rail group: the chain its rooms are sorted by.
type RoomList struct {
	Chain []SortKey
}

// Has reports whether a key is in the chain.
func (l RoomList) Has(key string) bool {
	for _, k := range l.Chain {
		if k.Key == key {
			return true
		}
	}
	return false
}

// Without is the chain with a key removed.
func (l RoomList) Without(key string) RoomList {
	out := make([]SortKey, 0, len(l.Chain))
	for _, k := range l.Chain {
		if k.Key != key {
			out = append(out, k)
		}
	}
	return RoomList{Chain: out}
}

// WithFront is the chain with a key at the front, moved there if it was elsewhere.
func (l RoomList) WithFront(key string) RoomList {
	rest := l.Without(key)
	return RoomList{Chain: append([]SortKey{{Key: key}}, rest.Chain...)}
}

// WithTail replaces the *ordering* end of the chain — every key that orders rooms
// within a band — keeping the partitions that come before it.
func (l RoomList) WithTail(keys ...string) RoomList {
	out := make([]SortKey, 0, len(l.Chain)+len(keys))
	for _, k := range l.Chain {
		if !orders(k.Key) {
			out = append(out, k)
		}
	}
	for _, key := range keys {
		out = append(out, SortKey{Key: key})
	}
	return RoomList{Chain: out}
}

// orders reports whether a key sorts rooms within a band rather than partitioning
// them into bands. It is what tells WithTail which half of a chain it is replacing.
func orders(key string) bool { return key == SortRecent || key == SortName }

// Label says what the list is doing, for the status line — the whole chain, because
// a key has just changed part of it and the rest is what makes the result make sense.
func (l RoomList) Label() string {
	if len(l.Chain) == 0 {
		return "unsorted"
	}
	phrases := make([]string, 0, len(l.Chain))
	for _, k := range l.Chain {
		phrases = append(phrases, sortPhrase(k))
	}
	if len(phrases) == 1 {
		return phrases[0]
	}
	return strings.Join(phrases[:len(phrases)-1], ", ") + ", then " + phrases[len(phrases)-1]
}

// sortPhrase is one key in words. Reversed keys are spelled as what they *do* rather
// than as "not": "oldest first" reads, "reverse newest first" does not.
func sortPhrase(k SortKey) string {
	switch k.Key {
	case SortUnread:
		if k.Rev {
			return "unread last"
		}
		return "unread first"
	case SortMentions:
		if k.Rev {
			return "mentions last"
		}
		return "mentions first"
	case SortDrafts:
		if k.Rev {
			return "drafts last"
		}
		return "drafts first"
	case SortRecent:
		if k.Rev {
			return "oldest first"
		}
		return "newest first"
	case SortName:
		if k.Rev {
			return "by name, Z first"
		}
		return "by name"
	}
	return k.String()
}

// RoomListRule overrides one rail group's list.
type RoomListRule struct {
	Group string
	// Sort replaces the whole chain for this group. Empty inherits.
	Sort []string
}

// ResolveRoomList works out what applies to one group: the global chain, with the rule
// naming that group replacing it whole.
func ResolveRoomList(chain []string, rules []RoomListRule, group string) RoomList {
	list := RoomList{Chain: resolveChain(chain)}
	for _, rule := range rules {
		if !strings.EqualFold(strings.TrimSpace(rule.Group), group) {
			continue
		}
		list = RoomList{Chain: resolveChain(rule.Sort)}
	}
	return list
}

// resolveChain is one level's chain, else the default. A chain of only unknown keys
// resolves to the default; setup reports the typo.
func resolveChain(chain []string) []SortKey {
	if parsed, _ := ParseSortChain(chain); len(parsed) > 0 {
		return parsed
	}
	return DefaultRoomChain()
}
