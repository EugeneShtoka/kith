# Design: network adapters (WhatsApp first)

Status: draft, 2026-10-01. From the whatsmeow spike (branch `spike/whatsmeow`,
`spike/whatsapp/`, its own module, never merged).

## What the spike proved

| Question | Answer |
| --- | --- |
| Builds cgo-free with kith's driver | Yes: whatsmeow's `sqlstore.NewWithDB(db, "sqlite3", …)` over modernc `sqlite`; one static binary, 27 MB. Same `go.mau.fi/util` (v0.10.1) as mautrix, no dependency clash |
| Links without a QR in the terminal | Yes: `PairPhone` gives an 8-character code typed under Linked devices → Link with phone number |
| Receives live | Yes: messages (text, protocol messages: the sender-key handover before a first group message), receipts, push names |
| Sends to the right chat | Yes, to the account's own chat by its LID (`…@lid`); see below |
| Groups, contacts | `GetJoinedGroups`: 19 groups, 1 to 1,024 members, no communities on this account. App-state sync filled 1,065 contacts |
| History sync | **Not measured yet**: it arrives once, right after pairing (re-pair to record it) |
| Coexists with the bridge | Yes: a separate linked device beside mautrix-whatsapp's (WhatsApp allows four) |

Licences (decided 2026-10-01): kith is AGPL-3.0-or-later from PR #14 on (was MIT up
to v0.1.0). whatsmeow is MPL-2.0 but needs go.mau.fi/libsignal, GPL-3.0, which an MIT
core could not link; AGPL-3.0 combines with GPL-3.0 (§13 of both) and with the AGPL
networks (Meta, Google Messages, LinkedIn, Signal's libsignal), so every adapter may run
in-process. Telegram via gotd/td is MIT.

## WhatsApp's model, mapped onto kith's

| WhatsApp | JID shape | kith |
| --- | --- | --- |
| A person (phone) | `359…@s.whatsapp.net` | a user |
| A person (LID, the hidden ID WhatsApp is moving to) | `273…@lid` | the same user; the store maps LID ↔ phone (`whatsmeow_lid_map`) |
| A direct chat | the other person's JID; the account's own is its LID | a DM room |
| A group | `120363…@g.us` | a group room |
| A community | a parent group (`IsParent`), sub-groups carry `LinkedParentJID` | a space with child rooms |
| An announcement group | `IsAnnounce` | a room where only admins write |
| A channel (newsletter) | `…@newsletter` | a read-mostly room; later |
| Status (stories) | `status@broadcast` | not shown; later |
| A message ID | per chat, `3EB0…` | an event ID, unique within its room |

Threads: none in WhatsApp (replies quote). Edits, deletions (`protocol:REVOKE`),
reactions, receipts and typing all exist and map onto what kith already has.

## The core change: IDs say their network

Today a `RoomID`/`EventID`/user ID is a Matrix ID and code relies on its sigils.
Proposal: **Matrix IDs stay bare; every other network's ID carries a prefix**:
`whatsapp:120363…@g.us`, `whatsapp:359…@s.whatsapp.net`, event `whatsapp:3EB0…`.

- No migration: the cache's keys, configs (`[[display.identity]] mxids`, place lists,
  jump targets) and drafts stay valid as they are.
- Unambiguous: a Matrix ID starts with a sigil (`!`, `@`, `$`, `#`); a prefixed one
  never does.
- `domain.NetworkOf(id)` replaces the bridge guess (`domain/bridge.go`) for native
  adapters; bridged Matrix rooms keep the guess.
- Identities: `mxids` is renamed `ids` in PR 1 (confirmed 2026-10-01), the user's config migrated in the same step.
  readable for one release? No: unreleased-compat rule says just rename and migrate the
  user's config).

Places to change (about 30, listed by `grep` in the session; the main ones):

- `domain/place.go`, `domain/message.go:73`: `matrix:`/`matrix.to` links and sigil
  parsing; links become per-network (`whatsapp:` has none, `wa.me` is a phone link).
- `domain/member.go:31`, `domain/assist.go:136`, `tui/deleted.go:75`,
  `tui/view_message.go:687` (`shortSender`): "localpart of an MXID" becomes
  `domain.ShortName(userID)` per network (a phone number, a push name).
- `tui/people.go:56`, `tui/model.go:836`, `domain/jump.go:71`, `kith-mcp/queries.go:424`:
  "is this an MXID / room ID" checks become `domain.IsUserID` / `IsRoomID`.
- `domain/roomfacts.go`: the place vocabulary's bare `!room` entry stays Matrix; a
  prefixed ID is a room too.

## Several accounts per network (decided 2026-10-01: required)

The user wants more than one WhatsApp account in one config. That changes the ID shape
above: `whatsapp:120363…@g.us` is ambiguous once two accounts are both in that group,
and the DM with one person is a *different chat* from each account.

What is per-account and what is not:

| Thing | Per account? | Why |
| --- | --- | --- |
| A room (group or DM) | **yes** | sending must go out through one account; a DM with Bob from A is not the DM from B |
| A message / event ID | **yes** | the same group message reaches both accounts with the same WhatsApp ID; `reactions` is keyed by `event_id` alone (schema.go), so it would collide |
| A person | no | a phone JID / LID is global; Bob is one person whichever account sees him, so identities, colours and `find_people` fold naturally |

ID shape (confirmed 2026-10-01): **`<network>:<account>/<native>`** for rooms and events,
**`<network>:<native>`** for people.

- `whatsapp:359881234567/120363…@g.us` — a group seen from that account
- `whatsapp:359881234567/972501234567@s.whatsapp.net` — that account's DM with a person
- `whatsapp:359881234567/3EB0…` — a message
- `whatsapp:972501234567@s.whatsapp.net` — the person
- `<account>` is the account's **own phone number** (digits), not a config label: it
  is stable across re-pairing and renaming, so cache rows, drafts, starred messages and
  place lists keep pointing at the right chats. `/` never occurs in a JID (a device
  JID's `:12` does, hence `/`, not `:`).
- Matrix stays bare (one Matrix account). A second Matrix account later would take the
  same shape (`matrix:<mxid>/!room…`); nothing here forecloses it.
- The same group seen by two accounts shows as two rooms (labelled by account). Folding
  them into one is a possible later nicety, not a requirement.

Config:

```toml
[[whatsapp.account]]
name  = "bg"            # label in the room list and for `kith login whatsapp bg`
phone = "+359881234567" # pairing needs it anyway; its digits are <account> in IDs
```

- `kith login whatsapp <name>` pairs one account (pairing code). Unknown name → error
  listing the configured ones; one account configured → name optional.
- Sessions: **one** whatsmeow store (`$XDG_DATA_HOME/kith/whatsapp.db`); its
  `sqlstore.Container` already holds many devices (`GetAllDevices`), each keyed by its
  JID. The daemon starts one `whatsmeow.Client` per configured, paired account.
- Removing a stanza stops that account's client; its rooms stay in the cache
  (read-only) until `kith logout whatsapp <name>` unlinks the device and drops them.

What else this touches:

- **"Me" is a set.** Code that asks "is this sender me" compares with one MXID today.
  It becomes `IsMe(userID)` over every connected account's own IDs (phone JID *and*
  LID for each WhatsApp account). Mentions, own-message styling, unread, notifications.
- **The daemon is keyed by the person, not the Matrix account.** `AccountKey(mxid)`
  names the cache, socket, session, ledger, schedule today. Keep that key (it is the
  config's Matrix account) so nothing moves; a WhatsApp-only setup keys by the config
  file instead. Decide in PR 3.
- **Room list shows the account** for non-Matrix rooms when more than one account of
  that network is configured (a small `bg`/`il` tag), and kith-mcp's `list_rooms`
  carries it, so an assistant drafting "to Bob" picks the right account's DM.
- `[agent.read]`/`[agent.write]` place lists can name a whole account
  (`whatsapp:359881234567`) as a place, like a space.

## Formatting reaches the TUI as spans, not Matrix HTML

`domain.Message.HTML` is Matrix's `formatted_body`, parsed in the TUI
(`tui/view_message.go` → `richtext.Parse`, including `data-mx-spoiler`). Move the parse
into the adapter: `domain.Message` carries `richtext.Span`s (wire field), each adapter
converts its own format (Matrix HTML; WhatsApp `*bold*` `_italic_` `~strike~`
```` ```mono``` ````; Telegram entities, already spans). The TUI draws spans only.
Sending already converts in the daemon (`matrix/markdown.go`); each adapter gets its own.

## The adapter seam

`api.Backend` is 16 roles (`internal/api/backend.go`). Split them:

- **Every network**: Sync, Rooms, Timeline, Unread, Reactions, Media, Members, Search,
  Drafts, Assist, Maintenance.
- **Matrix only**: Spaces (communities map onto them later), Membership (invites, bans),
  Threads, Verification, Keys.

`internal/matrix.InProc` stays the Matrix adapter. A new `internal/whatsapp` adapter
implements the shared roles over whatsmeow and writes into the **same cache**
(`internal/db`), so search, drafts, the assistant, notifications and unread work for it
unchanged. The daemon routes a call by the ID's network; a room list is the union.

Account data kith keeps in Matrix (`org.kith.starred`, `org.kith.spam`) stays local for
WhatsApp rooms (the cache tables already mirror it).

## One daemon, several networks

A daemon is one account today (socket and cache keyed by MXID). With adapters it is one
*person*: a Matrix account and a WhatsApp account side by side, one cache, one socket.
Config grows `[whatsapp]` (enabled, the pairing done by `kith login whatsapp`); the
session lives in its own store beside the crypto store
(`$XDG_DATA_HOME/kith/whatsapp-<account>.db`), keys in the keyring like Matrix's.

## Plan (each PR lands working)

1. **IDs say their network and account**: `domain.NetworkOf`, `AccountOf`, `NativeOf`,
   `ShortName`, `IsUserID`/`IsRoomID`; the ~30 call sites; Matrix behaviour unchanged.
2. **Spans on the wire**: formatting parsed in the adapter; the TUI draws spans.
3. **Adapter seam**: the role split; the daemon composes adapters; Matrix the only one.
4. **WhatsApp adapter, behind `[whatsapp] enabled`**: `kith login whatsapp` (pairing
   code), live messages, sending, groups and DMs as rooms, names and contacts.
5. History sync into the cache; media; reactions, edits, deletions, receipts; typing.
6. Communities as spaces; channels.

## Progress

- PR 1 (branch `ids-say-their-network`): written 2026-10-01, see
  `notes/recap-2026-10-01.md`. Account-as-a-place entry (`[agent.read]` naming a whole
  account) deferred to PR 4, where account labels exist.

## Risks and open questions

- WhatsApp's terms forbid unofficial clients; bans are rare but real. Say so in the docs.
- The phone must come online every ~14 days or linked devices are dropped.
- LID migration: the same person appears as phone JID and LID; the adapter must fold
  them (whatsmeow's LID map) or kith shows two people.
- History sync size and pacing: unmeasured (re-pair and record).
- The bridge and a native adapter on the same account would show every chat twice: the
  docs must say to use one or the other per account.
