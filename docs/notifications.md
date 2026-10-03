# Notifications

`kithd`, the daemon, decides and delivers notifications. It sees every message whether or not a terminal is attached, so a popup arrives with no `kith` open. The TUI only edits the rules and shows you why something was or was not delivered.

Everything here is configured in `config.toml` (see [configuration](configuration.md)). Most of it can also be changed from inside the app, which writes the file for you and asks the daemon to reload it. A change takes effect on the next message, with no restart.

## Turning notifications on

Notifications are **off by default**.

```toml
[notifications]
enabled = true
desktop = true          # freedesktop D-Bus popup (the built-in sink)
command = ""            # optional hook, run once per notification
```

With `enabled = true` and no rules, you are notified for **mentions**: that is the built-in account-wide rule, which the UI calls "all rooms". Your rules are layered on top of it and never remove it; a rule with no `match` and no `sender` sets the account-wide level (for example `show = "all"`).

### What a popup says

`title` and `body` are templates:

```toml
[notifications]
title = "{space} · {room} · {sender}"   # default
body  = "{body}"                         # default
# Tag bridged chats with the network they came from:
# title = "{space} · {room} · {sender} [{protocol}]"
```

| Placeholder | Value |
| --- | --- |
| `{space}` | The room's space, or tag. If it is in several, the first one by `[display] priority` (a tag reads by its name) |
| `{room}` | The room's name as kith shows it, aliases included |
| `{sender}` | The sender's display name (their MXID if it cannot be resolved) |
| `{mxid}` | The sender's Matrix ID |
| `{body}` | The message text, cut to 140 characters |
| `{protocol}` | The network the sender is on, taken from their MXID: `WhatsApp`, `Telegram`, `Signal`, `Slack`, `Matrix`, … |
| `{date}`, `{time}` | When it was sent, in local time (`2006-01-02`, `15:04`) |

If a field is empty, one separator next to it is dropped too. A room in no space reads `Room · Sender`, not `· Room · Sender`. Unknown `{words}` are left as they are. Set a template to `"-"` to make it deliberately empty.

**Spoilers stay covered.** If the sender marked text as a spoiler, the popup shows `++spoiler++` in its place. A notification appears on a screen that other people may be able to see.

### The command hook

`command` runs through `sh -c` once per notification. The message is passed in **environment variables** and never pasted into the command string, so a message body cannot inject shell code:

| Variable | Contents |
| --- | --- |
| `KITH_TITLE`, `KITH_BODY` | The rendered title and body templates |
| `KITH_SENDER`, `KITH_MXID`, `KITH_ROOM`, `KITH_SPACE`, `KITH_PROTOCOL` | The raw fields |
| `KITH_MESSAGE` | The message text |
| `KITH_DATE`, `KITH_TIME` | The send time |

```toml
[notifications]
desktop = false
command = 'notify-send -a kith "$KITH_TITLE" "$KITH_BODY"'
```

### Popup timeout

```toml
[notifications]
timeout = 15    # seconds (default)
# timeout = -1  # stays up until you dismiss it
# timeout = 0   # your notification daemon's own default
```

## Rules

A rule says **where** it applies, **when** it applies, and **what gets through**. The account-wide default, quiet hours, per-room mutes, "always let this person through" and do-not-disturb are all rules.

```toml
[[notifications.rule]]
name   = "the boss"            # optional; shown when this rule is the one that decided
match  = "!standup:example.org"
sender = "@alice:example.org"
when   = "09:00-18:00"
thread = "any"
show   = "all"
ring   = "all"
sound  = "/home/you/sounds/boss.wav"
```

| Key | Meaning | Values |
| --- | --- | --- |
| `name` | What to call the rule in the status line and the `W` overlay | Any text. Optional |
| `match` | The place it applies to. Empty means anywhere | See [Naming a place](#naming-a-place) |
| `sender` | One person | An MXID. Empty means anyone |
| `when` | A daily window when the rule is in force. It wraps past midnight if the end is before the start | `"HH:MM-HH:MM"`. Absent means always |
| `thread` | Where in a conversation it applies | `any` (default), `participating` (only threads you have written in), `none` (only messages outside threads) |
| `show` | The bar a message must clear to raise a popup | `none`, `mention`, `dm`, `all` |
| `ring` | The bar it must also clear to make a sound. Unset follows `show` | `none`, `mention`, `dm`, `all` |
| `sound` | The sound file to play when it rings | A path |

The levels are cumulative. `mention` lets through messages that mention you (and [tracked words](#tracked-words), if they are allowed to notify). `dm` adds every direct message. `all` lets everything through. `show` is checked before `ring`, so `show = "none"` is silent without also setting `ring`.

A rule only changes the fields it sets. Anything it leaves unset keeps the value from the next less specific rule.

A misspelled level, window or thread value is an error at startup, and on reload. It never falls back quietly: a notification setting that silently does nothing looks exactly like one that works.

### Naming a place

`match` uses the same place vocabulary as every other list in the config. Each entry says what kind of thing it names:

| Entry | Matches |
| --- | --- |
| `!abc:example.org` | That room, by ID |
| `room:Standup` | The room shown under that name |
| `space:Work` | Every room in that space |
| `protocol:WhatsApp` | Every room on that network |
| `dm` / `group` | Every direct message, or every room that is not one |
| `tag:Pinned` | Every room that tag holds, judged on the room alone (its state words match nothing here) |

A bare word that is not a room ID (such as `"Work"`) is **refused at startup**, with an error that names the entry. Room IDs are awkward to type, so it is usually easier to write rules [from the UI](#editing-rules-from-the-ui).

### Which rule wins

**More constraints beat fewer. When two rules have the same number of constraints, the one naming the narrower place wins.** From least to most specific:

1. The account-wide rule (no `match`, no `sender`)
2. A space, a network, a tag, `dm` or `group`
3. A person anywhere (`sender` only)
4. One room
5. A person within a space or class of rooms
6. A person in one room

If two rules land on the same level, a rule with a `thread` clause beats one without. After that, a do-not-disturb mute you just set beats a rule in the file. Among rules still tied, the one later in the file wins.

This ordering is why there is no "pierce" or "break through" setting. Quiet hours and account-wide do-not-disturb are broad rules, so any rule that names a room, a space or a person is narrower and gets through on its own.

### Worked examples

```toml
# The account-wide rule: mentions and DMs.
[[notifications.rule]]
name = "everything"
show = "dm"

# Quiet hours: the same rule on a schedule.
[[notifications.rule]]
name = "quiet hours"
when = "22:00-08:00"
show = "none"                  # or "mention" to let mentions through overnight

# A whole space: every message.
[[notifications.rule]]
match = "space:Work"
show  = "all"

# Mute one room…
[[notifications.rule]]
match = "!standup:example.org"
show  = "none"

# …except for one person in it. This rule is narrower than quiet hours and
# do-not-disturb, so it gets through both.
[[notifications.rule]]
name   = "the boss"
match  = "!standup:example.org"
sender = "@alice:example.org"
show   = "all"
sound  = "/home/you/sounds/boss.wav"

# Seen but not heard: popups without sound for a space.
[[notifications.rule]]
match = "space:Social"
ring  = "none"

# A busy room where only the threads you joined matter.
[[notifications.rule]]
match = "!busy:example.org"
show  = "none"

[[notifications.rule]]
match  = "!busy:example.org"
thread = "participating"
show   = "all"

# Every WhatsApp chat, and the rooms in your Pinned tag through any silence.
[[notifications.rule]]
match = "protocol:WhatsApp"
show  = "all"

[[notifications.rule]]
match = "tag:Pinned"
show  = "all"
```

The last rule turns the Pinned tag into a quick way to let a conversation through tonight's quiet hours: `/tag Pinned` (or `S`) puts the room in it or takes it out.

## Quiet hours

Quiet hours are a rule with a `when` window, so they can apply to the whole account or to one place:

```toml
[[notifications.rule]]
name  = "work, overnight"
match = "space:Work"
when  = "19:00-09:00"
show  = "none"
```

The start of the window is inclusive and the end is exclusive. `22:00-08:00` covers the late evening and the early morning.

## Do-not-disturb

Do-not-disturb is not in the config file. You turn it on for an afternoon, not permanently.

| Key | Effect |
| --- | --- |
| `ctrl+n` | **Hide**: no popups at all (a temporary `show = none`) |
| `alt+n` | **Mute sound**: popups still appear, silently (a temporary `ring = none`) |

Both keys work in every mode, including while you type. `:dnd` opens the same chooser as `ctrl+n`. Each key asks two questions:

1. **What:** the room you are in, the sender of the selected message, the room's space, or all notifications. The narrowest choice is listed first.
2. **For how long:** 1, 2, 4, 8 or 24 hours, or until you turn it off.

**Either key, pressed while anything is muted, lifts every mute at once.** Muting the same target again replaces the old mute, so "an hour" followed by "four hours" leaves one mute with the later deadline.

A mute is a rule with a deadline, and it ranks like any other rule. An account-wide mute is the broadest rule there is, so narrower rules (a room, a person, a tag) still get through it. A mute on one room beats that room's own rule in the file.

Do-not-disturb lives in `kithd`, so every attached terminal sees the same state. **A daemon restart clears it.** A mute that survived a restart could leave you unreachable without knowing why. The status line shows a countdown for an account-wide mute and a count for scoped ones.

## Rate limiting and burst summaries

A busy group can produce dozens of correct notifications in a few minutes. `max_per_room` caps how many popups one room may raise in each `rate_window`:

```toml
[notifications]
max_per_room = 5      # 0 (default) turns the limit off
rate_window  = "2m"   # default when max_per_room is set
```

Beyond the limit, messages are **counted, not dropped**. At most once per window, the count goes out as a single silent popup, "12 new messages", which **replaces** that room's previous summary instead of stacking under it. When the window passes without new messages, the count resets and the next message arrives as itself. Every held message is covered by the next summary.

## Sounds

```toml
[notifications]
sound = "/usr/share/sounds/freedesktop/stereo/message-new-instant.oga"  # global; empty = silent
sound_command = ""   # fallback player, e.g. "pw-play" or "paplay"
```

- `sound` is the account-wide default. A rule's own `sound` replaces it for whatever that rule matches, so a space, a room or a person can have a different sound. To silence one scope, use `ring = "none"`.
- The file is passed to your notification daemon as a `sound-file` hint, so the desktop plays it with its own audio settings and nothing extra is started. A silenced notification sends `suppress-sound` instead, which also stops the notification daemon's own sound.
- `sound_command` is for a notification daemon that ignores the hint. It is started with the file path as its only argument, with no shell.

## Why was this silenced?

- The **status line** names the rule behind the current state, using the rule's `name` when it has one. That is why a `name` is worth adding.
- **`W`** (or `/why` in the composer) opens **"Why is it quiet?"** for the room you are looking at, or the open thread. It lists every rule in force from least to most specific, including mutes and their time left, marks the rule that decides with `← decides`, and gives the result, for example "mention notifies, silently". It also counts the rules that depend on who sends the message, because those cannot be evaluated without an actual message.
- If notifications are switched off, or the room is in [Spam](spam.md), the overlay says so first and then shows the rules that would apply otherwise.
- **`,` → "Notification rules…"** lists every rule in your config with room IDs and MXIDs replaced by the names you know them by.

## Editing rules from the UI

Press `b` on the thing you want a rule about:

| Where | `b` offers scopes for |
| --- | --- |
| A message (timeline) | Its sender in this room, the sender anywhere, everyone in this room, and the sender or everything in each of the room's spaces |
| A room (room list) | The room, and everything in each of its spaces |
| A space (rail) | Everything in that space. Built-in groups such as Home, DMs and Unread cannot have rules |

Then pick a preset. The one already in effect is marked:

| Preset | Writes |
| --- | --- |
| Notify me for everything | `show = "all"` (also gets through quiet hours and do-not-disturb) |
| Only when I'm mentioned | `show = "mention"` |
| Mute — never notify | `show = "none"` |
| Seen but not heard | `ring = "none"` |
| Give it its own sound… | `sound = "<path>"` (an empty path removes it) |
| Name this rule… | `name = "<text>"` |
| Remove the rule | Deletes it, so the less specific rules apply again |

There is one rule per target, and each preset adds to it. Anything more complex, such as a `when` window or a `thread` clause, belongs in the config file.

`a` on the same targets gives them a local name instead (see [configuration](configuration.md)). It is a different feature that uses the same pointing gesture.

## After a restart: history does not notify

The first sync after the daemon starts is catch-up: everything that arrived while it was down. **None of it notifies.** Twenty popups about yesterday evening would tell you nothing the room list does not. Once the daemon has caught up, every new message is judged as it arrives.

Your own messages, edits and redactions never notify. Rooms in [Spam](spam.md) never notify, whatever the rules say.

`kith` starts a daemon if none is running, and anything that arrived before that was never notified. Run the daemon as a systemd user service so this does not happen (see [getting started](getting-started.md)).

## Tracked words

Tracked words are highlighted wherever they appear. By default they do not notify. With `notify` on, a message containing one clears the `mention` bar, just like a mention:

```toml
[display.tracked]
words  = ["deploy", "rollback"]
notify = true                 # default false

[[display.tracked.rule]]
words  = ["invoice"]
in     = ["space:Work"]
except = ["room:Noise"]
from   = ["@dana:example.org"]
notify = true                 # overrides the switch above, for these words
```

Rules add together rather than overriding each other. A tracked word does not break through do-not-disturb just because it is tracked. It still has to clear whatever rule is in force, so to have a word reach you at night, write a narrower rule for the place where it matters. See [search](search.md) for the word globs and `/tracked`.

## Verification codes to the clipboard

`kithd` can put a one-time code into your clipboard as it arrives, with no terminal open. This is **off by default** and deliberately hard to turn on by accident, because anyone who can message you can trigger it.

```toml
[clipboard]
command   = "wl-copy"          # required: a daemon has no terminal to copy through
auto_copy = true

[codes]
include = ["protocol:Google Messages", "!sms-bridge:example.org"]   # required
exclude = ["space:Work"]                                              # exclude beats include

# What a code looks like (defaults shown):
min_length    = 4
max_length    = 8
letters       = true
digits        = true
symbols       = ""       # extra characters, e.g. "-_"
require_digit = true
```

The guards:

- **Scoped.** `[codes] include` must name at least one place. With an empty list (which means everywhere), the daemon warns once at startup and copies nothing.
- **Live only.** A code received during catch-up after a restart is never copied, because an expired code pasted over your clipboard costs you something and gains nothing.
- **Needs a graphical session.** The daemon checks for `WAYLAND_DISPLAY` or `DISPLAY` before copying. Without one, it sends the code in a notification instead. A systemd user unit does not inherit these variables. Run `systemctl --user import-environment WAYLAND_DISPLAY DISPLAY DBUS_SESSION_BUS_ADDRESS` or start the unit with your graphical session.
- **Ignores do-not-disturb.** Copying does not interrupt you. It is also not blocked by [Spam](spam.md).
- **Announced.** Each copy raises a notification with the code and what happened to it, as long as notifications are enabled.
- Copied codes end up in your clipboard manager's history like anything else.

`[codes] include`/`exclude` do not affect the `c` key. Pressing `c` on a message always offers the code under the cursor. See [configuration](configuration.md) for `[clipboard]`.
