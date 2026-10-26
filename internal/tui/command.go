package tui

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The ":" command line acts on the client; "/" commands in the composer act on the
// composer's room. In the composer ":" starts an emoji shortcode instead.

// command is one entry on the command line.
type command struct {
	name    string // bare, without the colon
	arg     string // names the argument for the usage line; "" takes none
	summary string // one line, for completion and help
	// argOptional: `:search` with nothing opens the box, `:join` with nothing is a
	// usage error.
	argOptional bool
	// keyScope and keyAct name the binding this command mirrors, resolved through the live
	// keymap so a rebound key shows correctly. Zero when none.
	keyScope scope
	keyAct   action
	run      func(m Model, arg string) (Model, tea.Cmd)
}

// noArg adapts a Model method that takes no argument to command.run.
func noArg(f func(Model) (Model, tea.Cmd)) func(Model, string) (Model, tea.Cmd) {
	return func(m Model, _ string) (Model, tea.Cmd) { return f(m) }
}

// commands is the table the command line, its completion and the help overlay read.
// Scoped commands start from the pane the colon was typed in (defaultSearchScope);
// :mentions is the exception, being about you.
var commands = []command{
	{
		name: "files", keyScope: scopeCommand, keyAct: actFiles,
		summary: "every message with a file, across every room — {search.jump} goes to it",
		run:     func(m Model, _ string) (Model, tea.Cmd) { return m.openFiles(false) },
	},
	{
		name: "tracked", arg: "[word]", argOptional: true,
		summary: "every message with one of your tracked words — grouped by the word",
		run:     Model.openTracked,
	},
	{
		name: "profile", arg: "[name]", argOptional: true, keyScope: scopeCommand, keyAct: actProfile,
		summary: "switch this window to another profile — its rooms, unread and search",
		run: func(m Model, arg string) (Model, tea.Cmd) {
			if strings.TrimSpace(arg) == "" {
				return m.openProfiles()
			}
			return m.switchProfile(arg)
		},
	},
	{
		name:    "caught",
		summary: "what the filters catch — the preview, before you trust them",
		run:     noArg(Model.openCaught),
	},
	{
		name: "rescan", arg: "[since]", argOptional: true,
		summary: "rooms the filters would move into their tags, judging what is cached (30d, 12h; all when empty) — ticked ones move",
		run:     Model.openRescan,
	},
	{
		name: "mentions", keyScope: scopeCommand, keyAct: actMentions,
		summary: "every message that names you — everywhere, newest first",
		run:     noArg(Model.openMentions),
	},
	{
		name: "search", keyScope: scopeCommand, keyAct: actSearchRoom,
		summary: "search — the pane decides how wide it starts",
		run:     func(m Model, _ string) (Model, tea.Cmd) { return m.openSearchFor(false, "") },
	},
	{
		name: "threads", keyScope: scopeTimeline, keyAct: actListThreads,
		summary: "the conversations in scope — the pane decides how wide, newest first",
		run:     func(m Model, _ string) (Model, tea.Cmd) { return m.openThreads(true) },
	},
	{
		name:    "starred",
		summary: "every message you starred — {search.scope} narrows it, and {search.jump} goes to one",
		run:     noArg(Model.openStarred),
	},
	{
		name: "go", keyScope: scopeGlobal, keyAct: actJumpTo,
		summary: "go to a room, person or space by typing its name",
		run:     noArg(Model.openJump),
	},
	{
		// Here, not in the composer: it acts on a different room.
		name: "join", arg: "<#room:server or !id>",
		summary: "join a room by address, and open it",
		run:     Model.submitJoin,
	},
	{
		name: "help", keyScope: scopeCommand, keyAct: actHelp,
		summary: "every key, and what it does — the same overlay `?` opens",
		run: func(m Model, _ string) (Model, tea.Cmd) {
			m.reader = m.reader.opening(readerHelp)
			return m, nil
		},
	},
	{
		name: "shortcut", arg: "[keys]", argOptional: true,
		summary: "a key sequence that goes to what you are on (\"g w\") — the rail's space or tag, or the room; none asks, and {search.scope} there switches between the two",
		run:     func(m Model, arg string) (Model, tea.Cmd) { return m.bindShortcut(arg, nil) },
	},
	{
		name: "login", arg: "[network]", argOptional: true,
		summary: "set up an account on a network and sign it in; none offers the networks, and one set up already signs in again",
		run:     Model.openLogin,
	},
	{
		name: "import", summary: "copy an account's folders (Telegram's) into tags, once, choosing per tag what changes",
		run: Model.openImport,
	},
	{
		name: "from", arg: "[person]", argOptional: true, keyScope: scopeTimeline, keyAct: actSaid,
		summary: "everything someone said, in every room you share, by every ID kith links to them — none is the person in view",
		run:     Model.saidOf,
	},
	{
		name:    "person",
		summary: "the phonebook card of the person in view: the selected message's sender, else the room's one person",
		run:     noArg(Model.personInView),
	},
	{
		name: "people", arg: "[terms]", argOptional: true,
		summary: "every card in the phonebook — a name or key:value (role:attorney) narrows it, and choosing one opens it",
		run:     Model.openPhonebook,
	},
	{
		name: "archived", keyScope: scopeRooms, keyAct: actToggleArchived,
		summary: "show or hide the archived rooms of the place you are in, until kith restarts",
		run:     noArg(Model.toggleArchived),
	},
	{
		name: "new", summary: "create a room, a space, a tag, or a group, forum, channel or community on another network",
		run: noArg(Model.openNewRoom),
	},
	{
		name: "settings", keyScope: scopeCommand, keyAct: actSettings,
		summary: "the settings screen — the same one the key opens",
		run:     noArg(Model.openSettings),
	},
	{
		name: "dnd", keyScope: scopeGlobal, keyAct: actToggleDND,
		summary: "silence, for a while or for a place — opens the same chooser the key does",
		run:     noArg(Model.toggleDND),
	},
	{
		name:    "verify",
		summary: "ask your other sessions to verify this one — compare the emoji there",
		run: func(m Model, _ string) (Model, tea.Cmd) {
			if m.verify.active {
				return m.say("a verification is already going — finish or cancel it first"), nil
			}
			return m.pickAccount(accountThen{kind: thenVerify, network: domain.ProtocolMatrix})
		},
	},
	{
		name:    "todo",
		summary: "what people are waiting on you for, across every room with something unread",
		run:     noArg(Model.openTodo),
	},
	{
		name:    "scheduled",
		summary: "what is queued to send later, every room — choose one to cancel",
		run:     func(m Model, _ string) (Model, tea.Cmd) { return m, m.scheduledCmd("") },
	},
}

// commandTrigger is empty: the colon is the prompt's label, not text in the field.
const commandTrigger = ""

func (m Model) openCommandLine() (Model, tea.Cmd) {
	m = m.openPrompt(promptCommand)
	return m.openCompletion(targetPrompt, commandTrigger, 0)
}

// runCommandLine dispatches a command line; an unknown name is answered with the list.
func (m Model) runCommandLine(input string) (Model, tea.Cmd) {
	name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(input), ":"))
	if name == "" {
		return m, nil // an empty line is a canceled thought, not an error
	}
	name, arg, _ := strings.Cut(name, " ")
	arg = strings.TrimSpace(arg)
	for _, cmd := range commands {
		if !strings.EqualFold(cmd.name, name) {
			continue
		}
		if cmd.arg != "" && !cmd.argOptional && arg == "" {
			return m.say(":" + cmd.name + " " + cmd.arg), nil
		}
		return cmd.run(m, arg)
	}
	return m.say(fmt.Sprintf("no command %q — try %s", name, commandNames())), nil
}

// commandNames lists what the command line accepts, for the error that says so.
func commandNames() string {
	names := make([]string, 0, len(commands))
	for _, cmd := range commands {
		names = append(names, ":"+cmd.name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func (m Model) commandLineCandidates(query string) []candidate {
	q := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(query, ":")))
	rows := make([]candidate, 0, len(commands))
	for _, cmd := range commands {
		if !strings.HasPrefix(cmd.name, q) {
			continue
		}
		label := ":" + cmd.name
		if cmd.arg != "" {
			label += " " + cmd.arg
		}
		rows = append(rows, candidate{
			text: cmd.name, label: label,
			detail: m.commandDetail(cmd.summary, cmd.keyScope, cmd.keyAct),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].text < rows[j].text })
	return rows
}

// liveKeys replaces each {path} ([keys] binding, e.g. "search.scope") with its current
// key, so prose never names a key the config moved.
func (m Model) liveKeys(text string) string {
	return liveKeyToken.ReplaceAllStringFunc(text, func(token string) string {
		name := token[1 : len(token)-1]
		i := slices.IndexFunc(keyActions, func(r keyAction) bool { return r.name == name })
		if i < 0 {
			return token
		}
		if key := m.keys.keyHint(keyActions[i].scope, keyActions[i].act); key != "" {
			return key
		}
		return "(unbound)"
	})
}

var liveKeyToken = regexp.MustCompile(`\{[a-z_]+(\.[a-z_0-9]+)?\}`)

// commandDetail is a command's one line plus its live keyboard shortcut, if bound.
func (m Model) commandDetail(summary string, at scope, act action) string {
	summary = m.liveKeys(summary)
	if act == actNone {
		return summary
	}
	key := m.keys.keyHint(at, act)
	if key == "" {
		return summary
	}
	return summary + "  ·  " + key
}
