# Design: network adapters

Status: **built**, as of 2026-10-24. This started as the design for adding WhatsApp
beside Matrix (2026-10-01); it now describes how every network plugs in, so a new
adapter is written against it. Per-network user docs: [WhatsApp](../docs/whatsapp.md),
[Telegram](../docs/telegram.md), [Slack](../docs/slack.md),
[Signal](../docs/signal.md), [Messenger](../docs/messenger.md),
[Instagram](../docs/instagram.md), [LinkedIn](../docs/linkedin.md),
[Google Messages](../docs/google-messages.md).

## Networks and how each is built

| network | library | in-process? | login |
| --- | --- | --- | --- |
| Matrix | mautrix-go (goolm crypto) | yes | password, OAuth/OIDC, SSO |
| WhatsApp | whatsmeow | yes | pairing code typed on the phone |
| Telegram | gotd/td | yes | api_id and api_hash, phone, code, 2FA password |
| Slack | Slack's web API with a browser session | yes | the browser's session token |
| Messenger | mautrix-meta (`messagix`; E2EE over whatsmeow) | yes | password (Messenger Lite API) |
| Instagram | mautrix-meta | yes | the browser's cookies |
| LinkedIn | mautrix-linkedin (`linkedingo`) | yes | the browser's cookies |
| Google Messages | mautrix-gmessages (`libgm`) | yes | QR pairing with the phone |
| Signal | mautrix-signal (`signalmeow`, over Rust libsignal) | **no**: the `kith-signal` helper | QR, as a linked device |

**Licence.** kith is AGPL-3.0-or-later (MIT up to v0.1.0). The mautrix network
packages are AGPL and go.mau.fi/libsignal is GPL-3.0; AGPL-3.0 combines with both
(§13), so every adapter may run in-process. gotd/td is MIT.

**Why Signal is a helper.** `signalmeow`'s crypto is `libsignalgo`, cgo over Rust
`libsignal_ffi`, and every other kith binary is `CGO_ENABLED=0`. `cmd/kith-signal` is
the one binary that links it. kithd starts it, hands it its socket as fd 3 and the
store key, and keeps it alive with heartbeats (it exits after 10 s without one). It
serves a small `SignalService` over Connect RPC (`internal/api/signal/v1`); kithd's
`internal/signal` is an ordinary adapter on top of that, and kithd owns the cache.
Built only with `-tags signal` (`make build-signal`); without it every call is refused
with that instruction. Shipped in the Linux release archives and packages.

## IDs say their network and account

`internal/domain/network.go` (its header comment is the full table).

- **Matrix IDs stay bare** (`!room`, `@user:server`, `$event`): no migration for
  caches, configs and drafts written before other networks existed. Anything without a
  known prefix is Matrix; a Matrix ID starts with a sigil, a prefixed one never does.
- **Every other network**: rooms and events are `<network>:<account>/<native>`,
  people are `<network>:<native>`. A room and a message are per account (a DM with Bob
  from account A is not the DM from account B; the same group message reaches both
  accounts with one native ID). A person is global, so identities, colours and
  `find_people` fold across accounts.
- **`<account>` is the account's own stable network ID**, never a config label, so
  cache rows, drafts, stars and place lists survive renaming and re-pairing: phone
  digits (WhatsApp), team ID (Slack), user ID (Telegram, Messenger, Instagram),
  profile ID (LinkedIn), ACI (Signal), a hash of the paired phone's ID (Google
  Messages). The separator is `/`, which never occurs in a native ID.
- **Events unique only within a chat carry the chat**: `slack:<team>/<chan>/<ts>`,
  `telegram:<self>/<peer>/<id>`, `messenger|instagram|linkedin|gmessages:<self>/<thread>/<msg>`,
  `signal:<self>/<chat>/<author>.<millis>`. WhatsApp's message IDs are unique per
  account: `whatsapp:<self>/<msgid>` (a chat is told apart by the `@` of its JID).
- **People exceptions**: `slack:<team>.<user>`; `gmessages:+<number>`; WhatsApp has
  both a phone JID and a LID for one person.

Examples, with fictional numbers:

```
whatsapp:15550100001/120363000000000000@g.us       a group, seen from that account
whatsapp:15550100001/15550100002@s.whatsapp.net    that account's DM with a person
whatsapp:15550100001/3EB0000000000000              a message
whatsapp:15550100002@s.whatsapp.net                the person
```

API: `ParseID` → `ID{Network, Account, Native}`, `NetworkOf`, `IsUserID`,
`IsRoomID`, `IsMatrixUserID`/`IsMatrixRoomID`, `ShortName` (Matrix localpart, a
`+phone` for WhatsApp), `NativeID`/`NativePerson`, and `RoomOwner` (`OwnerOf`,
`Owns`): the slice of the cache each writer reconciles. `domain/bridge.go` still
guesses the network of a *bridged* Matrix room. `[[display.identity]]` lists `ids`
on any network.

## The seam: a router over capability interfaces

`api.Backend` (`internal/api/backend.go`) is what clients see: 19 roles (Sync, Rooms,
Spaces, Timeline, Unread, Reactions, Media, Members, Membership, NewChats, Ending,
Search, Drafts, Threads, Verification, Keys, Assist, Maintenance, Identity). kithd
(`cmd/kithd/main.go`) serves it as:

- **`route.Router`** (`internal/route`) over one adapter per network. A room call
  goes to the room's network (`NetworkOf`); a list is gathered across adapters.
- **`local.Service`** (`internal/local`) for what only needs the cache or an engine:
  drafts, search, assist, starred, the blocked list, clearing the cache.

An adapter implements the minimal `route.Adapter` (Start, Stop, Messages, Activity,
Unread, Reactions, Me) and then **only the capabilities its network has**, as small
interfaces (`internal/route/route.go`). There is no per-network switch: a network
without a capability answers `api.ErrNotOnNetwork`, and roles only one network can
fill (verification, key backup, creating a Matrix room) answer `api.ErrNetworkOff`
when it is not running. `cmd/kithd/capabilities.go` asserts each adapter's set at
compile time.

| capability | networks |
| --- | --- |
| rooms, counts, read state, history, people, encryption flag, spaces listed, sending, deleting, reacting, typing, media both ways | all |
| signing out (`SignOuter`) | all except Matrix |
| leaving a chat | WhatsApp, Telegram, Messenger, LinkedIn, Signal |
| starting a chat | WhatsApp, Slack, Telegram, Signal |
| polls: reading and voting | WhatsApp, Telegram, Signal (Matrix through its own role) |
| polls: creating | Matrix |
| archive mirror | WhatsApp, Telegram |
| deleting a chat, folders | Telegram |
| stars kept on the network | Matrix, WhatsApp |
| threads | Matrix, Slack |
| block list (`Blocker`) | WhatsApp, Telegram |
| sessions, resync, spaces edited, membership, verification, key backup, ignore list | Matrix |

kithd wraps each adapter as a `network` (`cmd/kithd/networks.go`): its config, its
status line, its saved sessions, and the login conversation it leads
(`api.LoginLeader`). Matrix is always constructed, but needs no account: a config
without `homeserver` runs only other networks.

## Formatting reaches clients as spans

`domain.Message` carries `Body` and `Format richtext.Formatted` (stored as kith's
sanitized markup subset); the wire carries text plus spans, never a network's own
format, and the TUI only draws spans. Each adapter converts its own:

- in: Matrix `formatted_body` (sanitized, reply fallback removed); WhatsApp and Slack
  markers (`*bold*`, `_italic_`, `~strike~`, ```` ``` ````); Telegram entities and
  Signal ranges as nested spans. Messenger, Instagram, LinkedIn and Google Messages
  are plain text.
- out: Markdown typed in the composer is converted per network (Matrix HTML, markers,
  entities, ranges), from one parser (`internal/markdown`).

## Several accounts per network

Every network has the same config shape:

```toml
[[whatsapp.account]]
name  = "personal"       # label in the room list, and for `kith login whatsapp personal`
phone = "+15550100001"   # pairing needs it
```

Telegram adds `phone`, Slack `workspace`; the others take only `name`. Matrix stays
top-level (`homeserver`, `user`), with `[[profile]]` for a second one.

- `kith login [network [account]]` (or `:login` inside kith): the daemon leads the
  steps. One account configured makes the name optional; an unknown name is an error
  listing the configured ones; with none, it sets one up and writes it into the
  config, which the daemon reloads without a restart.
- Signing out is `L` on the account's space: the network is told (device unlinked,
  session ended), the stored session is deleted, and the account's rooms go only if
  asked. The account stays in the config.
- The same group seen by two accounts is two rooms, labelled by account.

## "Me" is a set

Each adapter's `Me()` returns its own IDs: every connected account, and on WhatsApp
both the phone JID and the LID; on Matrix the account plus any `[[display.identity]]`
merged with it. The router unions them (`Router.Me`, exposed as `Identity.Selves`).
Mentions, own-message styling, unread and notifications all ask the set, never one ID.

## Storage

The daemon is keyed by a **storage instance**, not an account: `[storage] instance`,
or a hash of the config file's path (an older install keeps its Matrix-derived key).
Socket, lock and every file hang off it: `<data>/<kind>-<instance>.db`.

| store | what | at rest |
| --- | --- | --- |
| `cache-` | the message cache, every network | modernc SQLite |
| `crypto-` | Matrix Olm/Megolm | modernc SQLite; secrets pickled with a keyring key |
| `whatsapp-` | whatsmeow devices, every WhatsApp account in one container | **sealeddb** |
| `messenger-` | Messenger's E2EE devices | **sealeddb** |
| `signal-` | signalmeow's store (opened by `kith-signal`) | **sealeddb** |
| `telegram-` | update state, access hashes, backfill progress (nothing secret) | modernc SQLite |
| keyring only | Slack, LinkedIn, Instagram, Google Messages and Telegram sessions | keyring |

**Why two SQLite drivers.** The WhatsApp, Messenger and Signal stores hold identity
keys, prekeys and sessions: whoever copies them can impersonate the account and read
future messages. Those libraries write their own SQL, so the encryption has to sit
under SQLite, not in front of it. modernc has no encrypting VFS, and SQLCipher needs
cgo. `internal/sealeddb` is ncruces/go-sqlite3 (SQLite translated to Go from
WebAssembly) with the Adiantum VFS: every page and the WAL encrypted, the 32-byte key
only in the OS keyring, still pure Go, and no memory mapped writable and executable
(kithd's unit sets `MemoryDenyWriteExecute`). It is used only for those key stores;
the cache stays on modernc.

## Adding a network

1. A spike on a branch, in its own module: log in, receive, send, history.
2. Its prefix and native ID shapes in `domain/network.go`, with `<account>` the
   network's own stable ID.
3. An adapter: `route.Adapter`, then each capability the network has; assertions in
   `cmd/kithd/capabilities.go`. Its formatting conversion both ways.
4. Its config section (`[[<network>.account]]`), its login conversation, and its
   session in the keyring or in a sealeddb store if it holds keys.
5. A page in `docs/`, saying what works and what does not.

## Known risks

- WhatsApp's, Meta's and LinkedIn's terms forbid unofficial clients; bans are rare
  but real. The network pages say so.
- WhatsApp drops linked devices whose phone has not been online for about 14 days;
  Google Messages relays everything through the phone, which must stay online and
  has one web session at a time.
- Meta changes its private APIs often; its adapter has the most churn.
- A bridge and a native adapter on the same account show every chat twice: use one
  or the other per account.
