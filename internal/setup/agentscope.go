package setup

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// `[agent]` read/write scopes, and warnings for write entries that reading rules out.

// AgentReadScope is `[agent.read]` as a least-privilege scope: empty `rooms` shares
// nothing; `except` subtracts from `rooms`.
func AgentReadScope(agent config.Agent) domain.ModelScope {
	return domain.ModelScope{
		Only: agent.Read.Rooms, Except: agent.Read.Except,
		Encrypted: agent.Read.Encrypted, Listed: true,
	}
}

// AgentWriteScope is `[agent.write]` rooms/except/encrypted.
func AgentWriteScope(agent config.Agent) domain.ModelScope {
	return domain.ModelScope{
		Only: agent.Write.Rooms, Except: agent.Write.Except, Encrypted: agent.Write.Encrypted,
	}
}

// AgentPlaces is what resolving entries to rooms needs.
type AgentPlaces interface {
	Rooms(ctx context.Context) ([]domain.Room, error)
	Spaces(ctx context.Context) ([]domain.Space, error)
	RoomEncryption(ctx context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error)
}

// writeEntry is one entry of a write list, with the list it came from.
type writeEntry struct{ list, entry string }

// writeEntries is every entry `[agent.write]` rooms and send name, in file order.
func writeEntries(agent config.Agent) []writeEntry {
	var out []writeEntry
	for _, list := range []struct {
		name    string
		entries []string
	}{{"[agent.write] rooms", agent.Write.Rooms}, {"[agent.write] send", agent.Write.Send}} {
		for _, entry := range list.entries {
			if entry = strings.TrimSpace(entry); entry != "" {
				out = append(out, writeEntry{list.name, entry})
			}
		}
	}
	return out
}

// nothingShared is the one warning that covers every write entry at once.
func nothingShared(entries []writeEntry) string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, fmt.Sprintf("%s %q", e.list, e.entry))
	}
	return "agent: [agent.read] rooms is empty, so nothing is shared with an assistant and " +
		"nothing can be written either — " + strings.Join(names, ", ") +
		" can never take effect. List what it may read in [agent.read] rooms."
}

// AgentStaticWarnings flags write entries reading rules out by spelling alone, so any
// binary can warn at startup without a room list.
func AgentStaticWarnings(agent config.Agent) []string {
	entries := writeEntries(agent)
	if len(entries) == 0 {
		return nil
	}
	if !AgentReadScope(agent).Shares() {
		return []string{nothingShared(entries)}
	}
	var out []string
	for _, e := range entries {
		if why := ruledOut(e.entry, agent.Read); why != "" {
			out = append(out, fmt.Sprintf("agent: %s names %q, but %s, so an assistant can never write there",
				e.list, e.entry, why))
		}
	}
	return out
}

// ruledOut says why no room named by entry can be readable, or "" when that cannot be
// decided from the spelling.
func ruledOut(entry string, read config.AgentRead) string {
	for _, except := range read.Except {
		// Matching is case-insensitive, so an equal except entry removes every room.
		if strings.EqualFold(strings.TrimSpace(except), entry) {
			return "[agent.read] except names it too"
		}
	}
	var kinds []string
	for _, r := range read.Rooms {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if strings.EqualFold(r, entry) {
			return ""
		}
		kinds = append(kinds, entryKind(r))
	}
	only := func(kind string) bool {
		for _, k := range kinds {
			if k != kind {
				return false
			}
		}
		return len(kinds) > 0
	}
	switch kind := entryKind(entry); {
	// Reading names only room IDs, and not this one: a room has one ID.
	case kind == kindID && only(kindID) && !idListed(entry, read.Rooms):
		return "[agent.read] rooms lists room IDs and not this one"
	// A room is behind exactly one network.
	case kind == kindProtocol && only(kindProtocol):
		return "[agent.read] rooms names only other networks"
	// A room is a direct message or a group, never both.
	case kind == kindDM && only(kindGroup):
		return "[agent.read] rooms names only groups"
	case kind == kindGroup && only(kindDM):
		return "[agent.read] rooms names only direct messages"
	}
	return ""
}

// Entry shapes ruledOut can reason about; kindOther (names, spaces, `pinned`) needs
// the rooms.
const (
	kindID       = "id"
	kindProtocol = "protocol"
	kindDM       = "dm"
	kindGroup    = "group"
	kindOther    = "other"
)

func entryKind(entry string) string {
	lower := strings.ToLower(strings.TrimSpace(entry))
	switch {
	case strings.HasPrefix(strings.TrimPrefix(lower, "room:"), "!"):
		return kindID
	case strings.HasPrefix(lower, "protocol:"):
		return kindProtocol
	case lower == "dm":
		return kindDM
	case lower == "group":
		return kindGroup
	default:
		return kindOther
	}
}

// idListed reports whether a room ID appears among the read entries, spelled bare or as
// `room:!id`.
func idListed(entry string, rooms []string) bool {
	id := strings.TrimSpace(trimRoomPrefix(entry))
	for _, r := range rooms {
		if strings.EqualFold(strings.TrimSpace(trimRoomPrefix(r)), id) {
			return true
		}
	}
	return false
}

func trimRoomPrefix(entry string) string {
	entry = strings.TrimSpace(entry)
	if len(entry) >= len("room:") && strings.EqualFold(entry[:len("room:")], "room:") {
		return entry[len("room:"):]
	}
	return entry
}

// examplesShown is how many rooms a resolved warning names.
const examplesShown = 3

// resolveRooms loads rooms, their place facts and encryption; ok is false when there
// is nothing to resolve against.
func resolveRooms(ctx context.Context, places AgentPlaces, named domain.Places) (
	rooms []domain.Room, facts []domain.RoomFacts, crypto map[domain.RoomID]bool, ok bool,
) {
	rooms, err := places.Rooms(ctx)
	if err != nil || len(rooms) == 0 {
		return nil, nil, nil, false
	}
	spaces, err := places.Spaces(ctx)
	if err != nil {
		return nil, nil, nil, false
	}
	ids := make([]domain.RoomID, 0, len(rooms))
	for i := range rooms {
		ids = append(ids, rooms[i].ID)
	}
	crypto, _ = places.RoomEncryption(ctx, ids)
	facts = make([]domain.RoomFacts, len(rooms))
	for i := range rooms {
		facts[i] = named.Facts(rooms[i], domain.HoldersOf(rooms[i].ID, spaces))
	}
	return rooms, facts, crypto, true
}

// AgentWarnings flags write entries reading rules out, resolved against actual rooms
// when places can answer, else statically.
func AgentWarnings(ctx context.Context, places AgentPlaces, named domain.Places, agent config.Agent) []string {
	entries := writeEntries(agent)
	if len(entries) == 0 {
		return nil
	}
	static := AgentStaticWarnings(agent)
	read := AgentReadScope(agent)
	if places == nil || !read.Shares() {
		return static
	}
	rooms, facts, crypto, ok := resolveRooms(ctx, places, named)
	if !ok {
		return static
	}

	placeOnly := read
	placeOnly.Encrypted = true
	var out []string
	for _, e := range entries {
		var outside, sealed []string
		matched := 0
		for i := range rooms {
			if !facts[i].Names(e.entry) {
				continue
			}
			matched++
			switch {
			case !domain.AllowModel(placeOnly, facts[i], "configured", false).Allowed:
				outside = append(outside, rooms[i].DisplayName())
			case crypto[rooms[i].ID] && !read.Encrypted:
				sealed = append(sealed, rooms[i].DisplayName())
			}
		}
		switch {
		case matched == 0:
			out = append(out, staticFor(static, e)...)
		case len(outside) > 0:
			out = append(out, fmt.Sprintf(
				"agent: %s names %q, but %d of the %d rooms it matches are outside [agent.read], "+
					"so an assistant can never write there (for example: %s)",
				e.list, e.entry, len(outside), matched, examples(outside)))
		case len(sealed) > 0:
			out = append(out, fmt.Sprintf(
				"agent: %s names %q, but %d of the %d rooms it matches are encrypted and "+
					"[agent.read] encrypted is false, so an assistant can never write there (for example: %s)",
				e.list, e.entry, len(sealed), matched, examples(sealed)))
		}
	}
	return out
}

// staticFor is the static warning about one entry, if there was one.
func staticFor(static []string, e writeEntry) []string {
	prefix := fmt.Sprintf("agent: %s names %q,", e.list, e.entry)
	for _, w := range static {
		if strings.HasPrefix(w, prefix) {
			return []string{w}
		}
	}
	return nil
}

// examples lists a few room names, sorted so warnings are stable.
func examples(names []string) string {
	sort.Strings(names)
	if len(names) > examplesShown {
		return strings.Join(names[:examplesShown], ", ") + fmt.Sprintf(" and %d more", len(names)-examplesShown)
	}
	return strings.Join(names, ", ")
}
