package setup

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The two scopes `[agent]` declares, resolved, and the one mistake worth warning about
// in them: a write entry that reading already rules out.

// AgentReadScope is `[agent.read]` as the scope check takes it: least privilege, so
// an empty `rooms` shares nothing and `except` subtracts from what `rooms` names.
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

// AgentPlaces is what resolving an entry to rooms needs to ask: the rooms, the spaces
// they sit in, and which are encrypted.
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

// AgentStaticWarnings is the write entries reading rules out by their spelling alone,
// with no room list to hand — which is what lets every binary say it at startup.
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
		// Matching is case-insensitive throughout, so an entry spelled the same way
		// names the same rooms: every one of them is subtracted from reading.
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

// The entry shapes ruledOut can reason about. Everything else — a room by name, a space,
// `pinned` — is kindOther, about which nothing is decidable without rooms.
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

// resolveRooms loads what the resolved check reads: every room, its place facts, and
// which rooms are encrypted. ok is false when there is nothing to resolve against.
func resolveRooms(ctx context.Context, places AgentPlaces) (
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
		facts[i] = FactsOf(rooms[i], spaces)
	}
	return rooms, facts, crypto, true
}

// AgentWarnings is the write entries reading rules out, resolved against the rooms this
// account is in when places can answer, and the static check otherwise.
func AgentWarnings(ctx context.Context, places AgentPlaces, agent config.Agent) []string {
	entries := writeEntries(agent)
	if len(entries) == 0 {
		return nil
	}
	static := AgentStaticWarnings(agent)
	read := AgentReadScope(agent)
	if places == nil || !read.Shares() {
		return static
	}
	rooms, facts, crypto, ok := resolveRooms(ctx, places)
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

// examples is a few room names, sorted so the same config warns the same way twice.
func examples(names []string) string {
	sort.Strings(names)
	if len(names) > examplesShown {
		return strings.Join(names[:examplesShown], ", ") + fmt.Sprintf(" and %d more", len(names)-examplesShown)
	}
	return strings.Join(names, ", ")
}

// FactsOf is a room as a place entry can describe it: its ID and shown name, whether it
// is a direct message, the spaces it sits in and the network behind it.
func FactsOf(room domain.Room, spaces []domain.Space) domain.RoomFacts {
	facts := domain.RoomFacts{
		ID: string(room.ID), Name: room.DisplayName(), Direct: room.IsDirect,
		Protocol: domain.ProtocolMatrix,
	}
	for i := range spaces {
		for _, child := range spaces[i].Children {
			if child != room.ID {
				continue
			}
			facts.Spaces = append(facts.Spaces, spaces[i].DisplayName())
			if spaces[i].Bridge.IsBridged() {
				facts.Protocol = spaces[i].Bridge
			}
			break
		}
	}
	return facts
}
