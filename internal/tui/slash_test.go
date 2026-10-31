package tui

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// /me sends an emote msgtype, which other clients render in the third person.
func TestMeSendsAnEmote(t *testing.T) {
	t.Parallel()

	m, backend := attaching(t)
	m, _ = press(t, m, keyText("i"))
	m = typeInto(t, m, "/me is on the way")
	m, cmd := press(t, m, sendKey())
	m = drain(t, m, cmd)

	if len(backend.sent) != 1 {
		t.Fatalf("sent %d drafts, want one", len(backend.sent))
	}
	draft := backend.sent[0]
	if !draft.Emote {
		t.Error("the draft was not an emote")
	}
	if draft.Body != "is on the way" {
		t.Errorf("body = %q, want the words without the command", draft.Body)
	}
	if got := m.editorFor(fieldComposer).text; got != "" {
		t.Errorf("composer = %q, want it emptied", got)
	}
}

func TestEmoteRendersWithItsMarker(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@dana:x", SenderName: "Dana", Body: "waves", Timestamp: at(1), Emote: true},
	}}})
	frame := stripStyles(m.View().Content)
	if !strings.Contains(frame, "* waves") {
		t.Errorf("an emote is not marked in the frame:\n%s", frame)
	}
}

// /me with nothing after it says what it wants instead of sending an empty emote.
func TestMeWithNoWordsSaysWhatItWants(t *testing.T) {
	t.Parallel()

	m, backend := attaching(t)
	m, _ = press(t, m, keyText("i"))
	m = typeInto(t, m, "/me")
	m, cmd := press(t, m, sendKey())
	m = drain(t, m, cmd)

	if len(backend.sent) != 0 {
		t.Errorf("sent %+v, want nothing", backend.sent)
	}
	if m.status() == "" {
		t.Errorf("status = %q, want the usage", m.status())
	}
}

// Non-commands are sent: an unknown command as typed, a leading `//` minus one slash,
// a multi-line body whole.
func TestNonCommandsAreSentAsMessages(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, typed, more, want string }{
		{"unknown", "/shrug", "", "/shrug"},
		{"escaped", "//me is a command", "", "/me is a command"},
		{"multi-line", "/me and then", "\nmore words", "/me and then\nmore words"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, backend := attaching(t)
			m, _ = press(t, m, keyText("i"))
			m = typeInto(t, m, tc.typed)
			if tc.more != "" {
				m = m.store(fieldComposer, m.editorFor(fieldComposer).insert(tc.more))
			}
			m, cmd := press(t, m, sendKey())
			drain(t, m, cmd)
			if len(backend.sent) != 1 || backend.sent[0].Body != tc.want {
				t.Fatalf("sent %+v, want %q", backend.sent, tc.want)
			}
			if backend.sent[0].Emote {
				t.Error("sent as an emote")
			}
		})
	}
}

// `/` completes commands only at the start of the composer.
func TestSlashCompletesCommands(t *testing.T) {
	t.Parallel()

	m, _ := attaching(t)
	m, _ = press(t, m, keyText("i"))
	m = typeInto(t, m, "/")
	if !m.completion.active {
		t.Fatal("/ did not open the command list")
	}
	if len(m.completion.candidates) != len(slashCommands) {
		t.Errorf("%d candidates, want every command", len(m.completion.candidates))
	}
	// An exact match comes first among prefix matches.
	m = typeInto(t, m, "me")
	if len(m.completion.candidates) == 0 || m.completion.candidates[0].text != "/me" {
		t.Errorf("candidates = %+v, want /me first", m.completion.candidates)
	}
	m = typeInto(t, m, "n")
	if len(m.completion.candidates) != 1 || m.completion.candidates[0].text != "/mentions" {
		t.Errorf("candidates = %+v, want only /mentions", m.completion.candidates)
	}

	mid, _ := attaching(t)
	mid, _ = press(t, mid, keyText("i"))
	mid = typeInto(t, mid, "and/or")
	if mid.completion.active {
		t.Error("a slash inside a word opened the command list")
	}
}

// testdata/commands.json says which commands the composer knows and what words read as,
// for the desktop's port too (desktop/src/renderer/slash.ts); this holds the table to
// slashCommands and to the order submit and composerCommand read words in.
func TestTheCommandTableIsWhatTheTUIReads(t *testing.T) {
	t.Parallel()
	var table struct {
		Names []string `json:"names"`
		Cases []struct {
			Input   string  `json:"input"`
			Message *string `json:"message"`
			Builtin string  `json:"builtin"`
			User    string  `json:"user"`
			Arg     string  `json:"arg"`
		} `json:"cases"`
	}
	data, err := os.ReadFile("testdata/commands.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(slashCommands))
	for i, c := range slashCommands {
		names[i] = c.name
	}
	if !slices.Equal(names, table.Names) {
		t.Errorf("slashCommands are %v, the table says %v", names, table.Names)
	}
	for _, c := range table.Cases {
		body := strings.TrimSpace(c.Input)
		var got string
		switch {
		case strings.HasPrefix(body, "//"):
			got = "message " + body[1:]
		case strings.ContainsRune(body, '\n'):
			got = "message " + body
		default:
			for _, cmd := range slashCommands {
				if arg, ok := commandArg(body, cmd.name); ok {
					got = "builtin " + cmd.name + " " + arg
					break
				}
			}
			if name, arg, ok := commandWord(body); got == "" && ok {
				got = "user " + strings.TrimPrefix(name, "/") + " " + arg
			} else if got == "" {
				got = "message " + body
			}
		}
		want := "builtin " + c.Builtin + " " + c.Arg
		switch {
		case c.Message != nil:
			want = "message " + *c.Message
		case c.User != "":
			want = "user " + c.User + " " + c.Arg
		}
		if got != want {
			t.Errorf("%q reads as %q, the table says %q", c.Input, got, want)
		}
	}
}
