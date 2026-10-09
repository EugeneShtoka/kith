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

1. **The spaces rail** — your spaces and your tags. The group under the cursor decides what the room list shows.
2. **The room list** — the rooms in that group, with unread badges, and threads with unread replies listed beneath their room.
3. **The timeline and composer** — the open room's messages, a message cursor to act on them, and the composer at the bottom.

`tab` and `shift+tab` move between panes (they stop at the ends rather than wrapping). Inside the panes, `l`/`enter`/`right` goes right and `h`/`esc`/`left` goes back. `j`/`k` move, `gg`/`G` go to the top and bottom, and `ctrl+d`/`ctrl+u` scroll half a screen. A number before a motion repeats it — `5j`, and `12G` is the twelfth row; turn on `[display] row_numbers` for relative row numbers to count by.

The status line at the bottom shows what just happened and a short legend of the keys that apply where you are. A key sequence in progress (`g`, `s`, `y`, `z`) is shown there until you finish it.

## Moving between modes

A room opens **ready to type**: `enter` on a room puts you in the composer. `esc` leaves the composer for the **message cursor**, where single letters act on the selected message; `i` goes back to typing. `esc` again returns to the room list. To land on the message cursor instead, set `[display] open_in_insert_mode = false` (also on the settings screen).

While you are typing, letters are text. Only keys that cannot type anything — such as `ctrl+k`, `ctrl+o`, `ctrl+n` and `ctrl+d` — still act.

## The spaces rail

The rail lists every Matrix space you have joined and every [tag](#tags) in your
config. Nothing in it is built in: a first run writes these tags into the config as a
starting point, which you can change, rename or delete like any other:

| Tag | Holds | When it appears |
| --- | --- | --- |
| Invites (N) | Rooms you are invited to (`invite`) | While there is an invitation; shown first |
| All | Every room (`*`) | Always |
| DMs | Direct messages (`dm`) | Always |
| Unread | Rooms with something unread (`unread`) | Always |
| Drafts | Rooms where you left an unsent message (`draft`) | While there is one |
| Pinned | The rooms you put in it | While it holds something |
| Spam | Conversations moved to Spam (`spam`) — see [Spam](spam.md) | While something is there |
| Archived | The rooms you put in it: they stop counting and leave everything but the space they belong to | While it holds something |

Your spaces sit between Pinned and Archived. Invitations and spam are in no space.

A forum (a Telegram group with topics) is a space: its General topic and each other
topic are its rooms, each read, counted and filed on its own.

In the rail:

| Key | Does |
| --- | --- |
| `a` | Give the group a name only you see (empty clears it) |
| `b` | Set a notification rule for the whole space — see [Notifications](notifications.md) |
| `m` | Mark every unread room in the group read (asks first) |

The rail's order and hidden rows are `[display.rail]` (`order`, `hidden`); a space's or
tag's first-names-only rule is a `[[display.space_rule]]`.

These write to your config for you (a tag is renamed in its `[[tag]]`, not with `a`;
name and notification rules work on tag rows as on spaces). To arrange the rail by hand, use `[display.rail]`: `order` takes a space's name or a tag as `tag:<name>`, with `"-"` for a divider and `"*"` for every group you did not name; `hidden` removes groups; `hide_when_empty` hides groups while they hold nothing. Without an `order`, tags come first, then spaces.

```toml
[display.rail]
order = ["tag:Unread", "-", "Work", "Friends", "-", "*", "-", "tag:Archived"]
hide_when_empty = ["tag:Unread"]
```

### Tags

A tag is your own grouping of rooms, across networks: Family can hold your mother's
WhatsApp chat and your brother's Matrix DM. A room can carry several tags. Each
`[[tag]]` holds the rooms its `rule` matches, plus the rooms in `picked`, minus the
rooms in `excluded` (a room ID or `room:<name>`; excluded wins over picked):

```toml
[[tag]]
name     = "Family"
rule     = ["dm", "space:Family", "not room:Bank"]
picked   = ["whatsapp:44880000001/1500000002@s.whatsapp.net"]

[[tag]]
name = "Busy"
rule = ["unread", "mention", "not tag:Family"]
```

A room matches a rule when some term matches and no `not` term does; a rule of `not`
terms alone matches every room they leave, and an empty rule holds only picked
rooms. The terms are:

- `*`: every room;
- the place words: a room ID, `room:<name>`, `space:<name>`, `protocol:<network>`,
  `dm`, `group`;
- the state words: `unread`, `mention`, `draft`, `spam`, `invite`, which follow
  the room as it changes;
- `tag:<name>`: another tag's rooms.

Two tags whose rules name each other, directly or through others, are reported when
the config is read, and their references to each other match nothing; the rest of
the config still works. `hidden = true` keeps a tag out of the rail.

`S` on a room opens the filing picker: your spaces, then your tags, the ones holding
the room ticked. Ticking a tag puts the room in it, unticking takes it out; `/tag
<name>` does the same for one tag from the composer, and `/tag` alone opens the
picker. Either way kith writes the tag's `picked` or `excluded` list, whichever says it
with less: a room the rule already holds is not picked, and one it does not hold is
not excluded.

A tag can also change how its rooms behave elsewhere (each off unless set):

| Property | What it does |
| --- | --- |
| `counts_unread = false` | Its rooms count as read everywhere else: no badge, not `unread` or `mention` to other tags' rules, skipped by mark-all-read. The tag itself still sees them as they are. |
| `exclusive = true` | Its rooms show under it alone, in no other tag. Their spaces keep them. Of two exclusive tags holding a room, the first configured wins. |
| `space_exclusive = true` | Its rooms leave the spaces you made and the tags that are not space-exclusive. The spaces they belong to (a bridge's space, a community, the Matrix space that is their home) keep them, so they stay findable where they live. |
| `sticky = true` | The open room stays listed until you move off it, as in Unread. |
| `hide_when_empty = true` | No rail row while it holds nothing. |
| `first = true` | At the top of the rail, unless `[display.rail] order` places it. |
| `count_in_label = true` | The row says how many rooms it holds. |

**Copying folders.** `:import` copies an account's folders (Telegram's) into tags,
once: the folder list shows each one with what it would do — a new tag, or how an
existing tag would change among that account's chats — and you choose per tag before
**Copy**. Setting an account up offers the same.

**Editing tags in the app.** Settings (`,`) → **Tags** lists your tags and makes a new
one. A tag's page shows its name, rule, picked and excluded rooms, every property as
an on/off row, and which other tags take rooms out of its row (the exclusive ones, and
the space-exclusive ones). The rule and the room lists are edited an entry at a time:
choose an entry to change it (empty removes it), or the last row to add one. Renaming
a tag renames it everywhere it is named — `tag:<name>` in rules, place lists, the rail
order and priority, and its name in the rail's `hidden` and `hide_when_empty`.
Renaming it to another tag's name asks whether to combine the two: the other tag then
holds every room either held and keeps its own settings, and everything that named
the renamed tag names it. Their rules are joined, unless one has a `not` term (which
would start filtering the other's rooms) or the join would make tags name each other
in a circle; then the combined tag holds the rooms both held, picked one by one.
Deleting it asks first and takes it out of the rail's lists and priority; while a rule
or a list elsewhere still names it, the delete is refused and the status line says
where.

Exclusivity decides where a room shows, not what rules match: `tag:Family` in
another rule still matches a room Family claims.

A tag is also a place: `tag:Family` works wherever a room, space or network can be
named, in notification rules, do-not-disturb, `[agent.read]`, `[assist]`, spam lists
and the rest. As a place a tag is judged on the room alone, so its state words match
nothing there (`not unread` matches everything). Tagging a room that way widens what
names the tag: a room you pick into a tag `[agent.read]` lists becomes readable to the
assistant.

A room's tags are homes beside its spaces. `[display] priority` ranks both (spaces
by name, tags as `tag:<name>`), and the first decides which name rule applies, the
`{space}` of a notification and a download's folder. What it does not name follows:
spaces first, then tags in the order they are written. Keep a tag that holds every
room, such as All, last — the starter config writes it last — so it is a room's home
only when the room has no other: otherwise every room in no space would take its
name.

The filing picker (`S`) lists your spaces and tags in the same order: what
`priority` names first, then the rest as the rail shows them. Name rules and notification rules
can be set on a tag row from the rail (`F`, `b`) as on a space.

## The room list

Walking the list with `j`/`k` previews each room's timeline beside it; `enter` (or `l`) opens it. Each row carries what is waiting:

| Mark | Meaning |
| --- | --- |
| `3` | Unread count; drawn in the alert color when something unread names you |
| `·3` | Unread in a room a `counts_unread = false` tag holds (Archived, in the starter config) — shown, but not counted anywhere else |
| `✎` | You have an unsent draft here |
| `→` | The room was upgraded; `/replacement` goes to its replacement |
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

#### Exporting a conversation

`/export` in a room's composer writes the room to a Markdown file: everything the
cache holds, oldest first, or the span you give it, as `/summary` reads one (`7d`,
`yesterday`, `2026-09-18`, `500` for the last 500 messages). With a thread open it
writes just the thread. It goes into `[storage] export_dir` (an `exports` folder
beside kith's data unless you set one), named after the room and the day, or where
you say after the span: a folder, `/export ~/Documents/ChatHistory`, or a file,
`/export 7d ~/Documents/ChatHistory/friends-backup.md`. A missing folder is made,
and a file already there is never overwritten: the export takes the next free name.

Each day has a heading; each message its time and sender (named as you know them, in
full), the message it replies to, its words with their formatting, its attachment
and its reactions; a thread's replies sit under the message that began it.

## Threads in the room list

A thread's summary sits at its root in the timeline, which may be far up the scrollback. So threads with unread replies are also listed as rows beneath their room. `enter` on one opens the thread, `m` marks that thread read without opening it, and `alt+r` names it. `[display.threads] in_room_list` chooses `unread` (default), `all` or `never`, and `max_in_room_list` caps the rows (default 5).

## Read and unread

- **Opening** a room reads it. **Selecting** it in the list does not, unless you set `[display] read_delay` to a number of seconds to rest on it (`0` reads everything the cursor touches; `-1`, the default, never).
- `m` in the room list marks a room read without opening it; `m` in the rail marks the whole group.
- `M` marks a room you have read as still needing you, and clears that mark. It is the homeserver's marked-unread flag, so it shows on your other clients; reading the room clears it too. `/unread` does the same from the composer.
- **Archiving** a room is putting it in the starter config's Archived tag (`S`, or `/tag Archived`): it stays readable and writable, but stops counting toward any badge, leaves every group except the space it belongs to and Archived, and is skipped by group-wide mark-read.
- **Pinning** is putting it in the Pinned tag: it appears there as well as where it lives, and `tag:Pinned` is a place a notification rule can name — a way to let one conversation through do-not-disturb. See [Notifications](notifications.md).
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

**Jump bindings** are for the handful of rooms you open every day. Run `:shortcut` (or `/shortcut` in the composer) and type a sequence such as `g w`. It binds what you are on — the space or tag selected in the rail when the rail has the focus, else the room — and `tab` in the prompt switches to the other, as it switches a search's scope. `:shortcut g w` binds at once. It is saved as a `[[keys.jump]]` entry; rooms are recorded by ID, so a sequence means exactly one conversation even when the same person is reachable under one name over several bridges. `?` lists every jump resolved to the place it leads to. Leave the prompt empty to remove the binding. Sequences that collide with a built-in key are refused — see [Keybindings](keybindings.md#places-and-commands).

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

**A room that reads right to left can be mirrored**: the name and the time move to the right. Each message keeps its own direction: Hebrew or Arabic words sit against the name, English ones read from the left edge, and a message's quote, reactions and pictures go with it. What has no words of its own reads as the room does: "(deleted)", a file without a caption, a message of only links or emoji, and one that is only a bridge's English header ("↷ Forwarded" on a forwarded file, "Sent an album with 2 images:"). Set places by hand in `[display.direction]` (`rtl = ["space:Friends"]`, `ltr = [...]`, the place vocabulary; the narrowest entry wins), or turn on `auto = true` to guess each room from its newest messages when it opens. The guess is kept until you leave the room, so it never flips while you read. `/direction` cycles the room you are writing in: right to left, left to right, back to what the section says.

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
| `P` | Vote in the message's poll (several answers where it takes them), or take the vote back |
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

`keep` is off by default: when a deletion arrives, the words, formatting and attachment are erased from the cache, the attachment's file in the media cache included. With it on, `z o` reveals what the message said, and `H` shows every edit as it was received. A deleted message that a thread hangs off is always shown.

A deleted message's attachment is never drawn in the timeline, offered in the room's pictures, or saved from there. With `keep` on, its file moves to the media cache's `deleted/` folder (kept out of the cache's size limit, since it may not be fetchable again); `H` lists it, and `v` in that view opens it at full size.

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
- `[[display.space_rule]]` shows first names only in a space or tag (`space = "tag:Work"` for a tag).
- `[display] network_colors = true` names each room in every room list in its network's color; a place's `[[display.space_rule]] network_colors = true` or `false` overrides that for its own room list.
- `[display.rail] network_colors = true` colors a rail row whose rooms are all on one network the same way.
- `[display.theme.networks]` sets those colors (`#rrggbb`, a named color, or `"none"`). Unset, each network has its own, apart from the theme's text, accent, badge and alert colors: WhatsApp teal, Telegram blue, Slack magenta, Messenger and Instagram (Meta) orchid, LinkedIn periwinkle, Google Messages (SMS) coral. A bridged Matrix room counts as its bridge's network. All of it is in settings (`,`): under look, the switch everywhere, each space's and tag's choice (as everywhere, on, off) and each network's color; under rooms, the rail's switch.
- `[display] max_name_length` caps the sender column; `color_messages = true` tints message bodies in the sender's color.

kith keeps one directory of the people it knows, on every network and account. A
person is known by their number and by their ID on each network; where a network
says two are the same person (a Telegram user and the number Telegram shows for them,
a Slack profile's phone), or an ID is a number (a WhatsApp ID, a bridge's puppet),
they are one person. A person is named by the best name any account gives them: a
name saved in an address book (a WhatsApp account's contacts, your Telegram contacts)
first, then the name a bridge gives them, then their display name where you see them,
then the name they chose for themselves (a WhatsApp push name, a Telegram or Slack
profile). So a contact saved on one account names them on every network, someone a
network shows only as a number ("+972 54-…", "+359… (WA)") is named, and a name you
give someone with `a` reaches their linked accounts too. Your own names above come
first.

A bridge also knows the whole address book of each account logged into it. List the
bridges' provisioning URLs in `bridge_contacts` and kith reads those contact lists too,
with your Matrix login, every few hours: a contact saved on any of your bridged
accounts is named everywhere, whether or not they ever wrote to that account. Only
URLs on your homeserver's own host are asked, since the request carries your login.

Every sender keeps a stable color across restarts.

### Membership and management

In the room list:

| Key | Does |
| --- | --- |
| `p` | The people in this room — the only place a Matrix ID is browsable. In it: `a` name, `r` remove, `b` ban (both ask), `i` to start filtering |
| `i` | Invite someone by Matrix ID (`/invite` from the composer) |
| `J` | Join a room by ID or alias (`:join` from the command line) |
| `L` | Leave the selected room (asks first; `/leave` from the composer) |
| `S` | File the room into spaces or take it out: `space` ticks, `enter` applies |

`/unban @user:server` lifts a ban, `/replacement` goes to the room that replaced an upgraded one, and `:new` creates a room or a space. It offers four kinds: a private encrypted room, a private unencrypted room (for bridges and bots that cannot read encrypted rooms), a public room, and a space. A room created while a space is selected in the rail is filed into that space. Encryption is decided here — a room is encrypted from its first event or not at all.

Management actions check your power level first, so a refusal names the level you would need. Filing writes both sides of the relationship (the space's child and the room's parent), so the room is filed for every client. `[display] filing_spaces` controls which spaces `S` offers.

## Typing notices

Who else is typing is shown on the rule above the composer. `[display] typing = false` hides it; `send_typing = false` stops telling rooms when *you* are typing, while still showing everyone else.

## The command line and composer commands

`:` opens the command line for client-wide commands (`:go`, `:join`, `:mentions`, `:files`, `:starred`, `:threads`, `:scheduled`, `:dnd`, `:verify`, `:settings`, `:help`, …). Commands typed in the composer with `/` act on the room you are writing in (`/me`, `/upload`, `/search`, `/tag`, `/leave`, …), and your own scripts become `/` commands too. Both complete as you type. See [Commands](commands.md).

## Notifications and silence

`ctrl+n` turns on do-not-disturb and `alt+n` mutes only the sound; each asks what to silence and for how long, and either key turns it all back off. `/why` explains why the open room did or did not notify you. Rules, quiet hours and sounds are in [Notifications](notifications.md).

## Settings

`,` opens the settings screen: the everyday preferences, each showing its current value — notifications, sounds, quiet hours, whether rooms open ready to type, which key sends, name width, what badges count, image display, verification-code detection, emoji set, skin tone, reaction ranking, and your tags. Choosing a row toggles it, offers its choices or asks for a value. Changes are written back to your config file, keeping your values; the first time, a `.bak` copy is left beside it. Everything else is in [Configuration](configuration.md).

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
