package domain

import "strings"

// Room list sort order.

// The sort keys. Each is one comparator; a chain applies them in order.
const (
	// SortUnread puts the rooms with something unread first.
	SortUnread = "unread"
	// SortMentions puts rooms with an unread mention of you first.
	SortMentions = "mentions"
	// SortDrafts puts rooms holding an unsent draft first.
	SortDrafts = "drafts"
	// SortRecent is newest activity first.
	SortRecent = "recent"
	// SortName is by the room's displayed name.
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

// DefaultRoomChain is the unconfigured chain: unread, recent, name.
func DefaultRoomChain() []SortKey {
	return []SortKey{{Key: SortUnread}, {Key: SortRecent}, {Key: SortName}}
}

// RoomList is the sort chain for one rail group.
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

// WithTail replaces the chain's ordering keys (see orders), keeping its partitions.
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

// orders reports whether a key sorts rooms within a band rather than partitioning them.
func orders(key string) bool { return key == SortRecent || key == SortName }

// Label describes the whole chain in words, for the status line.
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

// sortPhrase is one key in words; reversed keys say what they do ("oldest first").
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
	Group string // a home entry (NamesHome)
	// Sort replaces the whole chain for this group. Empty inherits.
	Sort []string
}

// ResolveRoomList is the global chain, replaced whole by the last rule naming the
// group: its key (a space's name or tag:<name>), shown in the rail as label.
func ResolveRoomList(chain []string, rules []RoomListRule, group, label string) RoomList {
	list := RoomList{Chain: resolveChain(chain)}
	for _, rule := range rules {
		if !NamesHome(rule.Group, group, label) {
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
