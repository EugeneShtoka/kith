# Plan: failover between bridges (step 1b)

The step-by-step plan for Phase 3 of [design-clients.md](design-clients.md): the heartbeat,
a client failing over between bridges, resuming at another bridge, sends that go out
once across bridges, standby bridges, and a client on several bridges. What it must do
is decided there (Discovery, selection, failover; Standby; Heartbeat per network; open
questions 2, 6 and 9). This file is how it gets built, against the code as of
2026-11-23 (`7b4af62a`), and the few calls the design left to the build.

## Where the code stands

- **One bridge per client.** `device.toml` holds a `[[bridge]]` list (`config.Bridge`:
  name, addresses, key ID), but `setup.JoinedBridge` takes `Bridges[0]`, and
  `kith join` writes only the bridge joined. `dialBridge` already tries a bridge's
  addresses in order and pings an idle HTTP/2 connection (15 s idle, 10 s wait).
- **kith-proxy** (#629, open) is where failover goes (DECISIONS 2026-11-22): every client
  on a machine dials it, so the TUI, the window, kith-mcp and kith-notify fail over
  together and none of them learns about bridges. Its store (#632, open) numbers its
  own changes and keys messages by room and event ID, so a second bridge's copy of a
  message lands on the first's row.
- **Liveness.** `Status` gives each account a phase (`LOGGED_OUT`, `CONNECTING`,
  `ONLINE`, `FAILED`) and `online_at`. Nothing says an account is still alive, or has
  caught up, after it went online; no network's keep-alive is recorded. LinkedIn's
  `Heartbeat` handler only refreshes cookies; Signal's helper beat is between kithd and
  the helper. `Status` carries no version.
- **Resuming.** `Messages` resumes from a change number in the bridge's epoch, at most
  5000 behind (`tailBehind`), else `read_again`. Another bridge's number is always
  another epoch, so today a client failing over reads everything again.
  `message_changes` has no time column.
- **Sends.** `Draft.txn_id` dedupes a repeated send on one bridge. Across bridges it
  already holds where the network ID is derived from it: Telegram's `random_id`
  (fnv of the txn), Google Messages' tmp ID (SHA-1 UUID of it), LinkedIn's origin
  token (the txn itself). It does not where the ID is minted and remembered in memory:
  WhatsApp (`messageIDFor`), Signal (`sentAt`, the send time is the ID), Instagram
  (`otid`). Messenger mints a fresh `otid` on every send, so even a retry on one bridge
  is a second message. Matrix's `txnId` dedupes per device only. Every client mints a
  txn (kith-mcp too).
- **Notifications.** A notice's ID is `<boot>-<count>` (`Notifications.notify`): unique,
  but two bridges notifying one message give two IDs, so kith-notify shows both.
- **The mesh's roster** lists every bridge with its key and addresses
  (`Store.Bridge`), and every device; a bridge renders the roster into the paired
  devices it admits. So a client
  joined to one bridge can find and dial the others with the key it has.

## Calls

Decided 2026-11-25: 2, 3 and 5 as recommended, 7 with every hold time a setting; 1, 4
and 6 left on the recommendation (6's #632 has merged).

1. **The beat is a stream.** `Beat` (server stream on BackendService) sends the bridge's
   whole status every 30 s and at once on any account's change. The proxy holds one per
   bridge in its list; a standby kithd holds one per higher bridge. *Rejected:* polling
   `Status` every 30 s (an account going stuck waits for the next poll, and each poll is a
   request where the stream is one frame).
2. **One rule, per account, for both standby and the client's choice.** An account is
   *held* by a bridge when its beat is fresh and the account is alive and caught up (or
   stuck for a reason that is not the bridge's: the GM phone, a network down). It is
   *active* on the highest-priority bridge that holds it. A standby bridge connects an
   account only when no higher bridge holds it; a client takes each account's rooms from
   the bridge where it is active. When the top bridge holds everything, that is the
   whole-bridge rule of 2026-11-19; when it holds all but WhatsApp, WhatsApp alone moves
   down, rather than the whole bridge being unhealthy while its other accounts are fine.
   *Rejected:* whole-bridge health first and per account only when no bridge is healthy
   (two rules, and the switch between them is a third place to flap).
3. **Priority is the bridge's own**, `[bridge] priority` in that machine's `device.toml`
   (higher first; none means never standby: always active, as today), sent in its beat.
   Clients and standby bridges read it from the beat, so they cannot disagree. Ties go
   to the lower key ID. A client's `[[bridge]]` order is only the order it dials in to
   hear the beats. *Rejected:* a priority per client (two laptops choosing different
   active bridges while the bridges themselves agree on another).
4. **The `[[bridge]]` list is filled from the roster.** The proxy asks its bridge for the
   roster's bridges and adds those missing, by key ID, with the roster's addresses
   (tailnet ones included, per remote.md); a bridge removed from the roster is dropped.
   *Rejected:* `kith join` per bridge (one join already admits the device everywhere).
5. **Send IDs derived from the txn on every network.** WhatsApp's message ID, Instagram's
   and Messenger's `otid` and Signal's send time computed from the txn rather than
   remembered, so the second bridge sends the same message the network or its clients
   already hold; the txn carries its own time (`kith-<ms>-<random>`) for Signal's. Matrix
   puts the txn in the content (`kith.txn`) and the bridge checks the room's last 50
   events for it before sending. *Each is probed live before it counts* (step 4).
   *Rejected:* the timeline check on every network (WhatsApp and Signal do not echo own
   sends to a second device reliably, and the check is a race the derived ID is not).
6. **Failover builds on the proxy's store (#632).** The proxy resumes each bridge from its
   own position and writes what arrives into the one store, which already dedupes by
   room and event ID; clients keep reading the proxy's numbering and never see a
   failover. *Rejected:* failing over in a forwarding-only proxy (it would have to
   renumber, or send every client `read_again` at each failover).
7. **Hold times, each a setting** (Eugene, 2026-11-25): `[failover]` in `config.toml`,
   for every device, and in `device.toml` over it, as `[gui]` is: `stale_after`,
   `move_back_after`, `stand_down_after`, `take_over_after`, `standby_connect_every`.
   Their defaults: a client fails over at once on a dropped connection and after 90 s
   without a beat; it moves back up to a higher bridge after that bridge has held the
   account for 2 min. A standby bridge stands an account down after a higher bridge has
   held it for 2 min, and takes it over after 5 min with no higher bridge holding it.
   A standby bridge connects each idle account once a day until caught up (WhatsApp and
   Signal drop a linked device after about 30 days idle; checked in step 6, open
   question 8).

## Plan

Each step is its own PR, lands working behind defaults that keep today's behaviour,
and gets a failure review (CONTRIBUTING.md) before it is done.

### Step 1, feat: each account's heartbeat, and kithd's version

- `NetworkStatus` gains `alive_at` (the last evidence in the table's "alive" column),
  `caught_up` (since the latest connect), `not_ours` (stuck for a reason every bridge
  shares: the GM phone, the network down) and, for Matrix, `keys_ready` (this device
  cross-signed and the key backup restored, or none to restore). `StatusResponse` gains
  `version` (`buildinfo`) and `priority` (zero until step 6).
- Each adapter records its evidence: Matrix's `/sync` answers; whatsmeow's keep-alive
  (`KeepAliveTimeout`/`KeepAliveRestored`) and `OfflineSyncCompleted`; the Signal
  helper reporting the server websocket's keep-alive and `QueueEmpty`; Telegram's ping
  and `getDifference` with no gap; Slack's ping and hello; LinkedIn's realtime
  `Heartbeat` and the sync-token listing; GM's long poll and phone events; messagix's
  keep-alive and first sync.
- `Beat` (call 1) and the per-network windows; `kith status` shows each account as held
  or stuck and why, and the version.
- **Measuring.** kithd logs each account's longest gap between alive evidence per hour.
  A week of it on elitedesk's LinkedIn, Messenger and Instagram accounts sets their
  windows (3 min, 90 s, 90 s until then), recorded in design-clients.md's table.
- **Failure review** (caches a derived result: held is derived from events): a generator
  over each adapter's event sequences (connect, keep-alive, timeout, restored, queue
  drained, disconnect, reconnect mid-drain, the clock passing the window) asserting
  held exactly when the table says; blind spots: a reconnect that resets `caught_up`
  before its drain, a keep-alive answered on a connection already replaced, a quiet
  account.

**As built** (2026-11-25):

- **Two kinds of alive.** Only Matrix (each `/sync`), Slack (each pong, `LatencyReport`)
  and LinkedIn (each realtime `Heartbeat`) hand kith one event per answer. whatsmeow,
  signalmeow, gotd, libgm, messagix and instameow say only when their keep-alive *fails*,
  and reconnect on their own; for them an adapter reports `HeardAnswering` when it
  connects and `HeardSilent` (with the last answer) when the library says so, and the
  account is alive at any moment in between. A silence whose last answer is from before
  the account last went offline is an earlier connection's, heard late, and ignored.
- **Caught up**, per network: Matrix the first sync; WhatsApp `OfflineSyncCompleted`;
  Signal `QueueEmpty`, which the helper now forwards (`signal.v1.QueueEmpty`, so helper
  and kithd must be the same build); Slack the catch-up fetch finishing; LinkedIn the
  sync-token listing; Messenger messagix's connect, which comes after its first sync;
  Instagram the changes taken reaching the latest sequence ID there was at connect;
  Google Messages the long poll open with the phone answering. **Telegram** counts as
  caught up when gotd's update manager starts, before its `getDifference` returns: gotd
  has no hook for that.
- **not_ours** is only Google Messages' phone (`PhoneNotResponding`). A network down for
  everyone is not told apart from one down for this bridge.
- **keys_ready**: cross-signed and holding the backup key *or room keys* (a restore by
  recovery key or passphrase imports keys without keeping the backup key), or no
  cross-signing at all. A device with keys only from its own sends counts as ready.
- **version** is `buildinfo.Short`: the version and the commit's first 12 characters,
  since every local build is "dev".
- **Measuring**: an `alive gaps` line per account per hour in kithd's log
  (`longest`, `window`). It measures only the three networks with an event per answer;
  for Messenger and Instagram there is nothing to measure, since messagix and instameow
  report no answer, only a dropped socket, so their 90 s stands for a silence the
  library has already turned into a reconnect.
- **Held** is computed by whoever reads the status (`daemon.NetworkStatus.Held`) against
  its own clock; a client whose clock is off a remote bridge's by more than a window
  misjudges it. Step 5 should judge a beat by when it arrived.
- **Failure review**: `TestStateHeldFollowsItsConnection` walks every sequence of five
  steps (12 kinds) from four named starts against a model that tracks connections, not
  times; each guard in `State` was removed in turn and the test caught it. Left: a
  caught-up from an earlier connection arriving after the next connect (adapters order
  their own events, and none sends one late that we know of), and a quiet account that
  is actually broken on an answering-kind network (the library's own keep-alive is
  trusted).

### Step 2, fix: a notification's ID is its message's

- The notice ID is the message's event ID, and its revision for an edit that raises one;
  notices with no message (backups, media) keep `<boot>-<count>`. kith-notify and the
  window already show one ID once per profile; they now show one message once across
  bridges (two active after a partition, or a client failing over mid-notification).
- Small and independent; can go first.

### Step 3, feat: a stream resumes from a time

- Schema: `message_changes` gains `at` (when this bridge committed the change), indexed;
  rows before the migration get the message's own time.
- `MessagesRequest` gains `since_time`. The bridge replays every change committed at or
  after it less a 2 min grace (clock skew, and a message the old bridge took a moment
  before this one); older than its oldest change, `read_again` as today.
- **Failure review** (merges what two writers wrote: two bridges' copies of one
  message): `TestTheCacheAndTheClientResolveHistoryAlike` gains a second bridge whose
  changes arrive in another order and at other times, resumed from any time the first
  was last heard; the store's rows must equal either bridge's cache once settled. Blind
  spots: an edit or redaction that reached one bridge only, a clock 90 s apart, a resume
  time inside a burst, ties at one millisecond.

### Step 4, feat: a send goes out once whichever bridge sends it

- Call 5 on every network, Messenger included (its `otid` from the txn, which also
  makes a retry on one bridge safe); the proxy mints a txn for a send that comes
  without one.
- **Probes before merging**, each on a live account: the same send from two bridges of
  one account delivered once on WhatsApp (to another phone, and in a group), Signal,
  Telegram, Instagram, Messenger, LinkedIn and Matrix. A network where it shows twice
  gets the timeline check, or is listed in the PR as not safe to retry across bridges
  (the proxy then does not resend there, step 5).
- **Failure review** (concurrent): a send whose answer is lost after it went out, retried
  at a second bridge; the first bridge sending late, after the second; both at once; an
  edit of a message only one bridge has seen.

### Step 5, feat: the proxy fails over

- The `[[bridge]]` list filled from the roster (call 4), and the client half of
  `[failover]` (call 7); the proxy dials every bridge's
  `Beat`, and serves each account's rooms from the bridge where it is active (call 2),
  which while no bridge has a priority is the first in the list that holds it.
- On a switch: `Messages` resumed at the new bridge from the time last heard (step 3),
  its own position kept per bridge in the store; the unary calls go to the account's
  bridge.
- `Send` buffered up to 5 s when the bridge drops, then sent to the next with its txn
  (step 4); an upload in flight fails, as today, and the client starts it again.
- `Status` through the proxy names the bridge in use per account; the window and the TUI
  show it beside the connection line.
- This is where "a client on several bridges" lands too: one store, each room from the
  bridge whose accounts hold it, each profile's room set merged across bridges
  (design-mesh.md decision 6, design-desktop-cache.md, Failover).
- **Failure review** (concurrent, merges writers): a model test of the proxy against two
  or three fake bridges whose beats go stale, drop and come back in every order, with
  sends and a client's streams in flight; it asserts no message a bridge delivered is
  missing from the store once settled, no send is lost or sent through two bridges
  without its txn, and the proxy does not swap bridges inside the hold times. Blind
  spots: a bridge back before the 2 min hold, two bridges going at once, the roster
  removing the bridge in use, a bridge whose key rotated (it is the same bridge).

### Step 6, feat: standby bridges

- `[bridge] priority` (call 3) and the standby half of `[failover]` (call 7); kithd dials each higher bridge's `Beat` over the mesh's
  addresses and connects only the accounts none of them holds (call 2), with the hold
  times of call 7. A standby account connects once a day until caught up.
- `kith status` and the settings say which accounts are on standby and which bridge
  holds them.
- **Probes**: how long WhatsApp, Signal and Google Messages keep an idle linked device,
  and how long each queues messages for it (open question 8); the daily connect is
  changed if any is shorter.
- **Failure review** (concurrent): the model test of step 5 gains the bridges' own
  decisions, partitions between them and between them and clients, and clocks apart; it
  asserts that once connected and settled each account is active on exactly one bridge,
  the highest holding it, and that a partition leaves it active on each side and no
  account on none. Blind spots: two bridges of equal priority, a bridge that holds an
  account but whose beat cannot reach the standby one, Google Messages taken by a lower
  bridge while a higher one still holds it.

## Left as is

- Lowest-latency selection and sending through two bridges at once (decided against).
- A relay for bridges behind NAT: Tailscale, documented in remote.md.
- Clients that do not run kith-proxy (another OS, or it not answering) dial their first
  bridge as today and do not fail over.
