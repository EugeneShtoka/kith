# Keybindings

kith is modal and keyboard-driven, in the spirit of Vim: the same key means different things in the rail, the room list, the message cursor and the composer. This page lists every default binding, explains how to change them, and collects the terminal quirks that decide which keys can reach the client at all.

The authoritative list is always the one in the app: press `?` for the help overlay. It is generated from the live keymap, so it reflects your own bindings, your `[[keys.jump]]` places and script keys, and any problems with your configuration.

## How the keymap is resolved

- **Scopes.** Each table below is one section of the config file (`[keys.timeline]`, `[keys.rooms]`, …). When you press a key, the most specific scope wins — `[keys.timeline]` before `[keys.nav]`, a completion popup before the global keys.
- **Typing wins.** While a text field has the keyboard (the composer, a prompt, a filter), any key that produces a character types it. Only keys marked *anywhere* below, and the scope of that field, are consulted. A letter bound to an action can never become untypeable.
- **Overlays own the keyboard.** The help overlay, a confirmation, a prompt, a chooser and device verification each capture keys until you close them.
- **Sequences.** `gg`, `yy`, `gx`, `s u` and `z =` are key sequences. The first key waits for the rest (the status line shows what is pending); a key that does not continue the sequence ends it and does what it does on its own. There is no timeout.
- **Counts.** Outside text fields, digits typed before a motion are a count: `5j`, `12G`.

## Default bindings

Several keys can be bound to one action; the first is the one the status-line legend shows.

### `[keys]`

Client-wide keys. The ones marked *anywhere* work while you are typing too, so they are all keys that produce no text. The rest are ignored while a text field has the keyboard.

| Action | Default | What it does | Where |
| --- | --- | --- | --- |
| `focus_next` | `tab` | Next pane | anywhere, typing included |
| `focus_prev` | `shift+tab` | Previous pane | anywhere, typing included |
| `toggle_dnd` | `ctrl+n` | Do not disturb on/off | anywhere, typing included |
| `toggle_mute` | `alt+n` | Mute sound on/off | anywhere, typing included |
| `redraw` | `ctrl+l` | Redraw the screen | anywhere, typing included |
| `jump_to` | `ctrl+k` | Go to a room, person or space | anywhere, typing included |
| `jump_back` | `ctrl+o` | Back to the room you were in before this one | anywhere, typing included |
| `jump_forward` | `ctrl+i`, `alt+o` | Forward again | anywhere, typing included |
| `help` | `?` | Help overlay, generated from the live keymap. `/` (your `search.room` key) searches it | not while typing |
| `interrupt` | none | Quit, from anywhere: typing, a prompt, a picker. Checked before every other key. No key by default, as most terminals keep `ctrl+c` for copying; set one (`interrupt = "ctrl+c"`) to have it | anywhere, typing included |
| `paste` | `ctrl+v` | Paste the terminal's clipboard, for terminals that pass the key through (most paste on their own) | anywhere, typing included |
| `quit` | `q` | Quit | not while typing |
| `settings` | `,` (written `comma`) | Settings screen, each row showing its current value | not while typing |
| `command` | `:` | Command line — client-wide commands (`/` commands act on the open room) | not while typing |
| `why` | `W` | Explain why the open room is quiet: every notification rule in force | not while typing |

### `[keys.rail]`

The spaces rail, the left pane. Reordering, hiding and naming are written back to `[display.rail]` and `[[display.name]]` for you — see [Usage](usage.md#the-spaces-rail).

| Action | Default | What it does |
| --- | --- | --- |
| `name` | `a` | Name this group |
| `move_up` | `K` | Move it up |
| `move_down` | `J` | Move it down |
| `hide` | `H` | Hide it from the rail |
| `show_hidden` | `S` | Bring a hidden group back |
| `first_name_only` | `F` | First names only in this space |
| `notify_rule` | `b` | Notification rule for this space |
| `bind_jump` | `B` | Give this space a key sequence to reach it by |
| `mark_read` | `m` | Mark every unread room in this group read |

### `[keys.nav]`

Cursor motion, shared by every pane. A pane ignores what it has no use for. Any motion takes a count typed before it: `12j` moves twelve messages, `3G` goes to the third row (see `[display] row_numbers`).

| Action | Default | What it does |
| --- | --- | --- |
| `up` | `k`, `up` | Up |
| `down` | `j`, `down` | Down |
| `open` | `l`, `right`, `enter` | Open / enter pane to the right |
| `back` | `h`, `left`, `esc` | Back / leave pane to the left |
| `page_up` | `pgup` | Scroll a screen back |
| `page_down` | `pgdown` | Scroll a screen forward |
| `half_page_up` | `ctrl+u` | Scroll half a screen back |
| `half_page_down` | `ctrl+d` | Scroll half a screen forward |
| `select_oldest` | `gg` | Top: oldest loaded message, or first row of a list; with a count, that row (`12gg`) |
| `select_newest` | `G` | Bottom: newest message, or last row of a list; with a count, that row (`12G`) |
| `scroll_oldest` | `home` | Scroll to the start of the conversation, loading history |
| `scroll_newest` | `end` | Jump to the latest message |

### `[keys.rooms]`

The room list, the middle pane. Consulted before `[keys.nav]`. `accept` and `reject` act only on an invitation and `leave` only on a joined room; `leave` and `reject` ask first.

| Action | Default | What it does |
| --- | --- | --- |
| `accept` | `y` | Accept the selected invitation |
| `reject` | `d` | Reject the selected invitation |
| `people` | `p` | List the people in this room |
| `view_media` | `v` | Open this room's pictures in the image viewer, newest first |
| `rename_thread` | `alt+r` | Name the thread under the cursor |
| `list_threads` | `ctrl+t` | List the threads in the room under the cursor |
| `name` | `a` | Name this room |
| `notify_rule` | `b` | Notification rule for this room |
| `join` | `J` | Join a room by ID or alias |
| `leave` | `L` | Leave the selected room |
| `bind_jump` | `B` | Give this room a key sequence to reach it by |
| `mark_read` | `m` | Mark this room read without opening it |
| `mark_unread` | `M` | Mark this room unread, or clear the mark |
| `invite` | `i` | Invite someone to this room |
| `unban` | `U` | Lift a ban on this room |
| `new` | `n` | Create a room or a space |
| `spaces` | `S` | File this room into a space, or take it out of one |
| `archive` | `A` | Archive / un-archive: keep the room, stop it counting |
| `pin` | `P` | Pin / unpin: follow this conversation, and let it through a silence |
| `spam` | `!` | Spam / not spam: move this conversation out of the way, or back |
| `go_replacement` | `>` | Go to the room that replaced this one |

### `[keys.sort]`

The room list's order for the rail group you are looking at, for this session. They are sequences — `s`, then a letter — and live in the room list only, so `s` still saves an attachment in the timeline. The persistent order is `[display.rooms] sort`; see [Usage](usage.md#sorting-the-room-list).

| Action | Default | What it does |
| --- | --- | --- |
| `unread_first` | `s u` | Unread rooms on top, on/off |
| `mentions_first` | `s m` | Rooms that name you on top, on/off |
| `drafts_first` | `s d` | Rooms holding a draft on top, on/off |
| `recent` | `s r` | Sort by newest |
| `alphabetical` | `s a` | Sort by name |

### `[keys.search]`

Opening search and the lists that ride the results pane, then driving the results. See [Search](search.md).

| Action | Default | What it does | Where |
| --- | --- | --- | --- |
| `room` | `/` | Search this room | not while typing |
| `all` | `ctrl+f` | Search every room | not while typing |
| `mentions` | `@` | Every message that names you, newest first | not while typing |
| `files` | `gf` | Every message with a file, newest first | not while typing |
| `jump` | `enter` | Go to the selected message | search results |
| `close` | `esc` | Close the results | search results |
| `scope` | `tab` | Widen or narrow: room → group → everywhere | search results, search prompt |
| `star` | `*` | Unstar the selected message (the starred list) | search results |

### `[keys.timeline]`

The message cursor — the timeline when you are not typing. Consulted before `[keys.nav]`, which is why `enter` here goes to the message a reply answers instead of opening something.

| Action | Default | What it does |
| --- | --- | --- |
| `insert` | `i` | Compose a message |
| `rename_thread` | `alt+r` | Name the thread you are looking at |
| `name` | `a` | Name the sender of the selected message (or the open thread, with no message selected) |
| `notify_rule` | `b` | Notification rule for this sender or room |
| `copy_text` | `yy` | Copy this message's text |
| `copy_url` | `yu` | Copy a link from it |
| `open_url` | `gx` | Open a link from it |
| `open_url_focus` | `gX` | Open a link from it and go to the browser |
| `follow_link` | `gl` | Follow a Matrix link in it |
| `topic` | `gt` | Read the room's topic |
| `star` | `*` | Star it, or unstar it |
| `history` | `H` | What this message used to say |
| `copy_code` | `yc` | Copy the verification code in it |
| `download` | `s` | Save this message's attachment |
| `save_as` | `S` | Save it somewhere else — opens a folder picker |
| `view_media` | `v` | Open what the message carries outside the client: a picture in the image viewer (with the room's other pictures), a video in a player, any other file with the desktop's handler |
| `play` | `p` | Play a voice note in the player bar (again to pause), or open a video |
| `reply` | `r` | Reply to the selected message |
| `go_reply` | `enter` | Go to the message this answers |
| `redact` | `x` | Delete this message (asks first) |
| `edit` | `E` | Edit this message of yours, in the composer |
| `react` | `e` | React to the selected message |
| `open_thread` | `t` | Open the thread this message is in (back closes it) |
| `start_thread` | `T` | Start a thread on this message |
| `list_threads` | `ctrl+t` | List this room's threads |
| `next_mention` | `m` | Next mention of you |
| `prev_mention` | `M` | Previous mention of you |
| `next_attachment` | `f` | Next message with a file |
| `prev_attachment` | `F` | Previous message with a file |
| `prev_day` | `[` | Back a day (start of this day first) |
| `next_day` | `]` | Forward a day |
| `reveal` | `z o` | Uncover what this message is hiding — a spoiler, or a kept deletion |

### `[keys.insert]`

While composing. Anything not listed types itself. `send` and `newline` are a pair: to make `enter` send, swap them (the settings screen's *Enter key* row does both at once). See [Composer](composer.md).

| Action | Default | What it does |
| --- | --- | --- |
| `send` | `ctrl+enter`, `alt+enter` | Send |
| `newline` | `enter` | New line in the message |
| `attach` | `alt+u` | Attach a file — what you have typed becomes its caption |
| `complete` | `right` | Take a word of the completion being offered |
| `complete_all` | `tab` | Take the whole completion, or choose from the list |
| `model_preview` | `alt+m` | Show what the model would be sent for this draft — and send nothing |
| `cancel` | `esc` | Back to the message cursor |

### `[keys.completion]`

While a completion popup is open — `@` people, `:` emoji shortcodes, `#` rooms, `/` commands, and `from:` in a search. These shadow the always-on keys only while the popup is up, which is how `ctrl+n` is "next" here and do-not-disturb everywhere else.

| Action | Default | What it does |
| --- | --- | --- |
| `next` | `ctrl+n`, `down` | Next candidate |
| `prev` | `ctrl+p`, `up` | Previous candidate |
| `accept` | `tab`, `enter` | Insert the selected candidate |
| `dismiss` | `esc` | Close the popup |

### `[keys.spell]`

The correction walk over the misspellings in your draft. Inside it, `1`–`9` choose a suggestion (`spell.choose_1` … `spell.choose_9`).

| Action | Default | What it does | Where |
| --- | --- | --- | --- |
| `open` | `z =` | Correct the misspellings in the draft, one at a time | message cursor |
| `skip` | `s`, `space` | Leave this word, go to the next | correction walk |
| `add` | `a` | Add it to your dictionary — kept after a restart | correction walk |
| `ignore` | `i` | Accept it for this session | correction walk |
| `stop` | `esc` | Stop, leaving the rest underlined | correction walk |
| `open_insert` | `alt+s` | Correct the misspellings in what you are writing | composer |

### `[keys.emoji]`

The emoji browser, a filterable grid. Typing filters it, so the arrows and `pgup`/`pgdown` move.

| Action | Default | What it does | Where |
| --- | --- | --- | --- |
| `open` | `ctrl+e` | Emoji browser: react with any emoji from the message cursor, or insert one while composing | message cursor, composer |
| `accept` | `enter`, `tab` | Insert the selected emoji | emoji browser |
| `cancel` | `esc` | Close the browser | emoji browser |

### `[keys.composer]`

Helpers for writing a message.

| Action | Default | What it does | Where |
| --- | --- | --- | --- |
| `external_edit` | `alt+e` | Write, or finish, the message in `$EDITOR` | message cursor, composer |
| `cut` | `ctrl+x` | Cut the whole draft to the clipboard | composer |

### `[keys.picker]`

Any chooser overlay — the people list, identities, colors, links, spaces, threads. A chooser with letter actions (the people list) starts in *navigate* mode, where letters are actions and `filter` switches to typing; one without them (the switcher, the thread list) filters from the first keystroke.

| Action | Default | What it does |
| --- | --- | --- |
| `filter` | `i` | Start typing to narrow the list |
| `name` | `a` | Name the person under the cursor |
| `kick` | `r` | Remove the person under the cursor from this room |
| `ban` | `b` | Ban the person under the cursor from this room |
| `rename` | `alt+r` | Name the thread under the cursor |
| `toggle` | `space` | Tick the row under the cursor (in a list of checkboxes) |
| `accept` | `enter`, `tab` | Choose the item under the cursor, or apply the ticked ones |
| `close` | `esc` | Stop typing, then close |

### `[keys.react]`

The reaction prompt. With nothing typed yet, `1`–`9` and `0` send the quick palette's emoji at once (`react.pick_1` … `react.pick_10`). Otherwise type an emoji or a `:shortcode:`.

| Action | Default | What it does |
| --- | --- | --- |
| `send` | `enter` | Post the typed reaction |
| `cancel` | `esc` | Close the prompt |

### `[keys.player]`

The voice-note player bar. Live only while a note is loaded, and consulted *ahead of* the pane you are in, so these keys win while something is playing and no longer. The defaults are keys no pane binds.

| Action | Default | What it does |
| --- | --- | --- |
| `play_pause` | `space` | Play / pause (from the end, plays it again) |
| `back` | `[` | Back by the skip step |
| `forward` | `]` | Forward by the skip step |
| `slower` | `{` | Slower |
| `faster` | `}` | Faster |
| `normal_speed` | `=` | Back to the speed set for this place |
| `save_speed` | `w` | Remember this speed for a room, a space or a person |
| `stop` | `P` | Stop and close the player |

### `[keys.prompt]`

A one-line prompt: join by address, invite, a name, the search box. Anything not listed types itself.

| Action | Default | What it does |
| --- | --- | --- |
| `submit` | `enter` | Accept what you typed |
| `cancel` | `esc` | Close the prompt |

### `[keys.confirm]`

A pending question before something destructive (leave, reject, delete, kick, ban). Nothing else reaches the client until you answer.

| Action | Default | What it does |
| --- | --- | --- |
| `yes` | `y` | Go ahead |
| `no` | `n`, `esc` | Never mind |

### `[keys.verify]`

The device-verification overlay. See [Encryption](encryption.md).

| Action | Default | What it does |
| --- | --- | --- |
| `confirm` | `y` | Accept / confirm the emoji match |
| `cancel` | `n`, `esc` | Reject |

## `[keys.edit]` — editing text

These work in every text field: the composer, prompts and choosers' filters.

| Action | Default | What it does |
| --- | --- | --- |
| `left` / `right` | `left` / `right` | One character left / right |
| `word_left` | `ctrl+left`, `alt+left`, `alt+b` | One word left |
| `word_right` | `ctrl+right`, `alt+right`, `alt+f` | One word right |
| `start` | `home`, `ctrl+a` | To the start |
| `end` | `end`, `ctrl+e` | To the end |
| `delete_back` | `backspace` | Delete the character before the caret |
| `delete_forward` | `delete` | Delete the character after it |
| `delete_word_back` | `ctrl+w`, `ctrl+backspace`, `ctrl+h`, `alt+backspace` | Delete the word before it |
| `delete_word_forward` | `ctrl+delete`, `alt+d` | Delete the word after it |
| `delete_to_start` | `ctrl+u` | Delete everything before it |
| `delete_to_end` | `ctrl+k` | Delete everything after it |
| `undo` / `redo` | `ctrl+z` / `ctrl+r` | Undo / redo, in whole edits |
| `row_up` / `row_down` | `up` / `down` | One row up or down (in the composer) |

The aliases exist because terminals disagree: `ctrl+backspace` arrives as `ctrl+h` from most of them, and `alt+backspace` is readline's spelling.

A field's own binding wins over an editing key. In the composer that means `ctrl+e` opens the emoji browser and `ctrl+k` opens the switcher, because `[keys.emoji] open` and `[keys] jump_to` claim them. Use `end` to reach the end of the line, or rebind one of them. In prompts and choosers' filters both keep their editing meaning. `home` and `end` are the ends of the line in a field, not of the timeline.

## Counts

Outside text fields, digits typed before a motion are a count for it (`5j` moves five rows). That is the one use of keys the keymap does not name.

No key is fixed: the arrow keys, `esc`, `pgup`/`pgdown`, the palette digits and `alt+1`…`alt+5` are defaults like any other, and you can give them to something else. A few actions **must keep a key**, so no keymap can lock you out: moving (`nav.up`, `nav.down`, `nav.open`, `nav.back`, `nav.page_up`, `nav.page_down`) and each mode's cancel or close (`insert.cancel`, `prompt.cancel`, `confirm.no`, `verify.cancel`, `react.cancel`, `search.close`, `completion.dismiss`, `emoji.cancel`, `picker.close`). If your config leaves one of them with no key, it gets its default back, taking it from another action if it has to (that action then falls back to its own default), and the `?` overlay says so.

## Rebinding

Bindings live in the config file (`~/.config/kith/config.toml`), under the section named in each table. Only write what you change; everything omitted keeps its default. See [Configuration](configuration.md) for the file itself.

```toml
[keys]
quit = "-"                     # no key at all; bind interrupt to quit from anywhere

[keys.insert]
send    = "enter"              # enter sends…
newline = "ctrl+enter,alt+enter"  # …and these start a new line

[keys.timeline]
next_attachment = "f,alt+j"    # two keys for one action

[[keys.jump]]
chord  = "g w"
target = "space:Work"
```

### Key names

- A value is a **comma-separated list**: `"k,up"` binds both.
- A single character is written as itself: `"q"`, `"?"`, `"["`, `"*"`. Capitals are written as the capital letter (`"G"`, `"K"`), not as `shift+g`.
- Named keys: `enter`, `esc`, `tab`, `space`, `backspace`, `delete`, `insert`, `up`, `down`, `left`, `right`, `home`, `end`, `pgup`, `pgdown`, and `f1` to `f20`.
- Modifiers prefix a key, in any combination: `ctrl+`, `alt+`, `shift+`, `meta+`, `super+`, `hyper+` — for example `ctrl+enter`, `alt+shift+j`, `shift+tab`.
- The comma key is written `comma`, because `,` separates the list.
- A name the terminal can never report (`escape`, `ctrl+`) is reported and ignored rather than silently dead.

### Sequences

Write the steps separated by spaces: `"z ="`, `"s u"`, `"ctrl+w h"`. Two or three plain letters or digits may be run together, the way Vim spells them — `"gg"` and `"g g"` are the same binding. Anything else keeps its spaces.

A sequence cannot fire if a shorter prefix of it is already a complete binding in the same place — `"q x"` while `q` quits — and the keymap reports that rather than leaving you to discover it.

### Unbinding and defaults

- `"-"` leaves an action with no key. An empty string, or leaving the key out, means "use the default".
- If every key you give an action is unusable (taken or misspelled), it falls back to its built-in default. Only if that is taken too does the action end up unbound, and the help overlay shows it as `(unbound)`.

### Conflicts and validation

A bad keymap never stops kith from starting. Each problem — a key claimed twice in one mode, an action that must keep a key left without one, an unknown key name, a sequence that can never fire — is collected and listed at the bottom of the `?` overlay under **Keybinding config issues**, naming the binding that needs changing.

The same key may mean different things in different scopes — `a` names a group in the rail, a room in the room list and a sender in the timeline — and a more specific scope simply wins over a general one: `enter` follows a reply in the timeline and opens things everywhere else. A clash is two actions wanting one key in the *same* scope; the action listed first keeps it and the other is reported.

### Places and commands

Two kinds of binding carry a payload instead of naming an action.

- **`[[keys.jump]]`** binds a sequence to a place: `target = "room:!abcdef:example.org"` or `target = "space:Work"` (a rail group by name — a space, or All, DMs, Unread). You rarely write these by hand: press `B` on a room in the room list or on a group in the rail and type a sequence; the room is recorded by its ID. `?` lists every jump resolved to the name of the place it leads to. See [Usage](usage.md#jumping-around).
- **`keys` on a `[[commands.script]]`** runs one of your own commands. See [Commands](commands.md).

Both are resolved only where keys are commands (never while typing), and both are **refused** when they collide with a built-in binding — already bound, the start of a longer binding, or blocked by a shorter one. To give a built-in key to a jump or a script, unbind the action with `"-"` first. A chord bound twice in `[[keys.jump]]` is refused at startup.

## Terminal notes

What a terminal sends decides what the client can tell apart.

- **Kitty keyboard protocol.** Several defaults are only distinguishable on a terminal that speaks it (or xterm's modifyOtherKeys): kitty, foot, ghostty, recent xterm, and WezTerm with `enable_kitty_keyboard = true`. Elsewhere `ctrl+enter` arrives as plain `enter` and `ctrl+i` as `tab`. That is why `send` defaults to `ctrl+enter,alt+enter` — `alt+enter` reaches the client on every terminal — and why `jump_forward` has `alt+o` beside `ctrl+i`.
- **Keys that are other keys.** `ctrl+m` and `ctrl+j` are `enter`; `ctrl+i` is `tab` without the protocol; `ctrl+backspace` is usually sent as `ctrl+h` (both delete a word). Don't bind these expecting a separate key.
- **Keys the terminal keeps.** Many terminals handle paste (`ctrl+v` or `ctrl+shift+v`) themselves and send the text as a paste, which the client accepts wherever you can type.
- **Alt and esc.** A legacy terminal sends `alt+x` as `esc` followed by `x`; the client reads the pair as one key.
- **AltGr layouts.** On layouts where right Alt is AltGr, a combination like `alt+s` can produce a character (`ß`) before the client sees a key. Rebind to a letter you reach with left Alt.
- **Shifted function keys.** Some terminals report Shift+F1–F12 as F13–F24. `f13` to `f20` are accepted names, so bind the number your terminal sends; `f21` and above cannot be bound.
- **Mouse.** With `[display] mouse = true` (the default) the wheel scrolls the pane under the pointer and clicks do nothing. While the client receives mouse events, hold `shift` to select text with the terminal. Set `mouse = false` to give selection back.
- **Redraw.** If the terminal ever leaves stray characters behind, `ctrl+l` clears and redraws the screen.
