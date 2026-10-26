# Design: profiles (Phase 0 of design-clients.md)

Status: in progress. Planned 2026-10-25; steps 1–4 done (#340–#344). Phase 0 of [design-clients.md](design-clients.md), in
today's kithd with one bridge; the name `[[profile]]` is free since
[design-matrix-accounts.md](design-matrix-accounts.md).

## Goal

A profile is a named setup a client runs: which rooms and people it shows, which rooms
are archived in it, and its rail. Profiles live in kithd's config; a client names the
one it runs on every request, and kithd answers rooms, unread, search and the rest for
that profile. Each profile can have a kith window of its own on the device, so two
terminals can run "work" and "personal" at once against one kithd.

Today the TUI builds the rail, tags, badges and unread totals itself, from its own copy
of the config; kithd sends per-room counts and every room. Notifications, filters and
hidden people are decided in kithd. No request says who asks: the only client identity
is the seat (one TUI window per daemon), which guards draft writes and the hand-off of
a clicked link.

## Config

```toml
[[profile]]
name    = "default"         # the starter writes it
include = ["*"]             # every room

[[profile]]
name    = "phone"
include = ["dm", "tag:Family", "protocol:signal"]
exclude = ["space:Work"]
people  = { exclude = ["role:colleagues", "whatsapp:+15550100"] }
archive = { rooms = ["space:Bots"], push = false }
rail    = { order = ["tag:Unread", "tag:Family", "*"] }

[[profile]]
name    = "work"
include = ["space:Work", "protocol:slack"]
```

- **There is always a profile, written down.** A config with no `[[profile]]` is
  refused; the starter writes `default` with `include = ["*"]`. The first profile is
  what a client runs when it names none. Names are unique and not empty.
- **Rooms**: `include` / `exclude` take the place vocabulary (`space:`, `tag:`,
  `place:`, `room:`, `protocol:`, room IDs, `dm`, `group`), and `"*"` in `include`
  is every room, as in a tag's rule. An empty `include` is refused; `exclude` wins. A `tag:` whose rule reads a room's state (`unread`,
  `mention`, `draft`, `invite`, directly or through another tag) is refused at
  startup: a profile names rooms, not their state, and a room must not enter and leave
  a profile as it is read.
- **People**: `people.exclude` takes person IDs (any network's, as `[[tag]] people`
  does) and phonebook card fields, `key:value` (`role:colleagues`: everyone whose card
  has a `role` line holding `colleagues`, matched as search matches it). An entry whose
  key is a network's prefix is an ID, never a card field. An excluded person's
  messages are hidden in that profile as a spam-marked person's are everywhere: not in
  the timeline, search, counts or notifications; their identity's linked accounts with
  them. Never you.
- **Archive**: `archive.rooms` (place vocabulary) replaces the archive tag's `rule`
  in that profile: these are the rooms archived here. Hand filing (`S`, `/tag
  Archived`) still writes the tag's shared `picked` / `excluded`. `archive.push` is
  that profile's `push_archive`.
- **Rail**: `rail` replaces `[display.rail]` in that profile (order, hidden,
  hide_when_empty). A profile's value replaces the shared one, never deep-merges.

### Archive and the networks

A network's archive (Telegram's Archived folder, WhatsApp's archived chats) is one per
account, shared by every device and so by every profile. The existing knobs keep their
meaning:

- `[<network>.archive] follow`: a chat archived on the network is in the archive tag,
  in **every** profile.
- `[<network>.archive] mirror`: hand filing in any profile archives or unarchives on
  the network.
- `push`: a profile's archived rooms are pushed to the network. Pushing profiles
  combine as a union: a chat is archived on the network while any pushing profile
  archives it, and unarchived only when none does, so two profiles never undo each
  other.

So a profile's own archive is private to it only for networks it neither pushes nor
mirrors to, or whose archive is not followed. With sync on, it is the account's
archive, and every profile that follows that network sees it.

## The wire

- **The profile rides on every request** as a header, `Kith-Profile`, set by
  `daemon.Remote` through a Connect interceptor and read by one on the server into the
  request's context. No header means the first profile. An unknown name is an error
  (`api.ErrUnknownProfile`), never the first profile. kithd stores nothing about it.
- **What kithd narrows.** Every `BackendService` method is in one table, and a test
  fails on a method missing from it, so a new RPC must be classified:
  - *room lists, counts and search* answer only the profile's rooms: `Rooms`,
    `RefreshRooms`, `Spaces` (a space holding no profile room, directly or through a
    subspace, is left out), `CachedUnread`, `UnreadStream`, `CachedInvites`,
    `Invites`, `SearchMessages`, `LastMessages`, `Drafts`, `RoomsWith`, and the rest
    the table names;
  - *calls naming a room* answer for any room, in the profile or not: a link, a jump
    or `:join` opens it. The message, reaction and activity streams carry every room
    for the same reason, and a client ignores the rooms it does not show;
  - *not about rooms*: `Status`, `Accounts`, logins, config.
- **Membership** is kithd's `scopeIndex` facts (tags judged without room state), so
  kithd and the TUI agree on a room for the same config. It follows rooms, spaces,
  tags and config reloads; a stream re-checks each value as it is sent.
- **kith-mcp** names no profile, so it runs in the first one; naming one is open
  question 12 in design-clients.md.

## One window per profile

The seat becomes one per profile on a device. In Phase 0 every client is on kithd's
own machine, so a second window on the same profile is refused (or takes over with
`--force`), as today, and a window on another profile is seated beside it. Draft
writes stay named per window; a room in two profiles has two writers, and the drafts'
compare-and-swap (`ReplaceDraft` with `over`) already takes a second writer, as it does
for kith-mcp.

**A clicked link** (`kith --open`, `matrix:` / `tg:` links) goes to:

1. the window that has the link's room open;
2. else the most recently used window whose profile includes the room;
3. else the most recently used window;
4. with none open, the next window to attach (as today).

Windows tell kithd the room they have open and when a key was last pressed, over the
seat stream; kithd holds it in memory while the seat is held and forgets it after. A
link whose room is not known yet (an alias not resolved) goes by rule 3.

## The TUI

- `kith --profile <name>`; with none, the first profile.
- A key opens a picker of profiles; picking one re-takes the seat for that profile
  (refused if a window there holds it), reloads rooms, spaces and unread, and
  re-subscribes. The status line names the profile when there is more than one.
- The choice is not remembered between runs in Phase 0.
- The rail, badges and Unread row narrow on their own: what they are built from
  already has.

## Notifications: a topic per profile

Decided 2026-10-26. kithd decides once per message whether it notifies: rules,
filters, mutes, do-not-disturb, the rate limit and folding late ones. It then
publishes the notification, under one ID, to the topic of every profile holding the
room. It knows profiles, not who listens: no device, window or "open profile" is
tracked.

`kith-notify` shows them. It runs on each desktop as a user service (`kith` installs
and starts it as it does kithd), subscribes to the topics it names
(`--profile work --profile home`; the first profile without one), and shows each
notification once by its ID: a message in two profiles arrives on both topics. It
shows with the sinks kithd uses today (D-Bus, the command hook), reads the
`[notifications]` and `[clipboard]` settings from kithd's config as the TUI reads
its own, copies a code to its machine's clipboard, and reconnects when kithd
restarts. kithd stops showing anything and no longer needs D-Bus.

- Over the socket (and TLS in Phase 2) a topic carries the whole notification; a
  push topic (Phase 2, for Android) carries a wake only.
- Do-not-disturb stays in kithd for now, since the TUI toggles it there; the device's
  own do-not-disturb (design-clients.md) comes later.
- Not the `kith-notify` rejected on 2026-10-24, which held each device's rules; this
  one holds none.

## Plan

Each PR lands working.

1. **feat: `[[profile]]` in the config; kithd's notifications follow the first
   profile** (until step 5). Config, validation, the implicit `default`, the starter config, docs.
   `domain` gains the profile's room test over `RoomFacts`. kithd's notifications skip
   rooms outside the first profile. **Failure review** (enforces scope): a room whose
   spaces are not known yet, a tag that names a missing tag, a state word reached
   through a `tag:` chain, `not` entries, an empty `include`, a reload that removes the
   profile.
2. **feat: kithd answers for the profile named on the request.** Header, interceptors,
   `ErrUnknownProfile`, the method table and its completeness test, narrowing lists,
   counts, search and streams, spaces left out when empty. **Failure review**: the
   generator builds rooms, spaces and profiles and asserts no room outside the profile
   appears in any narrowed reply or stream value; plus a missing header, an unknown
   name, a reload mid-stream, a room moving into or out of a space or tag while
   subscribed.
3. **feat: the TUI picks a profile.** `--profile`, the picker key, the status line,
   reload and re-subscribe on switch.
4. **feat: one kith window per profile; links go to the right one.** The seat per
   profile, open room and last use over the seat stream, the link rule. **Failure
   review** (concurrent): two windows switching profile at once, a window taking over
   while a link waits, a seat lost mid-hand-off, every exit route, drafts in a room
   both profiles show.
5. **feat: kithd publishes notifications per profile; `kith-notify` shows them.**
   The topic stream (subscribe by profile names; one notification ID across topics;
   codes to copy ride on it), the `kith-notify` binary, its systemd unit, launchd
   plist and Nix module, `kith` installing it. kithd's own sink stays meanwhile, so
   nothing goes quiet. **Failure review** (concurrent, merges topics): a message in
   two subscribed profiles, a profile removed by a reload while subscribed, kithd
   restarting mid-stream, a folded "n new messages" replacing an earlier popup, a
   slow agent.
6. **feat!: kithd shows nothing itself.** Its in-process sinks and clipboard copy
   go; the agent does both. Breaking: without `kith-notify` running nothing pops up.
7. **feat: people in a profile.** `people.exclude` with IDs and card fields; kithd
   reads the phonebook folder (a depguard rule for it) and follows its changes; a
   hidden set per profile, applied where the shared one is (search, counts, threads,
   members, notifications, model context) and in the TUI's timeline. **Failure
   review** (enforces scope, caches counts): a card edited or removed, an identity
   linking a hidden ID to a shown one, you in an excluded card, a field value that is
   only part of a word, counts after the phonebook changes.
8. **feat: archive and rail per profile.** `archive.rooms`, `archive.push` with the
   union across profiles, `rail`. **Failure review** (caches tags, merges writers):
   the TUI's tag memo across a profile switch; two pushing profiles and `follow` in
   every order; hand filing while a push runs.

## Left open

- `people.include` (only these people): no use named yet.
- Remembering a client's profile between runs; a client on another device (Phase 2)
  will need it as a device setting.
- Which other sections a profile overrides (design-clients.md, open question 2).
