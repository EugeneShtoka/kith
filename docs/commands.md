# Commands

kith has two kinds of commands, and which one you use depends on what the command acts on:

- **`/` commands** are typed at the start of the composer. They act on **the room you are writing in**: its messages, its members, its search.
- **`:` commands** are typed on the command line, which opens when you press `:` anywhere except while typing. They act on **the client as a whole**, or across every room.

Both open a completion list as you type, showing each command's arguments and its keyboard shortcut if it has one. You can also add your own `/` commands by dropping a script into a folder.

## Composer commands (`/`)

| Command | What it does | Key |
| --- | --- | --- |
| `/me <what you are doing>` | Send an emote ("\* you wave") | |
| `/plain <message>` | Send this one message exactly as typed, with no Markdown | |
| `/upload <path>` or `/upload <path> \| <caption>` | Send a file, with an optional caption | `alt+u` |
| `/at <time> <message>` | Send later today, e.g. `/at 09:00 good morning` | |
| `/in <delay> <message>` | Send after a delay, e.g. `/in 2h see you then` | |
| `/scheduled` | What is queued to go out in this room; `enter` cancels one | |
| `/summary [span]` | Summarize the room since you last read it, or over a span (needs [assist](assist.md)) | |
| `/search [terms]` | Search this room; `tab` widens the scope | `/` |
| `/mentions` | Messages in this room that name you | `@` |
| `/files` | Messages in this room with a file | `gf` |
| `/threads` | This room's threads | `ctrl+t` |
| `/starred` | What you have starred here; it can unstar too | |
| `/tracked [word]` | Where your tracked words came up here | |
| `/topic` | Read the room's topic in full | `gt` |
| `/why` | Why this room does or does not notify you | `W` |
| `/caught` | What the spam filters catch here | |
| `/spam` | Move this room into Spam, or take it out | `!` |
| `/archive` | Stop this room counting as unread | `A` |
| `/unread` | Mark this room unread | `M` |
| `/pin` | Pin this conversation | `P` |
| `/invite <@user:server>` | Invite someone to this room | |
| `/leave` | Leave this room (asks first) | |

Keys that belong to the room list (`!`, `A`, `M`, `P`) are shown there; the others work from the timeline.

A few rules apply to every `/` command:

- **Only known commands are intercepted.** A message such as `/shrug` that matches no command, built-in or yours, is sent as ordinary text.
- **`//` escapes the slash.** `//etc/hosts is the file` sends `/etc/hosts is the file`.
- **A command is one line.** A multi-line message that happens to start with `/me` is sent as a message.
- **You can separate the argument with a colon:** `/in:2h see you then` works as well as `/in 2h see you then`.
- **A command missing its required argument prints its usage** rather than guessing.

Joining a room is not a composer command, because it acts on a different room from the one you are writing in. Use `:join` or the room list's join key instead.

## Command-line commands (`:`)

Press `:` from anywhere except the composer.

| Command | What it does | Key |
| --- | --- | --- |
| `:go` | Go to a room, person or space by typing its name | `ctrl+k` |
| `:join <#room:server or !id>` | Join a room by address and open it | |
| `:search` | Search; how wide it starts depends on the pane you are in | `/` |
| `:mentions` | Every message that names you, across all rooms | `@` |
| `:files` | Every message with a file | `gf` |
| `:threads` | Threads in scope, newest first | `ctrl+t` |
| `:starred` | Every message you have starred | |
| `:tracked [word]` | Every message with one of your tracked words, grouped by word | |
| `:caught` | Preview what the spam filters would catch | |
| `:scheduled` | Everything queued to send later, in every room; `enter` cancels one | |
| `:todo` | What people are waiting on you for, across rooms (needs [assist](assist.md)) | |
| `:dnd` | Silence notifications, for a while or for one place | `ctrl+n` |
| `:verify` | Ask your other sessions to verify this one | |
| `:settings` | The settings screen | `,` |
| `:help` | Every key and what it does | `?` |

Where the table says the pane decides, the starting scope follows where you pressed `:`. From the rail it is everywhere, from the room list it is that group, and from the timeline it is this room. `tab` widens or narrows the scope in the results. An unknown command name is reported together with the full list.

## Your own commands

Any executable file in `~/.config/kith/commands/` becomes a `/` command named after the file. `/standup` runs `~/.config/kith/commands/standup`, and **whatever the script prints on stdout lands in the composer** for you to read, edit and send, or not.

```sh
mkdir -p ~/.config/kith/commands
$EDITOR ~/.config/kith/commands/standup
chmod +x ~/.config/kith/commands/standup
```

A script appears in the `/` completion list the next time you open it, with no restart needed.

### Precedence and naming

- **Built-in commands win.** A script called `me` never runs, because `/me` is built in.
- **The file must be executable.** A file without the executable bit is treated as a note, not a command.
- **The file must start with a `#!` line.** kith runs the file directly. A file with no shebang fails with an error that says so, even though it may work when you run it from a shell.
- **The name must be a single path segment.** Nothing can reach outside the commands directory.

### How a script is run

There is **no shell anywhere in the chain**. kith runs the file directly, with whatever you typed after the command name as **one argument**, so nothing you type can be interpreted as shell syntax.

| What | Contents |
| --- | --- |
| `$1` | Everything after the command name, as one word (empty if nothing) |
| `$KITH_ARG` | The same |
| `$KITH_ROOM_ID` | The ID of the room you are writing in |
| `$KITH_ROOM` | That room's name as kith shows it |
| `$KITH_USER` | Your own Matrix ID |
| stdin | A JSON object holding whatever context the script declared (see below); `{}` when it declared none |

The script also inherits kith's own environment.

**What happens with the result:**

- Output is trimmed of its trailing newline and **replaces** the composer's contents. Sending it is still a keypress.
- A **non-zero exit, a timeout, or no output** leaves the composer exactly as you typed it, and the status line says why. If the script wrote to stderr, the first line of it is used as the reason.
- Output is capped at 8 KB. Output that had to be cut off is never sent, copied or discarded without you seeing it: it goes to the composer, or to the pager if the pager was already its destination.
- Only one command runs at a time. Pressing enter again while `/zoom` is still running reports that it is running rather than starting a second one.

### Configuration

```toml
[commands]
dir     = ""     # empty: the commands folder beside config.toml
timeout = 15     # seconds before a command is killed and reported
```

**Proposing the text is the default** because a script that misfires should cost you a keystroke, not a message in a room full of people. For scripts whose output you would never edit, such as "start a call and post the link", set `output = "send"` in the command's block (below). The policy lives in the config rather than in the scripts so a script copied from somewhere else cannot grant itself permission to post.

### Giving a script context: `[[commands.script]]`

A block per command lets it receive **what you are looking at**, choose where its output goes, and bind it to a key:

```toml
[[commands.script]]
name   = "who"
needs  = ["history:100"]
output = "pager"
keys   = "g w"
```

**`needs`** declares the context the script receives. Each item is taken from the message under the cursor, or from the newest message if nothing is selected:

| Need | The script receives |
| --- | --- |
| `message` | On stdin, `.message` holds who sent it, what it says and when. In the environment: `$KITH_MESSAGE_ID`, `$KITH_SENDER`, `$KITH_SENDER_NAME` |
| `url` | One `http`/`https` link from that message, in `$KITH_URL` and `.url`. If the message has several links, kith asks which one first |
| `history:n` | The last `n` messages in the room (1–500), oldest first, as `.history` on stdin |

Each message object has these fields: `event_id`, `room_id`, `sender`, `sender_name`, `body`, `timestamp` (RFC 3339), plus `edited`, `deleted` and `mine` when they are true. **A script is given what the screen shows**: a covered spoiler stays covered, and a deleted message is a placeholder. Only the context you declared is present; anything else is absent, not empty.

Bulk context goes on stdin and never on the command line, because a process's command line is readable by every user on the machine while its environment and stdin are not.

**`output`** sets where stdout goes:

| Value | Destination |
| --- | --- |
| `compose` | Replaces the composer (the default) |
| `send` | Straight to the room (output that had to be cut off still goes to the composer) |
| `status` | The first line, on the status bar |
| `pager` | A scrolling read-and-dismiss overlay, for reports |
| `clipboard` | Copied |
| `none` | Discarded; the script's side effect was the point |

**`keys`** runs the command without typing its name. It is written like any other binding, and it works everywhere except while you are typing. A script run from a key gets an empty argument. A key sequence that a built-in already uses is refused. To free one, unbind the built-in first with `"-"`. Two scripts cannot share a key. `?` lists your script bindings together with what each is given.

A `needs` or `output` value kith does not recognize is refused at startup.

## Example scripts

### A Jitsi call link, sent straight to the room

`~/.config/kith/commands/call`:

```sh
#!/bin/sh -eu
# /call [topic]: post a fresh Jitsi link named after this room.
slug=$(printf '%s' "$KITH_ROOM" | tr -cs 'A-Za-z0-9' '-' | tr 'A-Z' 'a-z' | sed 's/^-*//; s/-*$//')
token=$(od -An -N4 -tx4 /dev/urandom | tr -d ' \n')
link="https://meet.jit.si/${slug:-call}-$token"
if [ -n "${1:-}" ]; then
  printf 'Call about %s: %s\n' "$1" "$link"
else
  printf 'Joining a call: %s\n' "$link"
fi
```

```toml
[[commands.script]]
name   = "call"
output = "send"
```

`/call release plan` posts `Call about release plan: https://meet.jit.si/…` immediately. Without `output = "send"`, the line would wait in the composer for you to send.

### A standup template

`~/.config/kith/commands/standup`:

```sh
#!/bin/sh -eu
# /standup [today's plan]: a template to fill in, left in the composer.
printf 'Standup %s\n' "$(date +%F)"
printf '• yesterday: \n'
printf '• today: %s\n' "${1:-}"
printf '• blocked: nothing\n'
```

`/standup review the migration` fills the composer with the template, with today's line already written. You edit the rest and send it. Because the composer handles several lines, the template arrives as one multi-line message.

### Who has been talking: a report in the pager

`~/.config/kith/commands/who` (needs `jq`):

```sh
#!/bin/sh -eu
# /who: message counts per person over the last 100 messages.
jq -r '.history | group_by(.sender_name // .sender)
       | map({who: (.[0].sender_name // .[0].sender), n: length})
       | sort_by(-.n)[] | "\(.n)\t\(.who)"'
```

```toml
[[commands.script]]
name   = "who"
needs  = ["history:100"]
output = "pager"
```

`/who` opens an overlay listing the busiest people first, and nothing is written to the room.

## Keeping secrets out of scripts

Store API tokens in the desktop keyring, not in the script and not in the config:

```sh
printf '%s' 'the-secret' | secret-tool store --label='kith zoom' service kith-zoom key client-secret
```

The script then reads the token back:

```sh
secret=$(secret-tool lookup service kith-zoom key client-secret)
```

Never pass a secret as a command-line argument. Other processes can read it from `/proc`.

## Related

- [Writing messages](composer.md): drafts, scheduling and the send queue
- [Assist](assist.md): `/summary` and `:todo`
- [Keybindings](keybindings.md): binding keys, and unbinding built-ins with `"-"`
- [Search](search.md): scopes, `from:` and `since:`
