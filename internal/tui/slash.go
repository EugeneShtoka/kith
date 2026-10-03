package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Commands typed into the composer. A command belongs here when its target is the
// room you are writing in; another room named by address is the room list's join
// key, and the thing under the cursor is a key in that pane.
//
// Only known commands are intercepted — anything else starting with a slash is a
// message. A leading `//` escapes one slash, and a multi-line body is never a command.

// slashCommand is one command and what it does.
type slashCommand struct {
	name string // includes the slash: "/me"
	// arg names the argument for the usage line; "" means none. A command with a
	// required arg refuses to run without one.
	arg         string
	argOptional bool
	summary     string // one line, for the completion popup and the help overlay
	// keyScope and keyAct name the keybinding this command mirrors.
	keyScope scope
	keyAct   action
	// run acts on the argument in the open room. The composer still holds the
	// command text, so a run that sends has to clear it (see sendComposed).
	run func(m Model, arg string, room domain.Room) (Model, tea.Cmd)
}

// consuming wraps a run that opens a pane: it clears the command from the composer
// first, so the next enter does not run it again.
func consuming(open func(Model) (Model, tea.Cmd)) func(Model, string, domain.Room) (Model, tea.Cmd) {
	return func(m Model, _ string, _ domain.Room) (Model, tea.Cmd) {
		m.compose.input, m.compose.drafted = "", nil
		return open(m)
	}
}

// slashCommands is the table read by the dispatcher, `/` completion and help overlay.
var slashCommands = []slashCommand{
	{
		name: "/me", arg: "<what you are doing>",
		summary: "send as an emote — “* you wave” rather than “you: waves”",
		run: func(m Model, arg string, room domain.Room) (Model, tea.Cmd) {
			return m.sendComposed(room, arg, true)
		},
	},
	{
		name: "/summary", arg: "[2h | 7d | yesterday | 2026-09-18 | 50]", argOptional: true,
		summary: "what this room has been saying — since you last read it, or over the span you name",
		run: func(m Model, arg string, room domain.Room) (Model, tea.Cmd) {
			return m.openSummary(room, arg)
		},
	},
	{
		name: "/plain", arg: "<message>",
		summary: "send it exactly as typed — no Markdown, no rendering",
		run: func(m Model, arg string, room domain.Room) (Model, tea.Cmd) {
			// One message, not a mode: sendComposed clears the flag as it sends.
			m.plainSend = true
			return m.sendComposed(room, arg, false)
		},
	},
	{
		name: "/upload", arg: "<path[ | caption]>",
		keyScope: scopeInsert, keyAct: actAttach,
		summary: "send a file, with words under it if you add them",
		run: func(m Model, arg string, room domain.Room) (Model, tea.Cmd) {
			path, caption := splitCaption(arg)
			if path == "" {
				return m.say(uploadCommand + " needs a path — try " + uploadCommand + " ~/photo.png"), nil
			}
			// Cleared first: the words go with the file, not again on the next enter.
			m.compose.input, m.compose.drafted = "", nil
			return m.attachPath(room, path, caption)
		},
	},
	{
		name: "/at", arg: "<time> <message>",
		summary: "send it later today — /at 09:00 good morning",
		run: func(m Model, arg string, room domain.Room) (Model, tea.Cmd) {
			return m.scheduleComposed(room, "at", arg)
		},
	},
	{
		name: "/in", arg: "<delay> <message>",
		summary: "send it after a delay — /in 2h see you then",
		run: func(m Model, arg string, room domain.Room) (Model, tea.Cmd) {
			return m.scheduleComposed(room, "in", arg)
		},
	},
	{
		name: "/tracked", arg: "[word]", argOptional: true,
		summary: "where your tracked words came up here — {search.scope} widens, no word groups them",
		run: func(m Model, arg string, _ domain.Room) (Model, tea.Cmd) {
			m.compose.input, m.compose.drafted = "", nil
			return m.openTracked(strings.TrimSpace(arg))
		},
	},
	{
		name:     "/files",
		keyScope: scopeCommand,
		keyAct:   actFiles,
		summary:  "every message with a file in this room — {search.scope} widens it",
		run:      consuming(func(m Model) (Model, tea.Cmd) { return m.openFiles(false) }),
	},
	{
		name:     "/mentions",
		keyScope: scopeCommand,
		keyAct:   actMentions,
		summary:  "where you were named in this room — {search.scope} widens it",
		run:      consuming(func(m Model) (Model, tea.Cmd) { return m.openMentionsIn(m.defaultSearchScope()) }),
	},
	{
		name: "/search", arg: "[terms]", argOptional: true,
		keyScope: scopeCommand, keyAct: actSearchRoom,
		summary: "search this room — {search.scope} widens it, no terms just opens the box",
		run: func(m Model, arg string, _ domain.Room) (Model, tea.Cmd) {
			m.compose.input, m.compose.drafted = "", nil
			return m.openSearchFor(false, strings.TrimSpace(arg))
		},
	},
	{
		name:     "/threads",
		keyScope: scopeTimeline,
		keyAct:   actListThreads,
		summary:  "the conversations in this room — the same list `{timeline.list_threads}` opens",
		run:      consuming(func(m Model) (Model, tea.Cmd) { return m.openThreads(false) }),
	},
	{
		name:    "/starred",
		summary: "what you have starred here — {search.scope} widens it, and it unstars too",
		run:     consuming(Model.openStarred),
	},
	{
		name:    "/scheduled",
		summary: "what is waiting to go out in this room — {picker.accept} cancels one",
		run: func(m Model, _ string, room domain.Room) (Model, tea.Cmd) {
			m.compose.input, m.compose.drafted = "", nil
			return m, m.scheduledCmd(room.ID)
		},
	},
	{
		name:     "/topic",
		keyScope: scopeTimeline,
		keyAct:   actTopic,
		summary:  "what this room is for, in full — the same reader `gt` opens",
		run:      consuming(Model.openTopic),
	},
	{
		name:     "/why",
		keyScope: scopeCommand,
		keyAct:   actWhy,
		summary:  "why this room does or does not notify you — the whole rule chain",
		run:      consuming(Model.openWhy),
	},
	{
		name:     "/unread",
		keyScope: scopeRooms,
		keyAct:   actMarkUnread,
		summary:  "mark this room unread — read, and still needs you",
		run:      consuming(Model.toggleRoomUnread),
	},
	{
		name: "/tag", arg: "[name]", argOptional: true,
		summary: "put this room in a tag, or take it out — a new name makes the tag; no name picks among your spaces and tags",
		run:     func(m Model, arg string, room domain.Room) (Model, tea.Cmd) { return m.toggleTag(arg, room) },
	},
	{
		name:    "/caught",
		summary: "what the spam filters catch here — {search.scope} widens it",
		run:     consuming(Model.openCaught),
	},
	{
		name:     "/spam",
		keyScope: scopeRooms,
		keyAct:   actSpam,
		summary:  "move this conversation into Spam, or take it out — out of the counts, still readable",
		run:      consuming(Model.toggleSpam),
	},
	{
		name:     "/direction",
		keyScope: scopeRooms,
		keyAct:   actDirection,
		summary:  "read this room right to left, then left to right, then as [display.direction] says",
		run:      consuming(Model.cycleDirection),
	},
	{
		name: "/invite", arg: "<@user:server>",
		summary: "invite somebody to the room you are writing in",
		run: func(m Model, arg string, room domain.Room) (Model, tea.Cmd) {
			// Captured now: the room list re-sorts as messages arrive.
			m.aimedAt.member = room.ID
			return m.submitInvite(arg)
		},
	},
	{
		name: "/leave", summary: "leave the room you are writing in (asks first)",
		run: func(m Model, _ string, _ domain.Room) (Model, tea.Cmd) {
			// Irreversible, so through the room list's confirmation.
			return m.askLeave()
		},
	},
}

const uploadCommand = "/upload"

// composerCommand acts on a command in the composer; handled is false for an ordinary
// message.
func (m Model) composerCommand(body string, room domain.Room) (bool, Model, tea.Cmd) {
	if strings.HasPrefix(body, "//") {
		return false, m, nil // an escaped slash: submit() sends it, minus one slash
	}
	if strings.ContainsRune(body, '\n') {
		return false, m, nil
	}
	for _, cmd := range slashCommands {
		arg, ok := commandArg(body, cmd.name)
		if !ok {
			continue
		}
		if cmd.arg != "" && !cmd.argOptional && arg == "" {
			return true, m.say(cmd.name + " " + cmd.arg), nil
		}
		mdl, run := cmd.run(m, arg, room)
		return true, mdl, run
	}
	// Then user scripts; a built-in wins a name collision.
	if name, arg, ok := commandWord(body); ok {
		if path, found := m.userCommand(name); found {
			mdl, run := m.runUserCommand(name, path, arg, room)
			return true, mdl, run
		}
	}
	return false, m, nil
}

// commandWord splits a body into its leading /word and the rest.
func commandWord(body string) (name, arg string, ok bool) {
	if !strings.HasPrefix(body, "/") || len(body) == 1 {
		return "", "", false
	}
	name, arg, _ = strings.Cut(body, " ")
	return name, strings.TrimSpace(arg), true
}

// commandArg matches a command and returns its argument. Space or colon separates;
// the bare command matches with an empty argument.
func commandArg(body, command string) (string, bool) {
	if body == command {
		return "", true
	}
	for _, sep := range []string{" ", ":"} {
		if arg, found := strings.CutPrefix(body, command+sep); found {
			return strings.TrimSpace(arg), true
		}
	}
	return "", false
}

// allCommands is the built-in table plus the scripts found when the popup last opened,
// with the dispatcher's precedence (built-ins win). It touches no disk: it runs on
// every keystroke, so the scan happens once per popup open (scanUserCommandsCmd).
func (m Model) allCommands() []slashCommand {
	if len(m.completion.userCommands) == 0 {
		return slashCommands
	}
	all := slices.Clone(slashCommands)
	for _, cmd := range m.completion.userCommands {
		if slices.ContainsFunc(all, func(c slashCommand) bool { return c.name == cmd.name }) {
			continue
		}
		all = append(all, cmd)
	}
	return all
}

// scanUserCommandsCmd reads the commands directory off the event loop. A missing
// directory means no scripts, not an error.
func (m Model) scanUserCommandsCmd() tea.Cmd {
	dir, err := m.commandsDir()
	if err != nil || dir == "" {
		return func() tea.Msg { return userCommandsMsg{} }
	}
	return func() tea.Msg { return userCommandsMsg{commands: scanUserCommands(dir)} }
}

// userCommandsMsg carries the result of one directory scan back to the popup.
type userCommandsMsg struct{ commands []slashCommand }

// scanUserCommands lists the executable files in dir as commands — the same test
// userCommand applies, so the popup cannot offer what the dispatcher would refuse.
func scanUserCommands(dir string) []slashCommand {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // no commands directory is the usual case: no commands to offer
	}
	out := make([]slashCommand, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, statErr := os.Stat(filepath.Join(dir, entry.Name()))
		if statErr != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		out = append(out, slashCommand{
			name: "/" + entry.Name(), arg: "[…]",
			summary: "yours — " + entry.Name() + " in your commands folder",
		})
	}
	return out
}

// commandCandidates offers the commands for a "/" typed at the start of the composer,
// labeled with their usage.
func (m Model) commandCandidates(query string) []candidate {
	q := strings.ToLower(strings.TrimSpace(query))
	rows := make([]candidate, 0, len(slashCommands))
	for _, cmd := range m.allCommands() {
		if !strings.HasPrefix(strings.TrimPrefix(cmd.name, "/"), q) {
			continue
		}
		label := cmd.name
		if cmd.arg != "" {
			label += " " + cmd.arg
		}
		rows = append(rows, candidate{
			// Only the name is inserted; the usage is a hint.
			text: cmd.name, label: label, spaceAfter: cmd.arg != "",
			detail: m.commandDetail(cmd.summary, cmd.keyScope, cmd.keyAct),
		})
	}
	return rows
}
