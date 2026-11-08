# Clients: Android, and failover between bridges

What is left of the client topology begun 2026-10-24. Built: profiles, replay, remote
clients, bridges pairing in a mesh (its remaining steps are in
[design-mesh.md](design-mesh.md)), and the desktop client; how they work is in
[ARCHITECTURE.md](../ARCHITECTURE.md) and [docs/remote.md](../docs/remote.md). This file
holds what follows: failover between bridges (Phase 3), the desktop's partial cache, an
Android client (Phase 5), and an optional bridge on the phone (Phase 6).

## The invariant that shapes everything

kithd enforces **one process owns a device's keys**, guarded by `flock` (see
ARCHITECTURE.md). For Matrix that's one `device_id`'s olm/megolm state; for
WhatsApp, Signal, Messenger, Google Messages, it's *a linked-device slot* at the
network. Any multi-process story has to respect this: two bridges cannot share one
device identity, so each bridge is its own linked device per network.

## Where messages are kept

**The bridge's cache stays the source.** Notifications with no client open,
tracked words, content filters that move rooms or mark people, search over all
history, assist, kith-mcp, scheduled sends and replay after failover all run on
it. A device-only cache would make each of those wait for a device to be awake,
and make every device download everything.

**Each device keeps a partial cache** (not built yet): its profile's rooms and a recent
window, for offline reading and instant start. Scrollback beyond it comes from the
bridge. A device holding one is a replica of the mesh, syncing through its bridge.

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
each bridge sees the same events arrive on its own sync. The mesh carries only what
no network holds ([design-mesh.md](design-mesh.md)).

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
- WA: probably works (different app ID) — needs verifying before Phase 5
- Signal: same uncertainty as WA
- GM: no — one Android pairing, taken by the user's official GM
- Matrix / Telegram / Slack / LinkedIn: clean, independent sessions

Treat an on-device Android bridge as "Matrix/Telegram/Slack/LinkedIn supported,
WA/Signal pending verification, no GM." If on-phone WA/Signal via a local bridge
matters to a user, the hybrid shape sidesteps the whole issue by not needing a
bridge on the phone at all — the phone's client just dials a bridge
elsewhere.

## Discovery, selection, failover

The client holds `[[bridge]]` entries and tries them in priority order on
startup, with failover on disconnect.

**Discovery sources** (in order of simplicity, not necessarily shipped order):
1. Endpoints the user adds on the device (always available)
2. The mesh's roster, which lists each bridge's addresses
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
3. On connect, device resubscribes with what it last held. To the same bridge that
   is the bridge's own change number. To
   another bridge a change number means nothing (each numbers its own cache), and a
   network position misses edits and late messages, whose place in the room is old;
   the device sends the time it last heard from its bridge, less a grace window.
   Decided with this phase.
4. New bridge replays everything it changed after that time, and the device dedupes
   the overlap.
5. Device dedupes against recently-seen IDs.

**Dedup.** By network-level stable ID (`event_id` on Matrix, `message_id` on WA,
etc.). Device keeps a bounded set of recently-seen IDs per room.

**Encrypted history after failover.** Each bridge is its own Matrix device, so a
bridge added later holds no Megolm keys for messages sent before it existed, and
history it replays shows as undecryptable. Bridges do not coordinate, so this
rests on the network's own mechanisms: server-side key backup (kith restores
from it, and fetches a missing session's key on demand) and room-key requests to
the account's other verified devices. A new bridge is verified and restores the
backup as part of pairing; until it has, it is not offered for failover.

**Catchup.** Each bridge keeps its own sync position per network (sync_token on
Matrix, etc.) and its own append-only history in the SQLite cache. When a device
reconnects saying "I last saw X," the bridge replays everything newer that it
has. If the device was offline longer than the bridge's bounded window retains,
the device scrolls back on demand (same path as any client scrollback).

**Edge: sends during a failover gap.** Buffer briefly (3–5 seconds, matches
typical reconnect) and flush to the new bridge on reconnect. **A buffered send
must be idempotent across bridges**: if bridge A sent it but its acknowledgement
was lost, flushing to bridge B would post it twice. Matrix's `txnId` dedupes only
within one device, and each bridge is a different device, so the network will not
catch it. The device gives every send its own ID (carried in the event content
where the network allows, and kept in the device's outbox); before flushing, the
new bridge checks the room's recent timeline for that ID and drops the send if it
already went out. Matches
Signal/WA behaviour on flaky networks and doesn't violate "no offline writes"
(this is transient, not offline). Rejected (for v1): parallel sends to multiple
bridges, picking whichever responds first — more complex, better UX for lossy
networks, not needed day one.

## Push notifications

**UnifiedPush by default; no vendor gateway.** (Decided 2026-10-28; was FCM by
default.) FCM's API needs the Firebase project's service-account key, and its legacy
server keys stopped working in 2024: a self-hosted bridge cannot send FCM without a
gateway run by the kith project, as Matrix's Sygnal is run by its app's vendor. With
UnifiedPush, a distributor app on the phone (ntfy, self-hosted or not) gives the kith
app an endpoint URL; the bridge POSTs a wake-only body to it, encrypted (RFC 8291),
and the app connects and pulls. A phone with no distributor holds its bridge
connection open as a foreground service. Desktops need no push service: `kith-notify`
subscribes over the bridge's own connection. The push sink lands with the Android
client (Phase 5), the first that needs a wake.

**A topic per profile.** kithd already publishes each notification to the topic of
every profile the room is in, and `kith-notify` subscribes to topics. For push:

- **Topic names are secret**: never the profile's name. With UnifiedPush the topic is
  the endpoint URL the phone's distributor hands out, an unguessable capability, kept
  per profile on the bridge.
- **The payload is wake-only**: no body, no sender, no room. The device wakes,
  pulls from the bridge, and renders.
- **DND is the device's own.** A device in quiet hours ignores a wake, or
  unsubscribes for the night; the bridge never learns its schedule.

**Server-originated push (optional, deferred).** For networks whose servers can
push without the local bridge being online — Matrix (via a Sygnal-style pusher),
Telegram (MTProto push), Slack (webhooks) — the device could in principle
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

**Android lifecycle.** The UnifiedPush receiver is the only "always alive" thing. The
app's connection to the bridge runs as a foreground service only when the app is open
or briefly when a wake arrives, long enough to pull what the wake was for and post the
notification.

## Related work

As of 2026-10-24, from each product's public site and documentation.

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
- **Pidgin**: the pure-local, multi-protocol
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
   same shape as `kith-signal` (on the roadmap's backlog).

## Decisions (2026-10-24)

1. **One architecture, N:M bridges-to-devices.** User picks how many bridges and
   where via config; central / mesh / hybrid are deployment shapes, not branches
   in the code. Rejected: separate code paths for central-vs-distributed, because
   the generalized form is strictly simpler and failover only works if there is
   one implementation.

2. **Push by topic.** One secret topic per profile; the bridge publishes a
   wake-only payload to every profile a notification matches, and devices subscribe
   to their profile's topic. UnifiedPush by default (amended 2026-10-28: FCM needs a
   gateway run by the kith project). DND stays on the device.

3. **No offline writes on mobile.** Sending requires a live bridge connection
   (local or remote). Reading from local cache offline is fine. Removes the
   "scheduled queue migrates between devices" problem. Transient failover gaps
   (3–5s) buffer and flush on reconnect; not considered offline.

4. **The message cache stays on the bridge.** Devices keep a partial cache of
   their profile's rooms. Rejected: moving the cache to devices, which would stop
   everything kithd does with no client open.

5. **Bridge selection is pinned priority with fallback.** Not latency-measured.
   Simpler and sufficient at 2–4 bridges.

6. **Each bridge is independent — no bridge-to-bridge coordination of messages.**
   Networks are the source of truth; each bridge sees the same events on its own
   sync. Bridges pair with each other only for the soft state the mesh carries.

7. **Dedup on failover is by network-level stable ID.** Bounded set per room.

## Phasing

Built: Phase 0 (profiles), Phase 1 (replay), Phase 2a (remote clients), Phase 2b's
steps 1–4 (the mesh: roster, sync, the LID merge, read positions), Phase 4 (the desktop
client).

**Phase 2b, the rest.** The shared config, drafts, verdicts and key rotation crossing
bridges: steps 5–8 of [design-mesh.md](design-mesh.md).

**Phase 3 — multi-bridge failover.**
- Bridge heartbeat RPC (`Heartbeat(network, caught_up_as_of)`).
- Client-side bridge roster, pinned priority, failover on disconnect / stale
  heartbeat.
- Resuming at another bridge, by the time last heard less a grace window.
- Event dedup on device by network-level ID.
- Send-buffering during failover gaps (3–5s window).
- A client on several bridges showing everything its profile defines across them
  (design-mesh.md decision 6).

**The desktop's partial cache.** Offline reading in the desktop client makes it a
replica in the mesh, so it waits for the rest of Phase 2b.

**Phase 5 — Android client.** Kotlin app, woken by UnifiedPush: the push sink in
`notify` POSTs encrypted wakes to each profile's endpoints. Local SQLite cache for
offline reads. Dials a remote bridge (a home server, a VPS, or a laptop bridge in
the mesh) over TLS; falls over per Phase 3. No offline writes.

**Phase 6 — on-device bridge for mobile (optional).** gomobile AAR of
kithd for Android. Only if "a peer bridge is always reachable" turns out
to be too weak an assumption for a user. Battery is the open question; the WA
master-device constraint from above still applies.

## Open questions

1. **Which sections a profile overrides.** Built: rooms, people, rail, archive
   rules. Candidates: notification rules and quiet hours, display (theme, media),
   keys, assist and `[agent.*]` room lists. List them explicitly and merge the same
   way for each (a profile's value replaces the shared one, never deep-merges), so
   what a profile does is readable from its block.
2. **WAN discovery for multi-bridge.** Tailscale is lazy; self-hosted rendezvous
   with hole-punching is ambitious; "paste the current IP" is zero-build v1.
   Product values call.
3. **WhatsApp-on-Android alongside the official app.** Needs verifying before
   Phase 5. Can kith-on-Android register as a WA linked device while the official
   WA app is master on the same phone? Spike.
4. **Push payload policy.** Wake-only is the baseline. Any room ID or badge
   count? Any content leaks to Google/Apple. Decide the exact payload.
5. **Credentials on Android.** The Linux keyring holds them today, and the desktop's
   relay uses it; Android has Keystore. It needs the equivalent of
   `internal/session`'s pickle-key story.
6. **Heartbeat semantics.** How often, how stale is "stale", what counts as
   "caught up" per network (Matrix sync token age? WA keep-alive seen? Telegram
   update seq?). Per-network definition needed.
7. **Phonebook across bridges.** Shared phonebook is a file ([phonebook](../docs/phonebook.md)). In a multi-bridge config, which bridge's phonebook is
   canonical? Likely: file lives on each bridge independently and the mesh
   doesn't touch it; devices resolve names from whichever bridge they're
   currently talking to, with the shared file keeping them in rough sync. Needs
   thought.
