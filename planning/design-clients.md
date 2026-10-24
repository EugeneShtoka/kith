# Clients: Electron, Android, and the bridge/device topology (2026-10-24)

Spec in progress. Two new client platforms (Electron desktop, Android) plus a
topology refactor that lets kithd scale from "one central bridge" to "multiple
bridges with device failover" without branching the architecture. Later sessions
expand this; everything here reflects decisions through 2026-10-24.

## The invariant that shapes everything

kithd enforces **one process owns a device's keys**, guarded by `flock` (see
ARCHITECTURE.md). For Matrix that's one `device_id`'s olm/megolm state; for
WhatsApp, Signal, Messenger, Google Messages, it's *a linked-device slot* at the
network. Any multi-process story has to respect this: two bridges cannot share one
device identity, so each bridge is its own linked device per network.

## The three-layer architecture

```
Bridge (kithd-bridge, was kithd)
  - network sessions, cache, crypto stores
  - publishes raw event streams, unfiltered
  - no per-device state, no rules, no DND, no view
  - config = structural only (accounts, storage, keys, models)
  - 1..N instances across the user's devices and servers
                │
                │  Connect over TLS/mTLS (or unix socket when colocated)
                │
Device layer (kith-notify, per device, light)
  - holds view / filter / DND / notification rules
  - config = device-local, never sent to the bridge
  - subscribes to a bridge's event streams
  - decides locally: show in UI? buzz? which sink?
  - dispatches to local OS sink (libnotify / native / FCM)
  - serves the local GUI as its api.Backend
                │
                │  unix socket (same shape as today's kith↔kithd)
                │
GUI (TUI / Electron / Android)
  - talks only to local kith-notify, never a bridge directly
  - pure presentation
```

**Motivating property:** device config (filter rules, DND, view, keybindings)
never leaves the device. The bridge is a dumb pipe for events; the device layer is
where rules are evaluated. This matters when the bridge is on shared hardware (a
family homelab, a cloud VPS) and matters structurally even when it isn't, because
the device layer always does the same thing independent of where the bridge runs
— colocated on the same box, remote across the network, one bridge or several.

**Receive-all, filter-locally.** The bridge sends all events to every attached
device; each device filters by its own rules. Bandwidth cost is trivial at the
target scale (3–4 devices, normal messaging volume). Battery cost on mobile is
addressed via FCM wake tokens (bridge pushes a coarse wake signal; device wakes,
pulls recent events, applies local rules).

## Topology: one architecture, N:M

**Bridges and devices, N:M, user configures where each lives.** Common
deployment shapes:

| shape | bridges | devices |
| --- | --- | --- |
| "central"    | 1 (homelab/VPS) | N (laptops, phones) |
| "mesh"       | N (one per device, colocated) | N (same boxes) |
| "hybrid"     | M (on always-on devices) | N (M < N; others device-only) |
| "mobile-only"| 1 (phone, if practical) | 1 |

The hybrid shape is particularly attractive: 2 bridges (home PC, work laptop) + 2
device-only (phone, tablet) uses 2 WA linked-device slots instead of 4, while still
tolerating any single bridge going down.

Each bridge independently connects to every network. **Networks are the source of
truth for message state**, so bridges don't replicate messages between themselves —
each bridge sees the same events arrive on its own sync. The mesh CRDT (see
below) carries user-metadata that no network holds.

## Device model per network

Each bridge costs linked-device slots per network. Reality:

| network | bridge cost | notes |
| --- | --- | --- |
| Matrix | unlimited `device_id`s | each bridge registers its own, cross-signs |
| Telegram | multiple sessions | each bridge its own session |
| Slack / LinkedIn | session-based | each bridge its own; no cap |
| Google Messages | one Android pairing | GM-on-phone *is* master; others have no GM |
| WhatsApp | 4 linked devices + master phone | each bridge is one linked device |
| Signal | ~5 linked devices + master phone | each bridge is one linked device |
| Messenger | multi-device, lax cap | fine |

**The Android special case:** on a phone that already hosts the official WA /
Signal / GM apps as master, kith-on-Android as a *linked* device alongside the
master app:
- WA: probably works (different app ID) — needs verifying before Phase 3
- Signal: same uncertainty as WA
- GM: no — one Android pairing, taken by the user's official GM
- Matrix / Telegram / Slack / LinkedIn: clean, independent sessions

Treat an on-device Android bridge as "Matrix/Telegram/Slack/LinkedIn supported,
WA/Signal pending verification, no GM." If on-phone WA/Signal via a local bridge
matters to a user, the hybrid shape sidesteps the whole issue by not needing a
bridge on the phone at all — the phone's device layer just dials a bridge
elsewhere.

## Config scope: daemon vs device

Today's single `kith.toml` splits in two:

**Bridge config** (`bridge.toml`, per bridge):
- `[storage]` paths, instance, keys
- `[[account]]` credentials per network
- `[model]`, `[assist]` server-side bits
- Encryption, keyring service, reconnect behaviour

**Device config** (`device.toml`, per device, never leaves the device):
- `[display.rail]`, `[display.theme]`
- `[[place]]`, `[[filter]]` (includes archive-equivalent rules)
- `[[notification]]`, DND
- `[keys]`
- `[[bridge]]` entries: `(endpoint, pinned_pubkey, priority)` the device may dial
- Default rail selection, open-with behaviour

`ReloadConfig` runs on both files on their respective processes. A migration shim
reads today's unified file and routes fields on first start; the first write-back
splits it.

## What crosses between devices (the mesh CRDT)

Nothing message-level — the networks propagate messages. What crosses is a short
list of user-metadata that no network holds:

- **Drafts.** Started on phone, finished on laptop. Structured CRDT (text + cursor).
- **Per-room read cursor.** Monotonic max merge: the merged position is
  `max(a.position, b.position)` per room. **Guarantee: a message marked read never
  becomes unread again.** This is not LWW on a scalar — LWW would allow a stale
  write to regress the position. Max merge is monotonic by construction. Rides
  alongside the local-first unread state from #207–#210 (which is already shaped
  as "pending at network" vs "accepted"); mesh sync covers the pending window and
  anything the network itself fails to propagate.
- **Scheduled-send queue entries** (if we eventually need them to migrate between
  devices; today's decision is no offline writes, so this is marked for later).
- **Device roster + bridge roster.** Each entry = `(device_id, pinned_pubkey,
  role, endpoint, priority, added_by, added_at)`. Signed by the adding device so
  removal requires authority.
- **Learned ranking** (emoji, vocab). Not essential; nice-to-have.
- **Shared starred messages, filter moves** if the user wants them shared (device
  config stays device-local; a user flag opts into sharing selected categories).

**Device config does not cross.** This is a hard rule — filter rules, DND, view,
keybindings stay on the device that holds them.

**Shared phonebook** continues through the existing file mechanism
([phonebook](../docs/phonebook.md)), not the CRDT.

**Substrate.** A tiny CRDT (Automerge-shape) over Connect streams between paired
peers. Last-writer-wins for scalar flags, monotonic max for cursors, structured
CRDT only for drafts. Spike needed: pick a Go CRDT library or roll a minimal one.

## Pairing and trust

**Trust-on-first-use, pinned keys, no CA.** Each device and bridge generates its
own long-lived keypair at install. Pairing happens pairwise:

1. New device shows QR with its public key + a one-time nonce.
2. Existing device scans, verifies nonce out-of-band (short-code shown on both
   screens), writes the new device's pubkey into its known-devices list.
3. New device records the paired device's pubkey.
4. The CRDT roster entry propagates to all other paired devices (introduction via
   the paired peer).

**No special "root" device.** Any device can be lost; the rest keep working. Any
surviving device can revoke a lost one (roster-removal propagates). Rejected: a
local CA with signed device certs, because it makes one device "special" (lose
the root, can't enroll new devices) with no real gain at a 3–4 device mesh size.

mTLS between all paired entities (device↔bridge, device↔device for mesh).
bridge↔bridge is **not** paired because there is no bridge-to-bridge
coordination — each bridge is independent, syncing from the networks on its own.

## Discovery, selection, failover

The device layer holds `[[bridge]]` entries and tries them in priority order on
startup, with failover on disconnect.

**Discovery sources** (in order of simplicity, not necessarily shipped order):
1. User-added endpoints in `device.toml` (always available)
2. CRDT-propagated bridge roster (an entry added at pairing time)
3. mDNS on LAN (nice for same-network discovery)
4. WAN rendezvous (deferred — Tailscale is the lazy answer; a self-hosted
   rendezvous with hole-punching is ambitious; "paste the current IP" is the
   zero-build v1)

**Liveness.** TCP reachable is necessary but not sufficient — a bridge whose
network sync is stuck is useless. Each bridge publishes a heartbeat:
`(up, per-network sync-caught-up-as-of timestamp)`. Device discards bridges with
stale heartbeats.

**Selection.** Pinned priority with fallback. Dial by priority; on dial failure
or stale heartbeat, try next. Rejected (for v1): lowest-latency selection, because
it adds measurement overhead for no real benefit at 2–4 bridges.

**Failover.**
1. Current bridge disconnects, times out, or heartbeat goes stale.
2. Device dials next bridge in priority order.
3. On connect, device resubscribes streams with "give me events since cursor X"
   per stream, per room where applicable.
4. New bridge replays everything it has newer than X.
5. Device dedupes against recently-seen IDs.

**Dedup.** By network-level stable ID (`event_id` on Matrix, `message_id` on WA,
etc.). Device keeps a bounded set of recently-seen IDs per room.

**Catchup.** Each bridge keeps its own sync position per network (sync_token on
Matrix, etc.) and its own append-only history in the SQLite cache. When a device
reconnects saying "I last saw X," the bridge replays everything newer that it
has. If the device was offline longer than the bridge's bounded window retains,
the device scrolls back on demand (same path as any client scrollback).

**Edge: sends during a failover gap.** Buffer briefly (3–5 seconds, matches
typical reconnect) and flush to the new bridge on reconnect. Matches
Signal/WA behaviour on flaky networks and doesn't violate "no offline writes"
(this is transient, not offline). Rejected (for v1): parallel sends to multiple
bridges, picking whichever responds first — more complex, better UX for lossy
networks, not needed day one.

## Push notifications

**FCM default, custom endpoint option.** Users can configure a different push
endpoint (UnifiedPush, ntfy, self-hosted) via the device config. The bridge-side
`notify` package gains a push sink that posts wake tokens.

**Payload is wake-only.** No message body, no sender ID in the push payload.
Device wakes, pulls over Connect from the bridge, applies local rules, decides
what to render. Avoids leaking content to Google/Apple. The bridge-side wake
decision uses only coarse criteria (per-account enablement, "is this device
paired to this account at all"); the real decision stays on the device.

**Server-originated push (optional, deferred).** For networks whose servers can
push without the local bridge being online — Matrix (via a Sygnal-style pusher),
Telegram (MTProto push), Slack (webhooks) — the device-layer could in principle
receive push directly without any kith bridge intermediary. For WA, Signal, Meta,
GM this is impossible (linked-device model, no push for third-party clients).
Deferred: covered by the bridge-side push path for v1 since the bridge is where
the message arrives first anyway.

## Mobile: no offline writes

Sending requires a live connection to *some* bridge (local in colocated, remote
otherwise). Reading from a local cache offline is fine; composing while
disconnected is not a shipped feature. Removes the "scheduled queue migrates
between devices on failure" problem — if you can't reach any bridge, send fails
with a visible error.

**Android lifecycle.** FCM-registered intent receiver is the only "always alive"
thing. kith-notify runs as a foreground service only when the app is open or
briefly when FCM wakes it. The intent receiver is tiny Kotlin; it uses the FCM
wake token to decide whether to spin up kith-notify or just dismiss.

## Related work

Verified 2026-10-24.

| | **Pantheon** | **Ferdium** | **Rambox** | **Trillian** | **kith (planned)** |
|---|---|---|---|---|---|
| Network protocol work | Server-side (needs Mac for iMessage, linked WA host) | Webview per service | Webview per service | Native XMPP/IRC + Astra; modern networks limited | Native in-process adapters (whatsmeow, mautrix-*, gotd), pure Go |
| State lives | Pantheon's cloud | Per-webview localStorage | Device + Auth0 (service list only) | Astra cloud + per-device | Bridge SQLite; CRDT for soft state across devices |
| Multi-device | Through their cloud | Each install independent | Service list syncs via Auth0 | Astra syncs across all devices | N:M bridges and devices, failover |
| UX | Unified inbox + CRM overlay | Zero unification | Zero unification | Unified | Unified |
| Mobile | Planned/alpha | No | No | Yes | Planned (Android) |
| Who sees plaintext | Pantheon (server-side) | The service (web app) | The service | Trillian for Astra-synced | Only the user |
| Model | SaaS, invite-only | Open-source, self-hostable | Freemium commercial | Freemium commercial | Open-source, self-hosted |

**Where kith sits that none of them do:**

- **Pantheon** (pantheon.run, invite-only alpha, 2026): closest in intent —
  unified inbox for TG/WA/iMessage + BYO AI (ChatGPT/Claude/Grok/HF). Opposite in
  architecture: everything runs on their servers; your plaintext lives there.
  kith's inversion is total — everything in your process. Pantheon's existence
  validates the market; kith's answer is "same product category, but you own the
  data."
- **Ferdium / Rambox**: solve a different problem (webview launchers), zero
  unification, no real multi-device story. They already force per-install linked-
  device slots (ferdium on laptop + ferdium on tablet = 2 WA slots), which
  validates the "4 devices is enough" bet — people already live with that math.
- **Trillian**: closest ancestor conceptually (native multi-protocol with cross-
  device sync via Astra), but doesn't cover modern consumer messengers (WA, Signal,
  Meta aren't XMPP/IRC). Astra is single-central-server only, hosted by them.
- **Pidgin** (not asked, but worth naming): the pure-local, multi-protocol
  ancestor with no sync. kith is "what Pidgin would look like in 2026 with
  maintained bridge libraries, a unified cache, and an actual multi-device plan."

**Validation this gives the plan:**

1. Pantheon's existence confirms demand for a unified TG/WA/iMessage inbox with
   modern AI integration (parallel to kith-mcp).
2. No one in this lineup offers topology choice — Ferdium/Rambox are
   mesh-without-sync, Pantheon/Trillian are central-only. Topology flexibility
   plus data sovereignty is the differentiating wedge.
3. The 4-device cap is already how people live. "3–4 devices" isn't an edge case
   to engineer around; it's the baseline.
4. iMessage via a Mac host (Pantheon's model) is the only working approach today
   — if kith ever wants iMessage, it would need a `kith-imessage` helper on a Mac,
   same shape as `kith-signal` (noted for the design-adapters roadmap).

## Decisions (Eugene, 2026-10-24)

1. **One architecture, N:M bridges-to-devices.** User picks how many bridges and
   where via config; central / mesh / hybrid are deployment shapes, not branches
   in the code. Rejected: separate code paths for central-vs-distributed, because
   the generalized form is strictly simpler and failover only works if there is
   one implementation.

2. **Three-layer split: bridge / device / GUI.** Protocol work (network sessions,
   cache, crypto) in the bridge; view / filter / DND / notification rules in a
   per-device daemon; pure presentation in the GUI. The GUI never talks to a
   bridge directly — only to its local device layer. Rejected: in-kithd notify
   with per-device config stored on the bridge, because device rules leak intent
   (muted contacts, after-hours filters) and the bridge may be on shared hardware.

3. **Receive-all, filter-locally.** Bridge sends every event to every attached
   device; each device applies its own rules. Bandwidth is trivial at target
   scale; mobile battery addressed via FCM wake tokens with coarse bridge-side
   filtering.

4. **Pairing: trust-on-first-use with pinned keys, roster propagates via CRDT.**
   No CA, no special root device. Rejected: local CA with signed device certs —
   makes one device special with no real gain at mesh size 3–4.

5. **Push: FCM default, custom endpoint option.** Wake-only payload (no body, no
   sender). Device pulls on wake, applies local rules.

6. **Mesh CRDT: a tiny Automerge-shape CRDT over Connect streams between paired
   peers.** LWW for scalar flags, monotonic max for cursors, structured CRDT for
   drafts. Device config does not cross; only user-metadata that no network holds.

7. **Per-room read cursor is in the CRDT, merged by monotonic max.** Guarantee: a
   message marked read never becomes unread again. Not LWW on a scalar (which
   could regress); max-merge is monotonic by construction. Rides alongside the
   local-first unread state from #207–#210 (which handles the "pending at
   network" window); the mesh CRDT covers inter-device propagation when the
   network itself doesn't propagate reliably.

8. **No offline writes on mobile.** Sending requires a live bridge connection
   (local or remote). Reading from local cache offline is fine. Removes the
   "scheduled queue migrates between devices" problem. Transient failover gaps
   (3–5s) buffer and flush on reconnect; not considered offline.

9. **Config split: bridge.toml (structural) vs device.toml (view/preference).**
   Migration shim reads today's unified file on first start; first write-back
   splits it.

10. **Bridge selection is pinned priority with fallback.** Not latency-measured.
    Simpler and sufficient at 2–4 bridges.

11. **Dedup on failover is by network-level stable ID.** Bounded set per room.

12. **Each bridge is independent — no bridge-to-bridge coordination.** Networks
    are the source of truth; each bridge sees the same events on its own sync.
    Mesh CRDT runs between *devices*, not between bridges, and bridges don't
    participate (their state is derivable from the networks).

## Phasing

**Phase 0 — in-process device-layer refactor.** Factor `internal/notify`, the
view/filter/DND packages, and the per-room read cursor behind a clean interface
inside kithd. TUI goes through this interface; no new process yet. Makes the
eventual process split a transport change on an existing seam, not a redesign.
Benefits the single-bridge shape immediately (clients can cache the metadata
locally).

**Phase 1 — bridge-device process split, single bridge, mesh CRDT skeleton.**
- Extract `cmd/kith-notify` as a new binary. Device-layer loads `device.toml`,
  dials the local bridge over unix socket.
- Bridge sheds notification state and device config; `bridge.toml` is derived
  from today's `kith.toml` via a one-shot migration.
- TUI's wire swaps from kithd to kith-notify (same `BackendService`-shaped API).
- Mesh CRDT library in `internal/mesh/crdt` (spike: pick library or roll
  minimal); carries drafts and read cursors first.
- Bridge gains cursor-based stream replay (`StreamSince(cursor)`).

**Phase 2 — remote bridge transport and pairing.**
- Bridge listens on TCP+TLS alongside unix socket. Same `BridgeService`.
- Pairing flow: QR between any two paired entities (device↔bridge, device↔device).
  Reuse `internal/qrtext`; TOFU + pinned keys.
- Bridge heartbeat RPC (`Heartbeat(network, caught_up_as_of)`).
- Bridge-side push sink in `notify` posts FCM wake tokens.

**Phase 3 — multi-bridge failover.**
- Device-layer bridge roster, pinned priority, failover on disconnect / stale
  heartbeat.
- Event dedup on device by network-level ID.
- Send-buffering during failover gaps (3–5s window).
- CRDT-propagated bridge roster (so pairing on one device propagates endpoints
  to the others).

**Phase 4 — Electron client.** TypeScript Connect client over the device-layer's
local socket (or over TLS to a remote device-layer if someone wants that split —
noted as low priority). Platform glue: native notifications, file picker, media
viewer. This is the calendar-time phase — the seam work is cheap; the UI is a
from-scratch rewrite of what `internal/tui` does today.

**Phase 5 — Android client.** Kotlin app. The device-layer runs *inside* the app
as a short-lived foreground service, woken by FCM. Local SQLite cache for
offline reads. Dials a remote bridge (user's homelab, VPS, or a laptop bridge in
the mesh) over TLS; falls over per Phase 3. No offline writes.

**Phase 6 — on-device bridge for mobile (optional).** gomobile AAR of
kithd-bridge for Android. Only if "a peer bridge is always reachable" turns out
to be too weak an assumption for a user. Battery is the open question; the WA
master-device constraint from above still applies.

## Open questions (next sessions)

1. **Mesh CRDT shape in detail.** Spike: Automerge-Go vs a minimal rolled impl.
   What exactly goes in each CRDT variant (scalar LWW, monotonic max, structured),
   how roster entries authorize themselves (signed by adding device?), bounded
   size story, compaction / snapshotting.
2. **Phase 0 interface sketch.** Concrete Go interface for the device-layer
   inside kithd — what methods, where it sits in the layer diagram, how `Remote`
   exposes it to clients.
3. **Bridge-device wire shape.** Reuse `BackendService` (device-layer is just
   another subscriber) or introduce a narrower `BridgeService` with only the six
   streams + sends + reads + lookups the device-layer needs? Narrower is cleaner;
   reusing is simpler but exposes more surface than the device-layer uses.
4. **WAN discovery for multi-bridge.** Tailscale is lazy; self-hosted rendezvous
   with hole-punching is ambitious; "paste the current IP" is zero-build v1.
   Product values call.
5. **WhatsApp-on-Android alongside the official app.** Needs verifying before
   Phase 5. Can kith-on-Android register as a WA linked device while the official
   WA app is master on the same phone? Spike.
6. **Push payload policy.** Wake-only is the baseline. Any room ID or badge
   count? Any content leaks to Google/Apple. Decide the exact payload.
7. **Multi-instance on one device.** Today kith-notify would hold one bridge
   connection. Realistic: work-account + personal-account in one window.
   Device-layer dialing N bridges and merging is a real feature; decide before
   Electron UI hardens.
8. **Credentials/keyring on each platform.** Linux keyring today; Electron has
   `keytar` / safeStorage; Android has Keystore. Each needs the equivalent of
   `internal/session`'s pickle-key story. Enumerate.
9. **Heartbeat semantics.** How often, how stale is "stale", what counts as
   "caught up" per network (Matrix sync token age? WA keep-alive seen? Telegram
   update seq?). Per-network definition needed.
10. **Phonebook across bridges.** Shared phonebook is a file ([phonebook](../docs/phonebook.md)). In a multi-bridge config, which bridge's phonebook is
    canonical? Likely: file lives on each bridge independently and the CRDT
    doesn't touch it; devices resolve names from whichever bridge they're
    currently talking to, with the shared file keeping them in rough sync. Needs
    thought.
11. **Scheduled sends across bridges.** Today scheduled sends live in a bridge-
    local toml file. In the multi-bridge model: if the authoring bridge goes
    down before send time, does the message send? Options: migrate via CRDT,
    accept the loss, require the user to re-schedule. Not urgent; mobile has no
    offline writes so this only affects a desktop user with ephemeral bridges.
