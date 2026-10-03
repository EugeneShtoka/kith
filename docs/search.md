# Search

kith searches the **local cache** — every message the daemon has stored — with SQLite's full-text index. Search is instant, works offline, and every hit is a message the client can open in place. Nothing is sent to the homeserver.

## Starting a search

| Key or command | Opens | Starts at |
| --- | --- | --- |
| `/` | Message search | Depends on the pane you press it in (below) |
| `ctrl+f` | Message search | Every room |
| `/search [terms]` in the composer | Message search, running the terms at once | The open room |
| `:search` on the command line | Message search | Depends on the pane |

The results take over the timeline pane, with the search box at the bottom. Results appear as you type, newest first. Press `enter` to move from the box to the results, walk them with `j`/`k` (or the arrows, `ctrl+d`/`ctrl+u`, `gg`/`G`), and press `enter` on a hit to open it. `esc` closes the results and puts the conversation back.

### Scopes

A search covers one of three scopes, and the current one is always named in the results title.

| Scope | Covers |
| --- | --- |
| Room | The open room |
| Group | Every room in the rail group you are looking at — a space or a tag |
| Everywhere | Every room in the cache |

Where you start depends on the pane you ask from, read left to right:

| Asked from | Starts at |
| --- | --- |
| The spaces rail | Everywhere |
| The room list | The group |
| The timeline or the composer | The room (the group, if no room is open) |

`tab` cycles room → group → everywhere and re-runs the query without retyping it. It works while you are still typing and after you have moved to the results. The room scope is skipped when no room is open.

An upgraded room is searched as one conversation: searching the new room, or a group containing it, also searches the rooms it replaced.

## What you can type

The box takes **search terms, not a query language**. No input is invalid: punctuation simply doesn't narrow the search.

- **Words are ANDed.** `release notes` finds messages containing both words, in any order.
- **The last word is a prefix.** Results narrow as you type: `deplo` already finds "deploy" and "deployment". Earlier words must match whole words.
- **Quoted phrases** match the words in that order: `"release notes"`. A closed phrase is not prefix-matched.
- **Case and accents are folded.** Matching is case-insensitive, and diacritics on Latin letters are ignored, so `cafe` finds "café". There is no stemming: `deploys` is not `deploy` unless it is the last, prefix-matched word.

### Filters

Two filters are spelled in the query, because their values are open-ended. Filter names are case-insensitive, and a filter can be used with no terms at all — `from:dana since:7d` is a complete search.

| Filter | Matches |
| --- | --- |
| `from:dana` | Messages whose sender's Matrix ID or display name contains `dana`. `from:@dana:example.org` pins one account |
| `since:<when>` | Messages at or after that point |
| `until:<when>` | Messages before that point |

`<when>` is one of:

| Form | Example | Meaning |
| --- | --- | --- |
| A date | `2026-08-01` | That day. `since:` starts at its first moment; `until:` includes the whole day |
| `today`, `yesterday` | `since:yesterday` | The start of that day (`until:` includes it) |
| A count and a unit | `7d`, `2w`, `3m`, `1y`, `12h` | Counted back from now: hours, days, weeks, months, years |

Note that `m` is **months**, not minutes.

Anything that does not parse stays a search term. A half-typed `since:yest` narrows nothing until it becomes `since:yesterday`, rather than showing an error on the way.

### Completing `from:`

Typing `from:` opens a completion popup of people. It lists everyone who has posted **in the scope you are searching**, ranked by how much they have said, followed by the open room's members. Accepting inserts the person's Matrix ID, so two people with the same display name stay distinct. Cycling the scope refreshes the list. The popup uses the [completion keys](keybindings.md#keyscompletion): `ctrl+n`/`ctrl+p` to move, `tab` or `enter` to accept, `esc` to dismiss.

## Lists that use the results pane

These are searches with the question already asked. They need no terms: the list is on screen as soon as it opens, typing narrows it, `tab` changes the scope, and `enter` opens the conversation at that message.

| List | Key | Commands | Starts at | Shows |
| --- | --- | --- | --- | --- |
| Mentions | `@` | `:mentions` (everywhere), `/mentions` (this room) | Everywhere, wherever you press it | Every message that names you |
| Files | `gf` | `:files`, `/files` | The pane's scope, like search | Every message carrying an attachment, with the file name in its own column |
| Starred | — | `:starred`, `/starred` | The pane's scope | Every message you starred with `*` |
| Tracked | — | `:tracked [word]`, `/tracked [word]` | The pane's scope | Messages containing your `[tracked]` words, grouped by word |
| Caught | — | `:caught`, `/caught` | The pane's scope | What your spam filters would catch — see [Spam](spam.md) |

- **Mentions** counts a message as naming you when it carries a mention pill for you (outside any quoted reply), or lists you in its `m.mentions` — except a reply, where clients put the replied-to sender there automatically. A name typed as plain text is not a mention. The mentions list also shows mentions you have already read, which the unread badges cannot.
- **Starred** is a private bookmark list. `*` on a message in the timeline stars or unstars it; inside the starred list, `*` unstars the selected row. Stars are stored in your room account data, so they survive a cache rebuild and reach your other sessions, and nobody in the room sees them.
- **Tracked** with a word (`/tracked deploy`) narrows to that word and drops the grouping. Configure the words under `[tracked]` — see [Configuration](configuration.md).

The room's threads have their own list, `ctrl+t` or `/threads` — a chooser rather than the results pane. See [Usage](usage.md#threads).

## Opening a result

Each row shows the date and time, the sender, the room (in an everywhere search), and an excerpt with the matched words highlighted.

`enter` opens the hit's room with the message selected, and records the jump so `ctrl+o` takes you back. A hit inside a thread opens that thread. If the message has left the cache since the search ran, the status line says so instead of jumping to the wrong place.

## What is indexed

The index is the cache, so a message is searchable exactly when the daemon has stored it.

| Included | Not included |
| --- | --- |
| Messages received since you logged in, and history loaded by scrolling back | History older than anything the cache has loaded |
| Encrypted messages, once decrypted — the cache stores them decrypted | Messages that could not be decrypted |
| The current text of an edited message | Earlier versions of an edited message |
| The text of an attachment message — its caption, or the file name when there is none | The contents of the files themselves |
| Every room the cache holds, direct messages included | Deleted (redacted) messages |

To make an old conversation searchable, open the room and press `home`: it keeps loading history until the room's beginning (up to 20,000 messages in one room). Because encrypted rooms are indexed after decryption, the cache — like your key store — is sensitive; see [Database](database.md) and [Encryption](encryption.md).

## Limits

- A search returns at most **200** hits, newest first. When it hits the cap the header says so; narrow the query, the scope or the dates rather than scrolling.
- `from:` completion offers up to 300 people per scope.
- One `from:` per query; if you type two, the last one wins.
- There is no regular-expression, boolean (`OR`, `NOT`) or field syntax beyond the filters above, by design: a chat search box has to survive anything typed into it.
