# Design: replay (Phase 1 of design-clients.md)

Status: planned 2026-10-28. Phase 1 of [design-clients.md](design-clients.md), in
today's kithd with one bridge. The mesh CRDT, once planned for this phase, moved to
Phase 2: with one kithd and a TUI that keeps no store, there is one replica and nothing
to merge, so it lands with its first second replica (a paired bridge).

## Goal

A client that loses some of the message stream gets the rest from kithd's cache instead
of going without. Today the stream starts empty and nothing resumes it:

- **A slow reader loses values.** Each RPC subscriber queues 64 values and the rest are
  dropped (`internal/daemon/hub.go`); so does each adapter's own channel, and Matrix
  has a second such hop (`internal/matrix/streams.go`, `merge.go`). A burst after
  suspend is hundreds of messages. A dropped message in the open room is missing from
  the timeline until the room is opened again.
- **A reconnect re-reads, it does not resume.** When kithd restarts, the TUI reloads
  rooms, unread, last messages and the open room (`internal/tui/attached.go`, "no
  stream replays what arrived meanwhile"). That covers what is on screen, not what a
  client that keeps its own copy (Phase 4, 5) would need.

Phase 3's failover is built on the same request: a client moving to another bridge asks
it for what it missed.

## What a position is

kithd numbers every change to a cached message: a new message, an edit, a redaction, a
message seen again with more (media, its order). The number is a **change**, counted up
by this cache alone, and the cache has an **epoch**, a random ID made when the cache is
created and made again when it is rebuilt or cleared. Every message on the stream
carries both.

A client resumes with the last `(epoch, change)` it holds, and kithd replays every
cached message whose change is greater, in change order, then continues live.

**Why not the network's own position** (the last event ID per room, as
design-clients.md first had it): what changes is not always new. An edit or a
redaction rewrites a message whose place in the room is old; WhatsApp's offline queue,
Signal's delayed delivery and every history sync bring messages older than ones already
held. "Everything after event X" resends none of them. A change number does, because
it counts writes, not places in the room.

- The change is assigned inside the transaction that writes the row, and the cache has
  one writer connection, so the order changes commit in is the order they are
  numbered: a client that has seen change 11 has seen 10.
- Replay sends the message as the cache holds it now, not each change in turn; a
  message edited twice while a client was away arrives once, edited.
- **An unknown epoch** (the cache was rebuilt, or the client asks another bridge)
  means the change means nothing here: kithd answers that it cannot resume, and the
  client reloads as it does today.
- **Too far behind**: past a bound (some thousands of changes), kithd answers the same,
  and the client reloads instead of being sent the whole backlog.
- Profiles hold as on every stream: replay sends only rooms in the client's profile and
  leaves out people the profile hides.

## The stream

```proto
message MessagesRequest {
  // Resume after this change in this epoch; empty starts live, as today.
  string since_epoch  = 1;
  int64  since_change = 2;
}

message MessagesResponse {
  Message message = 1;
  string  epoch   = 2;
  int64   change  = 3;
  // Set on the first frame alone, when since could not be honoured: the client
  // reloads, then continues from this frame.
  bool    reset   = 4;
}
```

**The stream tails the cache.** Every adapter writes a message to the cache before it
puts it on its channel, so the cache already holds everything the stream carries, and
more. After each commit that numbered changes, the cache says so; each stream reads the
changes after the last one it sent, in profile, and sends them. Live and replay are one
path: a resume is a stream that starts further back. A slow reader lags and catches up;
nothing is dropped, whatever an adapter's channel dropped, and the sync loop never
waits on a reader. (Rejected: stamping the adapters' live values with their change and
ending a slow subscriber. Drops inside an adapter stay invisible, and a gap in the
numbers cannot be told from history pages that were cached but never sent.)

What this changes for a client:

- **A value is the row as kithd keeps it**, not a delta: an edit arrives as the edited
  message, a deletion as the deleted one. The client replaces the row by ID instead of
  merging. Live-only fields go: `Reverted` (a replaced row needs none), `Late`
  (never on the wire; the notifier keeps the adapters' channel). `RedactedAt`, which the
  history view shows, is kept in the cache so the row carries it.
- **History reaches the client too**: pages a network backfilled, which today only
  the cache saw. They merge into the timeline by their place in the room.
- **A write that changes nothing is not a change**: a sync echo or a redelivery of a
  cached message is not resent.

The notifier stays on the adapters' channel: it needs `Late` and `Placeholder`, and
what it misses is a separate matter from this stream.

## The client

`daemon.Remote` keeps the last `(epoch, change)` it delivered and sends it whenever it
opens the stream: after a reconnect, after a profile switch (where a change outside the
new profile was never delivered, so the client reloads anyway). The TUI replaces a row
by ID when one arrives, keeps its reload on reconnect for rooms and unread, and no
longer re-reads the open room when the resume succeeded.

## Plan

Each PR lands working.

1. **feat: kithd numbers message changes.** The epoch in the cache's metadata, made at
   creation and on rebuild or clear; a side table `message_changes(room_id, event_id,
   change)` with an index on `change`, written in every transaction that changes what
   the cache returns for a message (upsert, side rows, deletion, revert, expiry, poll),
   and only when it does; `RedactedAt` kept. A cache call reading the changes after a
   number, and a signal after each commit that numbered one. Migration, fingerprint
   row, docs/database.md. **Failure review** (caches a derived result): an edit and a
   redaction of an old message, a message written twice in one transaction, a sync
   echo that changes nothing, a side row alone changing (media, mentions, order), a
   trimmed message, two adapters writing at once, a clear and a rebuild.
2. **feat: the message stream tails the cache.** The request and response fields
   above; the stream reads changes in profile from `since` and then on each signal;
   `reset` for an unknown epoch or too far behind. The RPC stream leaves the hub.
   protoconv and its round trip. **Failure review** (concurrent, enforces scope): the
   generator interleaves cache writes with a resuming reader and asserts every change
   after `since` arrives exactly once, in order, as the cache holds it; plus a profile
   reload mid-read, a room leaving the profile between since and now, a hidden
   person's message, a since from the future, a signal while the reader is mid-batch.
3. **feat: the client resumes and replaces rows.** `Remote` keeps and sends the
   position and handles `reset`; the TUI replaces rows by ID and stops re-reading the
   open room after a resume. **Failure review** (concurrent): kithd restarting
   mid-burst, a burst larger than the old queue, a profile switch mid-read, two
   windows on one kithd, a history page into the open room.

## Left open

- **Resuming at another bridge** (Phase 3): a change means nothing there. The client
  sends the time it last heard from its bridge, less a grace window for clocks and
  delivery, and dedupes by network ID; decided with failover.
- **Reactions and activity** resume the same way if a gap shows up there; today the
  TUI reads reactions when it opens a room.
