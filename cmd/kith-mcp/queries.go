package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// allowed reports whether a room may be read, and says why not when it may not.
func (s *server) allowed(ctx context.Context, room domain.Room) error {
	return s.permits(ctx, room, s.readScope(), readTable)
}

// readScope is `[agent.read]` forced to least privilege, so an empty list never means
// every room.
func (s *server) readScope() domain.ModelScope {
	scope := s.scope
	scope.Listed = true
	return scope
}

// nothingSharedNote is what the assistant is told when `[agent.read] rooms` is empty.
const nothingSharedNote = "Nothing in this account is shared with you: [agent.read] rooms in " +
	"their kith config is empty, and an empty list shares nothing. You cannot read or write " +
	"any room until they list rooms or spaces there (for example rooms = [\"space:Work\"]). " +
	"Tell them that rather than retrying."

// errNothingShared is nothingSharedNote as an error.
var errNothingShared = errors.New(strings.TrimSuffix(nothingSharedNote, "."))

// writable reports whether a room may be written to: read scope first, then write scope,
// so a refusal names the right table.
func (s *server) writable(ctx context.Context, room domain.Room) error {
	if err := s.allowed(ctx, room); err != nil {
		return err
	}
	return s.permits(ctx, room, s.write, writeTable)
}

// The two tables a refusal can name.
const (
	readTable  = "[agent.read]"
	writeTable = "[agent.write]"
)

// permits asks one scope about one room, and words a refusal in the table's own terms.
func (s *server) permits(ctx context.Context, room domain.Room, scope domain.ModelScope, table string) error {
	if !scope.Shares() {
		return errNothingShared
	}
	facts, err := s.factsOf(ctx, room)
	if err != nil && scope.NeedsPlaces() {
		return err
	}
	// An unknown encryption state counts as encrypted (see encrypted).
	permit := domain.AllowModel(scope, facts, "configured", s.encrypted(ctx, room.ID))
	if permit.Allowed {
		return nil
	}
	if permit.Refusal == domain.ModelRefusedNothingListed {
		return errNothingShared
	}
	if permit.Refusal == domain.ModelRefusedEncrypted {
		return fmt.Errorf("this room is encrypted and %s encrypted is false, so it is outside what %s allows", table, table)
	}
	return fmt.Errorf("this room is outside what %s allows: its rooms/except lists do not admit it", table)
}

// factsOf describes a room for every scope list alike.
func (s *server) factsOf(ctx context.Context, room domain.Room) (domain.RoomFacts, error) {
	facts := domain.RoomFacts{ID: string(room.ID), Name: room.DisplayName(), Direct: room.IsDirect}
	var err error
	facts.Spaces, facts.Protocol, err = s.placeOf(ctx, room.ID)
	return facts, err
}

// errPlaceUnknown refuses a room whose spaces could not be read. Guessing "in no space"
// would fail open: an `except = ["space:…"]` entry would stop matching, and a
// `protocol:` entry would take a bridged room for a Matrix one.
var errPlaceUnknown = errors.New("could not tell which spaces this room is in, so it is " +
	"not shared for now; try again in a moment")

// placesKnown fails a listing whose scope checks would all be guesses: with the spaces
// unreadable, a scope naming places refuses every room, and an empty answer would read
// as "nothing there".
func (s *server) placesKnown(ctx context.Context) error {
	if read := s.readScope(); !read.Shares() || !read.NeedsPlaces() {
		return nil
	}
	if _, err := s.spaces(ctx); err != nil {
		return fmt.Errorf("%w: %w", errPlaceUnknown, err)
	}
	return nil
}

// placeOf is a room's spaces and the network behind it, for the scope check.
func (s *server) placeOf(ctx context.Context, roomID domain.RoomID) ([]string, domain.Protocol, error) {
	spaces, err := s.spaces(ctx)
	if err != nil {
		s.logger().Warn("space lookup for the scope check failed", "room", roomID, "err", err)
		return nil, domain.ProtocolMatrix, fmt.Errorf("%w: %w", errPlaceUnknown, err)
	}
	var names []string
	protocol := domain.ProtocolMatrix
	for i := range spaces {
		for _, child := range spaces[i].Children {
			if child != roomID {
				continue
			}
			names = append(names, spaces[i].DisplayName())
			if spaces[i].Bridge.IsBridged() {
				protocol = spaces[i].Bridge
			}
			break
		}
	}
	return names, protocol, nil
}

// encrypted asks the daemon whether a room is encrypted, answering yes on failure.
// "Encrypted" is kept for the process's life (it never reverts); "not encrypted" only
// for the tool call, since the room can turn encryption on at any time. A failure is
// asked again next time.
func (s *server) encrypted(ctx context.Context, roomID domain.RoomID) bool {
	s.mu.Lock()
	known := s.crypto[roomID]
	s.mu.Unlock()
	memo := memoOf(ctx)
	if known {
		return true
	}
	if memo.isPlain(roomID) {
		return false
	}
	state, err := s.backend.RoomEncryption(ctx, []domain.RoomID{roomID})
	if err != nil {
		// Fail closed, and do not remember it: one daemon hiccup must not hide a
		// room for the rest of the session.
		s.logger().Warn("room encryption lookup failed; treating the room as encrypted", "room", roomID, "err", err)
		return true
	}
	answer := state[roomID]
	if answer {
		s.noteEncrypted(roomID)
	} else {
		memo.notePlain(roomID)
	}
	return answer
}

// roomsInScope splits the room list by what `[agent.read]` allows.
func (s *server) roomsInScope(ctx context.Context) (in, out []domain.RoomID, err error) {
	readable, outside, err := s.readableRooms(ctx)
	if err != nil {
		return nil, nil, err
	}
	in = make([]domain.RoomID, 0, len(readable))
	for i := range readable {
		in = append(in, readable[i].ID)
	}
	return in, outside, nil
}

// readableRooms is the rooms `[agent.read]` allows, and the IDs of the rest.
func (s *server) readableRooms(ctx context.Context) (readable []domain.Room, outside []domain.RoomID, err error) {
	rooms, err := s.rooms(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the room list: %w", err)
	}
	if err := s.placesKnown(ctx); err != nil {
		return nil, nil, err
	}
	for i := range rooms {
		if s.allowed(ctx, rooms[i]) != nil {
			outside = append(outside, rooms[i].ID)
			continue
		}
		readable = append(readable, rooms[i])
	}
	return readable, outside, nil
}

// outsideScopeKey reports how many rooms `[agent.read]` leaves out: a fixed number for
// a given config, never one that depends on what the assistant asked, so it cannot be
// used to probe what those rooms contain or are called.
const outsideScopeKey = "rooms_outside_scope"

// peopleInScope is everyone who has posted in the given in-scope rooms, most talkative
// first; nobody for none. Only the rooms named are read: this asks for no room set
// wider than the scope.
func (s *server) peopleInScope(ctx context.Context, rooms []domain.RoomID) ([]domain.Member, error) {
	if len(rooms) == 0 {
		return nil, nil
	}
	people, err := s.backend.SearchSenders(ctx, domain.TheseRooms(rooms), peoplePool)
	if err != nil {
		return nil, fmt.Errorf("reading the people: %w", err)
	}
	return people, nil
}

// peoplePool is how many of the ranked posters a name is matched against.
const peoplePool = 2000

// roomNamed resolves what somebody typed to one of rooms: an ID exactly, or a name
// loosely. Only rooms the assistant may see are passed in, so neither a match nor a
// refusal says anything about a room outside the scope.
func roomNamed(rooms []domain.Room, want string) (domain.Room, error) {
	want = strings.TrimSpace(want)
	if want == "" {
		return domain.Room{}, errors.New("no room given")
	}
	var exact, loose []domain.Room
	for i := range rooms {
		switch {
		case string(rooms[i].ID) == want:
			return rooms[i], nil
		case strings.EqualFold(rooms[i].DisplayName(), want):
			exact = append(exact, rooms[i])
		case strings.Contains(strings.ToLower(rooms[i].DisplayName()), strings.ToLower(want)):
			loose = append(loose, rooms[i])
		}
	}
	// Exact names first; two rooms of one name are as ambiguous as two loose matches
	// (anyone in a DM can name themselves after another room).
	matches := exact
	if len(matches) == 0 {
		matches = loose
	}
	switch len(matches) {
	case 0:
		return domain.Room{}, fmt.Errorf("%w: %q — try list_rooms", errNoSuchRoom, want)
	case 1:
		return matches[0], nil
	default:
		names := make([]string, 0, len(matches))
		for i := range matches[:min(5, len(matches))] {
			names = append(names, fmt.Sprintf("%s (%s)", matches[i].DisplayName(), matches[i].ID))
		}
		// Never guess between rooms: a wrong guess posts to the wrong conversation.
		return domain.Room{}, fmt.Errorf("%q matches %d rooms — say which by ID: %s",
			want, len(matches), strings.Join(names, ", "))
	}
}

// errNoSuchRoom is the one answer for a room that does not exist and a room outside
// `[agent.read]`, so the assistant cannot tell them apart.
var errNoSuchRoom = errors.New("no room by that name or ID among the rooms [agent.read] allows " +
	"(a room outside it, including an encrypted room while [agent.read] encrypted is false, is not visible)")

// readableRoom resolves a room by name among those `[agent.read]` allows.
func (s *server) readableRoom(ctx context.Context, want string) (domain.Room, error) {
	readable, _, err := s.readableRooms(ctx)
	if err != nil {
		return domain.Room{}, err
	}
	return roomNamed(readable, want)
}

// roomsByID indexes the room list; a failed read yields an empty index.
func (s *server) roomsByID(ctx context.Context) map[domain.RoomID]domain.Room {
	rooms, err := s.rooms(ctx)
	if err != nil {
		s.logger().Warn("room list read failed; results lack room names", "err", err)
	}
	byID := make(map[domain.RoomID]domain.Room, len(rooms))
	for i := range rooms {
		byID[rooms[i].ID] = rooms[i]
	}
	return byID
}

func (s *server) listRooms(ctx context.Context, raw json.RawMessage) (any, error) {
	in, err := args[struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}](raw)
	if err != nil {
		return nil, err
	}
	rooms, err := s.rooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the room list: %w", err)
	}
	if err := s.placesKnown(ctx); err != nil {
		return nil, err
	}
	unread, uerr := s.backend.CachedUnread(ctx)
	if uerr != nil {
		// The list still answers, with every unread count shown as 0.
		s.logger().Warn("unread counts read failed", "err", uerr)
	}
	counts := map[domain.RoomID]int{}
	for _, u := range unread {
		counts[u.RoomID] = u.Messages
	}

	var out []roomView
	var outside int
	for i := range rooms {
		// Scope before the name filter, so the count does not depend on the query.
		if s.allowed(ctx, rooms[i]) != nil {
			outside++
			continue
		}
		name := rooms[i].DisplayName()
		if in.Query != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(in.Query)) {
			continue
		}
		out = append(out, roomView{
			ID: string(rooms[i].ID), Name: name,
			Direct: rooms[i].IsDirect, Unread: counts[rooms[i].ID],
			Writable: s.permits(ctx, rooms[i], s.write, writeTable) == nil,
		})
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Unread > out[b].Unread })
	if n := limitOr(in.Limit, 40, 200); len(out) > n {
		out = out[:n]
	}
	answer := map[string]any{"rooms": out, outsideScopeKey: outside}
	if !s.readScope().Shares() {
		// Say why it is empty, so it does not read as "no rooms".
		answer["note"] = nothingSharedNote
	}
	return answer, nil
}

func (s *server) findPeople(ctx context.Context, raw json.RawMessage) (any, error) {
	in, err := args[struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}](raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Query) == "" {
		return nil, errors.New("find_people needs a name to look for")
	}
	// The scope is the query's room set, not a filter on a limited answer.
	inScope, _, err := s.roomsInScope(ctx)
	if err != nil {
		return nil, err
	}
	people, err := s.peopleInScope(ctx, inScope)
	if err != nil {
		return nil, err
	}
	want := strings.ToLower(in.Query)
	type personView struct {
		UserID string `json:"user_id"`
		Name   string `json:"name,omitempty"`
	}
	var out []personView
	for _, person := range people {
		if strings.Contains(strings.ToLower(person.DisplayName), want) ||
			strings.Contains(strings.ToLower(person.UserID), want) {
			out = append(out, personView{UserID: person.UserID, Name: person.DisplayName})
		}
		if len(out) >= limitOr(in.Limit, 10, 50) {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("nobody called %q has posted in the rooms [agent.read] allows", in.Query)
	}
	return map[string]any{"people": out}, nil
}

func (s *server) findRoomsWith(ctx context.Context, raw json.RawMessage) (any, error) {
	in, err := args[struct {
		People []string `json:"people"`
		Limit  int      `json:"limit"`
	}](raw)
	if err != nil {
		return nil, err
	}
	if len(in.People) == 0 {
		return nil, errors.New("find_rooms_with needs at least one person")
	}
	inScope, outOfScope, err := s.roomsInScope(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := s.resolvePeople(ctx, inScope, in.People)
	if err != nil {
		return nil, err
	}
	// Scope goes into the query so the limit counts only in-scope rooms.
	var rooms []domain.Room
	if len(inScope) > 0 {
		rooms, err = s.backend.RoomsWith(ctx, ids, domain.TheseRooms(inScope), limitOr(in.Limit, 10, 50))
		if err != nil {
			return nil, fmt.Errorf("looking for rooms: %w", err)
		}
	}
	out := make([]roomView, 0, len(rooms))
	for i := range rooms {
		out = append(out, viewOf(&rooms[i]))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no room [agent.read] allows has all of %s in it", strings.Join(in.People, " and "))
	}
	return map[string]any{
		"rooms": out, "matched_people": ids, outsideScopeKey: len(outOfScope),
	}, nil
}

// resolvePeople turns names into Matrix IDs, refusing anything ambiguous.
func (s *server) resolvePeople(ctx context.Context, inScope []domain.RoomID, names []string) ([]string, error) {
	people, err := s.peopleInScope(ctx, inScope)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if strings.HasPrefix(name, "@") && strings.Contains(name, ":") {
			out = append(out, name)
			continue
		}
		var matches []string
		for _, person := range people {
			if strings.EqualFold(person.DisplayName, name) {
				matches = []string{person.UserID}
				break
			}
			if strings.Contains(strings.ToLower(person.DisplayName), strings.ToLower(name)) {
				matches = append(matches, person.UserID+" ("+person.DisplayName+")")
			}
		}
		switch {
		case len(matches) == 0:
			return nil, fmt.Errorf("nobody called %q has posted in the rooms [agent.read] allows", name)
		case len(matches) == 1:
			out = append(out, strings.Fields(matches[0])[0])
		default:
			return nil, fmt.Errorf("%q matches %d people — say which: %s",
				name, len(matches), strings.Join(matches[:min(5, len(matches))], ", "))
		}
	}
	return out, nil
}

func (s *server) readRoom(ctx context.Context, raw json.RawMessage) (any, error) {
	in, err := args[struct {
		Room   string `json:"room"`
		Sender string `json:"sender"`
		Limit  int    `json:"limit"`
	}](raw)
	if err != nil {
		return nil, err
	}
	room, err := s.readableRoom(ctx, in.Room)
	if err != nil {
		return nil, err
	}
	msgs, err := s.backend.CachedTimeline(ctx, room.ID)
	if err != nil {
		return nil, fmt.Errorf("reading the room: %w", err)
	}
	limit := limitOr(in.Limit, 40, 200)
	var out []messageView
	for i := range msgs {
		if in.Sender != "" && msgs[i].Sender != in.Sender {
			continue
		}
		out = append(out, s.view(msgs[i], false))
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return map[string]any{
		"room":     viewOf(&room),
		"messages": out,
	}, nil
}

func (s *server) searchMessages(ctx context.Context, raw json.RawMessage) (any, error) {
	in, err := args[struct {
		Query  string `json:"query"`
		Room   string `json:"room"`
		Sender string `json:"sender"`
		Since  string `json:"since"`
		Limit  int    `json:"limit"`
	}](raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Query) == "" {
		return nil, errors.New("search_messages needs something to look for")
	}
	req := domain.SearchRequest{
		Filter: domain.SearchFilter{Terms: in.Query, Sender: in.Sender},
		Limit:  limitOr(in.Limit, 20, 100),
	}
	if in.Room != "" {
		room, rerr := s.readableRoom(ctx, in.Room)
		if rerr != nil {
			return nil, rerr
		}
		req.Rooms = domain.TheseRooms([]domain.RoomID{room.ID})
	}
	if in.Since != "" {
		since, perr := time.Parse(time.RFC3339, in.Since)
		if perr != nil {
			return nil, fmt.Errorf("since: %q is not an RFC3339 date", in.Since)
		}
		req.Filter.Since = since
	}
	// Scope goes into the query, so the limit counts only in-scope rooms. Rooms outside
	// it are never searched, not even to count.
	inScope, outOfScope, serr := s.roomsInScope(ctx)
	if serr != nil {
		return nil, serr
	}
	if req.Rooms.None() {
		req.Rooms = domain.TheseRooms(inScope)
	}
	var hits []domain.SearchHit
	if !req.Rooms.None() {
		if hits, err = s.backend.SearchMessages(ctx, req); err != nil {
			return nil, fmt.Errorf("searching: %w", err)
		}
	}
	return map[string]any{
		"hits": s.shapeHits(ctx, hits), outsideScopeKey: len(outOfScope),
		"note": "matching is by term, not meaning — if this missed, try the words they would have typed, in their own language",
	}, nil
}

// hitView is one search result, named by room and event so read_around can be called on it.
type hitView struct {
	Room     string `json:"room"`
	RoomName string `json:"room_name,omitempty"`
	EventID  string `json:"event_id"`
	Sender   string `json:"sender"`
	Name     string `json:"sender_name,omitempty"`
	Sent     string `json:"sent"`
	Snippet  string `json:"snippet"`
}

// shapeHits drops any hit in a room outside `[agent.read]`; the query was already
// scoped, so this only guards a room that left the scope mid-call.
func (s *server) shapeHits(ctx context.Context, hits []domain.SearchHit) []hitView {
	byID := s.roomsByID(ctx)
	unmark := strings.NewReplacer(domain.HighlightStart, "", domain.HighlightEnd, "")
	var out []hitView
	for i := range hits {
		room, known := byID[hits[i].RoomID]
		if !known || s.allowed(ctx, room) != nil {
			continue
		}
		out = append(out, hitView{
			Room: string(hits[i].RoomID), RoomName: room.DisplayName(), EventID: string(hits[i].EventID),
			Sender: hits[i].Sender, Name: hits[i].SenderName,
			Sent:    hits[i].Timestamp.Format(time.RFC3339),
			Snippet: unmark.Replace(hits[i].Snippet),
		})
	}
	return out
}

func (s *server) readAround(ctx context.Context, raw json.RawMessage) (any, error) {
	in, err := args[struct {
		Room   string `json:"room"`
		Event  string `json:"event"`
		Before int    `json:"before"`
		After  int    `json:"after"`
	}](raw)
	if err != nil {
		return nil, err
	}
	room, err := s.readableRoom(ctx, in.Room)
	if err != nil {
		return nil, err
	}
	before, after := limitOr(in.Before, 5, 50), limitOr(in.After, 10, 50)
	msgs, err := s.backend.MessagesAround(ctx, room.ID, domain.EventID(in.Event), before, after)
	if err != nil {
		return nil, fmt.Errorf("reading around the message: %w", err)
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("%s is not in this room's cached history", in.Event)
	}
	out := make([]messageView, 0, len(msgs))
	for i := range msgs {
		out = append(out, s.view(msgs[i], false))
	}
	return map[string]any{
		"room":     roomView{ID: string(room.ID), Name: room.DisplayName()},
		"messages": out,
	}, nil
}

func (s *server) unreadSummary(ctx context.Context, raw json.RawMessage) (any, error) {
	in, err := args[struct {
		Limit int `json:"limit"`
	}](raw)
	if err != nil {
		return nil, err
	}
	unread, err := s.backend.CachedUnread(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading what is unread: %w", err)
	}
	if placeErr := s.placesKnown(ctx); placeErr != nil {
		return nil, placeErr
	}
	byID := s.roomsByID(ctx)
	type waitingView struct {
		Room     string `json:"room"`
		Name     string `json:"name,omitempty"`
		Messages int    `json:"messages"`
		Mentions int    `json:"mentions,omitempty"`
	}
	var out []waitingView
	for _, u := range unread {
		if u.Messages == 0 && u.Highlights == 0 {
			continue
		}
		room, known := byID[u.RoomID]
		if !known || s.allowed(ctx, room) != nil {
			continue
		}
		out = append(out, waitingView{
			Room: string(u.RoomID), Name: room.DisplayName(),
			Messages: u.Messages, Mentions: u.Highlights,
		})
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Mentions != out[b].Mentions {
			return out[a].Mentions > out[b].Mentions
		}
		return out[a].Messages > out[b].Messages
	})
	if n := limitOr(in.Limit, 20, 100); len(out) > n {
		out = out[:n]
	}
	// Not how many hidden rooms have unread messages: that would report their activity.
	_, outOfScope, err := s.roomsInScope(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"waiting": out, outsideScopeKey: len(outOfScope)}, nil
}
