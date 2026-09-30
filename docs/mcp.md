# kith-mcp: your Matrix account for AI assistants

`kith-mcp` is a [Model Context Protocol](https://modelcontextprotocol.io) server. It gives an MCP-capable assistant, such as Claude Code or Claude Desktop, tools to read your Matrix history and one tool to write, under rules you set in your config.

It is the third client of the daemon, alongside the TUI. It connects to the same unix socket `kithd` already serves and asks the same questions the TUI asks. That has three consequences:

- **Answers come from the local cache**, so they are instant, and kith-mcp never contacts the homeserver itself.
- **Encrypted rooms arrive decrypted**, because the daemon holds the crypto store. For that reason they are **excluded unless you explicitly allow them**.
- **kith-mcp holds no keys and no Matrix code.** Sending goes through the daemon, so a send that fails lands in the daemon's retry queue like any other.

Nothing starts until an assistant launches the binary. The session lasts as long as the assistant keeps it open.

## Before you start

- `kith-mcp` is built and installed alongside the other two binaries (`make build`, `make install`).
- **The daemon must be running** for the account you want to serve. Start it with the systemd user unit, or open `kith` once, which starts it for you. kith-mcp does not start the daemon itself.
- Write the `[agent.read]` and `[agent.write]` tables of your config first (see [Scope](#scope-where-the-assistant-may-read-and-write)). **By default nothing is shared**: with `[agent.read] rooms` empty, the assistant can read no room and write nowhere. Every tool says so, naming `[agent.read] rooms`.

```text
kith-mcp [--profile NAME] [--config PATH] [--version]
```

| Flag | Meaning |
| --- | --- |
| `--profile` | Which `[[profile]]` account to serve (default: the first) |
| `--config` | The config file (default: the XDG config directory) |
| `--version` | Print version information and exit |

The server speaks MCP over **stdio**: one JSON-RPC message per line on stdin and stdout, with diagnostics on stderr.

## Registering it with an assistant

Use the absolute path to the binary. An assistant started from a desktop session may not have `~/.local/bin` on its `PATH`.

### Claude Code

```sh
claude mcp add mx -- ~/.local/bin/kith-mcp
claude mcp add kith-work -- ~/.local/bin/kith-mcp --profile work   # a second account
```

### Claude Desktop

Add the server to `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "mx": {
      "command": "/home/alice/.local/bin/kith-mcp",
      "args": ["--profile", "work"]
    }
  }
}
```

### Any other stdio MCP client

Most clients take the same three fields:

```json
{
  "name": "mx",
  "command": "/home/alice/.local/bin/kith-mcp",
  "args": [],
  "transport": "stdio"
}
```

When a session starts, the server sends the assistant instructions: reading comes from a local cache that may be incomplete, a room it cannot see was excluded on purpose, it may write only where `[agent.write]` allows, and whether `send_message` sends or drafts is decided by your configuration, not by the assistant.

## Tools

Eight tools: seven read, one writes. A room argument accepts a room ID or the room's name as the client shows it. Exact matches win. A partial name that matches several rooms is an error that lists the candidates, so the tool never guesses. Every room and person in an answer carries both an ID and a display name.

### Reading

| Tool | Parameters | Returns |
| --- | --- | --- |
| `list_rooms` | `query` (optional, name contains), `limit` (default 40, max 200) | Rooms with unread counts, rooms that have unread messages first; `writable: true` on rooms `send_message` may write to |
| `find_rooms_with` | `people` (required, list of names or Matrix IDs), `limit` (default 10, max 50) | Rooms that **all** the named people are in |
| `find_people` | `query` (required), `limit` (default 10, max 50) | People whose display name or Matrix ID contains the query, from the rooms in scope |
| `read_room` | `room` (required), `limit` (default 40, max 200), `sender` (optional Matrix ID), `thread` (optional: a thread's root, or any message in it) | The latest messages, oldest first; with `thread`, only that thread |
| `search_messages` | `query` (required), `room`, `sender`, `since` (RFC 3339), `limit` (default 20, max 100) | Full-text hits across the cache |
| `read_around` | `room` (required), `event` (required, an event ID), `before` (default 5, max 50), `after` (default 10, max 50) | The conversation around one message |
| `unread_summary` | `limit` (default 20, max 100) | Rooms with unread messages, and how many |

Each message carries `event_id`, `sender`, `sender_name`, `sent` (RFC 3339) and `body`, plus `mine: true` on your own messages, `thread` (the root's event ID) on a message in a thread, and `reply_to` on a reply. A deleted message has the body `(deleted)`.

Search matches terms, not meaning. The tool description tells the assistant to retry with the words someone would actually have typed, in their own language, and to use `read_around` on a hit to see the full exchange.

### Writing: `send_message`

| Parameter | Meaning |
| --- | --- |
| `room` | The room's ID or name |
| `text` | The message, exactly as it should appear; nothing is added to it |
| `thread` | Optional: write into this thread, named by its root or any message in it |
| `reply_to` | Optional: the event ID of the message this answers. A reply to a message in a thread goes into that thread |

A `thread` or `reply_to` must be a message of that room's cached history, or nothing is written: an event of any other room is not found, whatever room it is in. **A thread reply is only ever sent**, never drafted. A draft belongs to the room, not a thread, so where your configuration would draft it (or when both the send and the retry queue fail) it is refused rather than written to the main timeline, and the refusal is recorded.

**The assistant may write only where `[agent.write]` allows**, and that is never wider than what `[agent.read]` allows. The tool's description, as the assistant receives it, spells out your write scope and send list, so it knows where it may write before it tries. A room it may read but not write is refused with an error naming `[agent.write]`. A room outside `[agent.read]` is answered as if it did not exist. Either way nothing is sent or drafted, and the refusal is recorded in the [ledger](#the-audit-trail-kith---agent-log) with the rule that refused it.

**The assistant cannot choose whether the message is sent or drafted.** Your `[agent.write] send` list decides, and the tool has no parameter to override it. The answer reports what happened:

| `action` | What happened |
| --- | --- |
| `sent` | Posted to the room |
| `queued` | The homeserver refused it for now; the daemon is retrying it |
| `drafted` | Left in that room's composer for you to send, or not |

The answer includes a `reason` whenever the result was not a plain send. The assistant is told to pass the outcome on to you rather than assume a message went out.

**Nothing can edit or delete.** kith-mcp can add to a conversation, but it cannot change or remove anything that was already said, including its own messages.

## Scope: where the assistant may read and write

```toml
[agent.read]
rooms     = []      # what it may read; EMPTY SHARES NOTHING
except    = []      # subtracted from what rooms names
encrypted = false   # whether encrypted rooms may be read at all

[agent.write]
rooms     = []      # where it may write at all (a draft or a send); empty = everywhere it may read
except    = []      # deny list, used only when rooms is empty
encrypted = false   # whether it may write into encrypted rooms it can read
send      = []      # of those, rooms that are sent to rather than drafted into
cooldown  = "1m"    # rest after a message goes out to a room
```

Reading and writing are separate tables because the useful policy is usually lopsided: an assistant that reads your whole account to tell you what you missed, and writes only in your own notes room.

**Each scope is narrowed by the one before it.** The assistant may write only in a room it may read, because it could not see the conversation otherwise. `send` applies only to rooms it may write to.

| Question | Answered by |
| --- | --- |
| May it read this room? | `[agent.read]` |
| May it write here at all? | `[agent.read]`, then `[agent.write]` `rooms`/`except`/`encrypted` |
| Sent or drafted? | `[agent.write] send`, then `cooldown` |

The two tables read their lists differently. **Reading is least privilege**; writing reads its lists the way `[assist]` does, inside what reading allows:

| `rooms` | `except` | `[agent.read]` | `[agent.write]` |
| --- | --- | --- | --- |
| non-empty | any | Those rooms, minus `except` | Only those rooms |
| empty | set | Nothing | Every readable room except those |
| empty | empty | Nothing | Every readable room |

**An empty `[agent.read] rooms` shares nothing.** A fresh install exposes no room to an assistant until you list one. Every read tool then refuses with a message naming `[agent.read] rooms`, `list_rooms` returns no rooms with a `note` saying why, and the session instructions and `send_message` description say it up front. To share everything, list both kinds of room: `rooms = ["dm", "group"]`.

With `encrypted = false`, **no encrypted room is in scope, whatever the lists say.** A room whose encryption status cannot be determined is treated as encrypted.

In the same spirit, when the daemon cannot say which spaces a room is in, a scope with `space:` or `protocol:` entries admits no room. The tool reports that and asks the assistant to try again; it does not guess "in no space", which would slip past an `except = ["space:…"]`. A `send` list naming spaces or networks drafts instead of sending. Scopes that name rooms only (`dm`, `group`, `room:`, IDs) are unaffected. The daemon's own model tasks follow the same rule when its cache is off.

**Empty `[agent.write]` lists mean every room the assistant may read.** Least privilege already applies one table up, so by default that is nowhere; list a room for reading and the assistant may draft in it without you saying so twice. Everything outside `send` is a draft that you still have to send yourself. To read without writing anywhere, exclude both kinds of room: `[agent.write] except = ["dm", "group"]`.

**`[agent.write] encrypted` is its own switch.** Nothing in the list vocabulary names "encrypted", so without it you could not say "read my encrypted rooms, but never write in them". Turning on `[agent.read] encrypted` lets the assistant read encrypted rooms; it can write in them only once you turn on `[agent.write] encrypted` too.

Entries use the same vocabulary as every other room list in the config:

| Entry | Matches |
| --- | --- |
| `!abc:example.org` | One room, by ID |
| `room:Standup` | One room, by its displayed name (or ID) |
| `space:Work` | Every room in that space |
| `protocol:WhatsApp` | Every room bridged from that network (`Matrix` for native rooms) |
| `dm` | Every direct message |
| `group` | Every room that is not a direct message |

Two of these are decided by other people:

- **What is in a space** is up to that space's admins. Anyone who can edit "Work" can add a room of yours to it, and `rooms = ["space:Work"]` then shares that room.
- **A name** is a room's own, or, for a DM, the other person's display name, which they can change to anything. A name two rooms share is refused when the assistant asks for it (it must say which, by ID). But `send = ["room:Notes"]` names every room called Notes.

For what matters most, prefer room IDs, and spaces only you administer.

```toml
[agent.read]
rooms  = ["dm", "group"]   # every room…
except = ["dm"]            # …except private conversations
```

```toml
[agent.read]
rooms     = ["space:Work"] # one space and nothing else
encrypted = true           # including its encrypted rooms

[agent.write]
encrypted = true           # and may draft in them too
```

```toml
[agent.write]
rooms = ["room:Notes"]     # read as widely as [agent.read] says, write only in Notes
send  = ["room:Notes"]     # and post there without review
```

**The assistant knows rooms are withheld, never which or what is in them.** `list_rooms`, `find_rooms_with`, `search_messages` and `unread_summary` return `rooms_outside_scope`: how many of your rooms `[agent.read]` leaves out. That number depends only on your config, never on what the assistant asked, so it cannot be used to test whether a word, a name or a person appears in a withheld room. Queries never run against withheld rooms, not even to be counted. Naming a withheld room gets the same answer as naming one that does not exist, and a withheld room is never offered as a candidate for an ambiguous name. The answer does say that rooms outside `[agent.read]` are invisible, so the assistant can tell you it may be looking in the wrong place.

### Warnings about dead write entries

An `[agent.write]` `rooms` or `send` entry that `[agent.read]` rules out can never take effect, because the assistant can never write where it cannot read. That is almost always a mistake, typically a room listed in `send` but not shared for reading, so the binaries **warn at startup** without refusing to start:

- **From the spelling alone**, when no room list is needed: `[agent.read] rooms` is empty; the entry is also in `[agent.read] except`; reading lists only room IDs and not this one; reading lists only other networks; `dm` while reading lists only `group`, or the reverse. Anything this cannot decide, such as a room ID while reading lists a space, stays silent.
- **Against your rooms**, when they are known: every room the entry matches that is outside `[agent.read]`, or encrypted while `[agent.read] encrypted = false`, is counted and a few are named. A room ID that is in a space `[agent.read]` lists is readable and does not warn.

`kith-mcp` and `kith` print the warnings on stderr at startup (`kith` before it takes the screen). `kithd` writes them to its log, checked against the rooms already in its cache.

```text
kith: warning: agent: [agent.write] send names "!abc:example.org", but 1 of the 1 rooms it matches are outside [agent.read], so an assistant can never write there (for example: Family)
```

**People are scoped too.** `find_people`, and the names `find_rooms_with` resolves, come only from people who have posted in a room the read scope allows. Someone seen only in an excluded room is not found, and is never listed as a candidate for an ambiguous name. Names are scoped the same way: a person is matched and shown under the name they use in the allowed rooms, never under a nickname from an excluded one. Who talks in a room is part of what the room is.

`[agent.read]` and `[agent.write]` are separate from `[assist]`, which scopes kith's own [language-model features](assist.md). They use the same vocabulary but are independent lists.

### What the scope protects, and what it does not

The scope is enforced by `kith-mcp`, on what it answers through its tools. It is **a policy for an assistant that uses the tools, not a sandbox.** The daemon's socket serves the whole account to any process running as your user, as the TUI needs it to. An assistant that can also run shell commands as you, such as a coding agent with a terminal, can reach that socket, or your config file, without going through `kith-mcp`.

If an assistant must never see some rooms whatever it does, do not give it a shell as your user: run it in a sandbox or as another user. The scope then holds, because the socket (`0600`) and your config file are unreachable from there. The app writes the config `0600`; a file you created yourself keeps your umask's mode, usually `0644`, and every binary warns while other users can read or write it, until you run `chmod 600` on it.

## Write policy

### `send`: which rooms are posted to

```toml
[agent.write]
send = ["room:Notes", "space:Work"]   # post there, draft everywhere else
# send = []                           # or: draft everywhere (the default)
```

Of the rooms the assistant may write to, a room that `send` names is **sent to**. Every other one gets a **draft**:

- The draft appears in that room's composer, marked with the assistant's name and the time, for example `✎ drafted by claude-code 2m ago`.
- The room joins the rail's **Drafts** group, so a message drafted while the client was closed is visible the next time you look.
- **If you already had a draft in that room, the assistant's text is appended after a blank line.** Your words come first and are never replaced. Every draft write, the client's and the assistant's, is made only over the version its writer read, so neither can drop the other's words:
  - If you have nothing unsaved in that room, the client takes the assistant's addition on its next poll, whether the room is open or not.
  - If you are typing there, your typing stays, and your next pause saves it with the assistant's addition after it; the composer shows the addition and says `the assistant added to this draft`.
  - If you send or clear the draft just as the assistant adds to it, the addition stays behind as the room's draft.
  - If the addition arrives while you are correcting a message, it waits in the draft the composer returns to when the edit ends (sent or canceled).
- If the composer is in the middle of an edit, nothing is written and the assistant is told to try again later.
- A draft already longer than 8,000 characters is not appended to.

**`send` cannot override either scope.** A room that `[agent.read]` or `[agent.write]` excludes, including an encrypted room while the relevant `encrypted = false`, is neither sent to nor drafted into, however `send` names it.

### `cooldown`: stopping a runaway

After a message is sent to a room, that room rests for `cooldown`. A second message inside the window is **drafted instead**, and the answer says why. A loop sends its next message within milliseconds, so any cooldown stops one. A person who asks for two messages a minute apart loses only a keystroke. `"0"` turns the cooldown off.

### Failed sends

If the homeserver refuses a send, the message goes into the daemon's retry queue with its original transaction ID, the same way it does in the client, so a retry cannot create a duplicate. If the queue cannot take it either, the message becomes a draft.

## The audit trail: `kith --agent-log`

Every write is recorded: sent, queued or drafted, with the room, the assistant's name, the text, and the reason for anything other than a plain send. So is every **refused** write, a `send_message` call for a room outside `[agent.read]` or `[agent.write]`, with the rule that refused it. A refusal never counts toward the cooldown. The ledger is a JSON Lines file per account, at `$XDG_STATE_HOME/kith/agent-sends-<account>.jsonl` (under `~/.local/state` by default).

```sh
kith --agent-log
kith --agent-log --profile work
```

```text
2026-09-24T10:02:11+03:00  sent     Notes  [claude-code 2.1.0]  Remember to renew the domain
2026-09-24T10:02:40+03:00  drafted  Notes  [claude-code 2.1.0]  And the certificate
  ↳ a message went to this room 29s ago and [agent.write] cooldown is 1m0s, so this one waits in the composer
2026-09-24T10:03:05+03:00  refused  Board  [claude-code 2.1.0]  Summary of today
  ↳ this room is outside what [agent.write] allows: its rooms/except lists do not admit it

3 entries in /home/alice/.local/state/kith/agent-sends-….jsonl
```

The log is printed oldest first, one entry per line, so `grep` and `tail` work on it. Reading it needs no daemon and no network.

**If the ledger cannot be written, kith-mcp refuses to write at all**, because a send that cannot be recorded cannot be counted toward the cooldown either. Reading tools are unaffected.

## Related

- [Writing messages](composer.md): drafts and the Drafts group
- [Assist](assist.md): kith's own language-model features and their `[assist]` scope
- [Encryption](encryption.md): why the daemon can hand over decrypted history
- [Configuration](configuration.md): profiles and where the config lives
