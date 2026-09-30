# Using kith

A guided tour of the interface and the things you do every day: finding a conversation, reading it, answering, and keeping your rooms in order. It assumes you are logged in and the client is running — see [Getting started](getting-started.md) if not.

Every key below is a default. The full list is in [Keybindings](keybindings.md), and `?` in the app shows the bindings you actually have.

## The screen

Roughly:

```text
┌ rail ──────┬ room list ─────────┬ timeline ─────────────────────────────┐
│ Invites (1)│ ▸ Team Standup   3 │ Team Standup · Today                  │
│ All        │   Alice ✎          │ ── Today ──────────────────────────── │
│ DMs        │   Release notes    │ 09:14 Alice   Morning! ★              │
│ Unread     │                    │ 09:15 Bob     Deploy is out (edited)  │
│ Work       │                    │         💬 3 replies · Carol          │
│ Friends    │                    │ ─────────────────────── Carol typing… │
│ Pinned     │                    │ › reply here_                         │
└────────────┴────────────────────┴───────────────────────────────────────┘
 status line and key legend
```

Three panes, left to right:

1. **The spaces rail** — your spaces and a few built-in groups. The group under the cursor decides what the room list shows.
2. **The room list** — the rooms in that group, with unread badges, and threads with unread replies listed beneath their room.
3. **The timeline and composer** — the open room's messages, a message cursor to act on them, and the composer at the bottom.

`tab` and `shift+tab` move between panes (they stop at the ends rather than wrapping). Inside the panes, `l`/`enter`/`right` goes right and `h`/`esc`/`left` goes back. `j`/`k` move, `gg`/`G` go to the top and bottom, and `ctrl+d`/`ctrl+u` scroll half a screen. A number before a motion repeats it — `5j`, and `12G` is the twelfth row; turn on `[display] row_numbers` for relative row numbers to count by.

The status line at the bottom shows what just happened and a short legend of the keys that apply where you are. A key sequence in progress (`g`, `s`, `y`, `z`) is shown there until you finish it.

## Moving between modes

A room opens **ready to type**: `enter` on a room puts you in the composer. `esc` leaves the composer for the **message cursor**, where single letters act on the selected message; `i` goes back to typing. `esc` again returns to the room list. To land on the message cursor instead, set `[display] open_in_insert_mode = false` (also on the settings screen).

While you are typing, letters are text. Only keys that cannot type anything — such as `ctrl+k`, `ctrl+o`, `ctrl+n` and `ctrl+d` — still act.

## The spaces rail

The rail lists every Matrix space you have joined, plus built-in groups:

| Group | Holds | When it appears |
| --- | --- | --- |
| Invites (N) | Rooms you are invited to | While there is an invitation; shown first |
| All | Every room except archived ones | Always |
| DMs | Direct messages | Always |
| Unread | Rooms with something unread | Always |
| *your spaces* | The rooms in each space | Always |
| Drafts | Rooms where you left an unsent message | While there is one |
| Pinned | Conversations you pinned with `P` | While something is pinned |
| Spam | Conversations moved to Spam | While something is there — see [Spam](spam.md) |
| Archived | Rooms you archived with `A` | While something is archived |

In the rail:

| Key | Does |
| --- | --- |
| `a` | Give the group a name only you see (empty clears it) |
| `K` / `J` | Move it up / down |
| `H` / `S` | Hide it / bring a hidden group back |
| `F` | Toggle first-names-only for senders in this space |
| `b` | Set a notification rule for the whole space — see [Notifications](notifications.md) |
| `m` | Mark every unread room in the group read (asks first) |
| `B` | Bind a key sequence that jumps to this group |

These write to your config for you. To arrange the rail by hand, use `[display.rail]`: `order` takes group keys (`home`, `dms`, `unread`, `invites`, `drafts`, `pinned`, `spam`, `archived`, or a space's name), with `"-"` for a divider and `"*"` for every group you did not name; `hidden` removes groups; `hide_when_empty` hides groups while they hold nothing.

```toml
[display.rail]
order = ["unread", "-", "Work", "Friends", "-", "*", "-", "archived"]
hide_when_empty = ["unread"]
```

## The room list

Walking the list with `j`/`k` previews each room's timeline beside it; `enter` (or `l`) opens it. Each row carries what is waiting:

| Mark | Meaning |
| --- | --- |
| `3` | Unread count; drawn in the alert color when something unread names you |
| `·3` | Unread in an archived room — shown, but not counted anywhere else |
| `✎` | You have an unsent draft here |
| `→` | The room was upgraded; `>` goes to its replacement |
| `✉` | An invitation: `y` accepts, `d` declines (asks first) |

What a badge counts is `[display] unread`: `messages` (the default) counts unread messages from the local cache; `notifications` uses the homeserver's count of what your push rules would have notified.

### Sorting the room list

The order is a **chain** of comparators, applied in turn until one decides. `~` before a key reverses it.

| Key | Puts first |
| --- | --- |
| `unread` | Rooms with something unread |
| `mentions` | Rooms where something unread names you |
| `drafts` | Rooms holding an unsent draft |
| `recent` | Newest activity (rooms with nothing cached stay last) |
| `name` | Alphabetical by the name shown |

```toml
[display.rooms]
sort = ["unread", "recent", "name"]   # the default

[[display.rooms.rule]]                # per rail group: replaces the chain whole
group = "dms"
sort  = ["drafts", "unread", "name"]
```

`group` is a space's name or one of `home`, `dms`, `unread`. To change the group you are looking at for this session only, use the sort sequences in the room list: `s u` (unread on top, on/off), `s m` (mentions on top), `s d` (drafts on top), `s r` (newest first) and `s a` (by name).

A room that was unread when you opened it keeps its place while you read it, so the row under the cursor never slides away.

### Threads in the room list

A thread's summary sits at its root in the timeline, which may be far up the scrollback. So threads with unread replies are also listed as rows beneath their room. `enter` on one opens the thread, `m` marks that thread read without opening it, and `alt+r` names it. `[display.threads] in_room_list` chooses `unread` (default), `all` or `never`, and `max_in_room_list` caps the rows (default 5).

## Read and unread

- **Opening** a room reads it. **Selecting** it in the list does not, unless you set `[display] read_delay` to a number of seconds to rest on it (`0` reads everything the cursor touches; `-1`, the default, never).
- `m` in the room list marks a room read without opening it; `m` in the rail marks the whole group.
- `M` marks a room you have read as still needing you, and clears that mark. It is the homeserver's marked-unread flag, so it shows on your other clients; reading the room clears it too. `/unread` does the same from the composer.
- `A` **archives** a room: it stays readable and writable, but stops counting toward any badge, leaves every group except its own space and Archived, and is skipped by group-wide mark-read. `/archive` from the composer, or list places in `[display] archived`.
- `P` **pins** a conversation: it appears in Pinned as well as where it lives, and `pinned` becomes a place a notification rule can name — a way to let one conversation through do-not-disturb. See [Notifications](notifications.md).
- `!` moves a conversation to **Spam** and back. See [Spam](spam.md).

**Read receipts.** `[display] send_receipts = false` stops announcing your read position to others; the client then sends a private receipt, so your badges still clear everywhere on your account. Both `send_receipts` and `read_delay` can be set per room, space or person:

```toml
[[display.read_rule]]
sender = "@alice:example.org"
send   = false        # read her messages without telling her
```

When you open a room, a **new** rule marks where your unread messages begin. It stays where it was drawn while you read, and is gone next time. Turn it off with `[display] unread_line = false`.

## Jumping around

| Key | Does |
| --- | --- |
| `ctrl+k` | The switcher: every room, space and person in one list — type a few letters, `enter` to go |
| `ctrl+o` / `ctrl+i` | Back to the previous room / forward again (`alt+o` also goes forward) |
| `B` on a room or group | Bind a key sequence that goes straight there |

The **switcher** works from anywhere, mid-sentence included; your draft stays with its room. With nothing typed it lists unread rooms first, then everything else, then groups. People you share no room with appear last as *start a DM*, so typing someone's name or Matrix ID is also how you start a conversation. `:go` opens it by name.

**Back and forward** record every room you actually open, however you got there — the switcher, a jump, a search hit, a link. Previewing rooms by walking the list is not recorded, and neither are overlays. Two keys to alternate between conversations is the answer to wanting two rooms on screen at once.

**Jump bindings** are for the handful of rooms you open every day. Press `B` on a room in the room list or a group in the rail, then type a sequence such as `g w`. It is saved as a `[[keys.jump]]` entry; rooms are recorded by ID, so a sequence means exactly one conversation even when the same person is reachable under one name over several bridges. `?` lists every jump resolved to the place it leads to. Leave the prompt empty to remove the binding. Sequences that collide with a built-in key are refused — see [Keybindings](keybindings.md#places-and-commands).

## The timeline

The timeline shows the open room with a **message cursor** (`▸`) that starts on the newest message.

| Key | Does |
| --- | --- |
| `j` / `k` | Next / previous message |
| `ctrl+d` / `ctrl+u` | Half a screen down / up |
| `pgdown` / `pgup` | A screen down / up |
| `gg` / `G` | Oldest loaded / newest message; `gg` at the top loads more history |
| `home` / `end` | Scroll to the room's very beginning, loading all of its history / jump to the latest |
| `[` / `]` | Back / forward a day, landing on the day's first message with its date separator at the top. `[` first goes to the start of the day you are in |
| `m` / `M` | Next / previous message that mentions you (wraps around) |
| `f` / `F` | Next / previous message with a file |
| `gt` | Read the room's topic in full |

A rule with the date separates each day. **Right-to-left and mixed-direction text** (Hebrew, Arabic, and either mixed with English) is laid out in the right order, including names, mentions and formatting.

**A room that reads right to left can be mirrored**: the name and the time move to the right. Each message keeps its own direction: Hebrew or Arabic words sit against the name, English ones read from the left edge, and a message's quote, reactions and pictures go with it. Set places by hand in `[display.direction]` (`rtl = ["space:Friends"]`, `ltr = [...]`, the place vocabulary; the narrowest entry wins), or turn on `auto = true` to guess each room from its newest messages when it opens. The guess is kept until you leave the room, so it never flips while you read. `D` in the room list, or `/direction`, cycles the room under the cursor: right to left, left to right, back to what the section says.

An upgraded room continues into the room it replaced: scrolling off the top of the new room carries on into the old one.

## Messages

On the message cursor:

| Key | Does |
| --- | --- |
| `r` | Reply. The composer shows what you are answering; `esc` abandons the reply |
| `enter` | Go to the message this one replies to, loading history or opening its thread if needed |
| `e` | React: `1`–`9`, `0` pick from the quick palette, or type an emoji or `:shortcode:` and `enter` |
| `ctrl+e` | Pick a reaction from the full emoji grid |
| `E` | Edit your own message in the composer; `enter` saves, `esc` restores what you were writing |
| `x` | Delete the message (asks first). Somebody else's needs the room's redact power level |
| `*` | Star or unstar — a private bookmark. `/starred` lists them; see [Search](search.md#lists-that-use-the-results-pane) |
| `H` | History: every version the message had, and its deletion — who, why and when |
| `yy` | Copy the text |
| `yc` | Copy the verification code in it (shown in the legend only when there is one) |
| `z o` | Uncover a spoiler (or a kept deletion); again to cover it |
| `a` | Name the sender — an alias and a color only you see |
| `b` | Notification rule for this sender, room or space |

Edited messages end in `(edited)` and starred ones in `★`. `/me` messages start with `*`.

The quick palette ranks the emoji you use most, by default blending this room, its space and everywhere. See `[display.reactions]` and `[display.emoji]` for the ranking, the static fallback set, skin tone and the size of the emoji vocabulary.

### Deleted messages

A deleted message keeps its row and reads `(deleted)`, or `(deleted by Bob — reason)` when someone else removed it. `H` shows the full record. `[display.deleted]` decides what is kept and shown:

```toml
[display.deleted]
mine   = "show"   # or "hide": your own deletions, usually a typo you fixed
others = "show"   # or "hide"
keep   = false    # true keeps what a deleted message said, in this machine's cache
```

`keep` is off by default: when a deletion arrives, the words, formatting and attachment are erased from the cache. With it on, `z o` reveals what the message said, and `H` shows every edit as it was received. A deleted message that a thread hangs off is always shown.

## Threads

A thread appears in the room as a summary under its root — `💬 3 replies · Carol`, timed by the latest reply.

| Key | Does |
| --- | --- |
| `t` | Open the thread the selected message belongs to; `esc`/`h` closes it |
| `T` | Start a thread on the selected message (it exists once you send the first reply) |
| `ctrl+t` | List the room's threads — the way back to one you have already read. Also in the room list |
| `alt+r` | Name the thread; a thread's default label is a snippet of its first message |

An open thread takes over the timeline pane, and what you type there is sent in the thread. `/threads` lists this room's threads; `:threads` can reach further.

## Media

### Pictures

By default a picture is a one-line chip with its name, size and dimensions (`[display.media] mode = "placeholder"`). Set `mode = "inline"` to draw pictures in the timeline out of text characters: `detail = "sextant"` (default, three times the detail) or `"half"` for terminals or fonts without sextant glyphs. `max_height` and `max_width` cap the size; `0` scales with the window.

`v` opens the picture in a real image viewer, with the room's other pictures in the order they were sent, so the viewer's next and previous walk the conversation. In the room list, `v` opens a room's pictures without opening the room. `[display.media] viewer` picks the program; empty uses the first known viewer installed.

Stickers are shown like pictures.

### Video, voice notes and other files

| On a message with… | Key | Does |
| --- | --- | --- |
| A video | `p` or `v` | Hands it to `[display.media] video_player` (default: the desktop's handler) |
| A voice note | `p` | Opens the player bar; `p` again plays or pauses |
| Any other file | `v` | Opens it with `[display.media] opener` (default: the desktop's handler) |
| Any attachment | `s` | Saves it to the download folder |
| Any attachment | `S` | Saves it somewhere else, choosing the folder in the desktop's file chooser (over the XDG portal; without one, it falls back to the download folder) |

The **voice-note player** is a bar above the status line, so you can keep reading, switch rooms and reply while it plays. While a note is loaded:

| Key | Does |
| --- | --- |
| `space` | Play / pause |
| `[` / `]` | Back / forward by `skip` seconds (default 10) |
| `{` / `}` | Slower / faster, a quarter at a time, pitch corrected |
| `=` | Back to the speed set for this place |
| `w` | Remember the current speed for this room, space or person |
| `P` | Stop and close the player |

It needs mpv or VLC. `[display.media.audio]` sets the starting `speed`, the `skip` step and `close_after` (how long the bar stays after a note ends; `-1` keeps it until `P`). Per-room, per-space and per-person speeds go in `[[display.media.rule]]`.

**Downloads** go to `[display.media] download_dir` (default `$XDG_DOWNLOAD_DIR`, else `~/Downloads`), laid out by `download_template` over `{space} {room} {person} {name} {ext} {date} {time}`. Nothing is overwritten: a second `photo.png` becomes `photo (2).png`. `[[display.media.rule]]` overrides the folder and template for a room or space. `[clipboard] copy_downloads` puts the saved path on your clipboard.

`[display.media] auto = false` stops fetching pictures until you ask, and `cache = false` keeps a room's attachments off this machine's disk; both can be set per room or space.

### Sending files

In the composer, `alt+u` opens the desktop's file chooser and sends the file with what you have typed as its caption. `/upload <path>` or `/upload <path> | caption` does the same by path. See [Composer](composer.md).

## Links

| Key | Does |
| --- | --- |
| `gx` | Open a link from the message in your browser, and stay here |
| `gX` | Open it and switch to the browser (see `[clipboard] focus_command`) |
| `yu` | Copy a link |
| `gl` | Follow a Matrix link — a `matrix:` URI or a `matrix.to` address — inside the client |

A message with several links asks which one. Links in formatted messages count too, including the address behind a named link.

`gl` opens a room or a message you can see directly. A room you are not in asks before joining, since everyone there sees you join. A person opens the switcher filtered to them, listing your conversations with them and the option to start one — following a link never creates a room on its own.

With `[display] hyperlinks = true` (default), links are also clickable in terminals that support OSC 8 (usually with `ctrl` held). To have your desktop's `matrix:` links open in kith, see `kith --open` in [Command line](cli.md).

## People and rooms

### Names and colors

Names you give are local — only you see them — and live in your config.

- `a` on a message names its **sender**: pick an existing person to merge this account into (for example the same contact over several bridges), or a new one, then a color. This writes a `[[display.identity]]`.
- `a` on a room or rail group gives it a name of your own (`[[display.name]]`); `alt+r` names a thread.
- `F` in the rail shows first names only in that space (`[[display.space_rule]]`).
- `[display] max_name_length` caps the sender column; `color_messages = true` tints message bodies in the sender's color.

Every sender keeps a stable color across restarts.

### Membership and management

In the room list:

| Key | Does |
| --- | --- |
| `p` | The people in this room — the only place a Matrix ID is browsable. In it: `a` name, `r` remove, `b` ban (both ask), `i` to start filtering |
| `i` | Invite someone by Matrix ID (`/invite` from the composer) |
| `U` | Lift a ban, by Matrix ID |
| `J` | Join a room by ID or alias (`:join` from the command line) |
| `L` | Leave the selected room (asks first; `/leave` from the composer) |
| `n` | Create a room or a space |
| `S` | File the room into spaces or take it out: `space` ticks, `enter` applies |
| `>` | Go to the room that replaced an upgraded one |

`n` offers four kinds: a private encrypted room, a private unencrypted room (for bridges and bots that cannot read encrypted rooms), a public room, and a space. A room created while a space is selected in the rail is filed into that space. Encryption is decided here — a room is encrypted from its first event or not at all.

Management actions check your power level first, so a refusal names the level you would need. Filing writes both sides of the relationship (the space's child and the room's parent), so the room is filed for every client. `[display] filing_spaces` controls which spaces `S` offers.

## Typing notices

Who else is typing is shown on the rule above the composer. `[display] typing = false` hides it; `send_typing = false` stops telling rooms when *you* are typing, while still showing everyone else.

## The command line and composer commands

`:` opens the command line for client-wide commands (`:go`, `:join`, `:mentions`, `:files`, `:starred`, `:threads`, `:scheduled`, `:dnd`, `:verify`, `:settings`, `:help`, …). Commands typed in the composer with `/` act on the room you are writing in (`/me`, `/upload`, `/search`, `/pin`, `/archive`, `/leave`, …), and your own scripts become `/` commands too. Both complete as you type. See [Commands](commands.md).

## Notifications and silence

`ctrl+n` turns on do-not-disturb and `alt+n` mutes only the sound; each asks what to silence and for how long, and either key turns it all back off. `W` (or `/why`) explains why the open room did or did not notify you. Rules, quiet hours and sounds are in [Notifications](notifications.md).

## Settings

`,` opens the settings screen: the everyday preferences, each showing its current value — notifications, sounds, quiet hours, whether rooms open ready to type, which key sends, name width, what badges count, image display, verification-code detection, emoji set, skin tone and reaction ranking. Choosing a row toggles it, offers its choices or asks for a value. Changes are written back to your config file, keeping your values; the first time, a `.bak` copy is left beside it. Everything else is in [Configuration](configuration.md).

## Help

`?` opens the help overlay: every key in every mode, your jump bindings with the places they lead to, your script keys, the `:` and `/` commands, the text-editing keys, and any problems with your keybinding config. It is generated from the running keymap, so it always matches what the keys do. `j`/`k` and the page keys scroll it, and `gg`/`G` (or `home`/`end`) jump to its ends. `/` searches it: type, and only the matching rows stay, under their section's title (a section whose title matches stays whole). `enter` keeps the filter while you scroll, `esc` clears it, and `?`, `q` or a second `esc` close the help. `:help` opens it too.

## Mouse

The wheel scrolls whatever is under the pointer — three rows in the timeline, one in a list, and overlays when one is open. Clicking does nothing, on purpose: the keyboard is the one way to say where you are. While kith receives mouse events, hold `shift` to select text with your terminal, or set `[display] mouse = false`.

## Related

- [Search](search.md) — offline full-text search, mentions, files and starred lists
- [Composer](composer.md) — writing, formatting, completion, spell-checking, scheduling
- [Notifications](notifications.md) — rules, do-not-disturb, sounds
- [Encryption](encryption.md) — verification and keys
- [Configuration](configuration.md) — the config file
- [Troubleshooting](troubleshooting.md)
