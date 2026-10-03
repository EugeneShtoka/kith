package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// typer records the typing notices this client sends.
type typer struct {
	apitest.Nop
	calls  int
	room   domain.RoomID
	typing bool
}

func (tp *typer) SendTyping(_ context.Context, roomID domain.RoomID, typing bool, _ time.Duration) error {
	tp.calls++
	tp.room, tp.typing = roomID, typing
	return nil
}

// Someone else typing shows on the rule above the composer — the line you are already
// looking at while you wait for them, and the one place it costs the conversation no
// room.
func TestTypingShowsOnTheComposerDivider(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.openRoom = "!a:x"
	m = update(t, m, activityMsg{a: domain.Activity{RoomID: "!a:x", Typing: []string{"@alice:x"}}})

	if got := m.typingNote(); !strings.Contains(got, "is typing") || !strings.Contains(got, "alice") {
		t.Errorf("typingNote = %q, want it to name alice", got)
	}
	if got := m.composerDivider(60); !strings.Contains(got, "is typing") {
		t.Errorf("divider = %q, want the typing note on it", got)
	}

	// m.typing carries the whole set, so an empty one means everybody stopped — and
	// that emptiness is the news.
	m = update(t, m, activityMsg{a: domain.Activity{RoomID: "!a:x"}})
	if got := m.typingNote(); got != "" {
		t.Errorf("typingNote = %q after everyone stopped, want empty", got)
	}
}

// Two typists are named; past that it becomes a count, which is what fits and what
// anyone actually wants to know.
func TestTypingNamesTwoThenCounts(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.openRoom = "!a:x"
	m = update(t, m, activityMsg{a: domain.Activity{RoomID: "!a:x", Typing: []string{"@alice:x", "@bob:x"}}})
	if got := m.typingNote(); !strings.Contains(got, "alice") || !strings.Contains(got, "bob") {
		t.Errorf("typingNote = %q, want both names", got)
	}
	m = update(t, m, activityMsg{a: domain.Activity{RoomID: "!a:x", Typing: []string{"@a:x", "@b:x", "@c:x"}}})
	if got := m.typingNote(); got != "3 people are typing" {
		t.Errorf("typingNote = %q, want a count", got)
	}
}

func TestTypingNoticeIsSentOnceWhileTyping(t *testing.T) {
	t.Parallel()

	tp := &typer{}
	m := sized(t, withRooms(t, starterNew(tp, config.Display{})))
	next, _ := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m = next
	m.focus = paneTimeline
	m.compose.insertMode = true

	m, cmd := press(t, m, keyText("h"))
	m = deliver(t, m, cmd)
	if tp.calls != 1 || tp.room != "!a:x" || !tp.typing {
		t.Fatalf("first keystroke: calls=%d room=%q typing=%v, want 1 !a:x true", tp.calls, tp.room, tp.typing)
	}
	m, cmd = press(t, m, keyText("i"))
	m = deliver(t, m, cmd)
	if tp.calls != 1 {
		t.Errorf("calls = %d after a second keystroke, want the notice not re-sent", tp.calls)
	}

	// Sending says what the notice was promising, so the notice stops with it.
	_, cmd = press(t, m, sendKey())
	m = deliver(t, m, cmd)
	if tp.calls != 2 || tp.typing {
		t.Errorf("after sending: calls=%d typing=%v, want a stop", tp.calls, tp.typing)
	}
}

// Emptying the composer is deciding not to say it, so the notice stops rather than
// standing until it expires.
func TestDeletingTheDraftStopsTheNotice(t *testing.T) {
	t.Parallel()

	tp := &typer{}
	m := sized(t, withRooms(t, starterNew(tp, config.Display{})))
	next, _ := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m = next
	m.focus = paneTimeline
	m.compose.insertMode = true

	m, cmd := press(t, m, keyText("h"))
	m = deliver(t, m, cmd)
	m, cmd = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = deliver(t, m, cmd)
	if m.compose.input != "" {
		t.Fatalf("draft = %q, want it emptied", m.compose.input)
	}
	if tp.calls != 2 || tp.typing {
		t.Errorf("after emptying: calls=%d typing=%v, want a stop", tp.calls, tp.typing)
	}
}

// Both halves are separately refusable: one setting hides what other people are doing,
// the other stops telling them what you are.
func TestTypingSettingsAreTwoSwitches(t *testing.T) {
	t.Parallel()

	off := false
	m := sized(t, withRooms(t, starterNew(apitest.Nop{}, config.Display{Typing: &off})))
	m.openRoom = "!a:x"
	m = update(t, m, activityMsg{a: domain.Activity{RoomID: "!a:x", Typing: []string{"@alice:x"}}})
	if got := m.typingNote(); got != "" {
		t.Errorf("typingNote = %q with typing display off, want nothing", got)
	}

	tp := &typer{}
	quiet := sized(t, withRooms(t, starterNew(tp, config.Display{SendTyping: &off})))
	next, _ := quiet.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	quiet = next
	quiet.focus = paneTimeline
	quiet.compose.insertMode = true
	quiet, cmd := press(t, quiet, keyText("h"))
	_ = deliver(t, quiet, cmd)
	if tp.calls != 0 {
		t.Errorf("sent %d typing notices with send_typing off, want 0", tp.calls)
	}
}

// Every way a message leaves ends the notice, not only a plain send.
func TestEditsAndCommandOutputStopTheNotice(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		send func(Model) (Model, tea.Cmd)
	}{
		{"edit", func(m Model) (Model, tea.Cmd) {
			m.compose.editing = "$mine:x"
			return m.submitEdit(domain.Room{ID: "!a:x"}, "fixed")
		}},
		{"command output", func(m Model) (Model, tea.Cmd) {
			return m.sendCommandOutput(commandRanMsg{room: "!a:x", body: "Zoom meeting started"})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			tp := &typer{}
			m := sized(t, withRooms(t, starterNew(tp, config.Display{})))
			next, _ := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
			m = next
			m.focus, m.compose.insertMode = paneTimeline, true
			m, cmd := press(t, m, keyText("h"))
			m = deliver(t, m, cmd)
			if tp.calls != 1 || !tp.typing {
				t.Fatalf("setup: calls=%d typing=%v, want a notice out", tp.calls, tp.typing)
			}

			m, cmd = c.send(m)
			m = deliver(t, m, cmd)
			if tp.calls != 2 || tp.typing {
				t.Errorf("after the %s: calls=%d typing=%v, want a stop", c.name, tp.calls, tp.typing)
			}
			if m.sentTyping.room != "" {
				t.Errorf("sentTyping = %q, want cleared", m.sentTyping.room)
			}
		})
	}
}
