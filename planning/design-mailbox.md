# Design: the mailbox, a mesh peer that cannot read (an addition to Phase 2b)

Status: proposed 2026-11-01, nothing decided yet (the questions are at the end). It adds to
[design-mesh.md](design-mesh.md), which it does not change except where
[What steps 5–8 must not assume](#what-steps-58-must-not-assume) says so. Nothing here is
built yet.

## Goal

The mesh syncs two bridges only while they can reach each other. Two machines that are
never on at the same time, such as a desktop used in the day and a laptop used at night,
never converge: each one's read positions, drafts and config edits wait for a meeting
that never happens. Past the 30-day horizon (decision 10) the one that was away comes back
through a snapshot, but only from a peer it can reach.

The **mailbox** is a store that is always reachable. Bridges leave their ops there and
pick up each other's. It runs anywhere: on a VPS of the user's, on a home server, or as a
service run by someone else. So it is built on the assumption that **whoever runs it can
read nothing it holds**:

- it cannot read a key or a value: no room IDs, names, drafts, tags or config;
- it cannot write an op, change one, or pass one off as a member's;
- it can see who talks to it and when, how many ops each device writes, and their sizes
  ([What the mailbox sees](#what-the-mailbox-sees));
- it can withhold or lose what it holds. That is an availability failure, and the
  members' own copies are unaffected ([What the mailbox can do](#what-the-mailbox-can-do)).

Not a goal: messages and media. The mesh never carries them (design-mesh.md, What
crosses). Backing them up is a separate design ([Backups are not the mesh](#backups-are-not-the-mesh)).

## What the mailbox is

**A peer, not a replica.** It holds sealed ops and sealed snapshots and passes them on. It
never writes an op, never applies one, and has no entries, no clock and no config. It is
**not counted** when the mesh asks whether every replica has converged.

- **In the roster** it is an entry with the role `mailbox` (beside `bridge` and `device`),
  added and removed by a member's signed op like any other. The entry holds its addresses
  and the ID its TLS key is pinned to. It has no signing authority: a `mailbox` entry
  never signs an op, and an op it signed would be refused as a non-member's.
- **It never dials.** Every bridge with a `mailbox` entry dials it, with the backoff and
  pinned mutual TLS of `dialBridge`. A bridge behind NAT can use it, since it only dials
  out.
- **More than one** may be listed. Each is independent, and a bridge syncs with each.
  Ops are exactly-once by dot (design-mesh.md, An op), so the same op arriving from two
  mailboxes is a duplicate.
- **Its own admission** is separate from the roster: the mailbox cannot read the roster
  ([Admission](#admission)).

## Sealed ops

Today an op crosses as `{body, sig}`: `body` is every signed field in the mesh's own
encoding, and `sig` is its origin's signature over it. Vectors hold `sha256(body)` per
origin (`Op.Digest`, `internal/mesh/crdt`). Every replica reads every op.

For the mailbox, each op also has an **envelope**:

```proto
message Sealed {
  bytes  origin = 1;      // device ID, in the clear: the mailbox orders by it
  uint64 seq    = 2;      // in the clear: the mailbox's vector is (origin, seq)
  bytes  epoch  = 3;      // which content key sealed it (Keys)
  bytes  nonce  = 4;
  bytes  box    = 5;      // AEAD(key, body ‖ sig), additional data origin ‖ seq ‖ epoch
}
```

- **Sealed once, by the origin, as it writes the op**, in the transaction that writes
  it. Every replica keeps the envelope beside the op and passes on exactly those bytes,
  as it passes on the signed body today. One dot has one envelope everywhere, so a
  mailbox holding two different envelopes under one dot is holding a fork
  ([Equivocation through a mailbox](#equivocation-through-a-mailbox)), never two honest
  re-sealings.
- **The envelope's identity is `sha256(envelope)`.** Heads exchanged with a mailbox carry
  it, where heads exchanged between bridges carry the body's digest as today. A bridge
  holds both for every op.
- **Additional data binds the clear fields**: a mailbox that moves a box to another
  origin or seq produces an envelope that does not open.
- **Padding**: `body ‖ sig` is padded to the next of 256 B, 1 KiB, 4 KiB, then a
  multiple of 4 KiB, so a read position, a draft and a config write do not tell
  themselves apart by size. The 1 MiB bound on an op stays.
- **The clock and the context are inside the box.** The mailbox cannot order ops by
  causality. It does not need to: it sends each origin's ops in seq order, and the
  receiver holds back what it cannot apply yet, as it already does (causal delivery).

**Snapshots** to and from a mailbox are sealed as a whole by the bridge that made them:
the entries and the roster's ops, padded, in boxes of at most 1 MiB, with the snapshot's
vector (`(origin, seq)` pairs only, no hashes of bodies) and the maker's ID in the clear.
The roster's ops inside stay signed by their origins, so a replica bootstrapping from a
mailbox's snapshot checks the chain back to the genesis as it does today. The maker
signs the whole snapshot, so a mailbox cannot splice two snapshots together.

## Keys

**A content key** is a random 256-bit key. Each one is an **epoch**, named by
`(number, the ID of the bridge that made it)`. Ops are sealed under the newest epoch
their origin holds when it writes them.

**Each replica has a sealing key** besides its signing key: an X25519 key pair whose
public half is in its roster entry, signed with the rest of the entry. It is separate
from the P-256 device key because a key used both to sign and to agree keys is poor
practice. A thin device has none: it never holds content keys.

**An epoch record** is the content key wrapped for each member bridge's sealing key
(HPKE, RFC 9180), signed by the bridge that made it:

```proto
message EpochRecord {
  bytes epoch = 1;                 // (number, maker)
  repeated Wrap wraps = 2;         // one per member bridge, by device ID
  bytes sig = 3;                   // the maker's device key
}
```

Epoch records are not ops: they go through the mailbox in the clear, since a wrap opens
only for its member, and a bridge needs them before it can open any box. Bridges also
exchange them in Hello.

**A new epoch is made**:

- **at genesis**, by the first bridge;
- **when a member bridge is removed**, by the bridge that writes the removal, wrapped for
  every remaining member. A removed bridge can read every op sealed before the new epoch
  (it held those keys), and none after. That is the best a shared key can do, and it
  matches decision 8: a removal stops new reads, and undoes none;
- **when a bridge joins**: the confirming bridge adds a wrap for the newcomer to the
  current epoch's record. The newcomer starts from a snapshot sealed under that epoch, so
  it never needs older keys.

**Two bridges making epoch n + 1 at once** (both heard of a removal) is not a conflict:
both records exist, both open for every member, and each envelope names its own. Writers
move to the newest by `(number, maker)`, so they converge on one.

**Key storage**: content keys and the sealing key's private half live in the mesh store
(`mesh-<instance>.db`), sealed under the cache's key as the store already is. They are
never in a backup that leaves the machine unencrypted.

## Recovery

If every member bridge is lost, the mailbox holds data nobody can open. That is the price
of a mailbox that cannot read. A user may choose to keep a way back:

- **A recovery key**: 32 random bytes shown once as words, to keep on paper or in a
  password manager. Each epoch record gains one more wrap, for a key derived from it.
  With it, a new kithd can open the newest snapshot on the mailbox.
- **Recovering starts a new mesh.** No member is left to sign the new bridge's add, so it
  makes a new genesis, adopts the snapshot's entries as its own ops (as a joining bridge
  adopts them today, design-mesh.md Pairing a bridge, step 4), and starts a new epoch.
  The old roster is kept as history. Devices pair again.
- **A passphrase instead of a key** would let whoever runs the mailbox try passwords
  offline against the wrap. It is not offered unless a question below decides otherwise.

## Admission

The mailbox cannot read the roster, so it keeps its own list of the device keys it
admits, per **mailbox account**:

- **Self-hosted**: the account is the mailbox itself, and its list is a file, like
  `devices-<instance>.toml` today. `kith mailbox add <url>` on a bridge pairs the bridge
  with it the way a device pairs with a bridge: a QR or a pasted URI, then the six digits.
- **Run by someone else**: the account is the service's. A bridge is admitted by
  logging in to it once. How is the service's business; the protocol only needs the
  account's list of admitted device IDs.
- **Removing a member** from the roster also asks every mailbox to drop its key
  (`Revoke{device ID}`, signed by the removing member, sent in the clear). A mailbox that
  never got it still admits the removed device, which can then read only ops sealed
  before the new epoch. It can still write ops, which every replica then refuses by the
  removal (decision 8).
- **Admitting a bridge** works the other way round: the bridge that confirms a pairing
  asks the mailbox to admit the newcomer, from an account that already admits it.

## Sync with a mailbox

The same `MeshService.Sync`, reduced to what a store can do:

1. **Hello** both ways. The mailbox's vector is per origin `(seq, envelope hash)`. It
   sends its wall clock, which the bridge only logs: the mailbox's clock decides nothing.
2. **Catch-up**: the bridge sends the envelopes after the mailbox's vector, per origin, in
   seq order. Those are every origin's, not just its own, as a bridge relays today. The
   mailbox sends the envelopes after the bridge's vector, or its newest snapshot when the
   bridge is behind what it keeps.
3. **Live**: the bridge sends each envelope as it commits it. The mailbox sends what
   other bridges leave, as they leave it.
4. **Ack**: from the mailbox, "stored durably", not "applied". From the bridge, as
   today.

**What the mailbox keeps.** Every envelope until a sealed snapshot covers its dot and it
has been held for the horizon (30 days, measured by the mailbox's own receive time,
since the clock is sealed). It keeps the newest snapshot of each maker whose vector no
other snapshot dominates. Nothing else.

**Who makes snapshots.** A bridge uploads one when the mailbox holds more than 30 days or
more than 64 MiB of envelopes past its newest snapshot, whichever comes first. Two
bridges uploading at once both succeed; the mailbox keeps both until one dominates.

**Changes to a bridge's own log**: none. A bridge keeps an op for the horizon, and longer
until every live replica has acknowledged it, as today. A mailbox's ack does not count
as a replica's. A bridge away for longer than the horizon gets a snapshot from whichever
peer, bridge or mailbox, holds one that covers it. With a mailbox listed, that peer is
always reachable.

## What the mailbox sees

Said plainly in `SECURITY.md` and wherever a mailbox is set up:

- the account, and the device IDs and sealing public keys of its members;
- each device's IP address and when it connects;
- how many ops each device writes, when, and their padded sizes. That shows when someone
  uses kith (a run of read positions is someone reading), but not what was read;
- epochs and revocations, which show that a member was removed, and when;
- snapshot sizes, which roughly show how much state the mesh holds.

It does not see keys, values, room IDs, names, drafts, config, tags, people or the
roster's names.

## What the mailbox can do

- **Forge, alter or replay an op: no.** Boxes are authenticated under keys it does not
  hold, the signature inside is the origin's, and a replayed dot is dropped as a
  duplicate.
- **Withhold ops or serve an old snapshot: yes.** A bridge sees it when the mailbox's
  vector is behind its own for its own origin: "the mailbox at X has lost or withheld
  your ops after N". Withholding another bridge's ops cannot be seen unless the two
  bridges also meet directly or through another mailbox. A blind store cannot promise
  availability, and listing two mailboxes is the answer.
- **Lose everything: yes.** Every member still holds the whole state. The next sync with
  an empty mailbox fills it again from the bridges.

## Equivocation through a mailbox

Today two bridges find a fork when their vectors' head hashes differ for one dot
(design-mesh.md, Equivocation). Through a mailbox, two bridges can each hold a different
op under one dot and never meet. So:

- A mailbox that receives a **second, different envelope** under a dot it holds keeps
  both and sends both to every bridge, flagged as a fork. It cannot tell an equivocation
  from corruption, and does not need to.
- A bridge opens both, checks both signatures, and handles them as a fork found directly:
  `forks`, `kith mesh`, the origin removed.
- A fork whose second half reaches the mailbox after the first half was compacted away
  is not seen. This is the same limit as today's "a fork is seen only at a vector's
  head".

## What steps 5–8 must not assume

Steps 1–4 have landed with unsealed ops. Steps 5 (config), 6 (drafts) and 7 (verdicts)
are the first to carry what a user would not want a third party to read. To keep the
mailbox possible without reworking them:

1. **Nothing may need an op's contents in order to route it.** Sync sends by
   `(origin, seq)` and holds back by context once opened. That is already true, and must
   stay true.
2. **Every op gets its envelope at write time**, from the step that adds envelopes
   (Plan, step 5a), before config and drafts cross. Ops written before then, such as the
   roster, read positions and aliases, reach a mailbox only inside a sealed snapshot.
   That is enough, since the mailbox is bootstrapped from a snapshot.
3. **A snapshot must be something one bridge makes alone and signs**: entries, roster
   ops, vector. That is already true.
4. **Kinds keep their state in the op**, not in a side channel between bridges, so a
   mailbox passing on envelopes carries everything.
5. **Hello's protocol version** goes up with envelopes. A bridge on the old version keeps
   syncing with bridges directly and is refused by mailboxes.

## Backups are not the mesh

Backing up history and media is a different problem: large, append-mostly, and it
belongs to one machine. kithd's scheduled backups (`[backup]`) are already encrypted to
an age public key whose private half only the user holds, so any store can keep them
without reading them. A later design can add:

- **a remote destination** for them, such as a mailbox account's blob store;
- **incremental upload**: content-defined chunks named by a keyed hash, so a store cannot
  confirm it holds a known file, and an unchanged photo is uploaded once;
- **restore to a new machine**, which matters most where a network gives a new linked
  device little or no history (Signal gives none, WhatsApp a limited window).

Nothing in the mesh depends on it.

## Plan

Each PR lands working, with a failure review as design-mesh.md asks.

- **Step 5a, feat: ops are sealed.** Sealing keys in roster entries, epoch records (genesis,
  removal, join), envelopes written with each op and kept beside it, `Op.sealed` on the
  wire, the protocol version raised. Bridges still sync directly. **Failure review**
  (merges writers, enforces scope): step 2's model with envelopes. Every replica must
  hold the same envelope per dot; two concurrent epochs; a removal concurrent with
  writes under the old epoch; a join between a removal and its new epoch; a wrap
  missing for one member, who then says so and asks again.
- **Step 9, feat: the mailbox.** `kith-mailbox` (one binary, SQLite and a blob folder), the
  reduced Sync, sealed snapshots, what it keeps, the `mailbox` role, `kith mailbox add`,
  admission and `Revoke`, `kith mesh` showing each mailbox's lag per origin and its
  store size, and `SECURITY.md`. **Failure review**: the model gains mailboxes that
  store and forward blindly, and may withhold, reorder, duplicate, lose everything, or
  serve an old snapshot. Two replicas that are never online at the same time must
  converge through it. Also: a fork whose halves reach one mailbox from two bridges, a
  removal whose `Revoke` never reaches a mailbox, a mailbox moving a box to another
  dot, a snapshot spliced from two.
- **Step 10, feat: a recovery key.** The extra wrap, `kith mesh recovery-key`, recovery as a
  new genesis from the newest snapshot. **Failure review**: recovery from a mailbox
  alone, from a mailbox holding an old snapshot, and with a wrong key.

## Questions to decide

1. **Seal between bridges too**, so there is one format and every op carries its
   envelope (proposed), or seal only for mailboxes?
2. **The cipher**: XChaCha20-Poly1305, whose random 192-bit nonces are safe without
   counters (proposed), or AES-256-GCM?
3. **Recovery**: a random recovery key only (proposed), also a passphrase (weaker against
   the mailbox), or nothing?
4. **Padding buckets** as above, or none (smaller, and leaks the kind of write)?
5. **Ops already written by steps 1–4** reach a mailbox only through a snapshot
   (proposed), or are re-sealed by a migration? The mesh has no release behind it yet.
6. **Admission to a self-hosted mailbox** by pairing, as above (proposed), or by a token
   in its config?
7. **Where this goes in the order**: step 5a before step 5 (proposed, so the config never
   crosses unsealed), with the mailbox itself after step 8?
