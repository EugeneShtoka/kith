package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Commands typed into the composer act on the room being written in; other rooms and
// the thing under the cursor are keys in their panes.
//
// Only known commands are intercepted; any other slash text is a message. A leading
// `//` escapes one slash, and a multi-line body is never a command.

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
	// run acts on the argument in the open room. The composer still holds the command
	// text, so a run that sends must clear it (see sendComposed).
	run func(m Model, arg string, room domain.Room) (Model, tea.Cmd)
}

// consuming wraps a run that opens a pane, clearing the command first so the next
// enter doesn't rerun it.
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
		name: "/export", arg: "[7d | yesterday | 2026-09-18 | 500] [~/folder | ~/folder/name.md]", argOptional: true,
		summary: "write this room, or the open thread, to a Markdown file — all the cache holds, or the span you name; into export_dir, or the folder or file you give",
		run: func(m Model, arg string, room domain.Room) (Model, tea.Cmd) {
			m.compose.input, m.compose.drafted = "", nil
			return m.openExport(room, arg)
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
		name:     "/hidden",
		keyScope: scopeTimeline,
		keyAct:   actShowMarked,
		summary:  "show the messages of people you hid, in this room until you leave it — or hide them again",
		run:      consuming(func(m Model) (Model, tea.Cmd) { return m.toggleMarkedShown() }),
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
		name:    "/ignored",
		summary: "the Matrix users you ignore — {picker.remove} unignores one",
		run:     consuming(Model.openIgnored),
	},
	{
		name:    "/discover",
		summary: "the Matrix public rooms directory — {picker.accept} joins the selected row",
		run:     consuming(Model.openPublicRooms),
	},
	{
		name:    "/knock",
		summary: "ask a moderator to let you into a knock-only Matrix room: /knock <room> [reason]",
		run:     runKnock,
	},
	{
		name: "/poll", arg: "<question> | <answer> | <answer> [| …] [| any]",
		summary: "ask a poll in this room — Matrix only; trailing \"| any\" lets voters pick several",
		run:     runCreatePoll,
	},
	{
		name:     "/topic",
		keyScope: scopeTimeline,
		keyAct:   actTopic,
		summary:  "what this room is for, in full — the same reader `gt` opens",
		run:      consuming(Model.openTopic),
	},
	{
		name:    "/why",
		summary: "why this room does or does not notify you — the whole rule chain",
		run:     consuming(Model.openWhy),
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
		summary: "what the filters catch here — {search.scope} widens it",
		run:     consuming(Model.openCaught),
	},
	{
		name:    "/direction",
		summary: "read this room right to left, then left to right, then as [display.direction] says",
		run:     consuming(Model.cycleDirection),
	},
	{
		name: "/shortcut", arg: "[keys]", argOptional: true,
		summary: "a key sequence that goes to this room (\"g w\"); none asks — {search.scope} there binds the rail's space instead",
		run:     func(m Model, arg string, room domain.Room) (Model, tea.Cmd) { return m.bindShortcut(arg, &room) },
	},
	{
		name:    "/replacement",
		summary: "go to the room that replaced this one, after an upgrade",
		run:     func(m Model, _ string, room domain.Room) (Model, tea.Cmd) { return m.goToReplacement(room) },
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
		name: "/unban", arg: "[@user:server]", argOptional: true,
		summary: "lift a ban in the room you are writing in; none asks who",
		run: func(m Model, arg string, room domain.Room) (Model, tea.Cmd) {
			m.compose.input, m.compose.drafted = "", nil
			m.aimedAt.member = room.ID
			if strings.TrimSpace(arg) == "" {
				return m.openPrompt(promptUnban), nil
			}
			return m.submitUnban(arg)
		},
	},
	{
		name: "/leave", summary: "leave the room you are writing in, or delete a private chat that cannot be left (asks first)",
		run: func(m Model, _ string, _ domain.Room) (Model, tea.Cmd) {
			// Irreversible, so through the room list's confirmation.
			return m.askLeave()
		},
	},
}

const uploadCommand = "/upload"

// composerCommand runs a command in the composer; handled is false for an ordinary message.
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

// allCommands is the built-ins plus the scripts found when the popup last opened
// (built-ins win), less those whose places leave out the open room. It runs on every
// keystroke, so it touches no disk.
func (m Model) allCommands() []slashCommand {
	if len(m.completion.userCommands) == 0 {
		return slashCommands
	}
	all := slices.Clone(slashCommands)
	room, open := m.currentRoom()
	for _, cmd := range m.completion.userCommands {
		if slices.ContainsFunc(all, func(c slashCommand) bool { return c.name == cmd.name }) {
			continue
		}
		if open && !m.commandAppliesIn(cmd.name, room) {
			continue
		}
		all = append(all, cmd)
	}
	return all
}

// scanUserCommandsCmd reads the commands directory off the event loop. A missing
// directory means no scripts.
func (m Model) scanUserCommandsCmd() tea.Cmd {
	dir, err := m.commandsDir()
	if err != nil || dir == "" {
		return func() tea.Msg { return userCommandsMsg{} }
	}
	return func() tea.Msg { return userCommandsMsg{commands: scanUserCommands(dir)} }
}

type userCommandsMsg struct{ commands []slashCommand }

// scanUserCommands lists dir's executables as commands, by the same test as
// userCommand, so the popup cannot offer what the dispatcher would refuse.
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

// commandCandidates offers commands for a "/" at the start of the composer, labeled with usage.
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
