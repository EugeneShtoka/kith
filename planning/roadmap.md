# Roadmap

What is planned, in order. Items land when they are ready, each behind configuration
that defaults to today's behaviour. For what already exists, see
[docs/README.md](../docs/README.md).

**Design docs for the larger initiatives** live beside this file:
- [design-clients.md](design-clients.md): the bridge/device/GUI split, N:M
  bridge-device topology, a mesh CRDT, and Electron and Android clients
- [design-adapters.md](design-adapters.md): how networks plug in (IDs that name their
  network and account, capability interfaces, adapters in-process or as a helper)
- [design-matrix-accounts.md](design-matrix-accounts.md): several Matrix accounts in
  one kithd, Matrix IDs that name their account, and the old `[[profile]]` removed
- [design-profiles.md](design-profiles.md): profiles in today's kithd (Phase 0 of
  design-clients.md), one window per profile, and the PRs that build them
- [design-mailbox.md](design-mailbox.md): a mesh peer that holds sealed ops it cannot
  read, for bridges that are never online at the same time (proposed, undecided)

Every item below is described here in full; nothing depends on notes kept elsewhere.

## Now

- **Google Messages: older history (#105).** Conversations, text, replies, reactions,
  typing and attachments work both ways; what is missing is the history from before
  kith was paired. The phone relays everything, so the backfill has to page through it
  without holding the phone's single web session for long.
- **LinkedIn: received attachments, checked live (#100).** Attachments are sent and
  shown; that a received one downloads has only been tested against fixtures.

## Next

Ordered. Anything that makes later work cheaper or safer goes first; anything that
changes a default goes while changing defaults is still free.

| step | items | why these together |
| --- | --- | --- |
| **1: several Matrix accounts** | `[[matrix.account]]` · Matrix IDs as `matrix:<account>/…` · Matrix-only calls name their account · `[[profile]]` removed | **Done** (#328–#338). **See [design-matrix-accounts.md](design-matrix-accounts.md).** Five PRs. Matrix is the one network limited to one account per daemon; every other network's accounts already sit side by side. Goes first because profiles (step 2) reuse the name `[[profile]]`, and because Matrix's bare IDs are the last place an account is implicit. Includes signing a Matrix account out from kith. |
| **2: multi-device and client topology** | profiles (named setups a device picks: rooms, people, rail, archive rules; unread, notifications and push topics per profile) · N:M bridges and devices · mesh CRDT for soft state · Electron client · Android client | **See [design-clients.md](design-clients.md); Phase 0 in [design-profiles.md](design-profiles.md) (done), Phase 1 in [design-replay.md](design-replay.md), Phase 2a in [design-remote.md](design-remote.md), Phase 2b in [design-mesh.md](design-mesh.md), Phase 4 in [design-electron.md](design-electron.md).** Seven phases, from profiles inside today's kithd, to an optional bridge running on the phone. The largest initiative here. Phase 0 (profiles) is useful on its own with one bridge; Phases 4 and 5 (Electron, Android) take the most calendar time. |
| **3: names, everywhere the same way (S4)** | the rest of the name pipeline | `first_name_only` already resolves against the room's own space (the first by `[display] space_priority` when a room is in several), and identity merge and room aliases are built. What is left is the rest of a global → space → chat pipeline: more rule types beside `first_name_only` (for example stripping a bridge's suffix, or a fixed name for one person in one space), and a per-chat rung that overrides the space's rule. It goes through the single narrowest-wins scope resolver every other setting uses, rather than a resolver of its own. It changes what `first_name_only` means in a room whose space has no rule, so the default is decided before it is built. |
| **4: Matrix sessions** | list an account's other sessions, rename them, sign old ones out | A lost laptop's session can only be ended from another client today. Signing out another session revokes its access token on the homeserver. |
| **5: verifying other people** | in-room SAS verification of another person | Today kith verifies only your own sessions (`:verify`, emoji SAS). Verifying a contact is the same SAS exchange carried in the room (`m.key.verification.request` as a room message) instead of to-device. |

## Follow-ups

Gaps that finished work left, most found by its failure reviews. Not ordered; each is
small and ships on its own.

### Desktop client

- **A loaded room that ends up empty.** If a frame moves a room's only row out of what
  the timeline holds while its next page fails, the timeline is loaded, empty, and
  nothing asks again. Found at 20 000 runs of the timeline property; it predates #449.
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
- **Words typed during a profile switch**, for a room the new profile does not hold,
  are saved only once a profile that holds it is back.

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
- **`/call` with no setup.** The Jitsi command template needs no account; offering
  `/call` from it by default on a fresh install is undecided.
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
- **Native macOS notifications.** Today the desktop notification sink is D-Bus; on
  macOS the notification command hook is the way to be notified.

## Deferred

- **Pinned messages (#76)**: reading `m.room.pinned_events` and listing a room's pins.
  Deferred on a measurement: pins turned out to be rare in the rooms it was measured
  on. It comes back when someone needs it.

## How to read this

- **IDs** (`#105`, `S4`) are stable identifiers, not order: commit messages refer to
  them, so they are never renumbered. They are not GitHub issue numbers.
- Every item ships on its own, behind configuration that defaults to today's
  behaviour.
