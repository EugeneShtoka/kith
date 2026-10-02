package tui

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// What a script is given: the context under the cursor (link, message, history).
// Scalars go in the environment and payloads on stdin, never argv — /proc/<pid>/cmdline
// is world-readable and would publish decrypted history. A script sees what the screen
// shows: spoilers stay covered and deleted messages stay placeholders.

const (
	envURL       = commandEnvPrefix + "URL"
	envMessageID = commandEnvPrefix + "MESSAGE_ID"
	envSender    = commandEnvPrefix + "SENDER"
	envSenderVia = commandEnvPrefix + "SENDER_NAME"
)

// pagerState is a script's output, split once into lines for the overlay.
type pagerState struct {
	title string
	lines []string
}

// openPager shows a script's output in the read-and-dismiss overlay, titled by the
// command. Truncation is noted at the top, where the overlay cannot cover it.
func (m Model) openPager(name, body string, truncated bool) (Model, tea.Cmd) {
	lines := strings.Split(body, "\n")
	if truncated {
		lines = append([]string{"(more than fits — showing the first " +
			strconv.Itoa(commandOutputLimit/1024) + "KB)", ""}, lines...)
	}
	m.pager = pagerState{title: name, lines: lines}
	m.reader = m.reader.opening(readerScript)
	return m.doneWithCommand().clearStatus(), nil
}

// pendingScript is a command waiting on the context it declared — a need can be a
// question (which of three links?) asked before it runs.
type pendingScript struct {
	name, path, arg string
	room            domain.Room
	needs           []domain.Need
	// picked holds the values already resolved, by kind.
	picked map[domain.NeedKind]string
}

// scriptNeeds is what a command declared, parsed (setup.Scripts has already refused
// unknown entries).
func (m Model) scriptNeeds(name string) []domain.Need {
	script, configured := m.conf.base.Commands.ScriptFor(name)
	if !configured {
		return nil
	}
	needs := make([]domain.Need, 0, len(script.Needs))
	for _, entry := range script.Needs {
		if need, ok := domain.ParseNeed(entry); ok {
			needs = append(needs, need)
		}
	}
	return needs
}

// scriptOutput is where a command's output goes: its block's `output`, else the
// composer.
func (m Model) scriptOutput(name string) domain.ScriptOutput {
	if script, configured := m.conf.base.Commands.ScriptFor(name); configured {
		if out, ok := domain.ParseScriptOutput(script.Output); ok {
			return out
		}
	}
	return domain.OutputCompose
}

// resolveScript asks for the next unanswered question need, and runs when none is
// left; each answer re-enters here.
func (m Model) resolveScript() (Model, tea.Cmd) {
	for _, need := range m.script.needs {
		kind, asks := contextForNeed[need.Kind]
		if !asks || m.script.picked[need.Kind] != "" {
			continue
		}
		msg, ok := m.scriptMessage()
		if !ok {
			return m.say(m.script.name + " needs a message and there is none here").cancelScript(), nil
		}
		items := contextSpecs[kind].items(m, msg)
		switch len(items) {
		case 0:
			said := m.script.name + ": " + contextSpecs[kind].empty(m)
			return m.say(said).cancelScript(), nil
		case 1:
			m.script.picked = withEntry(m.script.picked, need.Kind, items[0].value)
		default:
			title := m.script.name + " — which " + contextSpecs[kind].noun + "?"
			// Close the composer's completion: it gets keys before the picker and
			// would swallow the arrows and enter meant for this list.
			m = m.closeCompletion()
			m.picker = newPickerWith(pickerContext, pickerSpec{title: title}, items)
			m.picker.then = actRunScript
			return m, nil
		}
	}
	return m.launchScript()
}

// contextForNeed is which needs are questions, and about what; the rest have exactly
// one answer.
var contextForNeed = map[domain.NeedKind]contextKind{
	domain.NeedURL: ctxLink,
}

// pickedScriptContext files a chosen row as the answer and carries on resolving.
func (m Model) pickedScriptContext(value string) (Model, tea.Cmd) {
	if m.script.name == "" {
		return m, nil
	}
	for _, need := range m.script.needs {
		if _, asks := contextForNeed[need.Kind]; asks && m.script.picked[need.Kind] == "" {
			m.script.picked = withEntry(m.script.picked, need.Kind, value)
			break
		}
	}
	return m.resolveScript()
}

// cancelScript drops a command that cannot be given what it asked for; the composer
// keeps what was typed.
func (m Model) cancelScript() Model {
	m.script = pendingScript{}
	m.running = ""
	return m
}

// scriptKey runs the command a sequence is bound to, reporting whether one was. Not
// while typing; a binding whose script is missing says so rather than doing nothing.
func (m Model) scriptKey(press string) (Model, tea.Cmd, bool) {
	if m.typing() {
		return m, nil, false
	}
	name, bound := m.keys.scriptFor(press)
	if !bound {
		return m, nil, false
	}
	path, found := m.userCommand(name)
	if !found {
		return m.say("/" + name + ": no script of that name in your commands directory"), nil, true
	}
	room, open := m.currentRoom()
	if !open {
		return m.say("/" + name + " needs a room open"), nil, true
	}
	mdl, cmd := m.runUserCommand("/"+name, path, "", room)
	return mdl, cmd, true
}

// scriptMessage is the message a script's context comes from: the selection, else the
// newest message in the room.
func (m Model) scriptMessage() (domain.Message, bool) {
	if msg, ok := m.selectedMessage(); ok {
		return msg, true
	}
	shown := m.shownMessages()
	if len(shown) == 0 {
		return domain.Message{}, false
	}
	return shown[len(shown)-1], true
}

// scriptMessageJSON is one message as a script reads it, named like mxctl's event
// JSON. Body is the resolved text (the edit, or the redaction placeholder).
type scriptMessageJSON struct {
	EventID    string `json:"event_id"`
	RoomID     string `json:"room_id"`
	Sender     string `json:"sender"`
	SenderName string `json:"sender_name,omitempty"`
	Body       string `json:"body"`
	Timestamp  string `json:"timestamp"`
	Edited     bool   `json:"edited,omitempty"`
	Deleted    bool   `json:"deleted,omitempty"`
	Mine       bool   `json:"mine,omitempty"`
}

// scriptContextJSON is the object a script receives on stdin; undeclared keys are
// absent rather than null.
type scriptContextJSON struct {
	Message *scriptMessageJSON  `json:"message,omitempty"`
	History []scriptMessageJSON `json:"history,omitempty"`
	URL     string              `json:"url,omitempty"`
}

// scriptJSON builds that object for the needs this command declared.
func (m Model) scriptJSON() ([]byte, error) {
	var out scriptContextJSON
	for _, need := range m.script.needs {
		switch need.Kind {
		case domain.NeedMessage:
			if msg, ok := m.scriptMessage(); ok {
				one := m.scriptMessageRow(msg)
				out.Message = &one
			}
		case domain.NeedHistory:
			out.History = m.scriptHistory(need.Count)
		case domain.NeedURL:
			out.URL = m.script.picked[domain.NeedURL]
		case domain.NeedNone:
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller reports it as the command's own failure
	}
	return data, nil
}

// scriptMessageRow is one message as a script reads it; Summary keeps spoilers
// covered and redactions as placeholders, as the timeline drew them.
func (m Model) scriptMessageRow(msg domain.Message) scriptMessageJSON {
	return scriptMessageJSON{
		EventID:    string(msg.ID),
		RoomID:     string(msg.RoomID),
		Sender:     msg.Sender,
		SenderName: msg.SenderName,
		Body:       msg.Summary(),
		Timestamp:  msg.Timestamp.Format(time.RFC3339),
		Edited:     msg.Edited,
		Deleted:    msg.Redacted,
		Mine:       m.isMe(msg.Sender),
	}
}

// scriptHistory is the last count messages in the room, oldest first.
func (m Model) scriptHistory(count int) []scriptMessageJSON {
	shown := m.shownMessages()
	if len(shown) > count {
		shown = shown[len(shown)-count:]
	}
	rows := make([]scriptMessageJSON, 0, len(shown))
	for i := range shown {
		rows = append(rows, m.scriptMessageRow(shown[i]))
	}
	return rows
}

// scriptEnv is the declared scalars, for scripts too short to parse JSON.
func (m Model) scriptEnv() []string {
	var env []string
	for _, need := range m.script.needs {
		switch need.Kind {
		case domain.NeedURL:
			env = append(env, envURL+"="+m.script.picked[domain.NeedURL])
		case domain.NeedMessage:
			msg, ok := m.scriptMessage()
			if !ok {
				continue
			}
			env = append(env,
				envMessageID+"="+string(msg.ID),
				envSender+"="+msg.Sender,
				envSenderVia+"="+msg.SenderName,
			)
		case domain.NeedHistory, domain.NeedNone:
			// Payloads go on stdin.
		}
	}
	return env
}
