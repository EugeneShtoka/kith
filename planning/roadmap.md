# Roadmap

The single source of truth for what's planned, in order. Items land when ready,
each behind config that defaults to today's behaviour. For what already exists,
see [docs/README.md](../docs/README.md).

**Design docs for larger initiatives** live alongside:
- [design-clients.md](design-clients.md) — bridge/device/GUI split, N:M
  bridge-device topology, mesh CRDT, Electron and Android clients
- [design-adapters.md](design-adapters.md) — adapter capability interfaces,
  WhatsApp-first, foundations for every added network

## Now (in flight)

- **Signal (#101).** Spike complete; 5 build PRs remain per the design plan.
  Toolchain in place: libsignal v0.102.2 at `~/.local/src/libsignal`, cmake +
  clang installed.
- **LinkedIn (#100).** One item left: live-check that a received attachment
  downloads.

## Next

Ordered. Anything that makes later work cheaper or safer goes first; anything
that changes a default goes while changing defaults is still free.

| sprint | items | why these together |
| --- | --- | --- |
| **1 — networks round-out** | **#105** Google Messages | Finishes the sprint-12 cohort (Messenger done, LinkedIn almost, Signal landing). GM is paired as "Messages for web" is: conversations and attachments both ways; older history still to come. |
| **2 — Instagram decision** | Instagram — part of #99 (Messenger/Meta) or its own row | mautrix-meta covers Instagram; decide shape before implementing. |
| **3 — multi-device & client topology** | bridge/device/GUI split · N:M bridge-device topology · mesh CRDT for soft state · Electron client · Android client | **See [design-clients.md](design-clients.md).** Six phases from in-process device-layer refactor to optional on-device mobile bridge; the largest single initiative in this roadmap. Phase 0 (in-process refactor) is the natural prerequisite before any process split; Phases 4–5 (Electron, Android) are the calendar-time investment. |
| **4 — commands anyone can add** | **#109** one name · **#108** a command's places · **#107** templates | Shell-script-free versions of calls (Jitsi, Zoom, Meet). **In that order:** #109 is a rename and goes first so #107/#108 aren't written against the old names; #108 adds a field that templates then set; #107 is the largest row. Slack `/huddle` excluded — Slack's public API has no way to start a huddle. |
| **5 — seeing who is hidden** | **#110** a tag's people · **#111** a person's messages | Finishes sprint 13 (people as spam). #111 goes first or with it: #110's "their hidden messages" key opens #111's view. |
| **6 — what iamb's users trip over** | **#112** gaps filled · **#113** DMs you accept · **#115** missing keys fetched · **#116** photos without location · **#117** can't-write shown · **#118** sends that cannot succeed · **#119** reads that cannot fail forever · **#120** OAuth login · *(#121 ignore list, #122 small Matrix gaps — unordered)* | From the iamb comparison 2026-10-17. **In that order, by harm.** #112 silently loses messages after any kithd restart or outage, so first and alone. #113–#116 are wrong-but-visible. #117–#119 are small hardening. #120 is the one feature with a deadline — homeservers moving to Matrix's OAuth login service may stop taking passwords. *#114 dropped 2026-10-20: Eugene accepts the clock-drift bug.* |
| **7 — scripts** | **#90** scripts get context · **#92** the pager | Carried from sprint 10. |

## Backlog

Not ordered; take when a reason appears.

- **Tier 3 #9** — S4 name pipeline. A4 shipped 2026-09-18 unblocking this;
  changes what `first_name_only` means, so it's a decision rather than a
  refactor.
- **Tier 3 #8** — E3 key backup. Conditional on this account never having had
  key backup.
- **`logout` command** — revokes the session on the homeserver and removes it
  from the keyring.
- **Listing other sessions** — this account's other devices, renaming or
  signing out old ones.
- **In-room SAS verification** of another person. Today only self-verification
  of your own sessions is supported.
- **Pinned messages (reading)** — `m.room.pinned_events` and listing a room's
  pins. See *Deferred* below for the measurement that moved this off the
  critical path.
- **Unread summary** in the terminal window title.
- **Per-message delivery status** from bridges that report one (sent to the
  remote network, failed there).
- **Follow bridge-network links** — `wa.me` and others, the way `matrix:` /
  `matrix.to` and `tg:` / `t.me` already go to the bridged chat.
- **Server-side search** for history that was never cached locally.
- **Older history on Google Messages** (depends on #105 landing first).

## Later — platforms

- **Full Windows support**, released as a zip and through Scoop or winget. It
  builds for Windows today but has never run there. What it takes: Windows in
  the test matrix; toast notifications (the desktop sink is D-Bus); the native
  file dialog (the picker uses the XDG portal); clipboard and link-opening
  defaults; `matrix:` and `tg:` links through the registry and Windows
  Terminal; starting `kithd` at login; signed binaries so SmartScreen does not
  warn. Windows Terminal will be required: the old console cannot draw the
  TUI.
- **Native macOS notifications.** Today the desktop notification sink is
  D-Bus; on macOS the notification command hook is the way to be notified.

## Deferred

- **#76 pinned messages** — deferred on measurement: 0 of 342 rooms on this
  account has ever had a pin. Will come back if an account appears where it
  matters.

## How to read this

- **IDs** (`#101`, `P2-3`, `D2`) are stable identifiers, not order — they are
  referenced from the detail design docs and from earlier sessions, so they
  never get renumbered.
- Every item is independently shippable behind config that defaults to
  today's behaviour.
- Detail specs for in-flight sprint work (per-network design, per-feature
  acceptance criteria) live in `notes/` as local working documents and are
  linked from the design docs in this folder where relevant. The curated
  design docs here capture the architectural shape rather than per-sprint
  detail.
