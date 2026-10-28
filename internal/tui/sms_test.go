package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A file sent to a chat that sends as SMS asks first, naming it; the caption waits in
// the composer; a yes sends both, a no sends nothing and keeps the caption.
func TestAFileAsMMSAsksFirst(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "invoice.pdf")
	if err := os.WriteFile(file, []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, yes := range []bool{true, false} {
		backend := &sendingBackend{}
		m := sized(t, update(t, starterNew(backend, config.Display{}),
			roomsMsg{rooms: []domain.Room{{ID: "gmessages:a/1", Name: "Dana", SMS: true}}}))
		m, _ = m.selectRoom(m.filteredRooms()[0])
		m.focus, m.compose.insertMode = paneTimeline, true
		m = typeInto(t, m.clearStatus(), "here you go")
		m, cmd := updateWith(t, m, fileChosenMsg{path: file})
		m = deliver(t, m, cmd)
		if !m.confirm.active() || backend.path != "" {
			t.Fatalf("yes=%v: asked %v, sent %q", yes, m.confirm.active(), backend.path)
		}
		if got := m.confirmPrompt(); !strings.Contains(got, "invoice.pdf") || !strings.Contains(got, "MMS") {
			t.Errorf("the question: %q", got)
		}
		if m.compose.input != "here you go" {
			t.Errorf("the caption while asking: %q", m.compose.input)
		}
		key := "n"
		if yes {
			key = "y"
		}
		m, cmd = press(t, m, keyText(key))
		m = deliver(t, m, cmd)
		switch {
		case yes && (backend.path != file || backend.caption != "here you go" || m.compose.input != ""):
			t.Errorf("after yes: sent %q with %q, composer %q", backend.path, backend.caption, m.compose.input)
		case !yes && (backend.path != "" || m.compose.input != "here you go"):
			t.Errorf("after no: sent %q, composer %q", backend.path, m.compose.input)
		}
	}
}

// Sending to a chat, over whether it sends as SMS, every [composer] confirm_sms
// setting, each way of sending (enter, /me, /in) and each answer: it asks exactly when
// the chat sends as SMS and confirm_sms is not off; nothing goes before the yes; a yes
// sends (or schedules) what was typed, a no sends nothing and keeps the draft.
func TestSendingAsSMSAsksFirst(t *testing.T) {
	t.Parallel()

	on, off := true, false
	for _, sms := range []bool{false, true} {
		for _, setting := range []*bool{nil, &on, &off} {
			for _, way := range []string{"", "/me ", "/in 2h "} {
				for _, yes := range []bool{true, false} {
					name := fmt.Sprintf("sms=%v confirm=%v way=%q yes=%v", sms, setting != nil && *setting || setting == nil, way, yes)
					m, b := composingIn(t, domain.Room{ID: "gmessages:a/1", Name: "Dana", SMS: sms})
					m.conf.device.Composer.ConfirmSMS = setting
					m = typeInto(t, m, way+"see you")
					m, cmd := press(t, m, sendKey())
					asks := sms && (setting == nil || *setting)
					if m.confirm.active() != asks {
						t.Errorf("%s: asked %v", name, m.confirm.active())
						continue
					}
					if asks {
						if len(b.sent) != 0 || cmd != nil {
							t.Errorf("%s: sent before the answer", name)
						}
						if got := m.confirmPrompt(); got == "" {
							t.Errorf("%s: no question", name)
						}
						key := "n"
						if yes {
							key = "y"
						}
						m, cmd = press(t, m, keyText(key))
					}
					m = deliver(t, m, cmd)
					went := !asks || yes
					switch way {
					case "/in 2h ":
						if kept := m.compose.input != ""; kept == went {
							t.Errorf("%s: scheduled %v, draft %q", name, went, m.compose.input)
						}
					default:
						if (len(b.sent) == 1) != went {
							t.Fatalf("%s: sent %d", name, len(b.sent))
						}
						if went && (b.sent[0].Body != "see you" || b.sent[0].Emote != (way == "/me ")) {
							t.Errorf("%s: sent %+v", name, b.sent[0])
						}
						if !went && m.compose.input != way+"see you" {
							t.Errorf("%s: the draft is %q", name, m.compose.input)
						}
					}
				}
			}
		}
	}
}

// composingIn is composing in room.
func composingIn(t *testing.T, room domain.Room) (Model, *memberBackend) {
	t.Helper()
	b := &memberBackend{}
	m := sized(t, update(t, starterNew(b, config.Display{}), roomsMsg{rooms: []domain.Room{room}}))
	m, _ = m.selectRoom(m.filteredRooms()[0])
	m.focus, m.compose.insertMode = paneTimeline, true
	return m.clearStatus(), b
}

// The question names the chat and says why it asks.
func TestTheSMSQuestion(t *testing.T) {
	t.Parallel()

	m, _ := composing(t)
	room := domain.Room{ID: "gmessages:a/1", Name: "Dana", SMS: true}
	for _, action := range []pendingAction{pendingSendSMS, pendingScheduleSMS} {
		m.confirm = confirmState{action: action, room: room}
		if got := m.confirmPrompt(); !strings.Contains(got, "Dana") {
			t.Errorf("%v: %q, want it to name the chat", action, got)
		}
	}
}
