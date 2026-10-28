package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
)

// fake answers reads from fixtures and records writes.
type fake struct {
	// dir is the directory the daemon gives, dirErr why it cannot.
	dir    domain.Directory
	dirErr error
	// people is the person verdicts the daemon gives, peopleErr why it cannot.
	people    []domain.PersonVerdict
	peopleErr error
	rooms     []domain.Room
	spaces    []domain.Space
	unread    []domain.Unread
	messages  map[domain.RoomID][]domain.Message
	hits      []domain.SearchHit
	// searchAsked is the room set of each search, in order.
	searchAsked [][]domain.RoomID
	senders     []domain.Member
	// postedIn maps sender to rooms; no entry means everywhere.
	postedIn map[string][]domain.RoomID
	with     []domain.Room
	// sendersAsked and withRooms record the room sets the scoped calls were given.
	sendersAsked [][]domain.RoomID
	withRooms    [][]domain.RoomID
	around       []domain.Message
	encrypted    map[domain.RoomID]bool
	// spacesCalls and encryptionCalls count those reads, which scope checks repeat.
	spacesCalls, encryptionCalls int
	// beforeReplace runs once inside the next ReplaceDraft.
	beforeReplace   func()
	encryptionAsked [][]domain.RoomID
	// withAsked records the IDs find_rooms_with resolved.
	withAsked []string
	// selves is who the daemon says this person is; nil is @me:x.
	selves []string
	// files is each attachment's bytes by message; loaded records the event IDs fetched.
	files  map[domain.EventID][]byte
	loaded []domain.EventID

	// sendErr and queueErr drive the send → queue → draft ladder.
	sendErr  error
	queueErr error
	sent     []domain.Draft
	queued   []domain.ScheduledMessage
	drafts   map[domain.RoomID]domain.StoredDraft
}

func (f *fake) LoadImage(_ context.Context, _ domain.RoomID, event domain.EventID) ([]byte, error) {
	f.loaded = append(f.loaded, event)
	data, ok := f.files[event]
	if !ok {
		return nil, errors.New("no such file")
	}
	return data, nil
}

func (f *fake) Send(_ context.Context, room domain.RoomID, draft domain.Draft) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	draft.Body = string(room) + " " + draft.Body
	f.sent = append(f.sent, draft)
	return nil
}

func (f *fake) Schedule(_ context.Context, msg domain.ScheduledMessage) (string, error) {
	if f.queueErr != nil {
		return "", f.queueErr
	}
	f.queued = append(f.queued, msg)
	return "q1", nil
}

func (f *fake) Drafts(context.Context) ([]domain.StoredDraft, error) {
	out := make([]domain.StoredDraft, 0, len(f.drafts))
	for room := range f.drafts {
		out = append(out, f.drafts[room])
	}
	return out, nil
}

func (f *fake) store(draft domain.StoredDraft) {
	if f.drafts == nil {
		f.drafts = map[domain.RoomID]domain.StoredDraft{}
	}
	f.drafts[draft.RoomID] = draft
}

// ReplaceDraft saves only over the draft the writer read, as the cache does;
// beforeReplace runs first, as another writer landing in between.
func (f *fake) ReplaceDraft(_ context.Context, draft, over domain.StoredDraft) (bool, error) {
	if hook := f.beforeReplace; hook != nil {
		f.beforeReplace = nil
		hook()
	}
	current := f.drafts[draft.RoomID]
	if current.Body != over.Body || current.ReplyTo != over.ReplyTo || current.Editing != over.Editing ||
		current.EditSaved != over.EditSaved || current.ThreadRoot != over.ThreadRoot ||
		!current.Updated.Equal(over.Updated) {
		return false, nil
	}
	f.store(draft)
	return true, nil
}

func (f *fake) Rooms(context.Context) ([]domain.Room, error)        { return f.rooms, nil }
func (f *fake) Directory(context.Context) (domain.Directory, error) { return f.dir, f.dirErr }

func (f *fake) PersonVerdicts(context.Context) ([]domain.PersonVerdict, error) {
	return f.people, f.peopleErr
}

func (f *fake) Selves(context.Context) ([]string, error) {
	if f.selves == nil {
		return []string{"matrix:@me:x"}, nil
	}
	return f.selves, nil
}
func (f *fake) Spaces(context.Context) ([]domain.Space, error) {
	f.spacesCalls++
	return f.spaces, nil
}
func (f *fake) CachedUnread(context.Context) ([]domain.Unread, error) {
	return f.unread, nil
}

func (f *fake) CachedTimeline(_ context.Context, room domain.RoomID) ([]domain.Message, error) {
	return f.messages[room], nil
}

// SearchMessages answers as the cache does: only rooms in the set (none for empty),
// limit applied after.
func (f *fake) SearchMessages(_ context.Context, req domain.SearchRequest) ([]domain.SearchHit, error) {
	f.searchAsked = append(f.searchAsked, req.Rooms.IDs)
	var out []domain.SearchHit
	for i := range f.hits {
		if !req.Rooms.All && !slices.Contains(req.Rooms.IDs, f.hits[i].RoomID) {
			continue
		}
		out = append(out, f.hits[i])
		if req.Limit > 0 && len(out) == req.Limit {
			break
		}
	}
	return out, nil
}

// SearchSenders answers as the cache does: only people who posted in the set.
func (f *fake) SearchSenders(_ context.Context, set domain.RoomSet, limit int) ([]domain.Member, error) {
	f.sendersAsked = append(f.sendersAsked, set.IDs)
	if set.None() {
		return nil, nil
	}
	var out []domain.Member
	for _, person := range f.senders {
		if !set.All && !f.posted(person.UserID, set.IDs) {
			continue
		}
		out = append(out, person)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

func (f *fake) posted(userID string, rooms []domain.RoomID) bool {
	where, known := f.postedIn[userID]
	if !known {
		return true
	}
	for _, room := range where {
		if slices.Contains(rooms, room) {
			return true
		}
	}
	return false
}

// RoomsWith answers as the cache does: the limit applies within the set; empty is none.
func (f *fake) RoomsWith(_ context.Context, userIDs []string, set domain.RoomSet, limit int) ([]domain.Room, error) {
	f.withAsked = userIDs
	f.withRooms = append(f.withRooms, set.IDs)
	var out []domain.Room
	for i := range f.with {
		if !set.All && !slices.Contains(set.IDs, f.with[i].ID) {
			continue
		}
		out = append(out, f.with[i])
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

// MessagesAround answers the around fixture if any, else the event itself, found only
// in the room asked about.
func (f *fake) MessagesAround(_ context.Context, room domain.RoomID, event domain.EventID, _, _ int) ([]domain.Message, error) {
	if f.around != nil {
		return f.around, nil
	}
	msgs := f.messages[room]
	for i := range msgs {
		if msgs[i].ID == event {
			return []domain.Message{msgs[i]}, nil
		}
	}
	return nil, nil
}

func (f *fake) RoomEncryption(_ context.Context, rooms []domain.RoomID) (map[domain.RoomID]bool, error) {
	f.encryptionCalls++
	f.encryptionAsked = append(f.encryptionAsked, rooms)
	out := make(map[domain.RoomID]bool, len(rooms))
	for _, room := range rooms {
		out[room] = f.encrypted[room]
	}
	return out, nil
}

// twoRooms is one plain and one encrypted room, in a space.
func twoRooms() *fake {
	return &fake{
		rooms: []domain.Room{
			{ID: mx + "!open:x", Name: "Standup"},
			{ID: mx + "!secret:x", Name: "Private chat", IsDirect: true},
		},
		spaces: []domain.Space{{ID: mx + "!s:x", Name: "Work", Children: []domain.RoomID{mx + "!open:x"}}},
		unread: []domain.Unread{{RoomID: mx + "!open:x", Messages: 3}},
		messages: map[domain.RoomID][]domain.Message{
			mx + "!open:x": {
				{ID: mx + "$1", Sender: "matrix:@dana:x", SenderName: "Dana", Body: "morning", Timestamp: time.Unix(1_700_000_000, 0)},
				{ID: mx + "$2", Sender: "matrix:@me:x", SenderName: "Me", Body: "morning all", Timestamp: time.Unix(1_700_000_060, 0)},
			},
		},
		encrypted: map[domain.RoomID]bool{mx + "!secret:x": true},
	}
}

// shareAll and shareAllEncrypted share every room (`dm` + `group`).
var (
	shareAll          = domain.ModelScope{Only: []string{"dm", "group"}}
	shareAllEncrypted = domain.ModelScope{Only: []string{"dm", "group"}, Encrypted: true}
)

func newServer(f *fake, scope domain.ModelScope) *server {
	return &server{backend: f, scope: scope, log: logging.Discard()}
}

// call runs one tool and returns its decoded JSON.
func call(t *testing.T, s *server, name string, args map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		t.Fatalf("encoding the call: %v", err)
	}
	result, rpcErr := s.call(encoded)
	if rpcErr != nil {
		t.Fatalf("%s: rpc error %+v", name, rpcErr)
	}
	body, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("%s returned %T, want a result object", name, result)
	}
	if failed, _ := body["isError"].(bool); failed {
		t.Fatalf("%s failed: %s", name, text(t, body))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text(t, body)), &out); err != nil {
		t.Fatalf("%s produced unreadable JSON: %v", name, err)
	}
	return out
}

// callErr runs a tool expecting a refusal and returns what it said.
func callErr(t *testing.T, s *server, name string, args map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr := s.call(encoded)
	if rpcErr != nil {
		t.Fatalf("%s: rpc error %+v", name, rpcErr)
	}
	body, _ := result.(map[string]any)
	if failed, _ := body["isError"].(bool); !failed {
		t.Fatalf("%s succeeded, want a refusal", name)
	}
	return text(t, body)
}

func text(t *testing.T, body map[string]any) string {
	t.Helper()
	blocks, _ := body["content"].([]any)
	if len(blocks) == 0 {
		t.Fatalf("no content in %+v", body)
	}
	first, _ := blocks[0].(map[string]any)
	out, _ := first["text"].(string)
	return out
}

func TestHandshakeAndToolList(t *testing.T) {
	t.Parallel()

	s := newServer(twoRooms(), shareAllEncrypted)
	var out strings.Builder
	in := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		// A notification must produce no reply.
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"nonsense/method"}`,
	}, "\n") + "\n")
	if err := s.serve(in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d replies, want 3 — the notification must not be answered:\n%s", len(lines), out.String())
	}
	if !strings.Contains(lines[0], protocolVersion) || !strings.Contains(lines[0], `"tools"`) {
		t.Errorf("initialize = %s, want the protocol version and the tools capability", lines[0])
	}
	// Every tool is listed with a schema.
	for _, want := range []string{"list_rooms", "find_rooms_with", "read_around", "inputSchema"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("tools/list is missing %q", want)
		}
	}
	if !strings.Contains(lines[2], `"code":-32601`) {
		t.Errorf("an unknown method answered %s, want an error", lines[2])
	}
}

// Scope is checked on every listing.
func TestScopeExcludesAndSaysSo(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		scope   domain.ModelScope
		visible []string
	}{
		// Nothing listed shares nothing: least privilege.
		{"nothing configured", domain.ModelScope{}, nil},
		{"an except alone shares nothing", domain.ModelScope{Except: []string{"dm"}}, nil},
		// Every room listed, but never an encrypted one unless allowed.
		{"everything, unencrypted", shareAll, []string{"Standup"}},
		{"encrypted included", shareAllEncrypted, []string{"Standup", "Private chat"}},
		{"an allow list", domain.ModelScope{Only: []string{"space:Work"}, Encrypted: true}, []string{"Standup"}},
		// Except subtracts from what rooms names.
		{"a deny list", domain.ModelScope{Only: []string{"dm", "group"}, Except: []string{"dm"}, Encrypted: true}, []string{"Standup"}},
		{"denied by name", domain.ModelScope{Only: []string{"dm", "group"}, Except: []string{"room:Standup"}, Encrypted: true}, []string{"Private chat"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := call(t, newServer(twoRooms(), tc.scope), "list_rooms", nil)
			rooms, _ := out["rooms"].([]any)
			var names []string
			for _, room := range rooms {
				entry, _ := room.(map[string]any)
				name, _ := entry["name"].(string)
				names = append(names, name)
			}
			if strings.Join(names, ",") != strings.Join(tc.visible, ",") {
				t.Fatalf("visible = %v, want %v", names, tc.visible)
			}
			// Hidden rooms are counted, not silently omitted.
			outside, _ := out[outsideScopeKey].(float64)
			if int(outside) != 2-len(tc.visible) {
				t.Errorf("outside = %v, want %d", outside, 2-len(tc.visible))
			}
		})
	}
}

// A room outside the scope answers exactly like a missing room.
func TestReadingAnExcludedRoomLooksLikeNoRoom(t *testing.T) {
	t.Parallel()

	s := newServer(twoRooms(), shareAll)
	hidden := callErr(t, s, "read_room", map[string]any{"room": "Private chat"})
	missing := callErr(t, s, "read_room", map[string]any{"room": "Nonexistent"})
	strip := func(said, name string) string { return strings.ReplaceAll(said, name, "<room>") }
	if strip(hidden, "Private chat") != strip(missing, "Nonexistent") {
		t.Fatalf("hidden and missing rooms answer differently:\n %q\n %q", hidden, missing)
	}
	// A fragment of a hidden room's name is not listed as a candidate.
	if got := callErr(t, s, "read_room", map[string]any{"room": mx + "!secret"}); strings.Contains(got, "Private chat") {
		t.Fatalf("refusal = %q, want no hidden room named", got)
	}
}

// The out-of-scope count is query-independent and no query reaches a hidden room, or
// the count would reveal whether a word, name or person appears there.
func TestTheOutOfScopeCountDoesNotAnswerQueries(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	f.hits = []domain.SearchHit{{RoomID: mx + "!secret:x", EventID: mx + "$s", Timestamp: time.Unix(1_700_000_000, 0), Snippet: "the secret"}}
	f.with = []domain.Room{{ID: mx + "!secret:x", Name: "Private chat", IsDirect: true}}
	f.unread = append(f.unread, domain.Unread{RoomID: mx + "!secret:x", Messages: 9})
	s := newServer(f, shareAll)

	probes := []struct {
		tool string
		args map[string]any
	}{
		{"list_rooms", map[string]any{"query": "Private"}},
		{"list_rooms", map[string]any{"query": "zzz"}},
		{"search_messages", map[string]any{"query": "secret"}},
		{"search_messages", map[string]any{"query": "zzz"}},
		{"find_rooms_with", map[string]any{"people": []string{"matrix:@me:x"}}},
		{"unread_summary", nil},
	}
	for _, p := range probes {
		encoded, err := json.Marshal(map[string]any{"name": p.tool, "arguments": p.args})
		if err != nil {
			t.Fatal(err)
		}
		result, rpcErr := s.call(encoded)
		if rpcErr != nil {
			t.Fatalf("%s: rpc error %+v", p.tool, rpcErr)
		}
		body, _ := result.(map[string]any)
		if failed, _ := body["isError"].(bool); failed {
			continue // a refusal carries no count at all
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(text(t, body)), &out); err != nil {
			t.Fatalf("%s produced unreadable JSON: %v", p.tool, err)
		}
		if outside, _ := out[outsideScopeKey].(float64); outside != 1 {
			t.Errorf("%s %v: outside = %v, want 1 whatever was asked", p.tool, p.args, outside)
		}
	}
	for _, rooms := range append(f.searchAsked, f.withRooms...) {
		for _, id := range rooms {
			if id == mx+"!secret:x" {
				t.Fatalf("a query reached the hidden room: %v", rooms)
			}
		}
	}
}

func TestRoomNamedResolvesOrAsks(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	f.rooms = append(f.rooms, domain.Room{ID: mx + "!standup2:x", Name: "Standup (archive)"})
	s := newServer(f, shareAllEncrypted)

	// An exact name wins over a substring.
	out := call(t, s, "read_room", map[string]any{"room": "Standup"})
	room, _ := out["room"].(map[string]any)
	if room["id"] != mx+"!open:x" {
		t.Fatalf("read_room(Standup) = %v, want the exactly-named room", room)
	}
	// An ambiguous fragment lists candidates rather than picking one.
	got := callErr(t, s, "read_room", map[string]any{"room": "stand"})
	if !strings.Contains(got, mx+"!open:x") || !strings.Contains(got, mx+"!standup2:x") {
		t.Fatalf("refusal = %q, want it to say which rooms matched", got)
	}
	callErr(t, s, "read_room", map[string]any{"room": "nowhere"})
}

func TestReadRoomMarksYourOwnMessages(t *testing.T) {
	t.Parallel()

	s := newServer(twoRooms(), shareAllEncrypted)
	out := call(t, s, "read_room", map[string]any{"room": "Standup"})
	msgs, _ := out["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("read_room returned %d messages, want 2", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	second, _ := msgs[1].(map[string]any)
	if mine, _ := first["mine"].(bool); mine {
		t.Error("somebody else's message was marked as the account's own")
	}
	if mine, _ := second["mine"].(bool); !mine {
		t.Error("the account's own message was not marked — 'how do I write' depends on it")
	}
	// A sender filter shows how someone writes in one call.
	only := call(t, s, "read_room", map[string]any{"room": "Standup", "sender": "matrix:@me:x"})
	if msgs, _ := only["messages"].([]any); len(msgs) != 1 {
		t.Fatalf("filtered read returned %d messages, want 1", len(msgs))
	}
}

// The daemon's selves mark messages as mine, on any network.
func TestReadRoomMarksTheDaemonsSelvesMine(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	f.selves = []string{"matrix:@dana:x"}
	s := newServer(f, shareAllEncrypted)
	out := call(t, s, "read_room", map[string]any{"room": "Standup"})
	msgs, _ := out["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("read_room returned %d messages, want 2", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	second, _ := msgs[1].(map[string]any)
	if mine, _ := first["mine"].(bool); !mine {
		t.Error("a message from one of the daemon's selves was not marked mine")
	}
	if mine, _ := second["mine"].(bool); mine {
		t.Error("a message from an account that is not this person's was marked mine")
	}
}

// Names in, one room out.
func TestFindRoomsWithResolvesNamesAndRefusesAmbiguity(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	f.senders = []domain.Member{
		{UserID: "matrix:@noa:x", DisplayName: "Noa"},
		{UserID: "matrix:@evgeny:x", DisplayName: "Evgeny"},
		{UserID: "matrix:@evgeny2:x", DisplayName: "Evgeny B"},
	}
	f.with = []domain.Room{{ID: mx + "!open:x", Name: "Standup"}}
	s := newServer(f, shareAllEncrypted)

	out := call(t, s, "find_rooms_with", map[string]any{"people": []string{"Noa", "Evgeny"}})
	// An exact display name beats a longer one containing it.
	if strings.Join(f.withAsked, ",") != "matrix:@noa:x,matrix:@evgeny:x" {
		t.Fatalf("resolved %v, want the two Matrix IDs", f.withAsked)
	}
	if rooms, _ := out["rooms"].([]any); len(rooms) != 1 {
		t.Fatalf("find_rooms_with = %v, want one room", out["rooms"])
	}
	// A Matrix ID is unambiguous and passed through.
	call(t, s, "find_rooms_with", map[string]any{"people": []string{"matrix:@someone:x"}})
	if len(f.withAsked) != 1 || f.withAsked[0] != "matrix:@someone:x" {
		t.Fatalf("resolved %v, want the ID untouched", f.withAsked)
	}
	// A fragment matching two people asks rather than guessing.
	if got := callErr(t, s, "find_rooms_with", map[string]any{"people": []string{"Evg"}}); !strings.Contains(got, "matrix:@evgeny:x") || !strings.Contains(got, "matrix:@evgeny2:x") {
		t.Fatalf("refusal = %q, want it to name the candidates", got)
	}
	callErr(t, s, "find_rooms_with", map[string]any{"people": []string{"Nobody"}})
}

func TestSearchHidesWhatTheScopeHides(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	f.hits = []domain.SearchHit{
		{RoomID: mx + "!open:x", EventID: mx + "$1", Sender: "matrix:@dana:x", Timestamp: time.Unix(1_700_000_000, 0),
			Snippet: "the " + domain.HighlightStart + "match" + domain.HighlightEnd + " ordering"},
		{RoomID: mx + "!secret:x", EventID: mx + "$2", Sender: "matrix:@dana:x", Timestamp: time.Unix(1_700_000_000, 0), Snippet: "secret"},
	}
	// Encrypted rooms are out, so the second hit must not appear.
	out := call(t, newServer(f, shareAll), "search_messages", map[string]any{"query": "match ordering"})
	hits, _ := out["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("search returned %d hits, want 1", len(hits))
	}
	first, _ := hits[0].(map[string]any)
	if snippet, _ := first["snippet"].(string); strings.Contains(snippet, domain.HighlightStart) {
		t.Errorf("snippet = %q, want the highlight markers stripped", snippet)
	}
	if outside, _ := out[outsideScopeKey].(float64); outside != 1 {
		t.Errorf("outside = %v, want 1", outside)
	}
	// A bad date is refused.
	callErr(t, newServer(f, shareAll), "search_messages", map[string]any{"query": "x", "since": "last tuesday"})
}

func TestReadAroundNeedsTheMessageToExist(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	s := newServer(f, shareAllEncrypted)
	callErr(t, s, "read_around", map[string]any{"room": "Standup", "event": mx + "$gone"})

	f.around = []domain.Message{
		{ID: mx + "$1", Sender: "matrix:@dana:x", Body: "before", Timestamp: time.Unix(1_700_000_000, 0)},
		{ID: mx + "$2", Sender: "matrix:@dana:x", Body: "the ask", Timestamp: time.Unix(1_700_000_060, 0)},
	}
	out := call(t, s, "read_around", map[string]any{"room": "Standup", "event": mx + "$2"})
	if msgs, _ := out["messages"].([]any); len(msgs) != 2 {
		t.Fatalf("read_around = %v, want the conversation", out["messages"])
	}
}

func TestUnknownToolIsAProtocolError(t *testing.T) {
	t.Parallel()

	s := newServer(twoRooms(), shareAll)
	// `delete_message`, a plausible name, not a nonsense word.
	encoded, err := json.Marshal(map[string]any{"name": "delete_message", "arguments": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	_, rpcErr := s.call(encoded)
	if rpcErr == nil {
		t.Fatal("an unknown tool was accepted")
	}
	if !strings.Contains(rpcErr.Message, "delete_message") {
		t.Errorf("error = %q, want it to name the tool", rpcErr.Message)
	}
}

// find_people must not name people seen only in excluded rooms.
func TestFindPeopleOnlyNamesPeopleFromRoomsInScope(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	f.senders = []domain.Member{
		{UserID: "matrix:@dana:x", DisplayName: "Dana"},
		{UserID: "matrix:@shira:x", DisplayName: "Shira"},
		{UserID: "matrix:@dan:x", DisplayName: "Dan"},
	}
	// Shira and Dan posted only in the excluded encrypted DM.
	f.postedIn = map[string][]domain.RoomID{
		"matrix:@dana:x":  {mx + "!open:x"},
		"matrix:@shira:x": {mx + "!secret:x"},
		"matrix:@dan:x":   {mx + "!secret:x"},
	}
	s := newServer(f, shareAll)

	if got := callErr(t, s, "find_people", map[string]any{"query": "Shira"}); !strings.Contains(got, "nobody called") {
		t.Fatalf("find_people(Shira) = %q, want nobody — she has posted only in an excluded room", got)
	}
	if got := callErr(t, s, "find_people", map[string]any{"query": "matrix:@shira:x"}); !strings.Contains(got, "nobody called") {
		t.Fatalf("find_people(@shira:x) = %q, want nobody", got)
	}
	out := call(t, s, "find_people", map[string]any{"query": "dan"})
	people, _ := out["people"].([]any)
	if len(people) != 1 {
		t.Fatalf("find_people(dan) = %v, want only Dana", people)
	}
	if first, _ := people[0].(map[string]any); first["user_id"] != "matrix:@dana:x" {
		t.Fatalf("find_people(dan) = %v, want Dana", people)
	}
	// The scope reaches the query, not a filter on a truncated ranking.
	for _, asked := range f.sendersAsked {
		if len(asked) != 1 || asked[0] != mx+"!open:x" {
			t.Fatalf("SearchSenders was asked about %v, want only the room in scope", asked)
		}
	}

	// find_rooms_with must not resolve or suggest someone the scope hides.
	if got := callErr(t, s, "find_rooms_with", map[string]any{"people": []string{"Shira"}}); !strings.Contains(got, "nobody called") {
		t.Fatalf("find_rooms_with(Shira) = %q, want nobody", got)
	}
	if got := callErr(t, s, "find_rooms_with", map[string]any{"people": []string{"Da"}}); strings.Contains(got, "matrix:@dan:x") {
		t.Fatalf("find_rooms_with(Da) = %q, named somebody from an excluded room", got)
	}
}

// An empty in-scope set asks nothing, not every room.
func TestFindPeopleWithNothingInScopeFindsNobody(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	f.senders = []domain.Member{{UserID: "matrix:@dana:x", DisplayName: "Dana"}}
	s := newServer(f, domain.ModelScope{Only: []string{"room:Nowhere"}})

	if got := callErr(t, s, "find_people", map[string]any{"query": "Dana"}); !strings.Contains(got, "nobody called") {
		t.Fatalf("find_people = %q, want nobody when no room is in scope", got)
	}
	if len(f.sendersAsked) != 0 {
		t.Fatalf("SearchSenders was asked about %v with nothing in scope, want no call", f.sendersAsked)
	}
}

// The limit applies to in-scope rooms, after excluding.
func TestFindRoomsWithLimitCountsOnlyRoomsInScope(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	// The excluded room is most recently active, so first in the ranking.
	f.with = []domain.Room{
		{ID: mx + "!secret:x", Name: "Private chat", IsDirect: true},
		{ID: mx + "!open:x", Name: "Standup"},
	}
	s := newServer(f, shareAll)

	out := call(t, s, "find_rooms_with", map[string]any{"people": []string{"matrix:@dana:x"}, "limit": 1})
	rooms, _ := out["rooms"].([]any)
	if len(rooms) != 1 {
		t.Fatalf("find_rooms_with(limit 1) = %v, want the one room in scope", out["rooms"])
	}
	if first, _ := rooms[0].(map[string]any); first["id"] != mx+"!open:x" {
		t.Fatalf("find_rooms_with = %v, want Standup", rooms)
	}
	if outside, _ := out[outsideScopeKey].(float64); outside != 1 {
		t.Errorf("outside = %v, want 1", outside)
	}
	for _, asked := range f.withRooms {
		if len(asked) == 0 {
			t.Fatal("RoomsWith was asked about every room, want the scope in the query")
		}
	}
}

// An overlong request line is skipped, not buffered, and the session answers the next;
// so is a final line without newline.
func TestAnOverlongLineIsSkippedAndTheSessionContinues(t *testing.T) {
	t.Parallel()

	s := newServer(twoRooms(), shareAllEncrypted)
	var out strings.Builder
	huge := `{"jsonrpc":"2.0","id":9,"method":"tools/list","pad":"` + strings.Repeat("x", 3*maxLine) + `"}`
	in := strings.NewReader(huge + "\n" +
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if err := s.serve(in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d replies, want 2 (the overlong request unanswered):\n%.300s", len(lines), out.String())
	}
	if strings.Contains(out.String(), `"id":9`) {
		t.Error("the overlong request was answered")
	}
}

// The limit counts only in-scope rooms, so hidden hits cannot crowd out visible ones.
func TestSearchLimitCountsOnlyInScopeRooms(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	at := time.Unix(1_700_000_000, 0)
	for i := range 5 {
		f.hits = append(f.hits, domain.SearchHit{RoomID: mx + "!secret:x", EventID: domain.EventID(fmt.Sprintf(mx+"$s%d", i)), Timestamp: at})
	}
	f.hits = append(f.hits, domain.SearchHit{RoomID: mx + "!open:x", EventID: mx + "$open", Timestamp: at})

	out := call(t, newServer(f, shareAll), "search_messages", map[string]any{"query": "plan", "limit": 2})
	hits, _ := out["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("search returned %d hits, want the one in-scope hit", len(hits))
	}
	if outside, _ := out[outsideScopeKey].(float64); outside != 1 {
		t.Errorf("outside = %v, want 1 (rooms, not hits)", outside)
	}
	for _, rooms := range f.searchAsked {
		if len(rooms) == 0 {
			t.Error("a search ran over every room")
		}
	}
}

// With nothing shared, nothing is searched.
func TestSearchWithNothingSharedSearchesNothing(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	f.hits = []domain.SearchHit{{RoomID: mx + "!secret:x", EventID: mx + "$s", Timestamp: time.Unix(1_700_000_000, 0)}}
	out := call(t, newServer(f, domain.ModelScope{Only: []string{"space:Nowhere"}}), "search_messages", map[string]any{"query": "plan"})
	if hits, _ := out["hits"].([]any); len(hits) != 0 {
		t.Errorf("hits = %v, want none", hits)
	}
	for _, rooms := range f.searchAsked {
		if len(rooms) == 0 {
			t.Error("a search ran over every room")
		}
	}
}

// A listing costs one space list and one encryption lookup per call, however many rooms.
func TestAListingAsksTheDaemonOnce(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	for i := range 20 {
		f.rooms = append(f.rooms, domain.Room{ID: domain.RoomID(fmt.Sprintf(mx+"!r%d:x", i)), Name: fmt.Sprintf("Room %d", i)})
	}
	s := newServer(f, shareAll)
	call(t, s, "list_rooms", nil)
	if f.spacesCalls != 1 || f.encryptionCalls != 1 {
		t.Fatalf("list_rooms over %d rooms read spaces %d times and encryption %d times, want once each",
			len(f.rooms), f.spacesCalls, f.encryptionCalls)
	}
	// The next call rereads spaces and re-asks about plain rooms (encryption may have
	// turned on), but not the encrypted one.
	call(t, s, "unread_summary", nil)
	if f.spacesCalls != 2 || f.encryptionCalls != 2 {
		t.Fatalf("after a second call: spaces %d, encryption %d, want 2 and 2", f.spacesCalls, f.encryptionCalls)
	}
	if last := f.encryptionAsked[len(f.encryptionAsked)-1]; slices.Contains(last, mx+"!secret:x") {
		t.Errorf("asked again about a room already known encrypted: %v", last)
	}
}

// A room that turns encryption on between calls is out of scope on the second, when
// encrypted rooms are not shared.
func TestARoomThatTurnsOnEncryptionLeavesTheScope(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	s := newServer(f, shareAll)
	if got := call(t, s, "list_rooms", nil); !listsRoom(got, mx+"!open:x") {
		t.Fatalf("the plain room is not listed at first: %v", got)
	}
	f.encrypted[mx+"!open:x"] = true
	if got := call(t, s, "list_rooms", nil); listsRoom(got, mx+"!open:x") {
		t.Errorf("a room that turned encryption on is still listed: %v", got)
	}
}

// listsRoom reports whether a list_rooms answer includes the room.
func listsRoom(answer map[string]any, id string) bool {
	rooms, _ := answer["rooms"].([]any)
	for _, r := range rooms {
		if room, _ := r.(map[string]any); room["id"] == id {
			return true
		}
	}
	return false
}

// Assistants see people by this person's names for them (alias, phone book name),
// whole: a first name alone in a group loses who it was.
func TestAnAssistantReadsPeopleByTheirWholeNames(t *testing.T) {
	t.Parallel()
	f := twoRooms()
	f.dir = domain.NewDirectory([]domain.PersonName{{Source: "phone", ID: domain.PhoneID("15550100001"), Name: "Dana Levi"}}, nil)
	f.messages[mx+"!open:x"] = []domain.Message{{
		ID: mx + "$m", Sender: "matrix:@dana:x", SenderName: "Dana", Body: "@100000000000005 can you look?", Timestamp: time.Unix(1_700_000_000, 0),
		Mentions: []domain.Mention{{UserID: "whatsapp:15550100001@s.whatsapp.net", Name: "@100000000000005"}},
	}}
	s := newServer(f, shareAll)
	s.aliases = map[string]string{"matrix:@dana:x": "Dana Cohen"}

	out := call(t, s, "read_room", map[string]any{"room": "Standup"})
	msgs, _ := out["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("read_room returned %d messages, want 1", len(msgs))
	}
	got, _ := msgs[0].(map[string]any)
	if got["sender_name"] != "Dana Cohen" || got["body"] != "@Dana Levi can you look?" {
		t.Errorf("the assistant read %q saying %q, want Dana Cohen saying \"@Dana Levi can you look?\"", got["sender_name"], got["body"])
	}
}

// Counted mentions leave out hidden people, so the summary uses them; a room counted
// only by its server keeps the server's highlights.
func TestTheUnreadSummaryUsesCountedMentions(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	f.unread = []domain.Unread{
		{RoomID: mx + "!open:x", Counted: true, Messages: 0, Mentions: 0, Highlights: 2}, // both mentions were hidden people's
		{RoomID: mx + "!secret:x", Highlights: 1},
	}
	out := call(t, newServer(f, shareAllEncrypted), "unread_summary", nil)
	waiting, _ := out["waiting"].([]any)
	if len(waiting) != 1 {
		t.Fatalf("waiting = %v, want only the server-counted room", waiting)
	}
	if room, _ := waiting[0].(map[string]any); room["room"] != mx+"!secret:x" || room["mentions"] != float64(1) {
		t.Errorf("waiting = %v, want !secret:x with its server's one mention", room)
	}
}
