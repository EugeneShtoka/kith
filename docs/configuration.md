# Configuration

kith reads one TOML file. Only the account is required, and every other setting has
a working default. This page covers where the file lives, how it gets written, what
each section is for, and some common tweaks.

For the complete reference, with every option and its default documented inline, run
`kith --print-config`. It prints the same annotated file kith writes on first run.
The source copy is
[`internal/config/default.toml`](../internal/config/default.toml).

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
| `terminal` | `""` (detect) | What `kith --open` starts when a `matrix:`, `tg:` or `whatsapp:` link arrives and no client is running. See [getting-started.md](getting-started.md#open-matrix-tg-whatsapp-and-signal-links-in-kith). |

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
name directly (a link, a jump) still opens. Without `--profile`, kith runs in the
first. `gP` or `:profile <name>` switches the window to another; the status line names
the profile once there is more than one.

Each profile has a window of its own: two terminals can run `work` and `home` side by
side, and a second window in a profile already open is refused, as is switching into
one. A link clicked on the desktop (`kith --open`) goes to one window: the one with
the link's chat open, else the one used last whose profile holds it, else the one used
last. kithd's desktop notifications follow the first too: a room outside it never
notifies, whatever the [rules](notifications.md#rules) say.

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

### Validation

A value kith can't use, such as a misspelled theme preset, a malformed color or an
unknown media mode, **stops startup** with an error that names the key. It doesn't
fall back to a default. In a client whose failure mode is silence, a setting that was
quietly ignored would look exactly like one that works.

Key *names* are checked too: a key kith doesn't read (misspelled, or under the
wrong `[section]` header) stops startup with a list of the unknown keys.

## The settings screen

You rarely need to open the file. Inside kith:

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

A change made in the app is saved to `config.toml` right away:

- **Only settings that differ from the built-in defaults are written.** Anything you
  set yourself is kept, anything at its default is left out, and a later change to a
  default reaches you instead of being frozen in your file. A short file can still be
  a fully configured one.
- **Comments are not kept.** The rewritten file has a short header, then your values.
  The inline documentation is always available from `kith --print-config`.
- **The first rewrite leaves `config.toml.bak`** beside the file, holding what you
  wrote by hand. Later saves never overwrite it.
- **`kithd` is the one writer.** A window hands its change to the daemon, which
  checks it would run with it (a refused change is never written, and the status line
  says why), writes the file, puts it in force, and tells every open window, which
  shows it at once. `kith login` writes new accounts the same way.
- **A change is made on the file as it was read.** If the file changed meanwhile, in
  another window or by hand, the change is refused rather than written over it: the
  window takes the file as it is now and says your last change was not saved, so you
  can make it again.

When you edit the file by hand, restart `kith` to pick up the change. Settings the
daemon acts on (notifications, spelling, the assistant, deletion handling) take effect
when `kithd` restarts:

```sh
systemctl --user restart kithd
```

## Sections

Each top-level section, and what it's for. The linked page covers the feature, and
`kith --print-config` documents every key.

| Section | Purpose | More |
| --- | --- | --- |
| *(top level)* | `allow_token_file`, `terminal`, `bridge_contacts`. | [getting-started.md](getting-started.md) |
| `[matrix]` | The Matrix accounts (`[[matrix.account]]`) and which is the default. | [Matrix accounts](#matrix-accounts) |
| `[display]` | How the client looks and reads: name widths, mouse, links, the unread line, typing notices, read receipts, what a badge counts, the `priority` of homes, name rules, identities, your own names for things, how times and dates are written (`time_format`, `long_date_format`, `short_date_format`), and the emoji skin tone (`skin_tone`). | [usage.md](usage.md) |
| `[display.theme]` | The color palette: a preset plus per-role overrides. | [Themes](#themes) |
| `[display.theme.networks]` | The color each network's rooms are named in, where the room lists or the rail ask for it. | [usage.md](usage.md) |
| `[display.emoji]` | Emoji set size (`curated`, `standard`, `complete`) and your own shortcodes. | [composer.md](composer.md) |
| `[display.tracked]` | Words highlighted wherever they are said and listed by `/tracked`, optionally with notifications. | [search.md](search.md) |
| `[display.rail]` | Order, dividers and visibility of the spaces rail. | [usage.md](usage.md) |
| `[[tag]]` | Your own groupings of rooms in the rail, by rule, picked and excluded rooms. | [usage.md](usage.md#tags) |
| `[display.rooms]` | The room list's sort chain, globally and per rail group. | [usage.md](usage.md) |
| `[display.deleted]` | Whether deleted messages show as placeholders, and whether their content is kept in the cache. | [usage.md](usage.md) |
| `[display.media]` | Images (placeholder chip or inline), viewer, video player, download folder and layout, the media cache. | [usage.md](usage.md) |
| `[display.media.audio]` | The voice-note player: speed, skip, `mpv` or VLC. | [usage.md](usage.md) |
| `[display.reactions]` | The quick-react palette and how it ranks by your usage. | [usage.md](usage.md) |
| `[display.threads]` | Which threads appear as rows under their room. | [usage.md](usage.md) |
| `[display.direction]` | Which rooms read right to left, mirrored with names on the right: set by place, or guessed per room (`auto`). | [usage.md](usage.md#the-timeline) |
| `[notifications]` | Desktop delivery (D-Bus popup, command hook, sound, timeout, rate limit) and `[[notifications.rule]]`, the rules that decide what notifies. | [notifications.md](notifications.md) |
| `[clipboard]` | The copy command, link opener, window focusing, unattended code copy, copying saved-file paths. | [usage.md](usage.md) |
| `[codes]` | Where verification codes are looked for and what one looks like. | [notifications.md](notifications.md) |
| `[[profile]]` | Named setups, each with its own rooms; kithd's desktop notifications follow the first. | [Profiles](#profiles) |
| `[[filter]]` | Words, a sender or a language that move rooms into a tag (the starter's Spam, or any other). | [filters.md](filters.md) |
| `[composer]` | External editor (`alt+e`), temp-file suffix, Markdown on send. | [composer.md](composer.md) |
| `[spell]` | Spell-checking engine, dictionaries, rare-word detection, check before sending. | [composer.md](composer.md) |
| `[complete]`, `[complete.model]` | Word completion and ghost text, and the local llama.cpp model behind it. | [composer.md](composer.md) |
| `[assist]` and its sub-tables | The optional remote language model for `/summary`, `:todo`, rewrites and thread names: endpoint, scope, budgets, prompts. | [assist.md](assist.md) |
| `[agent.read]`, `[agent.write]` | What `kith-mcp` may read (**nothing until you list rooms** — the one room list where empty means none); where it may write (never wider than what it may read), where it sends instead of drafting, its cooldown. Write entries that reading rules out are warned about at startup. | [mcp.md](mcp.md) |
| `[commands]`, `[[command]]` | Your own slash commands: their folder and timeout; per command, its context, output (compose, send, …) and keys. | [commands.md](commands.md) |
| `[schedule]` | Send-later messages: `overdue_cutoff_hours` (default 24) holds back messages that come due too late. | [usage.md](usage.md) |
| `[log]` | `level`: how much the daemon, the client and `kith-mcp` log (`debug`, `info`, `warn`, `error`; default `info`). `--log-level` and `KITH_LOG_LEVEL` override it. `target`: where the client and a daemon it started log (`auto`, `journal`, `file`; default `auto`, the journal when there is one). `--log-target` and `KITH_LOG_TARGET` override it. | [troubleshooting.md](troubleshooting.md#where-the-log-goes) |
| `[keys]` and `[keys.*]` | Every keybinding, one table per mode. | [keybindings.md](keybindings.md) |

Settings that name a place say which kind with a prefix:

- `space:<name>`: a space by its name (a Telegram forum, a WhatsApp community and a
  Slack workspace are spaces too);
- `tag:<name>`: a tag;
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

`[display.theme]` has one `preset` and nine optional color roles.

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

These snippets go into your `config.toml`. Merge them with any section header you
already have, because TOML allows each `[table]` only once.

### Turn on desktop notifications

Notifications ship off. This setup pings for mentions and direct messages, stays
silent overnight, lets one room through anyway, and plays a sound:

```toml
[notifications]
enabled = true
desktop = true                 # the popup; off unless set
sound   = "/usr/share/sounds/freedesktop/stereo/message-new-instant.oga"

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
viewer. To draw it inline with block characters instead:

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

```toml
[display.rooms]
sort = ["mentions", "unread", "recent", "name"]

[[display.rooms.rule]]
group = "tag:DMs"
sort  = ["drafts", "unread", "name"]
```

### Copy through a command instead of OSC 52

`c` copies with the OSC 52 terminal escape. If your terminal or multiplexer blocks it,
name a command, which receives the text on stdin:

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
(and `$PPID` is kith):

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
comma key is written `comma`:

```toml
[keys]
jump_to = "ctrl+k,ctrl+p"
quit    = "-"                  # then only interrupt, if bound, quits
```

Press `?` in the app to see the resulting keymap. See
[keybindings.md](keybindings.md).
