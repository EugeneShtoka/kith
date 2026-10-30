# Configuration

kith reads two TOML files: the bridge's `config.toml`, which `kithd` reads (accounts,
tags, rules, what notifies), and `device.toml` beside it, which holds what only this
device reads (its look, keys, how it shows a notification), so a phone and a laptop
may differ. Only the account is required, and every other setting has a working
default. This page covers where the files live, how they get written, what each
section is for, and some common tweaks.

For the complete reference, with every option and its default documented inline, run
`kith --print-config` (config.toml) and `kith --print-device-config` (device.toml).
They print the same annotated files kith writes on first run. The source copies are
[`internal/config/default.toml`](../internal/config/default.toml) and
[`internal/config/default_device.toml`](../internal/config/default_device.toml).

## The config file

| Platform | Default path |
| --- | --- |
| Linux | `${XDG_CONFIG_HOME:-~/.config}/kith/config.toml` |
| macOS | `~/Library/Application Support/kith/config.toml` (or `$XDG_CONFIG_HOME/kith/config.toml` when that is set) |

All three binaries accept `--config <path>` to read a different file. If you run
`kith` (or `kith login`) and the file doesn't exist, it writes the annotated
default there and exits so you can fill in the account.

To start over from the reference:

```sh
kith --print-config > ~/.config/kith/config.toml
```

The file holds **no secrets**. Your password is never stored, and the access token
lives in the OS keyring, so you can keep the file in a dotfiles repository.

### Matrix accounts

Each `[[matrix.account]]` is one account; one daemon runs any number of them:

```toml
[[matrix.account]]
name       = "personal"                       # what `kith login matrix <name>` takes
homeserver = "https://matrix.example.org"   # the client-server API URL
user       = "@alice:example.org"           # your full Matrix ID

[[matrix.account]]
name       = "work"
homeserver = "https://matrix.work.example"
user       = "@alice:work.example"

[matrix]
default = "personal"   # which one a call naming none uses, when several could
```

Each account has its own session, device and crypto store, and is a space in the rail
holding its rooms (leaving that space signs the account out). A room two accounts are
in shows as two rooms, one per account.

Top-level keys:

| Key | Default | Purpose |
| --- | --- | --- |
| `allow_token_file` | `false` | Store the access token in a 0600 file under `~/.local/state/kith/` (one per account) when no OS keyring exists. Use it only on a headless machine. |
| `terminal` | `""` (detect) | In `device.toml`: what `kith --open` starts when a `matrix:`, `tg:` or `whatsapp:` link arrives and no client is running. See [getting-started.md](getting-started.md#open-matrix-tg-whatsapp-and-signal-links-in-kith). |
| `profile_in_use` | `""` (none) | In `device.toml`: the [profile](#profiles) a window (the TUI, the desktop) runs in, written when you pick or switch to one there. |

### Several accounts on a network

When a network has several accounts, a call that names none uses one of them:
opening a link (`tg:`, `t.me`, `wa.me`, `matrix.to`, `matrix:`), joining or knocking
on a room by its address, starting a direct message, `:verify`, `/discover` and the
key jobs (`--restore-keys` and the rest). It is picked in this order:

1. **An account already in that chat.** If several are, the network's default, when it
   is one of them; otherwise kith asks which of them.
2. **The network's only account.**
3. **The network's default**, named in its section:

   ```toml
   [telegram]
   default = "work"   # the name of one of its [[telegram.account]]s
   ```

4. **Otherwise kith asks which.** The key jobs, run with no one to ask, refuse and
   name the accounts instead.

A `default` that names no account of the network is refused at startup.

### Profiles

A profile is a named setup with its own rooms. `kith --profile <name>` runs in one:
the room list, the spaces, unread counts, invitations, drafts, scheduled messages and
search hold only its rooms, and a space holding none of them is left out. A room you
name directly (a link, a jump) still opens. Without `--profile`, kith runs in the one
`profile_in_use` remembers (below). `gP` or `:profile <name>` switches the window to
another; the status line names the profile once there is more than one. kithd builds
each profile's rail (its rows and the rooms each holds), so every window of a profile
draws the same one.

Each profile has a window of its own: two terminals can run `work` and `home` side by
side, and a second window in a profile already open is refused, as is switching into
one. A link clicked on the desktop (`kith --open`) goes to one window: the one with
the link's chat open, else the one used last whose profile holds it, else the one used
last. A profile is also a notification topic: kithd publishes each notification to
every profile holding its room, and `kith-notify` shows the profiles it listens to (the
first, unless told others; see
[notifications.md](notifications.md#where-popups-appear-kith-notify)). A room no
profile holds never notifies, whatever the [rules](notifications.md#rules) say.

A window (the TUI or the desktop) runs in the profile `device.toml`'s
`profile_in_use` names, which it writes when you pick or switch to one there;
`kith --profile` runs one start in another without writing it. With one profile it
runs in it unasked; with several and none picked yet, or the one it runs in removed
from `config.toml`, it asks which (and draws no rail meanwhile).

A config has at least one profile; the starter writes `default`, holding every room:

```toml
[[profile]]
name    = "default"
include = ["*"]

[[profile]]
name    = "work"
include = ["space:Work", "protocol:slack"]
exclude = ["tag:Archived"]
```

`include` and `exclude` take places, as the other room lists do
([below](#sections)), and `"*"` in `include` is every room; `include` names at
least one, and `exclude` wins. A tag
whose rule reads a room's state (`unread`, `mention`, `draft`, `invite`, itself or
through a tag it names) is refused there: a room would enter and leave the profile
as it is read. Names are unique, in any case.

#### A profile's own archive and rail

A profile can archive its own rooms and lay out its own rail; whatever it leaves unset
is the shared one:

```toml
[[profile]]
name    = "phone"
include = ["dm", "tag:Family", "protocol:signal"]
exclude = ["tag:Archived"]
archive = { rooms = ["not tag:Family"], push = false }
rail    = { order = ["tag:Unread", "tag:Family", "*"] }
```

- `archive.rooms` is the archive tag's rule in this profile (the tag
  `[whatsapp.archive]` and `[telegram.archive]` name, Archived by default), written
  as any tag's rule, `not` entries included. An empty list keeps the tag's own rule.
  `exclude = ["tag:Archived"]` then leaves out what this profile archives.
- Filing a room by hand (`S`, `/tag Archived`) still files it in every profile, and a
  chat archived on its network (`[<network>.archive] follow`) is archived in every
  profile: the network has one archive per account, shared by every device.
- `archive.push` archives this profile's archived rooms on their network, as a tag's
  `push_archive` does; unset keeps the tag's. Profiles that push add up: a chat stays
  archived on its network while any pushing profile archives it, so two profiles never
  undo each other, and a profile pushes only the rooms it holds. Letting go of a room
  unarchives it only where kith archived it: a chat you archived on your phone stays
  archived. A profile that stops pushing, or is removed, leaves the network as it is.
- `rail` replaces `[display.rail]` in this profile, whole, once it sets anything
  (`order`, `hidden`, `hide_when_empty`, `network_colors`).

kith saves these as `[profile.archive]` and `[profile.rail]` tables under the
`[[profile]]`; both forms read the same.

#### People in a profile

A profile can also hide people. Their messages, and the direct replies to them, leave
that profile's timeline, search, unread counts, thread counts and notifications, and
the assistant's summaries, as a [hidden person](filters.md#hiding-a-person)'s are everywhere. The
other profiles still show them.

```toml
[[profile]]
name    = "home"
include = ["*"]
people  = { exclude = ["role:colleagues", "whatsapp:+15550100", "@boss:example.org"] }
```

- **A person ID** (`whatsapp:+15550100`, `telegram:42`, `@boss:example.org`): any
  entry whose key is a network's prefix is an ID, as `[[tag]] people` takes them,
  never a card field. Every account kith links to that person is hidden with it.
- **A phonebook field**, `key:value` ([phonebook.md](phonebook.md)): everyone whose
  card holds the value under that key. The value is matched word for word, in any
  case: `role:colleagues` matches a role of `Colleagues` or `former colleagues`, not
  `colleaguesship`. An identifier key matches the identifier itself, so
  `phone:+15550100` hides that number, written any way. kithd reads the phonebook
  folder while a profile names a field, and again whenever a card in it changes.
- **Never you.** A card that names one of your accounts hides nobody, even when its
  fields match.

An entry that is neither an ID nor `key:value` is refused at startup. Unlike a muted
tag, a profile moves no room: a direct message with someone it hides stays where its
tags and the profile's own archive put it, empty in that profile. `/why` on a hidden message says which profile hides its
sender, and `z h` (`/hidden`) shows them in the open room until you leave it.

### The device file

`device.toml` sits beside `config.toml` (`~/.config/kith/device.toml` for the default
config; a config elsewhere keeps its own beside it). kith writes its annotated default
there on first run. Each key is in the file of whoever reads it: if `kithd` reads a
key, it is in `config.toml`; if only a client reads it, it is in `device.toml`. A
table whose keys are split keeps its name in both, so `[display] time_format` is in
`config.toml` and `[display] theme` in `device.toml`. The [sections](#sections) table
says which file each is in; `kith --print-device-config` lists every device key.

`kithd` never reads `device.toml`, and a device key written into `config.toml` is
refused there as any unknown key is. A `config.toml` from before the split still
holding device keys is split once, by whichever of `kithd` and the client opens it
first: the device keys move into `device.toml` (a setting `device.toml` already gives
a value other than its default keeps it), then `config.toml` is rewritten without
them, the original, comments and all, kept as `config.toml.before-device`. A rule's
`sound` moves to `device.toml`'s `[notifications.sounds]` under the rule's `name`; a
rule without one is named first, from what it matches. A key neither file reads stops
the split, named, and nothing is written.

### Validation

A value kith can't use, such as a misspelled theme preset, a malformed color or an
unknown media mode, **stops startup** with an error that names the key. It doesn't
fall back to a default. In a client whose failure mode is silence, a setting that was
quietly ignored would look exactly like one that works.

Key *names* are checked too: a key kith doesn't read (misspelled, or under the
wrong `[section]` header) stops startup with a list of the unknown keys.

One thing is warned of instead: a `tag:<name>` naming a tag `config.toml` does not
have. Tags live in `config.toml` while `device.toml` names them too, so a tag deleted
on one device must not stop another; the entry matches no room.

## The settings screen

You rarely need to open the files. Inside kith:

- `,` opens the settings screen: every setting in the file, in groups (Notifications,
  Names, Messages, Rail and room list, …, Keys, Tags). `enter` opens a group; under
  the list is what this page's reference says about the setting under the cursor.
  - A change is saved at once and you stay on its row, so several can change in a row;
    `esc` steps back a level.
  - On/off flips with `enter`. A number steps with `+` and `-`, or `enter` types it
    right below the list (the current value selected, so typing replaces it); the
    status line shows the keys. Text is typed the same way. Anything asked while a
    list fills the timeline pane (settings, a person's card, a picker) is typed there,
    after the list; over the timeline itself, input stays in the status line.
  - A list (priority, the rail's order, include lists) opens its entries: `enter`
    edits one below the list (emptied, it is removed), `x` removes the entry under the
    cursor, the last row adds one, and `K`/`J` move the entry under the cursor.
  - A list of records (read rules, media rules, people, names you gave, filters,
    commands, accounts, jump shortcuts) opens its records, each one's fields set as
    above; *Add one* is typed as its first field, *Remove this one* takes it out. A
    filter's *Add one* asks for each part in turn (name, tag, words or a sender,
    where it watches, what it moves) and writes the filter only at the end.
  - **Keys** has every binding by table, typed as the file writes them (`k,up`; empty
    restores the default, `-` binds nothing); a binding that would clash is refused
    with the reason.
  - **Places** has a page for every space and tag the rail shows (and hidden tags), and
    makes a new tag. A place's page holds everything about it: a tag's name, rule,
    rooms and [properties](usage.md#tags); the place's first-names switch, the
    [phonebook](phonebook.md#a-field-beside-names) field shown beside names, network
    colors and archived rooms; and its [filters](filters.md), the ones moving rooms
    into a tag or watching a space, with a row adding one with the place filled in.
    `e` on a rail row opens that place's page.
- `a` on a room, space, rail group, thread or person gives it your own name.
- `b` on a message, room or space writes a notification rule for it.
- `/why` shows every notification rule in force for the current room, in order, and
  marks the one that decided.

The `a` and `b` changes need Matrix and room IDs you never see in the UI. That's why
pointing at the thing in the app is easier than typing it into the file.

A change made in the app is checked as the file is when kith starts. One the file
would refuse is not applied, and the status line says why.

### How changes are written back

A change made in the app is saved right away, each setting to its own file:
`config.toml` through `kithd`, `device.toml` by the client itself.

- **Only settings that differ from the built-in defaults are written.** Anything you
  set yourself is kept, anything at its default is left out, and a later change to a
  default reaches you instead of being frozen in your file. A short file can still be
  a fully configured one.
- **Comments are not kept.** The rewritten file has a short header, then your values.
  The inline documentation is always available from `kith --print-config`.
- **The first rewrite leaves `config.toml.bak`** (or `device.toml.bak`) beside the
  file, holding what you wrote by hand. Later saves never overwrite it.
- **`kithd` is the one writer.** A window hands its change to the daemon, which
  checks it would run with it (a refused change is never written, and the status line
  says why), writes the file, puts it in force, and tells every open window, which
  shows it at once. `kith login` writes new accounts the same way.
- **A change is made on the file as it was read.** If the file changed meanwhile, in
  another window or by hand, the change is refused rather than written over it: the
  window takes the file as it is now and says your last change was not saved, so you
  can make it again.
- **`device.toml` keeps every window's change.** A window writes only the settings
  it changed onto the file as it is now, so another window's change made meanwhile is
  kept, and the window then runs the file as written.

When you edit the file by hand, restart `kith` to pick up the change. Settings the
daemon acts on (notifications, spelling, the assistant, deletion handling) take effect
when `kithd` restarts:

```sh
systemctl --user restart kithd
```

## Sections

Each top-level section, which file it is in, and what it's for. The linked page covers
the feature; `kith --print-config` and `kith --print-device-config` document every
key.

| Section | File | Purpose | More |
| --- | --- | --- | --- |
| *(top level)* | config, but `terminal` and `profile_in_use` device | `allow_token_file`, `terminal`, `bridge_contacts`, and `profile_in_use`, the profile a window runs in. | [getting-started.md](getting-started.md), [Profiles](#profiles) |
| `[matrix]` | config | The Matrix accounts (`[[matrix.account]]`) and which is the default. | [Matrix accounts](#matrix-accounts) |
| `[display]` | config: `time_format`, `long_date_format`, `short_date_format`, `priority`, `room_name_rules`, `unread`, `[[display.space_rule]]`, `[[display.identity]]`, `[[display.name]]`; device: the rest, `[[display.read_rule]]` included | How the client looks and reads: name widths, mouse, links, the unread line, typing notices, read receipts, what a badge counts, the `priority` of homes, name rules, identities, your own names for things, how times and dates are written (`time_format`, `long_date_format`, `short_date_format`), and the emoji skin tone (`skin_tone`). | [usage.md](usage.md) |
| `[display.theme]` | device | The color palette: a preset plus per-role overrides. | [Themes](#themes) |
| `[display.theme.networks]` | device | The color each network's rooms are named in, where the room lists or the rail ask for it. | [usage.md](usage.md) |
| `[display.emoji]` | device | Emoji set size (`curated`, `standard`, `complete`) and your own shortcodes. | [composer.md](composer.md) |
| `[display.tracked]` | config | Words highlighted wherever they are said and listed by `/tracked`, optionally with notifications. | [search.md](search.md) |
| `[display.rail]` | config | Order, dividers and visibility of the spaces rail. | [usage.md](usage.md) |
| `[[tag]]` | config | Your own groupings of rooms in the rail, by rule, picked and excluded rooms. | [usage.md](usage.md#tags) |
| `[display.rooms]` | device | The room list's sort chain, globally and per rail group. | [usage.md](usage.md) |
| `[display.deleted]` | config | Whether deleted messages show as placeholders, and whether their content is kept in the cache. | [usage.md](usage.md) |
| `[display.media]` | device | Images (placeholder chip or inline), viewer, video player, download folder and layout, the media cache. | [usage.md](usage.md) |
| `[display.media.audio]` | device | The voice-note player: speed, skip, `mpv` or VLC. | [usage.md](usage.md) |
| `[display.reactions]` | device | The quick-react palette and how it ranks by your usage. | [usage.md](usage.md) |
| `[display.threads]` | device | Which threads appear as rows under their room. | [usage.md](usage.md) |
| `[display.direction]` | device | Which rooms read right to left, mirrored with names on the right: set by place, or guessed per room (`auto`). | [usage.md](usage.md#the-timeline) |
| `[notifications]` | config: `max_per_room`, `rate_window`, `[[notifications.rule]]`; device: `enabled`, `desktop`, `command`, `sound`, `sound_command`, `[notifications.sounds]`, `timeout`, `title`, `body` | Desktop delivery (D-Bus popup, command hook, sound, timeout, rate limit) and `[[notifications.rule]]`, the rules that decide what notifies. | [notifications.md](notifications.md) |
| `[clipboard]` | config: `auto_copy`; device: the rest | The copy command, link opener, window focusing, unattended code copy, copying saved-file paths. | [usage.md](usage.md) |
| `[codes]` | config | Where verification codes are looked for and what one looks like. | [notifications.md](notifications.md) |
| `[[profile]]` | config | Named setups, each with its own rooms, archive, rail and notification topic. | [Profiles](#profiles) |
| `[[filter]]` | config | Words, a sender or a language that move rooms into a tag (the starter's Spam, or any other). | [filters.md](filters.md) |
| `[composer]` | device | External editor (`alt+e`), temp-file suffix, Markdown on send. | [composer.md](composer.md) |
| `[spell]` | config: `enabled`, `command`, `dictionaries`, `flag_rare_words`, `rare_ratio`; device: the rest | Spell-checking engine, dictionaries, rare-word detection, check before sending. | [composer.md](composer.md) |
| `[complete]`, `[complete.model]` | `[complete]` device; `[complete.model]` config, but its `declined` device | Word completion and ghost text, and the local llama.cpp model behind it. | [composer.md](composer.md) |
| `[assist]` and its sub-tables | config, but `name_threads` and `name_threads_after` device | The optional remote language model for `/summary`, `:todo`, rewrites and thread names: endpoint, scope, budgets, prompts. | [assist.md](assist.md) |
| `[agent.read]`, `[agent.write]` | config | What `kith-mcp` may read (**nothing until you list rooms** — the one room list where empty means none); where it may write (never wider than what it may read), where it sends instead of drafting, its cooldown. Write entries that reading rules out are warned about at startup. | [mcp.md](mcp.md) |
| `[commands]`, `[[command]]` | `[commands] dir` config; `[commands] timeout` and `[[command]]` device | Your own slash commands: their folder and timeout; per command, its context, output (compose, send, …) and keys. | [commands.md](commands.md) |
| `[schedule]` | config | Send-later messages: `overdue_cutoff_hours` (default 24) holds back messages that come due too late. | [usage.md](usage.md) |
| `[backup]` | config | kithd's scheduled backups: on or off (`enabled`), the age public key they are encrypted to (`key`, never the private one), the folder (`dir`), when (`every`, an interval with `7d` for days, or `cron`, a cron expression; one of them) and how many are kept (`keep`). Changes apply without a restart. | [usage.md](usage.md#backing-up-and-moving-to-a-new-machine) |
| `[remote]` | config | `listen`: the `host:port` addresses kithd serves paired devices on, over TLS 1.3 (default none: the socket only). `0.0.0.0` or `[::]` must be written out; a bare `:port` is refused. Changes apply without a restart. | [remote.md](remote.md) |
| `[[bridge]]` | device | A kithd on another machine this device dials: `name`, `addresses` (`host:port`, tried in order) and `id`, the device ID its key must have. `kith join` writes one when it pairs. | [remote.md](remote.md#pairing-by-qr-code) |
| `[log]` | config | `level`: how much the daemon, the client and `kith-mcp` log (`debug`, `info`, `warn`, `error`; default `info`). `--log-level` and `KITH_LOG_LEVEL` override it. `target`: where the client and a daemon it started log (`auto`, `journal`, `file`; default `auto`, the journal when there is one). `--log-target` and `KITH_LOG_TARGET` override it. | [troubleshooting.md](troubleshooting.md#where-the-log-goes) |
| `[keys]` and `[keys.*]` | device | Every keybinding, one table per mode. | [keybindings.md](keybindings.md) |

Settings that name a place say which kind with a prefix:

- `space:<name>`: a space by its name (a Telegram forum, a WhatsApp community and a
  Slack workspace are spaces too);
- `tag:<name>`: a tag. A tag that does not exist is an empty one: the entry matches no
  room, and is warned of, never refused (the client says where on its status line,
  kithd in its log). Deleting a tag cleans what it can ([Tags](usage.md#tags));
- `place:<name>`: every rail row shown as `<name>`, its label after a
  `[[display.name]]` rename or its name, in any case: a space, a tag, anything the
  rail shows. Where several rows share the name it means all of them;
- `room:<name>`: a room by the name kith shows for it; `protocol:<network>` a network.

A room ID needs no prefix. Every ID names its network, and a room the account that
sees it: `matrix:4f1c9e2a7b3d5c60/!abc:example.org` is a Matrix room as one of your Matrix accounts
sees it (`<account>` is a short hash of that account's MXID), `whatsapp:…` a WhatsApp
chat. Copy IDs as kith shows them; Matrix's own bare form (`!abc:example.org`,
`@dana:example.org`) is refused at startup with the form to use. A bare name is
refused at startup, naming the setting and the prefixes it takes, because a space, a
tag and a room can share one.

**The place vocabulary** is used by `[[notifications.rule]] match`,
`[[display.tracked.rule]] in` / `except`, `[display.direction]`, `[[storage.rule]]
match`, a tag's `rule`, and the room lists `[[profile]] include` / `exclude`, `[codes] include` / `exclude`, `[spam]
rooms` / `except`, `[assist] rooms` / `except` and `[agent.read]` / `[agent.write]`.
An entry is a room ID, any of the prefixes above, `dm` or `group` (a tag is judged on
the room alone: its state words match nothing as a place). An empty list means every
place everywhere except `[agent.read] rooms`, where it means none.

`kith-mcp` reads the config when it starts, so a room you put in a tag, or take out of
one, reaches an `[agent.*]` list naming that tag the next time it starts — as does any
other edit to those lists.

**Rail rows** (`space:`, `tag:` or `place:`) are what `[display] priority`, `[display.rail]
order` / `hidden` / `hide_when_empty`, `[[display.space_rule]] space` and
`[[display.rooms.rule]] group` take; the rail order also takes `*` and `-`.
`[display] filing_spaces` takes `space:`, `place:` or a space's ID. `[[keys.jump]]
target` takes `room:<room ID>` or a rail row.

**A room ID or a rail row** is what `match` takes in `[[display.read_rule]]`,
`[[display.media.rule]]` and `[[display.threads.rule]]`, for example
`match = "space:Work"`; a row there is the room's first home ([display] priority).

**Their own keys:**

- `[[display.name]]` names its `target`: a room ID, `room:<name>`, `space:<name>` or
  `thread:<root event ID>` (a tag is named in its `[[tag]]`);
- `[[display.identity]]` lists `ids` (user IDs, on any network);
- `[[filter]]` matches `words` and `from`.

When several rules match, the narrower one wins: a person in a room beats the room,
which beats the person anywhere, which beats a space.

## Themes

`[display.theme]`, in `device.toml`, has one `preset` and nine optional color roles.

| Preset | Look |
| --- | --- |
| `cobalt2` | The default: deep blue with green text and yellow highlights. |
| `gruvbox` | Warm, low-contrast browns and yellows. |
| `nord` | Cool, muted blue-greys. |
| `terminal` | ANSI colors 0–15, so kith uses your terminal's own color scheme. |

Any role you set overrides the preset, written as `#rgb` or `#rrggbb`:

| Role | Colors |
| --- | --- |
| `accent` | Focused frame, titles |
| `border` | Unfocused frame |
| `text` | Message bodies |
| `muted` | Timestamps, hints, dividers |
| `cursor` | The `▸` marker and the composer prompt |
| `badge` | Unread counts |
| `badge_alert` | Unread counts that mention you |
| `selected_bg` | The row under the cursor in the focused pane |
| `selected_dim_bg` | The same row when its pane isn't focused |

Nine roles cover every surface, and there is no per-widget styling.
`[display] color_messages = true` also tints each message body in its sender's color.

```toml
[display.theme]
preset = "terminal"
accent = "#ff8800"
```

## Examples

These snippets go into the file each one names. Merge them with any section header
you already have, because TOML allows each `[table]` only once.

### Turn on desktop notifications

Notifications ship off. Turning them on is this device's, in `device.toml`:

```toml
[notifications]
enabled = true
desktop = true                 # the popup; off unless set
sound   = "/usr/share/sounds/freedesktop/stereo/message-new-instant.oga"
```

What notifies, and whether it rings, is the bridge's, in `config.toml`. This setup
pings for mentions and direct messages, stays silent overnight, and lets one room
through anyway:

```toml
[[notifications.rule]]
name = "everything"
show = "dm"                    # none | mention | dm | all

[[notifications.rule]]
name = "quiet hours"
when = "22:00-08:00"
show = "none"

[[notifications.rule]]
match = "matrix:4f1c9e2a7b3d5c60/!oncall:example.org"  # narrower than quiet hours, so it gets through
show  = "all"
```

More in [notifications.md](notifications.md).

### Show images in the timeline

By default a picture appears as a text chip, and the view key opens it in an image
viewer. To draw it inline with block characters instead, in `device.toml`:

```toml
[display.media]
mode       = "inline"
detail     = "half"            # "sextant" (default) needs a font with Unicode 13 block glyphs
max_height = 12
```

### Archive noisy rooms, pin the ones that matter today

Archived and Pinned are tags the starter config writes. A config that never mentions
`[[tag]]` has the starter's tags (All, DMs, Unread, Drafts, Pinned, Archived, Spam,
Invites), and one without `[display.rail] order` the starter's order, so a config you
wrote by hand gets them too; `tag = []` means no tags at all, and is what kith writes
when you delete the last one. Archived rooms stop counting
towards unread badges but stay readable; pinned rooms also appear in a Pinned rail
group, as well as where they already live:

```toml
[[tag]]
name            = "Archived"
rule            = ["space:Bots"]
picked          = ["matrix:4f1c9e2a7b3d5c60/!announcements:example.org"]
counts_unread   = false
exclusive       = ["tags", "spaces"]
hide_when_empty = true

[[tag]]
name            = "Pinned"
picked          = ["matrix:4f1c9e2a7b3d5c60/!standup:example.org"]
hide_when_empty = true
```

In the room list, `S` (or `/tag Archived` in the composer) puts the room under the
cursor in a tag or takes it out, writing `picked` or `excluded` for you.

### Follow a network's own archive

Telegram and WhatsApp each have an archive of their own (Telegram's Archived folder,
WhatsApp's archived chats), which every device on the account shares. kith can go
along with it, per network, in either direction:

```toml
[telegram.archive]
follow = true       # a chat archived on Telegram is in the tag (the default)
mirror = true       # filing a chat into the tag in kith archives it on Telegram
tag    = "Archived" # which [[tag]] the archive is

[whatsapp.archive]
follow = true
mirror = false      # the default: kith's archive stays kith's own
```

With `follow` on, a chat the network archived is in the tag without any `picked`
entry, and leaves it when unarchived there. With `mirror` on, `S` or `/tag Archived`
on one of that network's chats archives or unarchives it on the network too, on every
device; with both on the network decides, so kith writes nothing to the tag's lists
for it. With `follow` off, the network's archive is ignored; with `mirror` off, kith's
filing stays in kith (taking a followed chat out writes `excluded`, as for any rule).

### Sort the room list

In `device.toml`:

```toml
[display.rooms]
sort = ["mentions", "unread", "recent", "name"]

[[display.rooms.rule]]
group = "tag:DMs"
sort  = ["drafts", "unread", "name"]
```

### Copy through a command instead of OSC 52

`c` copies with the OSC 52 terminal escape. If your terminal or multiplexer blocks it,
name a command in `device.toml`, which receives the text on stdin:

```toml
[clipboard]
command = "wl-copy"            # or "xclip -selection clipboard", "pbcopy"
```

Inside tmux you can instead keep OSC 52 and add `set -g set-clipboard on` to your
tmux config.

### Bring kith forward when a link is clicked

A `matrix:` or `tg:` link clicked elsewhere goes to the running kith (`kith --open`),
but its window stays where it was. `raise_command` is run by the kith that got the
link, through `sh -c`, so the terminal's own variables name that kith's pane or window
(and `$PPID` is kith). In `device.toml`:

```toml
[clipboard]
raise_command = "xdotool windowactivate $WINDOWID"
```

| Terminal | `raise_command` |
| --- | --- |
| X11, a terminal that sets `$WINDOWID` (xterm, urxvt, Alacritty) | `xdotool windowactivate $WINDOWID` |
| kitty, with `allow_remote_control` in `kitty.conf` | `kitten @ focus-window --match id:$KITTY_WINDOW_ID` |
| wezterm | `wezterm cli activate-pane --pane-id $WEZTERM_PANE` selects kith's tab and pane, but does not raise wezterm's window: follow it with your window manager's command, e.g. `&& i3-msg '[class="org.wezfurlong.wezterm"] focus'` on i3 |
| tmux | `tmux select-window -t $TMUX_PANE && tmux select-pane -t $TMUX_PANE` selects kith's window and pane; add the outer terminal's command to raise it |

It is not run for the link kith was started with, since that terminal was just
opened. A command that fails is reported on the status line.

### Rebind keys

Each value is a comma-separated list of key names, `"-"` unbinds an action, and the
comma key is written `comma`. In `device.toml`:

```toml
[keys]
jump_to = "ctrl+k,ctrl+p"
quit    = "-"                  # then only interrupt, if bound, quits
```

Press `?` in the app to see the resulting keymap. See
[keybindings.md](keybindings.md).
