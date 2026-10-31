# Architecture

> **Status (2026-10-08):** this describes the architecture as built, not a target.
> Four binaries exist and run: `kithd` holds every network's sessions, the SQLite
> cache, the crypto stores and the sync loops; `kith` renders and sends over a unix socket;
> `kith-mcp` is a Model Context Protocol server over the same socket, and `kith-notify`
> shows the daemon's notifications on the desktop it runs on. Every package in
> the layer diagram below is present.
>
> Where this file and the code could drift, the `depguard` rules in `.golangci.yml`
> decide. Read them as the enforced subset: a boundary described here and absent
> there is documentation, not a guarantee.

## Overview

kith is a terminal chat client for [Matrix](https://matrix.org), WhatsApp, Telegram,
Slack, Messenger, LinkedIn and Google Messages, built on the [Bubble Tea](https://github.com/charmbracelet/bubbletea)
Elm-architecture framework (v2 / `charm.land`). Each network is an **adapter** that
speaks to it as a client of its own: [`mautrix-go`](https://github.com/mautrix/go)
for Matrix (sync, the Client-Server API, and **pure-Go end-to-end encryption**
through its goolm implementation), [`whatsmeow`](https://github.com/tulir/whatsmeow)
for WhatsApp (a linked device), [`gotd`](https://github.com/gotd/td) for Telegram,
the web client's API for Slack, and mautrix's bridge libraries in-process for the
rest: `messagix` (mautrix-meta) for Messenger, `linkedingo` (mautrix-linkedin) for
LinkedIn and `libgm` (mautrix-gmessages) for Google Messages. No network is
required, Matrix included. There is no
libolm and no cgo, so every binary builds with `CGO_ENABLED=0` and cross-compiles
cleanly. SQLite, through the pure-Go `modernc.org/sqlite` driver, is the one local
cache every adapter writes into.

The adapters are not switched on by network anywhere. `internal/route` serves them as
one `api.Backend`: a call about a room goes to its network's adapter, a list is every
adapter's, a stream merges every adapter's. What a network can do is a set of small
capability interfaces it implements (history, sending, reactions, leaving, polls…),
declared together in `cmd/kithd/capabilities.go`; one a network lacks is an answer the
router gives, not a branch in the UI. `internal/local` answers what needs no network
at all from the cache: drafts, search, emoji memory, the assistant.

**A chat client is already a client, but it still wants a daemon.** Each network's
server is the server, so no protocol needs a companion process. The user does: a
notification from a process that runs only while you are looking at it is nearly
pointless, and a WhatsApp linked device or a Telegram session only hears what
happens while it is connected. So the connections, the local cache, the crypto
stores **and the notification decision** live in an always-on `kithd`, and the TUI
is a thin client over a unix socket.

That makes the wire contract real. It lives in `api/proto/backend/v1`: one Connect
service carrying every `api.Backend` method, plus the calls that are facts about *this
daemon* rather than about any network (notifications, the send queue, status). See
[The wire](#the-wire).

There is no in-process fallback. Two processes can never share one device's keys
(Matrix's olm/megolm state, WhatsApp's Signal sessions), so a single owner is
enforced by an exclusive `flock` taken before any store is opened.

The daemon holds **no per-client state**, which is what makes attach and detach
trivial and a crashed TUI harmless. Do-not-disturb looks like an exception and is
not: the daemon holds one set of temporary rules for the *account*, not one per
connection. What it knows of a window (the profile whose seat it holds, the room it
last said it has open, when it was last used, which picks the window a clicked link
goes to) lives only while that window's streams are open.

It also holds **no memory of what it missed**. A message notifies if it arrives
while the daemon is running. The catch-up batch after a restart is history, and
history does not interrupt you. That is the one place where "never miss a message"
means *received and cached* rather than *announced*.

## Layered design

```text
cmd/kithd         → the daemon: lock → session → stores → sync loop + socket;
        │              also `backup` and `restore`, which `kith backup|restore` run
cmd/kith          → the client: config.Load → attach to the daemon → Bubble Tea;
        │              also `login`, key import/export, dictionary and model installs
cmd/kith-mcp          → the MCP server (stdio): a third client of the daemon's socket
cmd/kith-notify       → shows the notifications the daemon publishes to its profiles
cmd/kith-relay        → the desktop window's link to the daemon: forwards BackendService
        │              to its socket, or to the [[bridge]] this device joined over
        │              pinned mutual TLS, and answers DeviceService (desktop/, Electron)
        │
internal/tui        → presentation (Bubble Tea models, overlays, pickers)
        │              talks to the world through the api.Backend interface
internal/api        → the Backend interface and its sentinel errors; the generated
        │              wire code lives beneath it in internal/api/backend/v1, and
        │              protoconv converts between it and internal/domain
internal/apitest    → the fakes the TUI's tests run against (api.Backend doubles)
internal/daemon     → both sides of the kithd wire (Connect over a unix socket, and
        │              over pinned mutual TLS to paired devices: [remote] listen),
        │              plus the daemon's own state: readiness, the stream fan-out,
        │              the notification decision, do-not-disturb, the send queue
internal/setup      → config → running structures, shared by every binary
internal/modelsetup → config → the daemon's model layer (prompts, completion server)
internal/route      → every network served as one api.Backend: routes a call by the
        │              room's network, lists and merges streams across adapters
internal/local      → what the daemon answers from its own stores, on any network:
        │              drafts, search, emoji memory, spelling, completion, models
internal/matrix     → the Matrix adapter, over mautrix-go (sync, send, crypto,
        │              verification, key backup, bridge contact lists)
internal/whatsapp   → the WhatsApp adapter, over whatsmeow: each account a linked
        │              device; the only importer of whatsmeow
internal/telegram   → the Telegram adapter, over gotd: each account a phone number
        │              it logs in as (forums as spaces, their topics as rooms)
internal/slack      → the Slack adapter: each account a workspace, signed in as the
        │              web client is
internal/meta       → the Messenger adapter, over messagix: each account a Meta
        │              account, signed in as Messenger Lite is
internal/linkedin   → the LinkedIn adapter, over linkedingo: each account the session
        │              a browser holds
internal/gmessages  → the Google Messages adapter, over libgm: each account an Android
        │              phone it is paired with
internal/readstate  → local-first reading, shared by the networks: the marks a network
        │              has not taken yet, and a read that cannot be kept reads nothing
internal/markdown   → the composer's Markdown to HTML, for every network that sends
        │              formatting
internal/db         → SQLite cache (rooms, timeline, account-data mirrors) + schema
internal/domain     → pure chat types + logic, shared by every network (no SDK,
        │              no I/O, no Bubble Tea)
internal/notify     → the notification decision (pure) + its delivery sinks
internal/schedule   → the durable queue behind messages written now, sent later
internal/backup     → one passphrase-encrypted file of the cache, config and local
        │              records (no sessions), and putting it back under the daemon's lock
internal/session    → the Matrix access token and pickle key, in the OS keyring
        │              (and the device keys the TLS listener pins)
internal/devicekey  → a device's key, its ID and self-signed certificate (crypto only)
internal/pairing    → what both ends of a QR pairing share: token, digits, kith://pair URI
internal/emoji      → emoji by name: the curated set and every Unicode name, generated
internal/agent      → the append-only ledger of what an assistant sent through kith-mcp
internal/media      → the attachment cache: originals on disk, drawn rows beside them
internal/richtext   → the one table of which formatting this client understands,
        │              for both directions: sanitize on receive, render to text+spans
internal/spell      → spelling and rare-word checks over hunspell dictionaries
internal/llm        → one OpenAI-compatible chat endpoint: a prompt, a reply, no state
internal/llamacpp   → the local completion model: spawns and supervises llama-server,
        │              reads its next-token distribution; its catalog of installable
        │              models is internal/llamacpp/models, data only
internal/vocab      → word completion ranked over bounded windows of recent messages
internal/audio      → the voice-note player: mpv over its JSON IPC socket, or VLC
        │              over its line-based remote control
internal/filedialog → the XDG desktop portal, for picking a file to attach
internal/phonebook  → people as Markdown cards in a folder other programs share
internal/qrtext     → a QR code drawn as text, for a phone to scan when pairing
internal/sealeddb   → SQLite encrypted at rest, for the network stores that act as
        │              the account (ncruces + Adiantum, pure Go); keys in the keyring
internal/launch     → starting a terminal for a link the desktop handed over:
        │              a tab in the one already open, a window only when that fails
internal/focus      → the other direction: raising the browser once a link has gone
        │              to it, a per-desktop ladder because there is no portable way
internal/logging    → the one log format (slog text, levels, secret scrubbing, the
        │              rotating log file, a journald slog.Handler over go-systemd's
        │              journal client, auto/journal/file target); imports only human
internal/human      → values formatted for people: a duration as "1m 30s", not "1m0s";
        │              stdlib only, so logging and every layer may use it
internal/themespec  → the theme as data: presets and overrides as color strings,
        │              validated; what every binary's config check needs
internal/theme      → the theme as styles: themespec's colors as lipgloss styles,
        │              for the TUI alone
internal/{config,themespec,theme,buildinfo}  → foundation (import sinks)

cmd/emoji-probe     → an interactive terminal diagnostic: writes glyphs to a tty
                      and asks what you actually saw
tools/              → diagnostics that are not part of any binary
scripts/            → CI, codegen and release helpers
```

The boundaries are **enforced by `depguard`** (see `.golangci.yml`), so a PR that
violates them fails lint rather than review. `make arch-check` adds more gates:

- every package under `internal/` and `cmd/` must be named by at least one rule, because depguard says nothing at all about a package no rule mentions;
- the clients' transitive imports are checked (below);
- no code exists only for tests. `go tool deadcode` fails on a function no binary can reach, and `scripts/testonly.go` fails on an exported function or method that only tests name (deadcode keeps those alive once their type reaches an interface or reflection). A helper only tests need goes in a `_test.go` file of its package (`export_test.go`).
- no duration is printed as Go writes it. `scripts/durationcheck.go` fails on a `time.Duration` handed to `fmt` or `log` as it is ("1m0s"); wrap it in `human.Duration` ("1m"). Log attributes need nothing: `internal/logging` formats every duration value itself.

- **`domain` is pure.** It imports the standard library, `richtext` and a grapheme
  segmenter, and nothing else (an allow list). It is the shared vocabulary; everything
  else depends on it.
- **The stores and the adapter have allow lists.** `daemon` (which every client
  links as the wire) reaches `api`, `config`, `devicekey`, `pairing`, `domain`, `notify`, `setup` and its
  transport; `db` only SQLite and `domain`; `matrix` the SDK, `db` and the helpers it
  drives; `session` and `schedule` only `domain` and their storage (`session` also
  `devicekey`, which is crypto only).
- **Infrastructure is Bubble-Tea-free.** `matrix`, `db`, `domain`, `media`,
  `session`, `notify`, `audio`, `daemon` and `schedule` must not import
  `bubbletea`. The daemon is on that list for the sharpest reason: it runs under
  systemd with no terminal at all.
- **`tui` imports from an allow list.** Through the seam: `api`, `apitest`, `domain`
  and `setup`. Leaf helpers it drives directly: `audio`, `config`, `filedialog`,
  `focus`, `logging`, `media`, `notify`, `richtext` and `theme`. Plus the rendering and text
  libraries. Never `db`, `matrix`, `daemon` or the SDK, and anything not listed is a
  lint failure, so a new dependency is a decision someone writes down.
- **Foundation is a sink.** `config`, `themespec`, `theme` and `buildinfo` each have
  an allow list: the standard library and their own few libraries, and no other
  internal package except `theme` on `themespec`.
- **The seam is a sink too.** `api` and `apitest` reach only `domain`, and `setup`
  only the packages whose types it builds (`audio`, `config`, `domain`, the model
  catalog `llamacpp/models`, `logging`, `notify`, `themespec`). It checks a theme
  through `themespec`, not `theme`, because every binary links `setup` and only the TUI
  draws. What only the daemon runs
  (the model prompts and the completion server's settings) lives in `modelsetup`, so a
  client that needs `setup` does not link a model client or a process supervisor.
  depguard checks *direct* imports, so `make arch-check` also runs
  `scripts/deps-check.sh` over the whole graph: `tui`, `daemon` and `kith-mcp` must not
  link `db`, `matrix`, `session`, `modelsetup`, the SDK or SQLite at any depth,
  `tui` and `kith-mcp` not `llm` or `llamacpp`, and the headless binaries (`kithd`,
  `kith-mcp`) nothing that draws: no Charm library, `tui` or `theme`.
- **The leaves are leaves.** `audio`, `llm`, `llamacpp`, `spell`, `launch` and
  `focus` import the standard library and themselves only; `vocab` adds `domain`,
  `spell` and x/text; `logging` adds go-systemd's
  journal client; `richtext` adds an HTML parser; `filedialog` and `notify` add D-Bus; `agent` adds `domain` and the XDG
  paths; `media` reaches only `domain`. Several of these spawn processes or talk to
  the network, and keeping them unable to see the cache or the config is what makes
  "this cannot leak what it never had" a property of the package rather than a
  promise from its caller.
- **Errors are returned, logged once, and shown with their reason.** Leaves and
  `domain` return errors and never log. `matrix` and `daemon` log what they cannot
  return (background work, a call that failed) through an injected `*slog.Logger`;
  the daemon logs every failed call with its name. The TUI shows a failure's reason
  in the status line and logs it to its file, never to the terminal it draws on.
  Where an error is deliberately dropped, a comment says why.
- **`richtext` is shared on purpose.** `matrix` sanitizes an incoming
  `formatted_body` with it and `tui` draws from the same table of understood tags.
  A client that accepts a tag it cannot draw shows nothing; one that draws a tag it
  never accepted shows something nobody sent. `domain` reads it for the same reason:
  covering a spoiler in a notification is the same question as covering it on screen.
- **The binaries are fenced too.** Each `cmd/` has its own allow list. `kith`
  reaches past the seam for two jobs, both before a daemon exists to ask: `matrix`
  and `session` for login, and `spell` and `llamacpp` for installing a dictionary or
  the completion model. It also uses three leaves: `agent`, to read the assistant's
  write ledger for its log command; `launch`, to open a terminal for a link clicked
  on the desktop; and `logging`. `kithd` may reach the stores but not the
  TUI. `kith-mcp` reaches the daemon client and nothing about Matrix, the cache or the
  terminal: a second opener of a single-writer database is the thing it must never
  become.

`internal/setup` exists because several callers need the same derivation from a
configuration. The daemon builds the policy, the rules and the sinks to *act* on;
the TUI builds the policy to *describe* (the status badge, the note saying what
still gets through); and `kith` builds all of it at startup to reject a bad config
before a login and a full sync. Two that disagreed would be a client saying one
thing while the daemon did another. It also keeps the config dependency out of
`notify` and `domain`, which stay pure value logic.

## The wire

The wire is generated, not hand-rolled. `api/proto/backend/v1/*.proto` is the
schema; `make proto` regenerates `internal/api/backend/v1` (protobuf types) and its
`backendv1connect` package (Connect stubs); and `internal/api/backend/v1/protoconv`
holds the **only** domain↔proto mapping, so both sides of the socket convert through
one place. The generated code is committed, and CI fails if it is stale.

`services.proto` defines one service, `BackendService`. It carries every
`api.Backend` method, grouped by the same roles, and the daemon's own calls: `Status`,
the do-not-disturb rules and `ReloadConfig`, the queue of messages to send later, and
`Follow`/`FollowStream`, which relay a `matrix:` link from `kith --open` to the
attached client. None of those is Matrix; the daemon is simply the process both ends
can always reach. One service means the client and the daemon are deployed together:
an old client cannot talk to a new daemon.

The socket is `0600` and there is no token on the wire: on kithd's own machine the
file permissions are the whole authorization story. `[remote] listen` serves the same
handler, behind the same interceptors, over TLS 1.3 to paired devices only: the
client's key is pinned in the handshake against the device list
(`internal/daemon/devices.go`), a throttle before TLS bounds handshakes per address,
and unpairing a device closes its connections ([SECURITY.md](SECURITY.md#a-listening-kithd)).

A client names the `[[profile]]` it runs in with a `Kith-Profile` header on every
request (none is the first; an unknown one is refused). The daemon narrows every call
about many rooms (lists, counts, search, the unread and invite streams) to the
profile's rooms. Which calls narrow is one table in `internal/daemon/profiles.go`; a
call missing from it is refused, so a new call cannot reach a client undecided. A
client that names a profile is also answered about a room, space or message only when
the profile holds it, as its rail does, and sent only its rooms' values on the streams
of every room (messages, reactions, typing). What each request names is a second
table, of request fields, in `internal/daemon/named.go`, read by the same interceptor
before any handler runs; a field missing from it refuses the call. A client naming no
profile is answered about any room. Nothing about the client is kept beyond its
request.

## The `api.Backend` interface

The `Backend` interface earns its place three times over: it keeps the mautrix-go
SDK and its types out of the presentation layer, it makes the TUI testable against
fakes, and it is the seam the daemon split runs along. The same TUI code drives an
in-process backend or a daemon over a socket without knowing which.

`Backend` embeds one interface per role, in `internal/api/backend.go`: `Sync`,
`Rooms`, `Spaces`, `Timeline`, `Unread`, `Reactions` (emoji ranking
included), `Media`, `Members`, `Membership`, `Search`, `Drafts`, `Threads`,
`Verification`, `Keys`, `Assist` (completion, spelling, the models and what they
install) and `Maintenance`. Both implementations implement all of it, and the TUI
takes the whole `Backend`. Logging in and resuming a session are deliberately not a
role: only the process that owns the session calls them, on `matrix.InProc` (the
daemon at startup, `kith login` in-process), so no socket client can replace the
client the daemon syncs with (`TestTheSocketCannotReplaceTheSession`). A consumer that needs one role takes that role (`api.Keys`
in `cmd/kith`). One that needs a few methods across roles still declares a small
interface of its own (`refreshBackend` and `backupBackend` in `internal/daemon`),
and `cmd/kith-mcp` keeps its `reader`/`writer` deliberately narrower than any role:
they are the whole of what an assistant can ask for.

`internal/api/errors.go` holds the sentinel errors the TUI branches on
(`ErrNoPower`, `ErrNoEncryption`, `ErrEditsRemain`, …). They survive the socket:
the daemon names the sentinel in an `Mx-Sentinel` header on the Connect error and
`Remote` wraps the same sentinel again, so `errors.Is` works the same on both sides.

Two conventions run through the interface:

- **Missing-vs-error semantics.** Lookups return `(value, found bool, err error)`.
  `found=false, err=nil` means "legitimately absent"; a non-nil `err` means the
  lookup itself failed and must not be treated as absent.
- **Compound operations** live behind one method where a naive caller would
  otherwise race (for example "send, then optimistically insert into the local
  timeline").

### Implementations

- **The served backend** (`cmd/kithd`) is the real one: `internal/route`'s router
  over every network's adapter, with `internal/local`'s service beside it over the
  one cache. The Matrix adapter is `InProc` (`internal/matrix`), wrapping a
  `*mautrix.Client` (with its `OlmMachine` for E2EE) and its on-disk stores; the
  WhatsApp, Telegram and Slack adapters wrap their own clients and stores. Only
  `kithd` builds them, and only one process at a time.
- **`Remote`** (`internal/daemon`) is the thin client the TUI runs against: no
  database, no network connection, no crypto store. Each method is a Connect call over
  the daemon's unix socket, and each stream is a subscription re-exposed as the same
  Go channel the served backend hands out, so nothing above `api.Backend` can tell
  the two apart. The daemon reads each of the served backend's channels exactly once and **fans out**
  to every subscriber, because a channel receive removes the value and there is more
  than one consumer: each attached client, and the daemon's own notifier, which
  publishes each notification to its profiles' streams for `kith-notify`. `Remote`
  also carries the surfaces that are *not* part of `api.Backend`, because there is no
  in-process implementation for them: `Status`, the do-not-disturb controls,
  `ReloadConfig`, the schedule queue and `Follow`. The TUI declares the narrow
  interface it needs for those rather than widening `api.Backend`.
- **`Nop`** (`internal/apitest`) is a zero-value double of the whole interface.
  Tests embed it and override only the methods under test.

## Sync model

Each network's connection runs inside the daemon: the Matrix `/sync` long-poll in one
goroutine, and WhatsApp's, Telegram's and Slack's own event connections. Each adapter
publishes on the same streams, `internal/route` merges them into one set of six
channels, and `internal/daemon`'s `Streams` drains each with one reader and copies every value, without blocking, to each subscriber. A client
attaches and detaches without racing for a shared channel. Events reach the UI only
as value messages on the Bubble Tea loop; no network's goroutine ever touches a model.

**Six streams, and the differences between them are deliberate:**

| stream | carries | shape |
| --- | --- | --- |
| `messages` | each message as the cache holds it after a change | whole rows, numbered, read from the cache |
| `unread` | per-room counts, read position, `m.marked_unread` | whole per-room state |
| `reactions` | `m.annotation` adds and removals | deltas |
| `invites` | pending invitations | the **whole current set**: emptiness is the news |
| `verifications` | SAS verification steps | deltas |
| `activity` | who is typing | the whole set per room |

Every one but `messages` is **drop-on-full per subscriber** (`subBuffer`): a slow
reader must never stall the sync loop. A client's `messages` stream reads the cache
instead: the cache numbers every change to a message, and the stream sends the changes
after the last one it sent, woken after each commit (`MessageLog`, `Streams.MessagesChanged`).
A slow reader lags and catches up, losing nothing, and a client resumes from the last
number it holds. The `messages` hub still feeds the in-process notifier. What that costs differs per stream and is stated where it is
decided. A dropped `activity` frame is a typing indicator that clears a moment late,
while `invites` is sent whatever its length precisely because a dropped empty set
would leave an answered invitation on screen.

Only `activity` has no paired cache read. The others each have one
(`CachedTimeline`, `CachedUnread`, `CachedReactions`, `CachedInvites`), so a client
that attaches late renders instantly rather than waiting for the next event. Who was
typing while you were away is not a fact worth keeping.

A seventh hub, `follows`, has no backend channel behind it: an inbound `Follow` rpc
feeds it, and the TUI holds `FollowStream` open to receive links clicked on the
desktop.

When the daemon restarts, every stream dies with it. `Remote` holds the attachment
(backoff plus a socket probe), `Backend.Attached` reports the loss so the TUI can
show it, and on reattach the TUI re-reads what it missed from the caches the daemon
kept warm. Messages are not re-read: `Remote` keeps the last change it delivered and
the message stream resumes after it; when it cannot (the cache was rebuilt or
cleared, or too much changed), it says so, and `Remote` passes that on as
`ChangeMessages` for the TUI to read the open room again.

### Encryption

Encryption comes up in two steps. `InProc.OpenCryptoStore` opens the crypto and
state store (a separate per-user SQLite file, never touched by the client cache) and
installs the room state store on the client. It is local, so the daemon runs it
before serving any RPC, even when the homeserver is down: mautrix reads
`client.StateStore` with no lock after most calls, so it must never change once
handlers run. `InProc.EnableEncryption` then wires mautrix-go's
`cryptohelper.CryptoHelper` (pure-Go goolm) into the client on that store. It needs
the homeserver, so a degraded start runs it once the homeserver answers, while RPCs
are live (`TestEncryptionComesUpUnderLiveRPCs`). Once wired, sends auto-encrypt in encrypted rooms, and incoming
`m.room.encrypted` events decrypt and re-dispatch to the normal message handler;
`Timeline` decrypts scrollback events it has keys for. The crypto store is unlocked by
a persistent **pickle key** kept in the OS keyring (`internal/session`), never on
disk. Setup is non-fatal: without a keyring the client still runs in unencrypted
rooms.

Built on top of that: SAS verification (incoming, and self-verification started
from here), key-backup restore from a recovery key or passphrase, key-backup
bootstrap for an account that has none (cross-signing keys, secret storage and a
server-side backup), and room-key export and import to a file. See
[docs/encryption.md](docs/encryption.md) for the user-facing side.

> **`/messages` omits `room_id`.** Scrollback events must have the fetched room's ID
> stamped on them *before* decryption and sender-name lookup: megolm keys off
> `room_id` to find the session, so an unstamped encrypted event silently decrypts
> to nothing (and its sender collapses to a bare ID).

## Data layer (`internal/db`)

A thin wrapper (`db.Cache`) over one SQLite file per account. It persists the room
list, the space hierarchy, a bounded window of messages per room (with their
formatting, attachments, edit history and deletions), reactions, members,
invitations, unread state and read positions, drafts, usage rankings, and mirrors
of this client's own account data (starred messages, filter moves). The backend
reads it for instant startup and reconciles with the homeserver behind it; live
sync events are cached write-through as they arrive. An FTS5 index over message
bodies makes search instant and offline, and the same index serves as the
vocabulary for word completion.

**The database is a cache.** Every row that matters is a copy of something the
homeserver still has, which is what licenses the upgrade rule: a file whose schema
version this binary does not recognize is copied aside and rebuilt, and the next
sync refills it. Constraints do the bookkeeping: every per-room table cascades from
`rooms(id)`, so leaving a room takes its history with it.

Contributors changing the schema should read **[docs/database.md](docs/database.md)**
first: table by table, the conventions, and exactly how to add a migration.

> **Dual-store atomicity.** kith writes to *two* independent stores, mautrix's
> crypto/state store and this client cache, and no single SQL transaction spans
> both. Any operation that must touch both can partially fail. Keep such compound
> operations behind one `Backend` method and give each a defined recovery path; do
> not assume a transaction can make them atomic. The sync position is the sharpest
> example: it lives in the crypto store, so a rebuilt cache must also reset it
> (`Cache.Rebuilt`), or the client would resume from a token that predates the wipe.

## TUI layer (`internal/tui`)

Strict Elm architecture on Bubble Tea v2, rendered with `charm.land/lipgloss/v2`
(styles live in `internal/theme`). Update does no I/O: calls to the daemon, the player,
the clipboard and the filesystem run as commands. The exceptions are single local
lookups taken where a command would only add a frame of delay: `os.Stat` on a user
command's script, which decides whether submit sends the line (runcmd.go), `exec.LookPath` for the viewer (viewer.go) and
`os.UserHomeDir` for `~` in attach paths (attach.go). The frame is a **three-pane layout**: a spaces
**rail**, the **room list** for the selected rail group, and the **timeline +
composer**, with focus moving between panes.

- **Rail**: the groups that filter the room list. Synthetic **All**, **DMs** and
  **Unread**, one entry per joined space, and conditional groups (Invites,
  Archived, Drafts, Pinned, Spam) that appear only when they have something in them.
  Renamed, reordered and hidden entries come from `[display.rail]`. kithd builds
  it per profile (`Rail`, re-read on `CHANGE_RAIL`); the TUI draws what it serves.
- **Room list**: the selected group's rooms with unread badges and, beneath a room,
  a row per unread **thread**.
- **Timeline**: the message list, bottom-aligned, with a modal message cursor. Rows
  carry a timestamp, the resolved sender name coloured per sender, and the body;
  reactions, an inline picture and a thread summary hang beneath. Replies quote
  their target beside the sender.
- **Composer**: a growing multi-line field with a shared line editor (caret, word
  motions, undo, paste) that every text field in the client uses. While typing, any
  key that produces text types it, so no binding can make a character unreachable.
- **Overlays**: one picker machinery for every chooser (emoji grid, people, links,
  rules, settings, threads, …), one-line prompts, the `?` help generated from the
  keymap, an SAS verification overlay, and a voice-note player bar that does *not*
  own the keyboard.

> **Every row must be exactly as wide as the client measured it.** A row one column
> over wraps, shifts every row below it, and (because the renderer only rewrites
> lines it believes changed) those cells survive into later frames, which reads as
> messages duplicated or missing. Two consequences: RTL runs are reordered by
> **grapheme cluster** (`reverseClusters`), because reversing the runes of 🙋‍♀️
> produces characters that are no longer one sequence and no longer that width; and
> `repaint()` (a full clear) is spent wherever a whole pane is replaced at once, with
> `ctrl+l` as the manual escape hatch. `cmd/emoji-probe` and `tools/pty-drive.py`
> are how this is checked against a real terminal (see
> [CONTRIBUTING.md](CONTRIBUTING.md#diagnostics-that-a-test-cannot-replace)).

## One instance per daemon (multi-tenancy)

One daemon serves every account its config has, on every network: several WhatsApp
numbers, Telegram accounts, Slack workspaces and Matrix accounts share its one cache,
so search, mentions and the room list span all of them. What is kept apart is the
**instance**: two configs (a test one beside your own, say) are **two daemons that
share nothing**. Every per-instance file is named by the instance ID,
`[storage] instance`, which kith chooses on first run and writes into the config (an
install from before instances kept its files under `domain.AccountKey`, the first 8
bytes of `sha256(MXID)` hex-encoded, and goes on using that). There is no instance
column anywhere:

| resource | path (default directories) | derived in |
| --- | --- | --- |
| message cache, every network | `$XDG_DATA_HOME/kith/cache-<instance>.db` | `domain.Storage` |
| Matrix crypto store, one per account (`<account>`: a hash of its Matrix ID) | `$XDG_DATA_HOME/kith/crypto-<instance>-<account>.db` | `domain.Storage` |
| WhatsApp devices and Signal sessions (encrypted at rest) | `$XDG_DATA_HOME/kith/whatsapp-<instance>.db` | `domain.Storage` |
| Messenger's encrypted chats' devices (encrypted at rest) | `$XDG_DATA_HOME/kith/messenger-<instance>.db` | `domain.Storage` |
| Signal's linked devices (encrypted at rest), opened by `kith-signal` | `$XDG_DATA_HOME/kith/signal-<instance>.db` | `domain.Storage` |
| Telegram update positions | `$XDG_DATA_HOME/kith/telegram-<instance>.db` | `domain.Storage` |
| socket | `$XDG_RUNTIME_DIR/kith/<instance>.sock` | `domain.Storage` |
| the Signal helper's socket (kithd listens, `kith-signal` is handed it) | `$XDG_RUNTIME_DIR/kith/signal-<instance>.sock` | `domain.Storage` |
| exclusive lock | `$XDG_RUNTIME_DIR/kith/<instance>.lock` | `domain.Storage` |
| scheduled messages | `$XDG_STATE_HOME/kith/scheduled-<instance>.toml` | `domain.Storage` |
| assistant ledger | `$XDG_STATE_HOME/kith/agent-sends-<instance>.jsonl` | `domain.Storage` |
| Matrix access token, pickle key, per account | OS keyring, service `keyring_service`, `<instance>\|matrix\|<account>` | `session` |
| WhatsApp's, Messenger's and Signal's store keys | OS keyring, service `keyring_service`, `<instance>\|whatsapp-store`, `\|messenger-store`, `\|signal-store` | `session` |
| Telegram, Messenger, LinkedIn and Google Messages sessions, Slack token and cookie | OS keyring, under each account | their adapters |
| **notifications, DND** | the daemon's own state | per daemon |
| **search, mentions, unread, threads, colours, emoji ranking** | that cache | per cache |

So the isolation is structural rather than enforced: search and the mentions list
are SQL against *one* instance's SQLite file, reached over *one* socket, and no query
could see another instance's rows because no table holds them. The file names carry
the instance ID rather than an account name, so a listing of a world-readable runtime
directory does not disclose who is logged in.

Two consequences, both deliberate:

- **There is no cross-instance view.** The TUI holds a single `api.Backend`, so the
  room list, the switcher, search and mentions are one instance's (every network's
  accounts in it, but not another instance's). Seeing two instances at once would be
  a fan-out `Backend` dialing N sockets and merging: a real feature, not a
  configuration.
- **Silence is per instance.** Do-not-disturb writes a temporary rule in the daemon
  you are attached to, so it quiets that instance and not the other.

## Config layer (`internal/config`)

XDG-compliant TOML in two files, each key in the file of whoever reads it. The
bridge's `config.toml` (`config.Config`) is what kithd reads: the accounts, tags,
notification rules, profiles, the assistant's policy. A device's `device.toml`
(`config.Device`) is what only a client reads: the colour palette, keybindings
(comma-separated, with chord sequences), the scripts' bindings, how a notification is
shown; a table split between them keeps its name in both. kithd's types have no
device field, so kithd reading one does not compile, and kithd never opens
`device.toml`. `config.SplitDevice` moves the device keys out of an older
`config.toml` once, run by kithd and every client before loading. `internal/setup`
turns the files into running structures (`Validate` for the bridge's,
`ValidateDevice` for a device's), and a spelling it does not recognize **stops
startup** rather than falling back. In a client whose failure mode is silence, a
quietly ignored setting looks exactly like a working one. The user-facing reference is
[docs/configuration.md](docs/configuration.md); `kith --print-config` and
`kith --print-device-config` print the annotated default files.

- **Theme.** `[tui.theme]` picks one of four palettes (`cobalt2`, `gruvbox`,
  `nord`, and `terminal`, which uses the ANSI colours so the client wears the
  terminal's own scheme) and overrides any of nine roles. Nine colours with nine
  jobs is the whole model; there is no per-widget styling.
- **Written back, not just read.** In-app settings edits go through one writer per
  file that writes only what differs from the defaults, so the user's values survive
  and the surrounding comments do not; the first rewrite leaves a `.bak` of the
  hand-written file. The daemon writes `config.toml` and re-reads it on `ReloadConfig`,
  and a file that will not parse is reported back rather than half-applied. A client
  writes `device.toml` itself (`config.UpdateDevice`): only the settings it changed,
  onto the file as it is now, under a lock, so another window's change is kept.

> **Secrets vs. preferences.** The config file holds **no secrets**: only the
> homeserver URL, user ID and preferences, so it is safe to share. The **password**
> is never stored: `kith login` prompts for it with echo off, passes it straight to
> the login call and discards it, and fails rather than reading it from a non-TTY.
> The durable credential, the access token, is kept by `internal/session` in the
> **OS secret store** (Keychain / Secret Service / Credential Manager) through a
> cgo-free keyring. Where no secret store exists the token is **not written to disk
> by default**: the session is simply not persisted. A user can opt into a `0600`
> file fallback with `allow_token_file = true`, and the store clears that plaintext
> copy as soon as a keyring becomes usable. An API key for a model endpoint follows
> the same rule: the config names a keyring entry, never the key.
