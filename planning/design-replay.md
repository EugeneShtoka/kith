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
- Every room, as the stream always carried: a client may have a room open from outside
  its profile (`profiles.go`), and the people a profile hides are taken out by the
  client's timeline, as before. (Corrected in step 2: this said replay was narrowed.)

## The stream

```proto
message MessagesRequest {
  // Resume after this change in this epoch; empty starts at the newest, as today.
  string since_epoch  = 1;
  int64  since_change = 2;
}

message MessagesResponse {
  Message message    = 1; // absent when read_again
  string  epoch      = 2;
  int64   change     = 3;
  // since could not be honoured, or the cache was cleared: the client reads again,
  // then continues from this frame. (Not `reset`: protobuf's Reset() takes the name.)
  bool    read_again = 4;
}
```

**The stream tails the cache.** Every adapter writes a message to the cache before it
puts it on its channel, so the cache already holds everything the stream carries, and
more. After each commit that numbered changes, the cache says so; each stream reads the
changes after the last one it sent, and sends them. Live and replay are one
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
  history view falls back on, is kept in the cache so the row carries it (step 3).
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
by ID when one arrives (step 2), keeps its reload on reconnect for rooms and unread, and no
longer re-reads the open room when the resume succeeded.

## Plan

Each PR lands working.

1. **feat: kithd numbers message changes.** The epoch in the cache's metadata, made at
   creation and on rebuild or clear; a side table `message_changes(room_id, event_id,
   change)` with an index on `change`, written in every transaction that changes what
   the cache returns for a message (upsert, side rows, deletion, revert, expiry, poll),
   and only when it does: triggers record what a write touched and the transaction
   numbers it before committing (a numbering trigger tripled a save's cost; recording
   adds about a third). Migration, fingerprint row, docs/database.md. **Failure review** (caches a derived result): an edit and a
   redaction of an old message, a message written twice in one transaction, a sync
   echo that changes nothing, a side row alone changing (media, mentions, order), a
   trimmed message, two adapters writing at once, a clear and a rebuild.
2. **feat: the message stream tails the cache.** A cache call reading the changes after
   a number, and a signal after each commit that numbered one (here, not in step 1:
   no exported call exists before its caller). The request and response fields
   above; the stream reads changes from `since` and then on each signal; `read_again`
   for an unknown epoch, a number never given, too far behind, or a clear. The RPC
   stream leaves the hub. The TUI replaces a row by ID (here, not in step 3: merging a
   row onto the shown one keeps an edit whose deletion the row carries). **Failure
   review** (concurrent): the generator interleaves commits with a reader resuming from
   any number and asserts every change after it arrives once at its latest number, in
   order, as the cache holds it; plus a commit landing during a read, a commit larger
   than a read, a since from another epoch, the future or too far back, a clear under a
   reader, two readers, and the stream ending with the backend's messages.
3. **feat: the client resumes.** `Remote` keeps and sends the position and handles
   `read_again`; the TUI stops re-reading the open room after a resume; `RedactedAt` kept in the cache, the one field the TUI
   reads that a row lacks (moved from step 1, where nothing read it yet). **Failure review** (concurrent): kithd restarting
   mid-burst, a burst larger than the old queue, a profile switch mid-read, two
   windows on one kithd, a history page into the open room.

## Left open

- **Resuming at another bridge** (Phase 3): a change means nothing there. The client
  sends the time it last heard from its bridge, less a grace window for clocks and
  delivery, and dedupes by network ID; decided with failover.
- **Reactions and activity** resume the same way if a gap shows up there; today the
  TUI reads reactions when it opens a room.
