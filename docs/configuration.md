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

### The account

Two keys at the top level identify the account:

```toml
homeserver = "https://matrix.example.org"   # the client-server API URL
user       = "@alice:example.org"           # your full Matrix ID
```

Other top-level keys:

| Key | Default | Purpose |
| --- | --- | --- |
| `allow_token_file` | `false` | Store the access token in a 0600 file under `~/.local/state/kith/` (one per account) when no OS keyring exists. Use it only on a headless machine. |
| `terminal` | `""` (detect) | What `kith --open` starts when a `matrix:` link arrives and no client is running. See [getting-started.md](getting-started.md#open-matrix-links-in-kith). |

### Profiles

For more than one account, name each one in a `[[profile]]` block instead of setting
`homeserver`/`user` at the top level. Setting both is refused.

```toml
[[profile]]
name       = "personal"
homeserver = "https://matrix.example.org"
user       = "@alice:example.org"

[[profile]]
name       = "work"
homeserver = "https://matrix.work.example"
user       = "@alice:work.example"
```

Select a profile with `--profile <name>`. The match is case-insensitive, and the
first profile is the default. A profile holds only the account; every other section
in the file is shared. See
[getting-started.md](getting-started.md#multiple-accounts) for logins and systemd
units per profile.

### Validation

A value kith can't use, such as a misspelled theme preset, a malformed color or an
unknown media mode, **stops startup** with an error that names the key. It doesn't
fall back to a default. In a client whose failure mode is silence, a setting that was
quietly ignored would look exactly like one that works.

Key *names* are checked too: a key kith doesn't read (misspelled, or under the
wrong `[section]` header) stops startup with a list of the unknown keys.

## The settings screen

You rarely need to open the file. Inside kith:

- `,` opens the settings screen. It lists the preferences with their current values,
  and you change them in place.
- `a` on a room, space, rail group, thread or person gives it your own name.
- `b` on a message, room or space writes a notification rule for it.
- `W` shows every notification rule in force for the current room, in order, and marks
  the one that decided.

The `a` and `b` changes need Matrix and room IDs you never see in the UI. That's why
pointing at the thing in the app is easier than typing it into the file.

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
- After saving, kith asks the daemon to re-read the file, so notification changes
  apply at once. If the daemon rejects the new file, the status line says so.

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
| *(top level)* | The account (`homeserver`, `user`) or `[[profile]]` blocks, `allow_token_file`, `terminal`. | [getting-started.md](getting-started.md) |
| `[display]` | How the client looks and reads: name widths, mouse, links, the unread line, typing notices, read receipts, what a badge counts, archived and pinned rooms, name rules, identities, your own names for things, and the emoji skin tone (`skin_tone`). | [usage.md](usage.md) |
| `[display.theme]` | The color palette: a preset plus per-role overrides. | [Themes](#themes) |
| `[display.emoji]` | Emoji set size (`curated`, `standard`, `complete`) and your own shortcodes. | [composer.md](composer.md) |
| `[display.tracked]` | Words highlighted wherever they are said and listed by `/tracked`, optionally with notifications. | [search.md](search.md) |
| `[display.rail]` | Order, dividers and visibility of the spaces rail. | [usage.md](usage.md) |
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
| `[spam]` | The Spam group: its room lists, the rules that move rooms there, and `[[spam.filter]]`. | [spam.md](spam.md) |
| `[composer]` | External editor (`alt+e`), temp-file suffix, Markdown on send. | [composer.md](composer.md) |
| `[spell]` | Spell-checking engine, dictionaries, rare-word detection, check before sending. | [composer.md](composer.md) |
| `[complete]`, `[complete.model]` | Word completion and ghost text, and the local llama.cpp model behind it. | [composer.md](composer.md) |
| `[assist]` and its sub-tables | The optional remote language model for `/summary`, `:todo`, rewrites and thread names: endpoint, scope, budgets, prompts. | [assist.md](assist.md) |
| `[agent.read]`, `[agent.write]` | What `kith-mcp` may read (**nothing until you list rooms** — the one room list where empty means none); where it may write (never wider than what it may read), where it sends instead of drafting, its cooldown. Write entries that reading rules out are warned about at startup. | [mcp.md](mcp.md) |
| `[commands]` | Your own slash commands: script folder, timeout, and per-command context, output (compose, send, …) and keys. | [commands.md](commands.md) |
| `[schedule]` | Send-later messages: `overdue_cutoff_hours` (default 24) holds back messages that come due too late. | [usage.md](usage.md) |
| `[log]` | `level`: how much the daemon, the client and `kith-mcp` log (`debug`, `info`, `warn`, `error`; default `info`). `--log-level` and `KITH_LOG_LEVEL` override it. `target`: where the client and a daemon it started log (`auto`, `journal`, `file`; default `auto`, the journal when there is one). `--log-target` and `KITH_LOG_TARGET` override it. | [troubleshooting.md](troubleshooting.md#where-the-log-goes) |
| `[keys]` and `[keys.*]` | Every keybinding, one table per mode. | [keybindings.md](keybindings.md) |

Some sections take repeatable blocks for per-place overrides. They name places in one
of three ways.

**The place vocabulary** is used by `[[notifications.rule]] match`,
`[[display.tracked.rule]] in` / `except`, and the room lists `display.archived`,
`[codes] include` / `exclude`, `[spam] rooms` / `except`, `[assist] rooms` / `except` and
`[agent.read]` / `[agent.write]`. An entry is:

- a bare room ID (`!abc:example.org`);
- `room:<name>`, `space:<name>` or `protocol:<network>`;
- `dm`, `group` or `pinned`.

A bare word that isn't a room ID is refused at startup, because an entry that could mean
three things is a typo that never matches. `pinned` is refused in `[assist]` and
`[agent.*]` lists: it changes each time you pin a room, so it cannot bound what an
assistant reaches. Name the rooms or spaces instead. An empty list means every place everywhere
except `[agent.read] rooms`, where it means none.

**A room ID or a space's name, as written**, is what `match` takes in
`[[display.read_rule]]`, `[[display.media.rule]]` and `[[display.threads.rule]]`: for
example `match = "!abc:example.org"` or `match = "Work"`. These are not checked at
startup.

**Their own keys:**

- `[[display.name]]` names its `target`: a room ID, `room:<name>`, `space:<name>`,
  `group:<rail row>` or `thread:<root event ID>`;
- `[[display.rooms.rule]]` names a rail `group`;
- `[[display.identity]]` lists `mxids`;
- `[[spam.filter]]` matches `words` and `from`.

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
match = "!oncall:example.org"  # narrower than quiet hours, so it gets through
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

Archived rooms stop counting towards unread badges but stay readable. Pinned rooms
also appear in a Pinned rail group, as well as where they already live:

```toml
[display]
archived = ["space:Bots", "!announcements:example.org"]
pinned   = ["!standup:example.org"]
```

In the room list, `A` and `P` write these entries for the room under the cursor.

### Sort the room list

```toml
[display.rooms]
sort = ["mentions", "unread", "recent", "name"]

[[display.rooms.rule]]
group = "dms"
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
