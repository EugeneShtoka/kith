# Design: replay (Phase 1 of design-clients.md)

Status: planned 2026-10-28. Phase 1 of [design-clients.md](design-clients.md), in
today's kithd with one bridge. The mesh CRDT, once planned for this phase, moved to
Phase 2: with one kithd and a TUI that keeps no store, there is one replica and nothing
to merge, so it lands with its first second replica (a paired bridge).

## Goal

A client that loses some of the message stream gets the rest from kithd's cache instead
of going without. Today the stream starts empty and nothing resumes it:

- **A slow reader loses values.** Each RPC subscriber queues 64 values and the rest are
  dropped (`internal/daemon/hub.go`); a burst after suspend is hundreds of messages. A
  dropped message in the open room is missing from the timeline until the room is
  opened again.
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
- **Too far behind**: past a bound (some thousands of messages), kithd answers the same,
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

kithd subscribes the client to the live hub first, then reads the cache for changes
after `since`, sends them, and then passes on live values, dropping any whose change it
has already sent. A message written between the subscribe and the read arrives once.

**The hub stops dropping messages on this stream.** A subscriber whose queue fills is
ended with a "behind" error instead of skipped, and the client resumes from its last
change; the sync loop never blocks on a slow reader, and nothing is lost. The other
streams keep their lossy hub: each is recovered by a cache read it already pairs with.

## The client

`daemon.Remote` keeps the last `(epoch, change)` it delivered and sends it whenever it
opens the stream: after a reconnect, after "behind", after a profile switch (where a
change outside the new profile was never delivered, so the client reloads anyway). The
TUI keeps its reload on reconnect for rooms and unread; the open room no longer needs
re-reading when the resume succeeded.

## Plan

Each PR lands working.

1. **feat: kithd numbers message changes.** The epoch in the cache's metadata, made at
   creation and on rebuild or clear; a side table `message_changes(room_id, event_id,
   change)` with an index on `change`, written in every transaction that writes a
   message row; `domain.Message` carries its change, set by the write. Migration,
   fingerprint row, docs/database.md. Verify first that every message an adapter puts
   on the stream has been written to the cache before it is sent, or the change does
   not exist yet. **Failure review** (caches a derived result): an edit and a
   redaction of an old message, a message written twice in one transaction, a message
   seen again with nothing new (no change, or a change: decide and test), two adapters
   writing at once, a clear and a rebuild.
2. **feat: the message stream resumes from a change.** The request and response fields
   above, replay in profile, subscribe-then-read with the overlap dropped, `reset` for
   an unknown epoch or too far behind, "behind" in place of a dropped value. protoconv
   and its round trip. **Failure review** (concurrent, enforces scope): the generator
   interleaves cache writes with a resuming subscriber and asserts every change after
   `since` arrives exactly once and in order; plus a profile reload mid-replay, a room
   leaving the profile between since and now, a hidden person's message, a since from
   the future, a client behind twice in a row.
3. **feat: the client resumes instead of losing messages.** `Remote` keeps and sends
   the position, handles `reset` and "behind"; the TUI stops re-reading the open room
   when the resume succeeded. **Failure review** (concurrent): kithd restarting
   mid-burst, a burst larger than the old queue, a profile switch while behind, two
   windows on one kithd.

## Left open

- **Resuming at another bridge** (Phase 3): a change means nothing there. The client
  sends the time it last heard from its bridge, less a grace window for clocks and
  delivery, and dedupes by network ID; decided with failover.
- **Reactions and activity** resume the same way if a gap shows up there; today the
  TUI reads reactions when it opens a room.
