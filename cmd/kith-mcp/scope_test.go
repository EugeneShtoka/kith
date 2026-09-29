package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/agent"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// everything is a send list that names every room in the fixture, which is the point of
// using it: whatever refuses the write, it is not the send list.
var everything = []string{"room:Standup", "room:Private chat", "dm", "group"}

// writerWith is a writer with separate read and write scopes.
func writerWith(t *testing.T, f *fake, read, write domain.ModelScope, send []string) *server {
	t.Helper()
	s := newWriter(t, f, read, send)
	s.write = write
	return s
}

// A room the assistant may read but not write to is refused — no send, no draft — and
// the refusal names [agent.write] and lands in the ledger.
func TestWriteOutsideTheWriteScopeIsRefused(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		write domain.ModelScope
	}{
		{"missing from write.rooms", domain.ModelScope{Only: []string{"room:Private chat"}, Encrypted: true}},
		{"named in write.except", domain.ModelScope{Except: []string{"space:Work"}, Encrypted: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := twoRooms()
			s := writerWith(t, f, shareAllEncrypted, tc.write, everything)
			// Readable, which is what makes this a write-scope refusal and not a read one.
			call(t, s, "read_room", map[string]any{"room": "Standup"})

			said := callErr(t, s, "send_message", map[string]any{"room": "Standup", "text": "hello"})
			if len(f.sent) != 0 || len(f.drafts) != 0 {
				t.Fatalf("a room outside the write scope was written to: sent=%+v drafts=%+v", f.sent, f.drafts)
			}
			if !strings.Contains(said, "[agent.write]") {
				t.Errorf("refusal = %q, want it to name [agent.write]", said)
			}
			ledger := entries(t, s)
			if len(ledger) != 1 || ledger[0].Outcome != agent.Refused || ledger[0].Room != "!open:x" ||
				!strings.Contains(ledger[0].Reason, "[agent.write]") || ledger[0].Text != "hello" {
				t.Fatalf("ledger = %+v, want one refused entry naming the room, the rule and the words", ledger)
			}
		})
	}
}

// Write is intersected with read: a write list that names a room the read lists refuse
// does not make it writable, and the refusal names the read table.
func TestWriteScopeCannotWidenTheReadScope(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	s := writerWith(t, f,
		domain.ModelScope{Only: []string{"group"}, Except: []string{"room:Standup"}},
		domain.ModelScope{Only: []string{"room:Standup"}}, everything)
	said := callErr(t, s, "send_message", map[string]any{"room": "Standup", "text": "hello"})
	if len(f.sent) != 0 || len(f.drafts) != 0 {
		t.Fatalf("an unreadable room was written to: sent=%+v drafts=%+v", f.sent, f.drafts)
	}
	if !strings.Contains(said, "[agent.read]") {
		t.Errorf("refusal = %q, want it to name [agent.read]", said)
	}
}

// Reading encrypted rooms does not by itself open them for writing: that is its own
// switch, and the refusal says which one to flip.
func TestEncryptedWritingIsItsOwnSwitch(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	s := writerWith(t, f, shareAllEncrypted, domain.ModelScope{}, everything)
	call(t, s, "read_room", map[string]any{"room": "Private chat"})
	said := callErr(t, s, "send_message", map[string]any{"room": "Private chat", "text": "hello"})
	if !strings.Contains(said, "[agent.write] encrypted") {
		t.Errorf("refusal = %q, want it to name [agent.write] encrypted", said)
	}

	s.write = domain.ModelScope{Encrypted: true}
	if out := call(t, s, "send_message", map[string]any{"room": "Private chat", "text": "hello"}); out["action"] != agent.Sent {
		t.Errorf("with both switches on, action = %v, want sent", out["action"])
	}
}

// Send is intersected with write: a room send names but write refuses is neither sent to
// nor drafted into.
func TestSendCannotOutrankTheWriteScope(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	s := writerWith(t, f, shareAll, domain.ModelScope{Except: []string{"room:Standup"}}, []string{"room:Standup"})
	callErr(t, s, "send_message", map[string]any{"room": "Standup", "text": "hello"})
	if len(f.sent) != 0 || len(f.drafts) != 0 {
		t.Fatalf("send outranked the write scope: sent=%+v drafts=%+v", f.sent, f.drafts)
	}
}

// Empty write lists mean every room the read scope allows — drafted, since send is empty.
func TestEmptyWriteScopeIsTheReadScope(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	s := writerWith(t, f, domain.ModelScope{Only: []string{"space:Work"}}, domain.ModelScope{}, nil)
	out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "hello"})
	if out["action"] != agent.Drafted || f.drafts["!open:x"].Body != "hello" {
		t.Fatalf("action = %v drafts = %+v, want a draft in the readable room", out["action"], f.drafts)
	}
}

// list_rooms says which of the rooms it shows may be written to, so the assistant can
// know before it tries.
func TestListRoomsMarksWhatIsWritable(t *testing.T) {
	t.Parallel()

	s := writerWith(t, twoRooms(), shareAllEncrypted, domain.ModelScope{}, nil)
	out := call(t, s, "list_rooms", nil)
	rooms, _ := out["rooms"].([]any)
	writable := map[string]bool{}
	for _, room := range rooms {
		entry, _ := room.(map[string]any)
		name, _ := entry["name"].(string)
		yes, _ := entry["writable"].(bool)
		writable[name] = yes
	}
	if len(writable) != 2 || !writable["Standup"] || writable["Private chat"] {
		t.Fatalf("writable = %v, want Standup writable and the encrypted room readable only", writable)
	}
}

// The tool description says where the assistant may write, from this config — the
// assistant reads the description before it ever calls the tool.
func TestSendMessageDescriptionStatesTheWriteScope(t *testing.T) {
	t.Parallel()

	s := writerWith(t, twoRooms(), shareAll,
		domain.ModelScope{Only: []string{"room:Notes", "space:Work"}}, []string{"room:Notes"})
	encoded, err := json.Marshal(s.toolList())
	if err != nil {
		t.Fatalf("encoding the tool list: %v", err)
	}
	var listed []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(encoded, &listed); err != nil {
		t.Fatalf("decoding the tool list: %v", err)
	}
	for _, tool := range listed {
		if tool.Name != "send_message" {
			continue
		}
		for _, want := range []string{"room:Notes", "space:Work", "[agent.write]", "encrypted"} {
			if !strings.Contains(tool.Description, want) {
				t.Errorf("send_message description lacks %q:\n%s", want, tool.Description)
			}
		}
		return
	}
	t.Fatal("send_message is not listed")
}

// With nothing listed for reading, every tool says so — once, naming the setting — rather
// than coming back empty in a way that reads like an empty account.
func TestNothingSharedIsSaidByEveryTool(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	s := newWriter(t, f, domain.ModelScope{}, everything)

	out := call(t, s, "list_rooms", nil)
	if rooms, _ := out["rooms"].([]any); len(rooms) != 0 {
		t.Errorf("list_rooms showed %d rooms with nothing shared", len(rooms))
	}
	if note, _ := out["note"].(string); !strings.Contains(note, "[agent.read] rooms") {
		t.Errorf("list_rooms note = %q, want it to name [agent.read] rooms", note)
	}
	questions := map[string]map[string]any{
		"read_room":       {"room": "Standup"},
		"search_messages": {"query": "morning"},
		"read_around":     {"room": "!open:x", "event": "$1"},
		"unread_summary":  nil,
		"find_people":     {"query": "Dana"},
		"find_rooms_with": {"people": []string{"Dana"}},
		"send_message":    {"room": "Standup", "text": "hello"},
	}
	for name, args := range questions {
		if said := callErr(t, s, name, args); !strings.Contains(said, "[agent.read] rooms") {
			t.Errorf("%s said %q, want it to name [agent.read] rooms", name, said)
		}
	}
	if len(f.sent) != 0 || len(f.drafts) != 0 {
		t.Fatalf("wrote with nothing shared: sent=%+v drafts=%+v", f.sent, f.drafts)
	}
	if ledger := entries(t, s); len(ledger) != 1 || ledger[0].Outcome != agent.Refused {
		t.Errorf("ledger = %+v, want the refused send recorded", ledger)
	}
	if !strings.Contains(s.instructions(), "Nothing in this account is shared") {
		t.Error("the session instructions do not say nothing is shared")
	}
	if !strings.Contains(s.writePolicy(), "nowhere yet") {
		t.Errorf("send_message description = %q, want it to say nowhere", s.writePolicy())
	}
}
