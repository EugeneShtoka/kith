package tui

import (
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Room-list order, resolved per rail group on every draw (see domain.RoomOrder), so
// it follows config reloads and session overrides.

// roomList is a rail group's order: config's answer with this session's per-group
// override on top. Overrides last the session only, so they never disagree with the
// file for long.
func (m Model) roomList(group string) domain.RoomList {
	list := domain.ResolveRoomList(m.prefs.display.Rooms.Sort, m.rail.rules, group)
	if chain, ok := m.rail.chains[group]; ok {
		list.Chain = chain
	}
	return list
}

// roomListRules converts the configured rules once rather than on every draw.
func roomListRules(rooms config.Rooms) []domain.RoomListRule {
	if len(rooms.Rules) == 0 {
		return nil
	}
	out := make([]domain.RoomListRule, 0, len(rooms.Rules))
	for _, rule := range rooms.Rules {
		out = append(out, domain.RoomListRule{Group: rule.Group, Sort: rule.Sort})
	}
	return out
}

// setRoomOrder changes what orders the current group, leaving the partitioning bands
// in front of it alone (see RoomList.WithTail).
func (m Model) setRoomOrder(keys ...string) (Model, tea.Cmd) {
	return m.changeChain(func(list domain.RoomList) domain.RoomList {
		return list.WithTail(keys...)
	})
}

// toggleSortKey adds a partitioning key to the front of the group's chain, or
// removes it.
func (m Model) toggleSortKey(key string) (Model, tea.Cmd) {
	return m.changeChain(func(list domain.RoomList) domain.RoomList {
		if list.Has(key) {
			return list.Without(key)
		}
		return list.WithFront(key)
	})
}

// changeChain edits the current group's chain and says what the list now does. The
// cursor stays on its room: selection is by ID, so the row moves, not the cursor.
func (m Model) changeChain(edit func(domain.RoomList) domain.RoomList) (Model, tea.Cmd) {
	entry, ok := m.currentGroup()
	if !ok {
		return m, nil
	}
	m.rail.chains = withEntry(m.rail.chains, entry.key, edit(m.roomList(entry.key)).Chain)
	m = m.say(isolate(entry.label) + ": " + m.roomList(entry.key).Label())
	return m, repaint()
}

// orderRooms sorts a group's rooms by its chain, stably over the alphabetical order
// they arrive in. Each room's keys are read once, before sorting: a comparison is made
// n log n times, and reading a label there (a space lookup each) was most of a keypress
// in a few hundred rooms.
func (m Model) orderRooms(rooms []domain.Room, list domain.RoomList) []domain.Room {
	if len(rooms) < 2 || len(list.Chain) == 0 {
		return rooms
	}
	view := m.unreadView()
	keys := make([]roomKeys, len(rooms))
	for i := range rooms {
		keys[i] = m.roomKeysOf(i, &rooms[i], view)
	}
	chain := compileChain(list.Chain)
	// The one read of the Model the comparator needs, once per room: a comparator that
	// were a Model method would copy the whole Model on each of n log n calls.
	name := func(k *roomKeys) string {
		if !k.named {
			k.name, k.named = strings.ToLower(m.roomLabel(rooms[k.index])), true
		}
		return k.name
	}
	// Indices, not the keys themselves, are what moves: generic and small, where
	// sort.SliceStable swapped whole structs through reflection.
	order := make([]int32, len(rooms))
	for i := range order {
		order[i] = int32(i)
	}
	slices.SortStableFunc(order, func(a, b int32) int {
		ka, kb := &keys[a], &keys[b]
		for _, key := range chain {
			cmp, fixed := compareRooms(key.by, ka, kb, name)
			if cmp == 0 {
				continue
			}
			if key.rev && !fixed {
				return -cmp
			}
			return cmp
		}
		return 0
	})
	ordered := make([]domain.Room, len(rooms))
	for i, at := range order {
		ordered[i] = rooms[at]
	}
	return ordered
}

// sortBy is a sort key resolved once per sort, so a comparison switches on a small
// integer rather than comparing key names.
type sortBy uint8

const (
	byNothing sortBy = iota
	byUnread
	byMentions
	byDrafts
	byRecent
	byName
)

// chainKey is one compiled link of a chain.
type chainKey struct {
	by  sortBy
	rev bool
}

// compileChain resolves a chain's key names; one this client does not know sorts
// nothing, as before.
func compileChain(chain []domain.SortKey) []chainKey {
	out := make([]chainKey, 0, len(chain))
	for _, k := range chain {
		by := byNothing
		switch k.Key {
		case domain.SortUnread:
			by = byUnread
		case domain.SortMentions:
			by = byMentions
		case domain.SortDrafts:
			by = byDrafts
		case domain.SortRecent:
			by = byRecent
		case domain.SortName:
			by = byName
		}
		out = append(out, chainKey{by: by, rev: k.Rev})
	}
	return out
}

// roomKeys is what the sort keys read of one room.
type roomKeys struct {
	index                    int // into the rooms being sorted
	unread, mentions, drafts bool
	last                     time.Time
	known                    bool // last is a cached message's time
	// name is the lower-cased label, read on first use (orderRooms' name): only rooms tied
	// on every earlier key need one, and a label is the dear part.
	name  string
	named bool
}

// roomKeysOf reads a room's keys. A room that was unread when opened
// (m.opened.wasUnread) keeps its unread-band place until left, so reading it does not
// slide it away from under the cursor.
func (m Model) roomKeysOf(index int, room *domain.Room, view unreadView) roomKeys {
	k := roomKeys{
		index:    index,
		unread:   view.tallies(*room) || room.ID == m.timeline.opened.wasUnread,
		mentions: m.unread[room.ID].Mentions > 0,
		drafts:   m.hasDraft(room.ID),
	}
	k.last, k.known = m.lastMessage[room.ID]
	return k
}

// compareRooms is one key's comparator: negative when a comes first, zero to defer to
// the next key. fixed marks an answer that `~` must not flip. name is a room's
// lower-cased label, read on first use.
func compareRooms(by sortBy, a, b *roomKeys, name func(*roomKeys) string) (cmp int, fixed bool) {
	switch by {
	case byUnread:
		return flagFirst(a.unread, b.unread), false
	case byMentions:
		return flagFirst(a.mentions, b.mentions), false
	case byDrafts:
		return flagFirst(a.drafts, b.drafts), false
	case byRecent:
		if a.known != b.known {
			// Rooms with no cached history sort last in both directions.
			return flagFirst(a.known, b.known), true
		}
		return b.last.Compare(a.last), false
	case byName:
		return strings.Compare(name(a), name(b)), false
	case byNothing:
	}
	return 0, false
}

// flagFirst compares two booleans as "true comes first".
func flagFirst(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return -1
	}
	return 1
}

// noteActivity records a room's newest message time. Edits and redactions carry the
// original's timestamp, so they do not move a room.
func (m Model) noteActivity(msg domain.Message) Model {
	if msg.RoomID == "" || msg.Timestamp.IsZero() {
		return m
	}
	if at, known := m.lastMessage[msg.RoomID]; known && !msg.Timestamp.After(at) {
		return m
	}
	m.lastMessage = withEntry(m.lastMessage, msg.RoomID, msg.Timestamp)
	return m
}

// handleLastMessages merges the cache's last-message times, keeping any newer ones
// that arrived meanwhile. A failure leaves the alphabetical order.
func (m Model) handleLastMessages(msg lastMessagesMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelWarn, "load last-message times", msg.err)
	if msg.err != nil || len(msg.at) == 0 {
		return m, nil
	}
	latest := make(map[domain.RoomID]time.Time, len(m.lastMessage)+len(msg.at))
	maps.Copy(latest, m.lastMessage)
	for roomID, at := range msg.at {
		if known, ok := latest[roomID]; ok && !at.After(known) {
			continue
		}
		latest[roomID] = at
	}
	m.lastMessage = latest
	return m, nil
}
