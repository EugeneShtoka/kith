# Design: the mailbox, a mesh peer that cannot read (an addition to Phase 2b)

Status: proposed 2026-11-01; its questions (at the end) all decided by 2026-11-18. Built:
step 5a (sealed ops, #609), step 9 (the mailbox, #624) and step 10 (a recovery key, #625). It
adds to [design-mesh.md](design-mesh.md), which it does not change except where
[What steps 5–8 must not assume](#what-steps-58-must-not-assume) says so. The sections
below stay as the design; [As built (step 9)](#as-built-step-9) and
[As built (step 10)](#as-built-step-10) record where they differ or add to it.

## Goal

The mesh syncs two bridges only while they can reach each other. Two machines that are
never on at the same time, such as a desktop used in the day and a laptop used at night,
never converge: each one's read positions, drafts and config edits wait for a meeting
that never happens. Past the mesh's 30-day horizon the one that was away comes back
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

Not a goal: messages and media. The mesh never carries them: each bridge
syncs them from the networks. Backing them up is a separate design ([Backups are not the mesh](#backups-are-not-the-mesh)).

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
  Ops are exactly-once by dot (`internal/mesh/crdt`), so the same op arriving from two
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
- **Padding**: `body ‖ sig` is padded to the next multiple of 4 KiB, so a read
  position, a draft and a config write do not tell themselves apart by size. The 1 MiB
  bound on an op stays.
- **The cipher** is AES-256-GCM from the standard library, the media archive's cipher.
  Its 96-bit nonces are random, which stays safe only well below 2³² boxes under one
  key, so a bridge makes a new epoch once it has sealed 2²⁸ under the current one (Keys).
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
  matches the mesh's rule for a removal (it stops new writes, and undoes none): here it
  stops new reads, and undoes none;
- **when a bridge has sealed 2²⁸ ops** under the current epoch, by that bridge, so that
  random AES-GCM nonces never come near a collision;
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
  adopts them today, `adopt` in `internal/mesh`), and starts a new epoch.
  The old roster is kept as history. Devices pair again.
- **A passphrase instead of a key** would let whoever runs the mailbox try passwords
  offline against the wrap. It is not offered (question 3).

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
  removal, as the mesh judges a removed replica's ops by their context.
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
(`forks` in the mesh store). Through a mailbox, two bridges can each hold a different
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

## As built (step 9)

- **One binary, one file.** `kith-mailbox serve` keeps envelopes, sealed snapshots (each
  whole, as the frame that carries it), epoch records and admission in one SQLite store
  (`mailbox.db`, encrypted under a key derived from its device key `key.pem`); no blob
  folder. `internal/mailbox` is its store and its half of a session; it reads only an
  envelope's clear origin and seq, a snapshot's maker and vector, and a record's wraps.
- **The protocol** is the mesh's, raised to 3: a bridge of protocol 2 would refuse a
  roster naming a mailbox. A mailbox's Hello carries `MailboxState` (snapshot due, store
  size); envelopes cross alone (`Envelopes`, forks flagged), snapshots as
  `SealedSnapshot` a box per frame, who is admitted as `Admission`.
- **Blind vectors.** A bridge tells a mailbox seqs only, in its Hello and its Acks: the
  hash of an op's body would let whoever runs it confirm a guess at what the op says.
  The mailbox's own vector carries envelope hashes, which a bridge checks at the
  mailbox's head and answers with its own envelope where they differ: that is how both
  halves of a fork reach one mailbox, which then tells every bridge of both.
- **Sealed snapshots** are the snapshot's own frame, length-prefixed, padded and cut
  into boxes of at most 1 MiB, each head (the additional data) naming the maker, a
  random snapshot ID, the box's place and the epoch; the maker signs the maker, the
  clear vector and every box. A bridge refuses one whose signature is not its maker's,
  a bridge of its roster (or of the snapshot's, when it does not know the maker yet): a
  removed bridge still holds old content keys, so the boxes alone prove nothing. Each box
  counts as a seal under the epoch, and sealing one makes an epoch when none serves.
- **What the mailbox sends a bridge behind it** is, for each origin it cannot fill with
  envelopes, the kept snapshot that brings it furthest there, then the next, each once a
  session: snapshots of two makers may each cover what the other does not.
- **Admission is state, not requests.** Pairing by hand (`kith-mailbox pair`, then `kith
  mailbox add` on the bridge, six digits) admits a key; after that, each session a bridge
  says which keys are bridges of its roster and which were removed, after what it
  leaves, and again when that changes. A key never revoked is admitted on an admitted
  bridge's word; a revoked one only by pairing it again, so a bridge that has not heard
  of a removal cannot let the key back in. The word is the TLS-authenticated session's,
  not a signed `Revoke`. Revoking a key cuts its sessions and drops the snapshots it made,
  which the bridges would refuse while the mailbox went on counting what they cover.
  The admission list is in the store, not a file.
- **Wants cross a mailbox.** A bridge's epochs whose key it lacks are kept per bridge
  and passed to the others (`Epochs.for`), and the wrap one makes goes back through it.
- **A rotated bridge** the mailbox does not know yet syncs once as the key it rotated
  from (kithd keeps it 30 days), which tells the mailbox of the new key.
- **The mailbox's clock** is logged when it is off, and decides only what it keeps.
- **Each bridge lists the mailbox itself** (`kith mailbox add` there) unless it meets one
  that did: the entry is a roster write like any other.
- **Left:** envelopes or a snapshot waiting for a key are tried again within a session
  (the model's sessions never overlap, so a key arriving mid-session is not generated;
  the next session sends them again regardless); a mailbox that loses everything while
  the last copy of an op is on a revoked bridge loses it for the mesh (a revoked key may
  not even leave; the review generated this only before leaving came before admitting).

## As built (step 10)

- **The key in the mesh** is `recovery`, a register any bridge writes, holding the
  public half of an X25519 key derived from the 32 bytes (SHA-256 over a label and the
  bytes). Concurrent writes all stand and are all wrapped for; a write over them
  replaces them, and the old keys still open the epochs wrapped for them.
- **Wraps.** Whenever a bridge seals (an op, a snapshot), it first wraps every epoch it
  holds the key of, the one it has just made too, for each recovery key that no record
  of the epoch wraps it for yet: one record per epoch, signed by that bridge, its wrap
  named by `crdt.RecoveryID(public half)`. So what the mailbox held before the key was
  made opens too, once any bridge holding those epochs writes once.
- **The written form** is kithd's recovery key's (`devicekey.Secret`), its check taken
  over the kind (`devicekey.MeshRecovery`) and the bytes, so either key typed for the
  other does not check out.
- **Recovering** (`kith mesh recover`, `Store.Recover`) runs on a kithd whose roster
  lists no other bridge, paired with the mailbox (`kith mailbox add`). It asks the
  mailbox for everything (a Hello holding nothing) and reads to the mailbox's first Ack,
  which a mailbox now sends once it has passed on all it held when a session began. It
  opens each epoch wrapped for the key, merges each snapshot that opens and is signed by
  its maker, a bridge of its roster, then applies each envelope that opens, every op
  judged as a bridge judges a peer's, in a mesh of its own. It writes that mesh's
  entries as its own ops (each key's latest value; the accounts, each old bridge's own,
  stay behind), the old roster's members as removed (but itself, so a kithd restored
  from the old one's key keeps its ID, and the mailboxes), and so the recovery key
  itself, which goes on opening the new mesh. Ops after the newest snapshot come back
  too, not only the snapshot.
- **Left:** a fresh epoch made for a snapshot, with no op sealed under it after, is
  wrapped in the same seal; the review never generates that case, so its mutant
  survives. Recovery takes the latest of a key's concurrent values, so a draft written
  on two bridges at once comes back as the later one.

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

## A relay to a bridge behind NAT (an idea, undecided)

A bridge at home behind NAT cannot be dialled from a phone on mobile data. A relay that
both dial out to, and that passes the TLS stream between them live, would let them reach
each other without port forwarding or Tailscale. It stores nothing, so unlike the
mailbox it holds no ops, sealed or not, and it never sees inside the pinned mutual TLS.
It came up as part of a possible paid tier (one subscription tiered by backup size, with
the mailbox and the relay included); that is not decided. It answers the same need as
design-clients.md's open question 2 (WAN discovery), which is decided for now as
Tailscale, documented and not built (2026-11-18); the relay stays an idea for the paid
tier.

## Questions to decide

Decided 2026-11-10 (Eugene): 1, seal between bridges too; 4, pad every op to a multiple
of 4 KiB; 7, step 5a before step 5. Decided 2026-11-17: 2, AES-256-GCM. Decided
2026-11-18: 3, a random recovery key only; 5, old ops only through a snapshot; 6,
admission by pairing; 7, the mailbox itself (step 9) after step 8. Every question here
is decided.

1. ~~**Seal between bridges too**~~, so there is one format and every op carries its
   envelope. *Decided: yes.*
2. ~~**The cipher**~~: AES-256-GCM, standard library only, the media archive's cipher.
   *Decided.* (XChaCha20-Poly1305 was proposed.)
3. ~~**Recovery**~~: a random recovery key only, as kithd's own recovery key is.
   *Decided.* (A passphrase as well was weaker against whoever holds the mailbox's data;
   no recovery at all was the other option.)
4. ~~**Padding**~~: every op padded to a multiple of 4 KiB. *Decided.*
5. ~~**Ops already written by steps 1–4**~~ reach a mailbox only through a snapshot.
   *Decided.* No migration re-seals them; the mesh has no release behind it.
6. ~~**Admission to a self-hosted mailbox**~~ by pairing, as a device pairs with a bridge.
   *Decided.* (A token in its config was the other option.)
7. ~~**Where this goes in the order**~~: step 5a before step 5, so the config never
   crosses unsealed. *Decided.* The mailbox itself (step 9) follows step 8. *Decided
   2026-11-18.*
