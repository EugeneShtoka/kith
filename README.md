# kith

**A keyboard-driven [Matrix](https://matrix.org) client for the terminal — with a daemon
that keeps working when the terminal is closed.**

[![CI](https://github.com/EugeneShtoka/kith/actions/workflows/ci.yml/badge.svg)](https://github.com/EugeneShtoka/kith/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.26%2B-00ADD8?logo=go)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

kith is a full daily-driver Matrix client: end-to-end encryption, spaces, threads,
reactions, media and voice notes, in a three-pane terminal UI (spaces rail · room
list · timeline). What sets it apart is what happens around the chat window — an
always-on daemon, an offline index of everything you have received, and a local
rules engine that decides what deserves your attention.

> **Status: early, usable.** kith is used every day, but it is pre-1.0: expect rough
> edges and configuration changes between releases. Linux is the primary platform;
> macOS builds are produced but less exercised.

## Highlights

### It keeps working when the terminal is closed

`kithd` owns the Matrix session, the sync loop, the local cache and the crypto
store. The TUI is a thin client that attaches over a unix socket. Close it and
notifications still arrive, failed sends are retried, scheduled messages go out on
time, and (if you opt in) verification codes are copied to your clipboard — with
nothing open.
[Architecture →](ARCHITECTURE.md)

### Search everything, offline, instantly

Every message — encrypted ones included, once decrypted — lands in a local SQLite
cache with a full-text index. Search the room, the group you are in or the whole
account, filter with `from:alice since:7d until:2026-08-31`, and
jump straight into the conversation at the hit. The same index gives you a list of
every message that **mentions you** (`@`) and every message with a **file** (`gf`) —
including ones you have already read — plus your starred messages and a watch list
of **tracked words** highlighted wherever anyone says them. [Search →](docs/search.md)

### Notifications that you decide, not the server

Notification rules live on your machine: per room, space, sender or thread, with
daily quiet hours, timed and scoped do-not-disturb, per-sound overrides, and per-room
rate limiting that folds a burst into one *"12 new messages"* summary. The narrowest
rule wins, so "the on-call room gets through at night" is just a rule. When something
is quiet, `W` shows the whole rule chain and marks the one that decided.
[Notifications →](docs/notifications.md)

### Spam is a place, not a mute

A conversation you mark — or one your content filters catch — **leaves the group it
was in**: out of the rail, out of every unread count, out of notifications, yet still
readable, searchable and answerable. Promotion rules are individually switchable,
only you demote, `/caught` previews what a filter *would* catch over your history
before you trust it, and `/why` names the rule and filter behind a verdict. Filter
verdicts are stored in your account data, so they survive a cache rebuild and reach
your other machines. [Spam →](docs/spam.md)

### Help writing, in the languages you actually use

Spell-checking as you type with every installed dictionary at once — mixed-language
messages just work, with rare-word detection for languages whose dictionaries accept
almost anything. Word and phrase completion is ranked by what has really been said in
*that* room and shown as inline ghost text, optionally finished by a small **local**
model that the daemon runs on demand and never sends anywhere. Point `[assist]` at any
OpenAI-compatible endpoint for `/summary` of what you missed, `:todo` extraction and
thread naming — it sees only the rooms you allow, never an encrypted one unless you say
so, and `alt+m` shows exactly what a request would send before it goes.
[Composer →](docs/composer.md) · [Assist →](docs/assist.md)

### Your AI assistant can read your chats — under your rules

`kith-mcp` is a [Model Context Protocol](https://modelcontextprotocol.io) server over
the same daemon. Any MCP-capable assistant can list and search rooms, find people and
the rooms they share, read around a message and summarise what is unread — with
encrypted rooms decrypted locally and message reads scoped by an `[agent.read]` allow/deny
list. It can also write, within a separate `[agent.write]` scope that never exceeds what it
may read, but the assistant does not get to choose how: rooms you list
are **sent to**, everything else gets a **draft** in its composer marked with who wrote
it, a per-room cooldown stops loops, and every action is recorded in a ledger
(`kith --agent-log`). [MCP →](docs/mcp.md)

The scope is only as strict as the facts it is decided from. Some of those facts are
other people's: a space's admins decide which rooms are in it, and a DM carries its
peer's chosen name. The scope also cannot stop an assistant's provider from seeing
what you let the assistant read. If you need a boundary no one else can move, run your
own homeserver, name rooms by ID, and use a local model.

### Your scripts are slash commands

Drop an executable into `~/.config/kith/commands/` and it is a slash command — a
`/standup` that drafts your standup, a `/call` that posts a meeting link. Scripts get
the room, the message under the cursor or recent history as input, and their output
goes where you say: the composer for review, straight to the room, a pager, the
clipboard. Bind one to a key if you like. No shell in the chain, and a timeout.
[Commands →](docs/commands.md)

### Pure Go, single binaries, real encryption

Full end-to-end encryption via [mautrix-go](https://github.com/mautrix/go)'s pure-Go
crypto and a pure-Go SQLite driver: `CGO_ENABLED=0`, no libolm, clean cross-compiles.
Interactive SAS verification, server-side key backup that the daemon keeps filled,
and key export/import in the standard format, interoperable with other clients.
Releases are signed and ship with build attestations and an SBOM.
[Encryption →](docs/encryption.md)

## And everything else you expect

- A spaces rail with synthetic **All / DMs / Unread / Drafts** groups, plus Invites,
  Pinned, Spam and Archived when they apply; a room-list sort you define as a chain
- Threads, replies, reactions (frequency-ranked), edits, deletions, stickers, stars,
  and an edit/deletion history view
- Images drawn in text, a voice-note player bar (mpv or VLC) with speed remembered per
  person, video hand-off, attachments with captions
- Drafts that stay with their room across restarts; a send queue; scheduled messages
- Markdown on send and on receive; right-to-left and bidirectional text done properly
- Read receipts and typing notices you control, per space, room or person
- A `ctrl+k` switcher, per-room key sequences (`B`), `ctrl+o`/`ctrl+i` history, and
  `gl` to follow a Matrix link without leaving the client
- Fully configurable keybindings with generated help (a bad keymap never stops the
  client from starting), themes, multiple accounts
- Starts and serves cached history even when the homeserver is down
- `matrix:` / `matrix.to` link handling from the desktop

## Quick start

Install a package, or build from source:

| System | Install |
| --- | --- |
| Arch Linux | `yay -S kith-bin` |
| Debian, Ubuntu, Fedora, Alpine | the `.deb`, `.rpm` or `.apk` from [Releases](https://github.com/EugeneShtoka/kith/releases) |
| macOS | `brew install --cask eugeneshtoka/tap/kith` |
| Nix | `nix profile install github:EugeneShtoka/kith` |
| From source (Go 1.26.3+) | `git clone https://github.com/EugeneShtoka/kith && cd kith && make install` |

```sh
kith login     # prompts for the password; only the access token is kept, in the OS keyring
make deploy      # from a clone: enable and start the daemon under systemd
                 # (a package: systemctl --user enable --now kithd)
kith           # attach and go
```

If no daemon is running, `kith` starts one itself and tells you. Press `?` inside
the client for every keybinding, and `,` for settings.

Full walkthrough, per platform, with profiles and encryption setup:
[Getting started](docs/getting-started.md).

## Documentation

| | |
| --- | --- |
| **Using kith** | [Getting started](docs/getting-started.md) · [Using the client](docs/usage.md) · [Keybindings](docs/keybindings.md) · [Search](docs/search.md) |
| **Features** | [Notifications](docs/notifications.md) · [Spam](docs/spam.md) · [Composer](docs/composer.md) · [Assist (LLM)](docs/assist.md) · [Commands](docs/commands.md) · [MCP server](docs/mcp.md) · [Encryption](docs/encryption.md) |
| **Reference** | [Configuration](docs/configuration.md) · [Command line](docs/cli.md) · [Troubleshooting](docs/troubleshooting.md) |
| **Project** | [Architecture](ARCHITECTURE.md) · [Database](docs/database.md) · [Roadmap](docs/roadmap.md) · [Contributing](CONTRIBUTING.md) · [Security](SECURITY.md) |

The complete, annotated configuration reference is built in:
`kith --print-config`.

## How it is built

- **Elm architecture** on [Bubble Tea](https://github.com/charmbracelet/bubbletea) v2.
- **Enforced layer boundaries** — the UI depends only on an `api.Backend` interface,
  never on the Matrix SDK; `depguard` fails the build if a boundary is crossed.
- **One owner for the crypto state** — the daemon takes an exclusive lock before
  opening any store, because two processes sharing one device's olm/megolm state
  corrupt it. There is deliberately no in-process fallback.
- **Tested and gated** — race-tested, coverage floors, lint, vulnerability and secret
  scanning, and architecture checks on every push. `make check` runs them locally.

See [ARCHITECTURE.md](ARCHITECTURE.md) and [CONTRIBUTING.md](CONTRIBUTING.md).

## Contributing

Issues and pull requests are welcome — please read [CONTRIBUTING.md](CONTRIBUTING.md)
first. Security issues: see [SECURITY.md](SECURITY.md). This project follows a
[Code of Conduct](CODE_OF_CONDUCT.md).

## License

[MIT](LICENSE) © 2026 Eugene Shtoka
