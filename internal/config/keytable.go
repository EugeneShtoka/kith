package config

//go:generate go test -run TestDefaultTOMLKeysSectionIsGenerated . -update

// keyBinding is one configurable action: its path under [keys], its default, and what
// it does. doc is the help overlay's label and default.toml's comment; note is extra
// prose for default.toml only.
type keyBinding struct {
	path string // "timeline.reply"; a top-level action has no dot
	def  string
	doc  string
	note string
}

// keySection is one [keys.*] table as default.toml documents it.
type keySection struct {
	table string // "" is [keys] itself
	intro string // prose above the header
	outro string // prose after the bindings
	binds []keyBinding
}

// keySections is the one table of keybindings: DefaultKeys, the help overlay's labels
// and the [keys] part of default.toml are all derived from it.
var keySections = []keySection{
	{
		intro: `Keybindings. Each value is a comma-separated list ("k,up"); names are what the
terminal reports ("enter", "esc", "tab", "shift+tab", "ctrl+u", "pgup"); the comma
key is "comma", and the minus key "minus". A space separates the steps of a
sequence ("s u"); "gg" is also a sequence. ` + "`?`" + ` shows the live bindings.

Groups are per mode, and the more specific group wins ([keys.timeline] over
[keys.nav]). While typing, any key that produces text types it. Omit a key to keep
its default; "-" binds nothing.

By default esc cancels, the arrows move and pgup/pgdown page, and all of them can be
rebound. Moving, going back and each mode's cancel or close always keep a key: one
left with none gets its default back. A binding that collides within a mode is
reported in ` + "`?`" + ` and falls back to its default.`,
		binds: []keyBinding{
			{path: "quit", def: "q", doc: "quit", note: "Not while typing, where q types itself."},
			{path: "interrupt", def: "-", doc: "quit, from anywhere — typing, a prompt, a picker",
				note: "No key by default: most terminals keep ctrl+c for copying. Checked before every other key."},
			{path: "paste", def: "ctrl+v", doc: "paste the terminal's clipboard, from anywhere",
				note: "For terminals that pass the key through rather than pasting themselves."},
			{path: "help", def: "?", doc: "this help"},
			{path: "settings", def: "comma", doc: "settings, showing what each is set to"},
			{path: "command", def: ":", doc: "command line — global commands, as / is for this room"},
			{path: "why", def: "W", doc: "why is it quiet — every rule in force for this room"},
			{path: "focus_next", def: "tab", doc: "next pane"},
			{path: "focus_prev", def: "shift+tab", doc: "previous pane"},
			{path: "toggle_dnd", def: "ctrl+n", doc: "do not disturb on/off",
				note: "Works while typing too, as do the pane, redraw and jump keys."},
			{path: "toggle_mute", def: "alt+n", doc: "mute sound on/off"},
			{path: "redraw", def: "ctrl+l", doc: "redraw the screen"},
			{path: "jump_to", def: "ctrl+k", doc: "go to a room, person or space", note: "By typing its name."},
			{path: "jump_back", def: "ctrl+o", doc: "back to the room you jumped from",
				note: "ctrl+i equals tab except on terminals with the Kitty keyboard protocol, hence alt+o."},
			{path: "jump_forward", def: "ctrl+i,alt+o", doc: "forward again"},
		},
		outro: `Key sequences bound to places. Write them from the app with ` + "`B`" + ` on a room or rail
row. Targets:

  room:<!id:server>  a room, by ID (names are ambiguous across bridges)
  space:<space>      a rail group, by name

` + "`?`" + ` lists them resolved to names. A chord bound twice, or colliding with a command
(bound, a prefix of one, or prefixed by one), is refused.

  [[keys.jump]]
  chord  = "g w"
  target = "space:Work"

  [[keys.jump]]
  chord  = "g d"
  target = "room:!dana:example.org"`,
	},
	{
		table: "rail",
		intro: `The rail. The first move writes the whole order out, after which a newly joined
space appends at the end.`,
		binds: []keyBinding{
			{path: "rail.name", def: "a", doc: "name this group", note: "A local name; an empty one clears it."},
			{path: "rail.move_up", def: "K", doc: "move it up"},
			{path: "rail.move_down", def: "J", doc: "move it down"},
			{path: "rail.hide", def: "H", doc: "hide it from the rail"},
			{path: "rail.show_hidden", def: "S", doc: "bring a hidden group back"},
			{path: "rail.first_name_only", def: "F", doc: "first names only in this space"},
			{path: "rail.notify_rule", def: "b", doc: "notification rule for this space"},
			{path: "rail.mark_read", def: "m", doc: "mark every unread room in this group read", note: "Asks first."},
			{path: "rail.bind_jump", def: "B", doc: "give this space a key sequence to reach it by",
				note: "Writes a [[keys.jump]] entry."},
		},
	},
	{
		table: "nav",
		intro: "Cursor motion, shared by every pane.",
		binds: []keyBinding{
			{path: "nav.up", def: "k,up", doc: "up"},
			{path: "nav.down", def: "j,down", doc: "down"},
			{path: "nav.open", def: "l,right,enter", doc: "open / enter pane to the right"},
			{path: "nav.back", def: "h,left,esc", doc: "back / leave pane to the left"},
			{path: "nav.page_up", def: "pgup", doc: "scroll a screen back"},
			{path: "nav.page_down", def: "pgdown", doc: "scroll a screen forward"},
			{path: "nav.half_page_up", def: "ctrl+u", doc: "scroll half a screen back",
				note: "Outside the composer only; while typing ctrl+u clears the line."},
			{path: "nav.half_page_down", def: "ctrl+d", doc: "scroll half a screen forward"},
			{path: "nav.select_oldest", def: "gg", doc: "the top: oldest message, or the first row of a list — with a count, that row (12G)",
				note: "In the timeline this pulls more history in."},
			{path: "nav.select_newest", def: "G", doc: "the bottom: newest message, or the last row of a list — with a count, that row"},
			{path: "nav.scroll_oldest", def: "home", doc: "scroll to the start",
				note: "The ends of the conversation; while typing they move the view only."},
			{path: "nav.scroll_newest", def: "end", doc: "jump to the latest"},
		},
	},
	{
		table: "rooms",
		intro: `The room list. Consulted before [keys.nav]. accept/reject apply to invitations,
leave to joined rooms; leave and reject ask first.`,
		binds: []keyBinding{
			{path: "rooms.join", def: "J", doc: "join a room by ID or alias"},
			{path: "rooms.leave", def: "L", doc: "leave the selected room"},
			{path: "rooms.accept", def: "y", doc: "accept the selected invitation"},
			{path: "rooms.reject", def: "d", doc: "reject the selected invitation"},
			{path: "rooms.people", def: "p", doc: "list the people in this room"},
			{path: "rooms.name", def: "a", doc: "name this room", note: "A local name; an empty one clears it."},
			{path: "rooms.notify_rule", def: "b", doc: "notification rule for this room"},
			{path: "rooms.mark_read", def: "m", doc: "mark this room read without opening it"},
			{path: "rooms.bind_jump", def: "B", doc: "give this room a key sequence to reach it by",
				note: "Writes a [[keys.jump]] entry; an empty sequence at the prompt unbinds it."},
			{path: "rooms.mark_unread", def: "M", doc: "mark this room unread, or clear the mark",
				note: "MSC2867, synced to your other clients."},
			// ">" for forward: the replacement is the same conversation, later.
			{path: "rooms.go_replacement", def: ">", doc: "go to the room that replaced this one",
				note: `An upgraded room is marked "→".`},
			{path: "rooms.spaces", def: "S", doc: "file this room into a space or a tag, or take it out of one",
				note: "Tick with space, apply with enter."},
			{path: "rooms.new", def: "n", doc: "create a room or a space",
				note: "Filed into the selected space. Encryption is chosen here."},
			{path: "rooms.invite", def: "i", doc: "invite someone to this room", note: "Prompts for an @user:server."},
			{path: "rooms.unban", def: "U", doc: "lift a ban on this room"},
			{path: "rooms.view_media", def: "v", doc: "open this room's pictures in an image viewer, newest first"},
			{path: "rooms.direction", def: "D", doc: "read this room right to left, left to right, or as configured",
				note: "Cycles the room's own entry in [display.direction]; the timeline mirrors, names on the right."},
			// `P` beside `A`: the two are opposites — stop counting this, keep this in
			// front of me — and a capital for each, since both edit the config rather than
			// moving a cursor.
			// "!" for "report spam", the gesture every mail client has spelled that way for
			// twenty years — and the only punctuation in this scope, because unlike its
			// neighbors it is not a filing decision but a verdict.
			{path: "rooms.spam", def: "!", doc: "spam / not spam: move this conversation out of the way, or back",
				note: "See [spam]."},
			{path: "rooms.rename_thread", def: "alt+r", doc: "name the thread under the cursor",
				note: "An empty name clears it."},
			// The same chord as the timeline's, for the same list: which pane you asked
			// from decides which room, not what the list is.
			{path: "rooms.list_threads", def: "ctrl+t", doc: "list the threads in the room under the cursor"},
		},
	},
	{
		table: "sort",
		intro: `Room list sorting for the current group, this session only ([display.rooms] holds
the defaults). A bare ` + "`s`" + ` is deliberately unbound so these sequences work.`,
		binds: []keyBinding{
			{path: "sort.unread_first", def: "s u", doc: "unread rooms on top, on/off"},
			{path: "sort.mentions_first", def: "s m", doc: "rooms that name you on top, on/off"},
			{path: "sort.drafts_first", def: "s d", doc: "rooms holding a draft on top, on/off"},
			{path: "sort.recent", def: "s r", doc: "sort by newest"},
			{path: "sort.alphabetical", def: "s a", doc: "sort by name"},
		},
	},
	{
		table: "search",
		intro: `Message search over the local cache: instant, offline, newest first. Input is terms,
never a query language; a "quoted phrase" matches in order, and the last word is a
prefix. ` + "`room`" + ` scopes to where you pressed it (rail: everywhere; room list: that
group; timeline: the open room); ` + "`scope`" + ` cycles room → group → everywhere. Filters:
` + "`from:dana`, `since:7d`, `since:yesterday`, `since:2026-08-01`, `until:2026-08-31`" + `.`,
		binds: []keyBinding{
			{path: "search.room", def: "/", doc: "search this room", note: "The pane you press it in picks the scope."},
			{path: "search.all", def: "ctrl+f", doc: "search every room"},
			{path: "search.mentions", def: "@", doc: "every message that names you, newest first"},
			{path: "search.files", def: "gf", doc: "every message with a file, newest first",
				note: "Starting in this room."},
			{path: "search.jump", def: "enter", doc: "go to the selected message", note: "In its own room."},
			{path: "search.scope", def: "tab", doc: "widen or narrow: room → group → everywhere"},
			{path: "search.close", def: "esc", doc: "close the results"},
			// The same glyph as the timeline's, doing the same thing from the other side.
			{path: "search.star", def: "*", doc: "unstar the selected message (the starred list)"},
		},
	},
	{
		table: "timeline",
		intro: "The timeline's message cursor. Consulted before [keys.nav].",
		binds: []keyBinding{
			{path: "timeline.insert", def: "i", doc: "compose a message"},
			{path: "timeline.reply", def: "r", doc: "reply to the selected message"},
			{path: "timeline.go_reply", def: "enter", doc: "go to the message this answers"},
			{path: "timeline.react", def: "e", doc: "react to the selected message"},
			{path: "timeline.redact", def: "x", doc: "delete this message (asks first)", note: "For everyone."},
			{path: "timeline.edit", def: "E", doc: "edit this message of yours, in the composer",
				note: "esc restores your draft."},
			{path: "timeline.next_mention", def: "m", doc: "next mention of you",
				note: "The mention and attachment motions wrap."},
			{path: "timeline.prev_mention", def: "M", doc: "previous mention of you"},
			// f is the file, and vim's own f/F already search forward and backward, so the
			// shift-is-backwards reading is already in the muscle.
			{path: "timeline.next_attachment", def: "f", doc: "next message with a file"},
			{path: "timeline.prev_attachment", def: "F", doc: "previous message with a file"},
			{path: "timeline.prev_day", def: "[", doc: "back a day (start of this day first)"},
			{path: "timeline.next_day", def: "]", doc: "forward a day"},
			{path: "timeline.name", def: "a", doc: "name the sender of the selected message",
				note: "A local name; an empty one clears it."},
			// b for bell.
			{path: "timeline.notify_rule", def: "b", doc: "notification rule for this sender or room"},
			// vim's "open a fold", written spaced like z=.
			{path: "timeline.reveal", def: "z o", doc: "uncover what this message is hiding — a spoiler, or a kept deletion",
				note: "Press again to cover it."},
			{path: "timeline.copy_text", def: "yy", doc: "copy this message's text",
				note: "The copy and open keys ask which when a message holds several candidates."},
			{path: "timeline.copy_url", def: "yu", doc: "copy a link from it"},
			{path: "timeline.copy_code", def: "yc", doc: "copy the verification code in it"},
			// vim opens the link under the cursor with gx; so does this.
			{path: "timeline.open_url", def: "gx", doc: "open a link from it"},
			// The same key with shift: open it *and take me there*.
			{path: "timeline.open_url_focus", def: "gX", doc: "open a link from it and go to the browser",
				note: "See [clipboard] focus_command."},
			// `gl` — go to the link — and *not* vim's `gf`, which this client already binds
			// to the attachment list.
			{path: "timeline.follow_link", def: "gl", doc: "follow a Matrix link in it",
				note: "A matrix: or matrix.to address, mention pills included, in this client: opens a " +
					"room or message, asks before joining, or opens the switcher for a person."},
			// `gt` — go to the topic — beside `gl` and `gx`, the two other `g` bindings that
			// open something the message or the room points at.
			{path: "timeline.topic", def: "gt", doc: "read the room's topic"},
			// `*` is the glyph of the thing, and vim's `*` — search for the word under the
			// cursor — has no meaning in a timeline, so the letter is free.
			{path: "timeline.star", def: "*", doc: "star it, or unstar it",
				note: "Private, synced account data. `/starred` lists them."},
			// `H` for history, one press rather than a chord — the capitals in this pane
			// are already the bigger version of their lowercase key (f/F, s/S, m/M, e/E).
			{path: "timeline.history", def: "H", doc: "what this message used to say",
				note: "Every version and its deletion, if any (complete only with [display.deleted] keep)."},
			// Save and save-as, the pair they have always been on other keyboards.
			{path: "timeline.download", def: "s", doc: "save this message's attachment",
				note: "Sending a file is the composer's alt+u."},
			{path: "timeline.save_as", def: "S", doc: "save it somewhere else — opens a folder picker",
				note: "The desktop's chooser (XDG portal), falling back to the download directory."},
			{path: "timeline.view_media", def: "v", doc: "open this picture and the ones before it in an image viewer"},
			{path: "timeline.play", def: "p", doc: "play this voice note, or watch this video"},
			{path: "timeline.open_thread", def: "t", doc: "open the thread this message is in (back closes it)"},
			{path: "timeline.start_thread", def: "T", doc: "start a thread on this message"},
			{path: "timeline.list_threads", def: "ctrl+t", doc: "list this room's threads"},
			{path: "timeline.rename_thread", def: "alt+r", doc: "name this thread", note: "An empty name clears it."},
		},
	},
	{
		table: "insert",
		intro: `While composing. Unlisted keys type themselves; editing the text is [keys.edit].

` + "`send` and `newline`" + ` are a pair. For enter-sends, swap them:

    send    = "enter"
    newline = "ctrl+enter,alt+enter"

ctrl+enter differs from enter only on terminals with the Kitty keyboard protocol or
modifyOtherKeys; alt+enter works everywhere.`,
		binds: []keyBinding{
			{path: "insert.send", def: "ctrl+enter,alt+enter", doc: "send"},
			{path: "insert.newline", def: "enter", doc: "new line in the message"},
			{path: "insert.attach", def: "alt+u", doc: "attach a file — what you have typed becomes its caption",
				note: "Through the desktop's file chooser."},
			{path: "insert.cancel", def: "esc", doc: "back to the message cursor"},
			// right: forward, whichever arrow that is in the draft being written. tab: the
			// whole suggestion, or the chooser when there is nothing to take.
			{path: "insert.complete", def: "right", doc: "take a word of the completion being offered",
				note: "See [complete]."},
			{path: "insert.complete_all", def: "tab", doc: "take the whole completion, or choose from the list"},
			{path: "insert.model_preview", def: "alt+m", doc: "show what the model would be sent for this draft — and send nothing",
				note: "See [assist]."},
			{path: "insert.choose_1", def: "alt+1", doc: "take the first option on offer", note: "See [complete]."},
			{path: "insert.choose_2", def: "alt+2", doc: "take the second option on offer"},
			{path: "insert.choose_3", def: "alt+3", doc: "take the third option on offer"},
			{path: "insert.choose_4", def: "alt+4", doc: "take the fourth option on offer"},
			{path: "insert.choose_5", def: "alt+5", doc: "take the fifth option on offer"},
		},
	},
	{
		table: "edit",
		intro: `Editing text, in every field that takes typing: the composer, prompts and filters.
Checked after the field's own actions, so a binding there wins (ctrl+e browses emoji
in the composer). The aliases exist because terminals disagree: ctrl+backspace
arrives as ctrl+h from most (0x08), and alt+backspace is readline's spelling.`,
		binds: []keyBinding{
			{path: "edit.left", def: "left", doc: "one character left"},
			{path: "edit.right", def: "right", doc: "one character right"},
			{path: "edit.word_left", def: "ctrl+left,alt+left,alt+b", doc: "one word left"},
			{path: "edit.word_right", def: "ctrl+right,alt+right,alt+f", doc: "one word right"},
			{path: "edit.start", def: "home,ctrl+a", doc: "to the start",
				note: "In a field, not the timeline's top."},
			{path: "edit.end", def: "end,ctrl+e", doc: "to the end"},
			{path: "edit.delete_back", def: "backspace", doc: "delete the character before the caret"},
			{path: "edit.delete_forward", def: "delete", doc: "delete the character after it"},
			{path: "edit.delete_word_back", def: "ctrl+w,ctrl+backspace,ctrl+h,alt+backspace",
				doc: "delete the word before it"},
			{path: "edit.delete_word_forward", def: "ctrl+delete,alt+d", doc: "delete the word after it"},
			{path: "edit.delete_to_start", def: "ctrl+u", doc: "delete everything before it"},
			{path: "edit.delete_to_end", def: "ctrl+k", doc: "delete everything after it"},
			{path: "edit.undo", def: "ctrl+z", doc: "undo"},
			{path: "edit.redo", def: "ctrl+r", doc: "redo"},
			{path: "edit.row_up", def: "up", doc: "one row up (in the composer)"},
			{path: "edit.row_down", def: "down", doc: "one row down (in the composer)"},
		},
	},
	{
		table: "completion",
		intro: "The completion popup (`@` for mentions). Accepting a person inserts a real mention.",
		binds: []keyBinding{
			{path: "completion.next", def: "ctrl+n,down", doc: "next candidate"},
			{path: "completion.prev", def: "ctrl+p,up", doc: "previous candidate"},
			{path: "completion.accept", def: "tab,enter", doc: "insert the selected candidate"},
			{path: "completion.dismiss", def: "esc", doc: "close the popup"},
		},
	},
	{
		table: "spell",
		intro: "The correction walk through the draft's misspellings: `alt+s` while typing, `z=`\n" +
			"from the message cursor. 1-9 choose a suggestion. `add` saves the word to your\n" +
			"personal dictionary; `ignore` accepts it for this session. (Some layouts turn alt+s\n" +
			"into ß; bind e.g. alt+k instead.)",
		binds: []keyBinding{
			{path: "spell.open", def: "z =", doc: "correct the misspellings in the draft, one at a time",
				note: "From the message cursor; shown as z=."},
			{path: "spell.open_insert", def: "alt+s", doc: "correct the misspellings in what you are writing"},
			{path: "spell.skip", def: "s,space", doc: "leave this word, go to the next (space too)"},
			{path: "spell.add", def: "a", doc: "add it to your dictionary — kept after a restart"},
			{path: "spell.ignore", def: "i", doc: "accept it for this session"},
			{path: "spell.stop", def: "esc", doc: "stop, leaving the rest underlined"},
			{path: "spell.choose_1", def: "1", doc: "take the first suggestion and go to the next word"},
			{path: "spell.choose_2", def: "2", doc: "take the second suggestion and go to the next word"},
			{path: "spell.choose_3", def: "3", doc: "take the third suggestion and go to the next word"},
			{path: "spell.choose_4", def: "4", doc: "take the fourth suggestion and go to the next word"},
			{path: "spell.choose_5", def: "5", doc: "take the fifth suggestion and go to the next word"},
			{path: "spell.choose_6", def: "6", doc: "take the sixth suggestion and go to the next word"},
			{path: "spell.choose_7", def: "7", doc: "take the seventh suggestion and go to the next word"},
			{path: "spell.choose_8", def: "8", doc: "take the eighth suggestion and go to the next word"},
			{path: "spell.choose_9", def: "9", doc: "take the ninth suggestion and go to the next word"},
		},
	},
	{
		table: "emoji",
		intro: "The emoji browser. Typing filters; `:` in a message also completes shortcodes.",
		binds: []keyBinding{
			{path: "emoji.open", def: "ctrl+e", doc: "browse emoji to insert",
				note: "From the message cursor it reacts instead."},
			{path: "emoji.accept", def: "enter,tab", doc: "insert the selected emoji"},
			{path: "emoji.cancel", def: "esc", doc: "close the browser"},
		},
	},
	{
		table: "composer",
		intro: "Writing a message: see [composer].",
		binds: []keyBinding{
			{path: "composer.external_edit", def: "alt+e", doc: "write a message in $EDITOR"},
			{path: "composer.cut", def: "ctrl+x", doc: "cut the whole draft to the clipboard", note: "ctrl+z restores it."},
		},
	},
	{
		table: "picker",
		intro: `Chooser overlays (people, identities, colors). One with letter actions starts in
navigate mode; ` + "`filter`" + ` switches to typing.`,
		binds: []keyBinding{
			{path: "picker.filter", def: "i", doc: "start typing to narrow the list"},
			{path: "picker.name", def: "a", doc: "name the person under the cursor"},
			{path: "picker.toggle", def: "space", doc: "tick the row under the cursor (in a list of checkboxes)"},
			{path: "picker.increase", def: "+", doc: "raise the number under the cursor", note: "In settings."},
			{path: "picker.decrease", def: "minus", doc: "lower the number under the cursor", note: "In settings."},
			{path: "picker.kick", def: "r", doc: "remove the person under the cursor from this room", note: "Asks first."},
			{path: "picker.ban", def: "b", doc: "ban the person under the cursor from this room", note: "Asks first."},
			{path: "picker.rename", def: "alt+r", doc: "name the thread under the cursor", note: "In the thread list."},
			{path: "picker.accept", def: "enter,tab", doc: "choose the item under the cursor, or apply the ticked ones"},
			{path: "picker.close", def: "esc", doc: "stop typing, then close"},
		},
	},
	{
		table: "react",
		intro: "The react prompt. A pick key reacts with that palette position, while nothing is typed.",
		binds: []keyBinding{
			{path: "react.send", def: "enter", doc: "post the typed reaction"},
			{path: "react.cancel", def: "esc", doc: "close the prompt"},
			{path: "react.pick_1", def: "1", doc: "react with the palette's first emoji"},
			{path: "react.pick_2", def: "2", doc: "react with the palette's second emoji"},
			{path: "react.pick_3", def: "3", doc: "react with the palette's third emoji"},
			{path: "react.pick_4", def: "4", doc: "react with the palette's fourth emoji"},
			{path: "react.pick_5", def: "5", doc: "react with the palette's fifth emoji"},
			{path: "react.pick_6", def: "6", doc: "react with the palette's sixth emoji"},
			{path: "react.pick_7", def: "7", doc: "react with the palette's seventh emoji"},
			{path: "react.pick_8", def: "8", doc: "react with the palette's eighth emoji"},
			{path: "react.pick_9", def: "9", doc: "react with the palette's ninth emoji"},
			{path: "react.pick_10", def: "0", doc: "react with the palette's tenth emoji"},
		},
	},
	{
		table: "prompt",
		intro: "One-line prompts (joining by address).",
		binds: []keyBinding{
			{path: "prompt.submit", def: "enter", doc: "accept what you typed"},
			{path: "prompt.cancel", def: "esc", doc: "close the prompt"},
		},
	},
	{
		table: "confirm",
		intro: "Confirmations of destructive actions.",
		binds: []keyBinding{
			{path: "confirm.yes", def: "y", doc: "go ahead"},
			{path: "confirm.no", def: "n,esc", doc: "never mind"},
		},
	},
	{
		table: "verify",
		intro: "The device-verification overlay.",
		binds: []keyBinding{
			{path: "verify.confirm", def: "y", doc: "accept / confirm the emoji match"},
			{path: "verify.cancel", def: "n,esc", doc: "reject"},
		},
	},
	{
		table: "player",
		// The unshifted pair moves through the note, the shifted pair changes how fast it
		// goes.
		intro: `The voice-note player bar (see [display.media.audio]). While a note is loaded
these are consulted before the focused pane.`,
		binds: []keyBinding{
			{path: "player.play_pause", def: "space", doc: "play / pause (from the end, plays it again)"},
			{path: "player.back", def: "[", doc: "back by the skip step"},
			{path: "player.forward", def: "]", doc: "forward by the skip step"},
			{path: "player.slower", def: "{", doc: "slower"},
			{path: "player.faster", def: "}", doc: "faster"},
			{path: "player.normal_speed", def: "=", doc: "back to the speed set for this place"},
			{path: "player.save_speed", def: "w", doc: "remember this speed for a room, a space or a person"},
			{path: "player.stop", def: "P", doc: "stop and close the player"},
		},
	},
}
