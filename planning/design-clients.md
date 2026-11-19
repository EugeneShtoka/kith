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
| "central" | 1 (homelab/VPS) | N (laptops, phones) |
| "mesh" | N (one per device, colocated) | N (same boxes) |
| "hybrid" | M (on always-on devices) | N (M < N; others device-only) |
| "mobile-only" | 1 (phone, if practical) | 1 |
| "standby" | M, one running at a time, by priority (homelab, then laptop, then phone) | N |

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
| Google Messages | one active web session | many computers may pair, but opening one makes the others inactive (a tablet stays active beside it), so two bridges would keep taking it from each other |
| WhatsApp | 4 linked devices + master phone | each bridge is one linked device |
| Signal | ~5 linked devices + master phone | each bridge is one linked device |
| Messenger | multi-device, lax cap | fine |

**The Android special case:** on a phone that already hosts the official WA /
Signal / GM apps as master, kith-on-Android as a *linked* device alongside the
master app:

- WA: probably works (different app ID) — needs verifying before Phase 5
- Signal: same uncertainty as WA
- GM: a web session paired with the same phone's GM app, and only while it is the
  active one; unverified
- Matrix / Telegram / Slack / LinkedIn: clean, independent sessions

Treat an on-device Android bridge as "Matrix/Telegram/Slack/LinkedIn supported,
WA/Signal/GM pending verification." If on-phone WA/Signal via a local bridge
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
4. Across the internet: Tailscale (decided 2026-11-18, open question 2). kith
   builds nothing for it; [remote.md](../docs/remote.md#reaching-a-bridge-from-outside-its-network)
   says how to put a bridge's tailnet address beside its LAN one.

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

**Standby: one bridge runs, chosen by device priority.** (Proposed 2026-11-12; adopted
2026-11-18, built in Phase 3.) A
device that may run a bridge has a priority, such as a homelab first, a laptop second,
a phone last; a device with none never runs one and is only a client. At most one
bridge is *active*, connected to the networks. The others are *standby*: their network
sessions are linked and they stay in the mesh, but they hold no network connection and
act as clients of the active bridge. A phone then runs its own bridge only when it
sees no other.

- **Each bridge decides alone, with no vote.** It is active unless it sees a healthy
  bridge of higher priority, directly or over the tailnet (open question 2). Healthy is the liveness above: a fresh heartbeat whose networks
  are caught up. A reachable bridge whose sync is stuck does not count, so a phone
  takes over from a homelab that is up but not syncing.
- **No flapping.** A bridge stands down after it has seen a healthy higher one for a
  while, and takes over only after missing every higher one for a few minutes, so a
  relay dropping or a phone moving between Wi-Fi and mobile data does not swap the
  active bridge each minute.
- **Two active bridges are allowed.** A partition can leave one on each side. That is
  the N:M topology above, which already works: the mesh merges their state, and send
  IDs keep a send from going out twice. When they see each other again, the lower one
  stands down. So nothing needs consensus, which with two or three bridges often has
  no majority and would stop a cut-off side from running at all.
- **A standby bridge keeps its sessions alive.** It connects to each network on a
  schedule, long enough that the network does not drop it as an idle linked device,
  and catches up; otherwise the first takeover, on a trip with the homelab out of
  reach, finds the phone logged out.
- **Clients choose as before.** The active bridge is the highest-priority healthy one,
  which is what pinned priority selection already picks; a standby bridge's heartbeat
  says it is not caught up, so clients pass it over.
- **What it buys:** a phone that sleeps as a client while a higher bridge runs, one
  Google Messages session instead of several taking it from each other, and one
  connection per network.
- **What it does not:** each bridge still holds its own linked device per network (the
  invariant: keys never move between processes), so every device given a priority
  costs a WhatsApp and a Signal slot. Priorities are opt-in for that reason.

## Heartbeat per network

Proposed 2026-11-18 for open question 6, and decided 2026-11-19 as proposed. LinkedIn's
window and Messenger's and Instagram's are measured on live accounts when Phase 3 builds
the heartbeat.

A heartbeat says, for each account a bridge holds, two things: its connection to the
network is **alive**, and it has **caught up** since connecting. Neither can be read from
the last message's time, since a quiet account gets nothing for hours. Each network gives
its own evidence, some of which kith already handles:

| network | alive: seen within the window | caught up after connecting | kith today | window |
| --- | --- | --- | --- | --- |
| Matrix | a `/sync` answered; the long poll returns at least every timeout even when quiet | the first `/sync` after connecting returned | `ONLINE` after the first sync, `online_at` | 90 s |
| WhatsApp | whatsmeow's keep-alive answered (every 20–30 s); `KeepAliveTimeout` and `KeepAliveRestored` say when it fails and comes back | `OfflineSyncCompleted` after `Connected`: the server's queue for this device is drained | `Connected` only | 90 s |
| Signal | the helper's websocket keep-alive answered, as the helper reports it to kithd (it beats every 2 s) | `QueueEmpty` after connecting | the helper's beat to kithd, not the server's | 90 s |
| Telegram | an MTProto ping answered | `getDifference` after connecting returned with no gap left | gaps filled by `getDifference`, in order | 3 min |
| Slack | the RTM websocket's ping answered | the RTM connection's hello, then the history fetched since the last event | `rtm.ManageConnection` | 90 s |
| LinkedIn | a realtime `Heartbeat` event (linkedingo's `Heartbeat` handler) | the sync-token listing after connecting returned | `RealtimeConnect`; #601's retry for a missing conversation | 3 min, to be measured |
| Google Messages | the long poll open and pinged; the phone answering is separate (`PhoneNotResponding`, `PhoneRespondingAgain`) | the long poll open and the phone answering | both phone events handled | 3 min |
| Messenger, Instagram | messagix's socket keep-alive answered | the first sync after connecting | connection state only | 90 s, to be measured |

What follows from the table:

- **One beat, every account in it.** The bridge sends one heartbeat every 30 s with,
  per account, when it was last seen alive and whether it has caught up. A client calls
  the bridge stale after 90 s without a beat, whatever its accounts say.
- **Each network's own window.** An account is stuck when nothing in its "alive" column
  was seen within its window, or it has not caught up a few minutes after connecting.
  One number for every network would call a quiet LinkedIn account stuck, or miss a
  WhatsApp socket that is open but no longer answers keep-alives.
- **What is not the bridge's fault does not count against it.** A Google Messages phone
  that stops answering, or a network that is down for everyone, is the same on every
  bridge, so failing over or taking over (standby) gains nothing. Those accounts say so
  in the beat and are left out of "healthy".
- **Healthy** for failover and for standby is a fresh beat with every logged-in account
  alive and caught up, except as above (decided 2026-11-19). One stuck account makes a
  bridge unhealthy, whichever profile uses it, so every client and every standby bridge
  agree on which bridge is healthy. When none is, each with a different account stuck,
  is open question 9.

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
| --- | --- | --- | --- | --- | --- |
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

8. **(Adopted 2026-11-18, Phase 3) Standby bridges by device priority.** One bridge is active;
   the rest stand by, with their sessions linked, and each takes over only when it
   sees no healthy bridge of higher priority. Two active at once is tolerated, not
   prevented. Rejected: electing a leader by consensus, which a two- or three-bridge
   mesh split in half cannot reach.

## Phasing

Built: Phase 0 (profiles), Phase 1 (replay), Phase 2a (remote clients), Phase 2b's
steps 1–4 (the mesh: roster, sync, the LID merge, read positions), Phase 4 (the desktop
client).

**Phase 2b, the rest.** The shared config, drafts, verdicts and key rotation crossing
bridges: steps 5–8 of [design-mesh.md](design-mesh.md).

**Phase 3 — multi-bridge failover.**

- Bridge heartbeat RPC: one beat every 30 s, per account alive-as-of and caught up, by
  [Heartbeat per network](#heartbeat-per-network).
- Client-side bridge roster, pinned priority, failover on disconnect / stale
  heartbeat.
- Resuming at another bridge, by the time last heard less a grace window.
- Event dedup on device by network-level ID.
- Send-buffering during failover gaps (3–5s window).
- Standby bridges: a priority per device, standing down to a healthy higher bridge,
  taking over when none is seen, and keeping sessions alive while on standby.
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
master-device constraint from above still applies. With standby bridges (Phase 3) the
phone's bridge runs only when it sees no other, which answers most of the battery
question.

## Open questions

1. ~~**Which sections a profile overrides.**~~ *Decided 2026-11-19:* notification rules,
   quiet hours among them, and nothing more for now. Built: rooms, people, rail, archive
   rules. A profile's rules replace the shared ones, never deep-merge, so what a profile
   does is readable from its block. Display, keys, assist and the `[agent.*]` room lists
   stay shared until someone asks.
2. ~~**WAN discovery for multi-bridge.**~~ *Decided 2026-11-18:* Tailscale, documented
   in remote.md, nothing built. A self-hosted rendezvous and a pasted address were the
   other options; the live relay in [design-mailbox.md](design-mailbox.md) stays an idea
   for a paid tier.
3. **WhatsApp-on-Android alongside the official app.** Needs verifying before
   Phase 5. Can kith-on-Android register as a WA linked device while the official
   WA app is master on the same phone? Spike.
4. ~~**Push payload policy.**~~ *Decided 2026-11-18:* wake only. No room ID and no
   badge count: the push carrier would see them, and the app syncs from the bridge over
   TLS right after waking.
5. **Credentials on Android.** The Linux keyring holds them today, and the desktop's
   relay uses it; Android has Keystore. It needs the equivalent of
   `internal/session`'s pickle-key story.
6. ~~**Heartbeat semantics.**~~ *Decided 2026-11-19:* the
   [Heartbeat per network](#heartbeat-per-network) table as proposed, LinkedIn and
   Messenger/Instagram measured when Phase 3 is built; healthy is every logged-in
   account alive and caught up.
7. ~~**Phonebook across bridges.**~~ *Decided 2026-11-19:* each bridge keeps its own
   [phonebook](../docs/phonebook.md) folder and the mesh never touches it. The cards are
   files other apps edit too, and a folder sync (Syncthing, say) already merges them; the
   mesh would need a three-way merge of free text. A device reads names from the bridge
   it is talking to.
8. **Standby per network.** How long each network keeps an idle linked device before
   dropping it, and so how often a standby bridge must connect; and how long it queues
   messages for that device, so what a bridge taking over can still catch up on. The
   hold times before standing down and taking over follow from heartbeat semantics
   (6).
9. **No bridge healthy.** Two bridges each have a different account stuck: WhatsApp on
   the homelab, Signal on the laptop. Neither is healthy, so by the rule above both take
   over and every client passes both over. Either standby is decided per account (each
   account active on the highest-priority bridge where it is alive, a standby bridge
   connecting only the accounts stuck above it, a client taking each account from the
   bridge where it is active), or per bridge (the one with most accounts alive, ties by
   priority, or the higher one kept and the rest shown as stuck).
