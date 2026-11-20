# Roadmap

What is planned, in order. Items land when they are ready, each behind configuration
that defaults to today's behaviour. For what already exists, see
[docs/README.md](../docs/README.md).

**Design docs for the larger initiatives** live beside this file:

- [design-clients.md](design-clients.md): failover between bridges, the desktop's
  partial cache, and an Android client
- [design-mesh.md](design-mesh.md): the mesh's remaining steps: the shared config,
  drafts and verdicts crossing bridges, and key rotation
- [design-mailbox.md](design-mailbox.md): a mesh peer that holds sealed ops it cannot
  read, for bridges that are never online at the same time (decided 2026-11-18: built
  after mesh step 8)
- [design-desktop-cache.md](design-desktop-cache.md): the desktop's partial cache, its
  profile's rooms and a recent window kept by the relay for offline reading and instant
  start (a draft, reopened 2026-11-19 for a design phase of its own)

Every item below is described here in full; nothing depends on notes kept elsewhere.

## Next

Ordered. Anything that makes later work cheaper or safer goes first; anything that
changes a default goes while changing defaults is still free.

| step | items | why these together |
| --- | --- | --- |
| **1a: the rest of the mesh** (design-mesh.md steps 6–8, then the mailbox) | 6: room and thread drafts in the mesh, concurrent versions all kept with the words each was written over, written after 5 s idle or on leaving the room, a draft over 1 MiB kept on its bridge · 7: tag and person verdicts on networks that keep none, the newest winning · 8: `kith devices rotate` for a device or a bridge, a rotation op signed by both keys, a bridge answering on its old key by SNI for a while, clients rewriting their pin · 9: the self-hosted mailbox ([design-mailbox.md](design-mailbox.md)), a bridge admitted by pairing, a lost mesh recovered by a random recovery key | **Steps 1a–1d are multi-device and client topology; see [design-mesh.md](design-mesh.md), [design-mailbox.md](design-mailbox.md) and [design-clients.md](design-clients.md).** Profiles, replay, remote clients, bridges pairing with read positions crossing, the desktop client, every op sealed, between bridges too (5a, #609), and the shared config (5, #613) are built. Each numbered step is its own PR with a failure review. Ops written before #609 reach a mailbox only in a snapshot. Two windows on two bridges, or a window and kith-mcp, can write one draft at once, and no typed or appended word may be lost. Matrix's verdicts stay in account data, which already reaches every bridge. A lost key is not rotated: the device is removed and paired again. |
| **1b: failover between bridges** (Phase 3) | a heartbeat every 30 s with each account alive-as-of and caught up, by the per-network table in design-clients.md · a client's `[[bridge]]` list, by pinned priority, failing over on disconnect or a stale heartbeat · resuming at another bridge from the time last heard, deduped by network ID · sends buffered 3–5 s with an ID that stops a second bridge sending them again · a bridge offered only once verified with its keys restored · standby bridges: one active at a time by device priority, the rest keeping their sessions alive, and per account when no bridge is healthy (each account active on the highest bridge where it is alive) · a client on several bridges · notification IDs from the message · kithd's version in `Status` | The heartbeat table's windows are decided (2026-11-19); LinkedIn's and Meta's are measured as it is built. Bridges reach each other across the internet over Tailscale, which kith documents and does not build ([remote.md](../docs/remote.md)). Standby gives one Google Messages session instead of several taking it from each other, at a WhatsApp and a Signal slot per device given a priority. |
| **1c: the desktop's partial cache** | a design and planning phase first: one cache or two on a machine that runs kithd (a standby kithd's cache falls behind while its window reads another bridge), a cache of answers or a mesh replica, which machines keep one · then the build, by the plan it ends with | Offline reading and instant start. Answered so far, provisional: 500 messages per room, none older than two weeks, 256 MiB per profile; small image previews only; reads made offline kept and sent once the bridge is back, marking unread refused; the store deleted when the bridge refuses this device. Draft: [design-desktop-cache.md](design-desktop-cache.md). |
| **1d: Android client** (Phase 5) | spikes first: WhatsApp, Signal and Google Messages linked beside their official apps on one phone · credentials in Keystore decided · a `buf breaking` check · a UnifiedPush sink in `notify` (encrypted, wake only, a secret endpoint per profile) · a Kotlin app with its own SQLite cache, failing over per 1b, sending only when connected · Matrix single sign-on from a remote client | Takes the most calendar time of step 1. The push carries a wake and nothing else: the app syncs from the bridge over TLS right after. |
| **2: names, everywhere the same way (S4)** | the rest of the name pipeline | **Done** (#599): `[display] first_name_only` (off by default) and `[[display.name_rule]]` (`first_name_only`, `strip`, `name`; per space, room and person, narrowest wins), shaped by kithd for every window and notification. `first_name_only` already resolves against the room's own space (the first by `[display] space_priority` when a room is in several), and identity merge and room aliases are built. What is left is the rest of a global → space → chat pipeline: more rule types beside `first_name_only` (for example stripping a bridge's suffix, or a fixed name for one person in one space), and a per-chat rung that overrides the space's rule. It goes through the single narrowest-wins scope resolver every other setting uses, rather than a resolver of its own. It changes what `first_name_only` means in a room whose space has no rule, so the default is decided before it is built. |
| **3: Matrix sessions** | list an account's other sessions, rename them, sign old ones out | A lost laptop's session can only be ended from another client today. Signing out another session revokes its access token on the homeserver. |

## Follow-ups

Gaps that finished work left, most found by its failure reviews. Not ordered; each is
small and ships on its own.

### Desktop client

- **A thread whose root cannot be fetched** never loads older replies: the root is
  asked for first and not again after a failure.
- **Banners clear only on the next connection check.** A failed page's banner comes
  down when that page is read (#449); every other failure (reactions, receipts, sends,
  search, links, filing) stays until a device or status read succeeds.
- **A draft that still fails to save when the window closes is lost.** On a profile
  switch and a reload it is kept (#450).
- **Failed sends** go to the window's own "Not sent" list; the terminal client queues
  them in kithd instead.
- **The person card**:
  - Ignore and Block, as the terminal client has them;
  - starting a DM from a card when there is none;
  - a member list, so a card opens from one, and **Card** on rooms other than DMs;
  - `from:key:value` in search, resolved through the people call.
- **Settings**:
  - the lists of records, each written whole: notification rules (with quiet hours as
    a time range), name rules, identities, space rules and filters;
  - place lists, with Places settings narrowed to the profile;
  - the tag editor;
  - rows that pair two keys, such as a code's length as one range.
- **The filing picker**: a filter box, and New tag and New space rows.
- **Commands**: their key bindings and setting up their secrets, which are in the
  terminal client only.
- **A second bridge** joined from the window is added after the first, which stays
  the one dialed; choosing between them waits for Phase 3.
- **The verification dialog** is wider than a phone's screen.
- **`kith --open` with no window running** starts a terminal client; whether it starts
  the desktop instead is a device setting, undecided.
- **Version skew with a remote bridge**: the relay matches the app, but a kithd on a
  server may be older. A version in `Status`, refused on mismatch, would serve the
  remote terminal client too.
- **Keyboard-first use**: whether the desktop wants a vim-style navigation layer like
  the terminal client's, or conventional shortcuts are enough.

### Terminal client and kithd

- **The terminal client onto kithd's narrow calls**:
  - filing (`FileRoom`): it still rewrites the whole config, and so can still drop an
    entry that also names another profile's rooms, which kithd's filing no longer does;
  - settings (`ConfigSettings`, `SetConfigSetting`);
  - the phonebook (`People`, `PersonCard`, `SetPersonField`), needed once the terminal
    client dials a remote bridge.
- **One command runner.** kith-relay runs a copy of the terminal client's code for
  `[[command]]` scripts (capture, timeout, failure text); one package should hold it.
- **A scheduled message asked for again after it went out** is queued again, under the
  same transaction ID. Matrix drops the duplicate; other networks may not.
- **Rankings across profiles.** Word completion and emoji ranking with the `global`
  scope rank from every room, whatever profile asks; so do direct-chat candidates, the
  directory and message counts. Room key export is not narrowed to a profile either.
- **A profile's calls and lists judge rooms differently.** Calls follow the rail, while
  lists, streams and the queue also hold a room kithd no longer lists. So a draft in a
  room since left is listed but cannot be edited, and a room just created or joined
  from a terminal client that names a profile is refused until the next sync lists it.
- **A profile's own notification rules.** A `[[profile]]` block may hold notification
  rules, quiet hours among them, which replace the shared ones for that profile's rooms
  (decided 2026-11-19; design-clients.md open question 1). Nothing else is overridden
  yet.
- **Words typed during a profile switch**, for a room the new profile does not hold,
  are saved only once a profile that holds it is back.
- **Creating a room from a script**: a `create_room` tool on kith-mcp and a
  `kith room new` command, both over kithd's existing `CreateRoom`, so a hook can open
  a group without the app.

### Remote clients

- **Matrix single sign-on from a remote client.** The OAuth and SSO flows
  (`internal/matrix/oauth.go`) run in kithd and listen for the browser's redirect on
  kithd's own loopback, but the browser opens on the client's machine, whose loopback
  is another; the login cannot finish. A password login works remotely; SSO must be
  done on kithd's machine for now. The fix: the client listens on its own loopback and
  hands kithd the redirect it receives (the code and state) over the login's stream,
  kithd keeping the PKCE verifier and the token exchange. Logins that read a browser's
  cookies (Meta, Instagram, LinkedIn) need the same check: whose browser they read when
  the client is remote.

### Tests

- **The slowest tests, made faster or removed.** Every push runs `make quick`, and the
  desktop's tests alone take about 87 s of it. Time each suite, Go and desktop, and list
  the slowest. Remove a test whose cases another test already covers; rewrite the rest
  to check the same cases in less time. The desktop's remaining fast-check properties
  (16 files) are likely candidates: they repeat random runs, and a replacement is
  deterministic, an exhaustive search within a bound as the timeline's tests are
  (#457). A replacement must still fail on the bugs its test was written for.

## Backlog

Not ordered; taken when a reason appears.

- **Unread summary in the terminal window title**, for example `kith (3)`, so a
  backgrounded terminal tab shows something is waiting.
- **Per-message delivery status** from bridges that report one (sent to the remote
  network, failed there), shown on the message as kith's own send status is.
- **Following the other networks' links**: Messenger (`m.me`), Instagram (`ig.me`,
  `instagram.com/direct`), Slack message links and LinkedIn messaging links go to the
  chat in kith, the way Matrix, Telegram, WhatsApp and Signal links already do.
- **Server-side search** for history that was never cached locally (Matrix's
  `/search`, and the networks that have one), merged into the same results pane as a
  separate, slower tier.
- **iMessage**, only through a helper on a Mac (the shape of `kith-signal`); no other
  approach works today.

## Later: platforms

- **Full Windows support**, released as a zip and through Scoop or winget. It builds
  for Windows today but has never run there. What it takes: Windows in the test matrix;
  toast notifications (the desktop sink is D-Bus); the native file dialog (the picker
  uses the XDG portal); clipboard and link-opening defaults; `matrix:` and `tg:` links
  through the registry and Windows Terminal; starting `kithd` at login; signed
  binaries so SmartScreen does not warn. Windows Terminal will be required: the old
  console cannot draw the TUI.
- **The desktop client on macOS and Windows**: it is packaged for Linux only.
- **Native macOS notifications.** Today the desktop notification sink is D-Bus; on
  macOS the notification command hook is the way to be notified.

## Deferred

- **Pinned messages (#76)**: reading `m.room.pinned_events` and listing a room's pins.
  Deferred on a measurement: pins turned out to be rare in the rooms it was measured
  on. It comes back when someone needs it.

## How to read this

- **IDs** (`#100`, `S4`) are stable identifiers, not order: commit messages refer to
  them, so they are never renumbered. They are not GitHub issue numbers.
- Every item ships on its own, behind configuration that defaults to today's
  behaviour.
