# Design: windows, not seats

Status: planned 2026-11-12, decisions taken the same day (below); built the same day.

## Goal

Any number of windows run in one profile at once, on any devices: the TUI in two
terminals, the desktop on the laptop, a client on the phone through its bridge. Today
kithd serves **one window per profile** (the seat, `internal/daemon/seat.go`): a second
window is refused, or with `kith --force` takes over and the first saves and closes. The
seat dates from kithd and its one client sharing a machine. The 2026-10-25 decision was
one window per device per profile, but the seat is keyed by profile alone, so a window
on another device takes it too. Now that devices reach a bridge remotely (Phase 2a) and
several clients exist, the seat is in the way.

## What the seat does, and where each job goes

| job | today | after |
| --- | --- | --- |
| one window per profile | `Seat` RPC, `ErrSeatTaken`, `kith --force`, step aside | **gone** |
| draft writes | refused from a window without the seat (`seats.write`) | **every write is made over the version it read and rebased** (already how both clients write) |
| drafts saved before leaving | the step-aside asks the holder to save and close | gone with the takeover; a window saves as it types |
| a clicked link | to the window on the device with the room open, else the last used | unchanged: that is the window registry, not the seat |
| notification topics | named by the seat's key of a profile (`seatKey`) | unchanged, named `profileKey` |

So the seat's only real work is protecting drafts from a second writer, and
`RebaseDraft` already does that per write: it was built for kith-mcp appending while
the person types, and the TUI (`internal/tui/draft.go`) and the desktop
(`desktop/src/renderer/drafts.ts`) both write through it.

## Design

**Windows.** kithd keeps the window registry that link routing uses (`viewer`: device,
profile, open room, last used, listening) and drops the sitting (`sitter`, `held`,
`take`, `leave`, `write`). `seats` becomes `windows`. A window registers by listening
for links and saying what it has open (`Present`), as today.

**Drafts.** A room's draft has as many writers as there are windows on it, plus
kith-mcp. Each writes over the version it last read, and kithd rebases in one step
(`domain.RebaseDraft`): words another writer added since go after the writer's; a stored
draft that no longer holds the writer's base is kept whole beside its words. So when
two windows changed one draft at once **both are kept**: the composer holds both, the
other's after its own, and the window says that words from another window were added,
so nothing appears silently. This is the rule the mesh already plans for two bridges
(design-mesh.md, Drafts), so one rule covers windows, devices and bridges.

**Every window sees a draft change.** `Changed` gains `CHANGE_DRAFTS`: kithd sends it
when a stored draft changes; each window reads the drafts again and takes a changed one
where it has no unsaved typing, as it takes kith-mcp's today (the TUI's poll,
`handleDraftsLoaded`, becomes this). A message begun on the laptop is there to finish on
the phone, and two windows rarely type over each other unseen.

**Profiles.** Switching profile no longer takes the new profile's seat first
(`seatProfileKey`, `takeSeatIn`); a window names its profile on each call, as it does.

## Decided 2026-11-12 (Eugene)

1. Any number of windows per profile, on any devices, and on one device (two terminals).
2. Drafts written at once in two windows: both are kept.
3. The seat goes; no compatibility for it (removed flags and calls are just unknown).

## Plan

One PR, since kithd and its clients ship together (CONTRIBUTING.md, Protocol changes):

1. **kithd**: drop the `Seat` RPC and `ErrSeatTaken` (`services.proto`, sentinels,
   `remote_seat.go`), the sitting in `seat.go` (renamed `windows.go`), the guard in
   `ReplaceDraft`'s handler and the `client` field it reads; add `CHANGE_DRAFTS`, sent on
   every stored draft change.
2. **TUI**: drop `TakeSeat`, `--force`, the seat-lost path (`seat.go`, `attached.go`,
   `program.go`, `unsaved.go`'s save-before-quit stays as the normal quit); read drafts
   on `CHANGE_DRAFTS` instead of polling; say when another window's words were added.
3. **Desktop**: drop the seat states (`session.ts`'s `Seat`, `gate.ts`, the "taken" and
   "stepped aside" screens, dialogs gated on `seated`); read drafts on `CHANGE_DRAFTS`.
4. **Docs**: `docs/cli.md` (`--force`), `docs/troubleshooting.md` (one window per
   profile), ARCHITECTURE.md (what kithd knows of a window), design-mesh.md (Drafts: two
   writers on one bridge becomes many).

## Failure review (merges more than one writer's data)

`RebaseDraft`'s tests take two writers: a window and kith-mcp. The generator gains a
second window, so it also makes: two windows typing over the same base; a window writing
over a version the other replaced; one window sending (deleting over its version) while
the other types; a write whose answer was lost, retried while the other window wrote;
and kith-mcp appending in between each. The property: no typed or appended word is lost,
and every window reads the same draft after the last write. Each new case is seen to fail
on the old code where it can (the guard refuses the second window: there the loss is the
refused typing), the fix mutation-checked, and what the generator still never makes is
reported.

## Open

- **Repeated text.** Keeping a draft that no longer holds the writer's base whole beside
  its words can repeat text when both windows went on typing from different versions.
  Live `CHANGE_DRAFTS` makes that rare; if it is common in practice, a text CRDT for
  drafts (design-mesh.md, Open) answers it.
- **Which window a link goes to** when two on one device have the room open: the last
  used, as today.
