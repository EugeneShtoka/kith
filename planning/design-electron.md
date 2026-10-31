# Design: the desktop client (Phase 4 of design-clients.md)

Status: planned 2026-10-29, decisions at the end. This is Phase 4
of [design-clients.md](design-clients.md): a GUI client in Electron, TypeScript and React
(decided 2026-10-29). It is a client of the bridge, as the TUI is. It picks a profile, holds
a seat, and reads the message stream that resumes from `(epoch, change)`
([design-replay.md](design-replay.md)). It reaches a remote bridge over pinned mutual TLS
([design-remote.md](design-remote.md)). Notifications are not shown by the client:
`kith-notify` shows them on each device.

## Goal

The goal is a desktop window for people who will not live in a terminal, built on the same
bridge, profiles and rules as the TUI. It also has to work with things a terminal cannot
show: pictures at full size, drag and drop, and a native file picker.

What stands in the way:

- **The TUI is about 38k lines**, and some of its logic lives on the client and not in
  kithd. That logic includes the rail, draft rebase, read policy, merging rows, the emoji
  tables and name labels (see "What runs on the client"). A second client has to either
  port it or get it from kithd.
- **Device plumbing is written in Go only.** That covers finding the local instance's
  socket, `device.toml` and its validation, the device key in the keyring, pinned TLS, and
  pairing.
- **The wire is generated for Go only**, and there is no Node toolchain in the gates
  beyond markdownlint.

### What the first usable release holds

The TUI's features, grouped, with the first release's cut. "Later" means a PR of its own
after the MVP, and until then the TUI covers it.

| area | MVP | later |
| --- | --- | --- |
| **Connection** | Local kithd, and a `[[bridge]]` once Phase 2a step 5 lands. A banner for state and `Status.waiting`. Picking a profile, with a seat per profile ("take over" = force). | Pairing UI: showing a QR to pair a phone (`StartPairing`), pasting a `kith://pair` URI. |
| **Rail and rooms** | The rail (All, DMs, Unread, spaces, tags, conditional rows) and the room list, with unread and mention badges. Sort per `[display.rooms]`. Thread rows under a room. Invites: accept or decline. | Filing into tags or spaces, spam verdicts, the tag editor, Places settings, archive toggles, creating a room or DM, knock, public rooms. |
| **Timeline** | Rich text from spans. Edits and deletions. Replies quoted. Reactions shown and toggled. Inline images at real size, and files downloaded or opened. Unread line, date rules, typing, read receipts per read policy. Scrollback. RTL per message. | Threads pane (read in MVP through the room's thread rows, reply later). Polls (tallies shown in MVP, vote and create later). Starred, message source, edit history, "everything X said", voice notes and video (HTML media), spoilers, OTP highlighting, export. |
| **Composer** | Plain text with markdown on (`[composer] markdown`), reply, edit, delete. Attachments from the dialog, drag and drop, and paste. Mentions. Emoji `:shortcodes:` and a picker with skin tone. Drafts kept and rebased. Typing sent. | Spell marks and the correction walk, word completion and ghost text, scheduled send, slash commands, `[[command]]` scripts, model assist, the SMS confirmation (`confirm_sms` is honoured from the MVP: it stops a send). |
| **Find** | Search pane (`SearchMessages`), jump to result (`MessagesAround`), ctrl+K switcher (rooms, people, spaces), back and forward. | Search filters UI (the query syntax works in the MVP as typed), `:mentions`, `:files`, `:caught`. |
| **Accounts and settings** | None. The TUI and CLI do these. | Network logins including QR steps (`LoginStep.qr` drawn as an image), SAS verification, sign out, the settings editor, phonebook cards, DND. |
| **Desktop glue** | Links from `kith --open` and notification clicks arrive by `FollowStream`. Tray icon with the profile's unread count, single instance, open http(s) links in the browser. | Starting the desktop app from `kith --open` when nothing runs, a global shortcut, macOS and Windows. |

Logins, verification and the settings editor are rare, risky and already covered by the
TUI. Leaving them out keeps the first release to reading and writing messages.

## Architecture

```text
renderer (React, sandboxed, no Node)        ── untrusted content lives here
   │  contextBridge: kith.call / kith.subscribe / kith.files / kith.open (bytes, allowlist)
preload (sandboxed)
   │  ipcRenderer.invoke + MessagePort per stream
main (Node 24 in Electron 45)
   │  @connectrpc/connect-node, HTTP/1.1 over a unix socket
kith-relay (Go, a child of main)            ── device key, pinning, device.toml
   │  socket → local kithd   |   pinned mTLS (HTTP/2) → [[bridge]]
kithd
```

### Renderer security

Message bodies, names, link targets and pictures all come from other people. The renderer
is where they are drawn, so it gets the least:

- `contextIsolation`, `sandbox` and no `nodeIntegration`. These are Electron's defaults
  since 12, 20 and 5 ([security
  checklist](https://www.electronjs.org/docs/latest/tutorial/security)), set explicitly
  anyway.
- The app is served from a custom `app://` protocol, not `file://`. The CSP header is
  `default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob:
  kith-media:; media-src kith-media:; connect-src 'none'`.
- Navigation is refused (`will-navigate`). `setWindowOpenHandler` denies everything. Links
  go through `kith.open`: http, https and mailto go to `shell.openExternal` after parsing
  with `URL`; `matrix:` and `tg:` go to `ResolveLink`.
- **No HTML ever.** Bodies arrive as `Formatted{text, spans}` that kithd has already
  parsed and sanitized (`internal/richtext`). The renderer builds React elements from the
  spans. A lint rule bans `dangerouslySetInnerHTML` and `innerHTML`.
- Pictures are never remote URLs. `kith-media://<room>/<event>` is a `protocol.handle` in
  main that calls `LoadImage` and caches the bytes in the device's media cache
  (`[display.media] cache_dir`, the client's since #391).
- Fuses (`@electron/fuses`): RunAsNode, NodeOptions and NodeCliInspectArguments are off.
  Embedded ASAR integrity is on, and only the ASAR is loaded.

**The preload API is narrow.**

- `kith.call(method, bytes)` takes the RPC name and the request in protobuf binary. Main
  checks the name against an allowlist that grows with features (the MVP's is about 40
  calls). It decodes with `fromBinary` and checks `event.senderFrame` is `app://`.
- Calls that reach beyond reading and writing messages are left off the list until a
  feature needs them and its PR reviews them. Examples are `UpdateConfig`, `SignOut`,
  `DeleteChat`, `ExportRoomKeys`, `ClearCache` and the pairing calls.
- `kith.subscribe(stream)` returns a `MessagePort`.
- `kith.files` covers the open dialog, a dropped file (`webUtils.getPathForFile` in the
  preload), and saving an attachment.
- Binary on IPC, not objects: the request is typed by its schema at the boundary, and
  `int64` stays exact. Generated code maps `int64` to `bigint`, which JSON cannot carry.

Rejected: a generic proxy for every RPC. An XSS in the renderer holds whatever the preload
holds, so the allowlist is the scope.

### Main, and where the Connect client lives

Main holds the Connect client, the stream supervisor and everything that touches the disk.
The renderer holds the state.

**The transport.** `createConnectTransport({httpVersion: "1.1", baseUrl: "http://kithd",
nodeOptions: {socketPath}})`. In connect-node 2.3.0, `nodeOptions` is passed to Node's
`http.request`, which takes `socketPath`. This was checked on 2026-10-29: `Status` and the
`Messages` stream were read over the live kithd socket with code generated by
protoc-gen-es 2.16.0, and `change` arrived as a `bigint`.

**The relay (decision 1).** Main always dials a unix socket. Which one it dials, and what
lies behind it, is `kith-relay`'s job. `kith-relay` is a small Go binary that main starts
as a child, with its own 0600 socket in the runtime dir. It does the following:

- It forwards `BackendService` to the local kithd's socket, or to the device's
  `[[bridge]]` with `daemon.Dial` (Phase 2a step 5). That means pinned mutual TLS with the
  device key from the keyring, HTTP/2 PING, and, in Phase 3, failover and dedup. Streams
  are flushed frame by frame (`httputil.ReverseProxy`, `FlushInterval: -1`).
- It serves a small `DeviceService` (`api/proto/device/v1`): where this device dials, its
  profile, its device settings resolved (`DeviceSettings`), and
  `UpdateDeviceSettings(base_revision, …)` through the same writer and validation as the
  TUI's settings editor.
- It exits when its stdin closes, so it never outlives the window.

Rejected: pinned TLS and the keyring in TypeScript. Node's documented pinning hook,
`checkServerIdentity`, runs only after CA verification succeeds. For a self-signed peer,
the pin check would be hand-written after the handshake, and each new HTTP/2 session would
need a socket verified beforehand (`http2.connect`'s `createConnection` is synchronous).
Reading the device key Go keeps would need a native keyring module. On Windows, go-keyring
and keyring-rs name their entries differently. Each of these is a second implementation
of a scope check (pinning) and a merge of writers (`device.toml`). The relay keeps both in
Go once, along with Phase 3's failover. It costs one more process, but the desktop already
needs the kith package for `kith-notify`.

**The streams.** Main subscribes the window to `Messages`, `UnreadStream`, `Reactions`,
`Invites`, `ActivityStream`, `Changed`, `ConfigChanged`, `FollowStream` and `Seat`. It does
this the way `daemon.Remote.attach` does today:

- one context per attempt, so the first stream to end detaches the rest;
- backoff of 250 ms rising to 5 s;
- `Kith-Profile` set on every request.

Main keeps the last `(epoch, change)` it posted to the renderer, and resumes with it. A
`read_again` becomes a "reload" event. A profile switch takes the new profile's seat first,
then reopens. Frames are batched about every 16 ms into one port message, so a burst after
suspend is one store update and not thousands. A renderer reload starts afresh, with
nothing to resume.

### State, and the timeline

**Zustand** keeps one normalized store: rooms by ID, a timeline per open room as an ordered
array of rows keyed by ID, plus unread, reactions and invites. Stream batches are applied
outside React, and components select narrowly, so a frame for another room re-renders
nothing on screen.

Rejected:

- Redux Toolkit: the same model with more ceremony.
- TanStack Query: a cache of request and response. Rows replaced by ID from a stream fight
  its invalidation model. It may later serve one-off lookups such as search and members.
- MobX and Jotai: no gain here.

**The timeline is `react-virtuoso`** (4.18, MIT):

- `computeItemKey` is the row's ID;
- `firstItemIndex`, started high and decreased by each page prepended, keeps scrollback
  from jumping;
- `initialTopMostItemIndex: LAST`, `followOutput` and `alignToBottom` handle the bottom;
- variable heights are measured on their own, so an edit that grows a row needs nothing
  ([docs](https://github.com/petyosi/react-virtuoso)).

Rejected: `VirtuosoMessageList`, which needs a commercial license (an AGPL app cannot take
it), and `@tanstack/react-virtual`, which is headless, so prepend anchoring and stick-to-
bottom would be ours to write.

**Bidi is the browser's.** Each body is `dir="auto"` with `unicode-bidi: plaintext`.
`text_bidi.go` and `reverseClusters` exist only because a terminal is an LTR grid, and they
are not ported. Room mirroring (`[display.direction]`) comes later.

## Code generation

- **`buf.gen.ts.yaml`**, a second template, runs `protoc-gen-es` (`target=ts`) over
  `api/proto/backend/v1` (and `device/v1`) into `desktop/src/gen/`. It is a second file
  because the Go template also generates `signal/v1`, which a client never sees. Connect v2
  needs no `protoc-gen-connect-es`: the service descriptors come from protoc-gen-es
  ([MIGRATING](https://github.com/connectrpc/connect-es/blob/main/MIGRATING.md)).
- **The plugin is pinned as markdownlint is.** `tools/protoc-gen-es/package.json` plus its
  lock (protoc-gen-es 2.16.0 pulls in protoplugin, TypeScript 5.4.5 and `@typescript/vfs`)
  is installed by `ensure-tools.sh` with `npm ci --ignore-scripts` into `.tools/`. It is not
  `desktop/node_modules`, so `proto-check` never installs Electron's tree. A check fails if
  its version differs from `@bufbuild/protobuf` in `desktop/package.json`: the runtime and
  the generator must agree.
- **The generated code is committed** (CONTRIBUTING: "Commit the generated code with the
  schema change"). `make proto` runs both templates. `proto-check` generates both into a
  scratch dir and compares file by file, as it does now.
- **A proto change breaks the desktop build in the same PR.** That is intended: one
  service means clients and kithd ship together (ARCHITECTURE.md, "The wire").

## What runs on the client today

Here is what the TUI (with `internal/domain` and friends) does that kithd does not, and
where each item goes. The guide for each: if a mistake would make two clients disagree
about shared state, it moves into kithd. If it is presentation, the client owns it. If it
is small, pure and must agree with Go, it is ported with a shared **conformance table**.
That table is a JSON file of cases under `testdata/`, read by both a Go test and a vitest
suite. It is test data, not a production seam.

| logic (Go today) | size | where |
| --- | --- | --- |
| Rail: tags, room facts, homes, room membership per row (`domain/tag.go`, `roomfacts.go`, `homes.go`, `tui/tagged.go`) | ~1.1k | **Moves to kithd**: a `Rail` call per profile, with rows and their room IDs, re-read on `Changed`. Building it on the client needs the whole bridge config (`GetConfig` returns TOML) and the tag rules, three times over once Android exists. The TUI moves to it in the same PR. |
| Draft rebase onto kith-mcp's appends (`tui/draft.go`) | 957 | **Moves to kithd**: `ReplaceDraft` takes the base the client edited from and the text, and kithd rebases. This is a merge of writers, so it gets one implementation, and the mesh CRDT (Phase 2b) inherits it. The TUI moves too. |
| Name labels: aliases, identities, `[[display.name]]`, first-name rules (`domain/people.go`, `tui` roomLabel) | ~600 | **Moves to kithd** with roadmap item S4 (one resolver). Until then the MVP shows the names `Rooms`, `Members` and `Directory` already carry. Width truncation stays in the client. |
| Row replace by ID and timeline order (`domain.CompareMessages`, `MergeMessages`) | ~150 | **Port** plus a conformance table. |
| Reactions aggregate, poll tally, thread grouping (`domain/reaction.go`, `poll.go`, `thread.go`) | ~330 | **Port** plus conformance tables. |
| Read policy: `read_delay`, `[[display.read_rule]]`, `send_receipts` (`domain/read.go`, `tui/readpolicy.go`) | ~180 | **Port** plus a table. These are device settings, so it is the client's choice to make. |
| Rich text | n/a | Already parsed by kithd. The client renders spans. **The offsets are UTF-8 bytes**, so they are converted to UTF-16 indices before slicing a JS string. A conformance case covers emoji, ZWJ and RTL. |
| Emoji tables (`internal/emoji`, ~5.9k generated) and order, tiers, skin tone | ~500 | **Generated** to `desktop/src/gen/emoji.ts` by `scripts/gen-emoji.py`, committed. Ranking stays `EmojiScores`. The palette must be instant, so it is not an RPC. |
| Mention ranking (`domain/mentionrank.go`) | 35 | **Port.** Candidates come from `MentionCandidates`. |
| Unread line, date rules, time formats | ~400 | **GUI's own** (`Intl`). |
| Bidi, key bindings, terminal media | ~2k | **Not ported**: the browser and the GUI do them their own way. |
| Locating the instance, `device.toml`, device key, pinning | n/a | **`kith-relay`** (decision 1). |
| Spell marks, word completion's phrase index, OTP codes, links in plain text, `[[command]]` scripts, phonebook cards | ~1.6k | **Later**, each with its feature. The phonebook needs an RPC first (design-remote.md left it open). |

## Device settings

The rule from design-remote.md holds: if only a client reads a key, it goes in
`device.toml`. The desktop is a device, so it reads and writes that file through
`DeviceService`, which uses the TUI's validation and writer.

- **Shared with the TUI**, because they mean the same thing in both:
  - `[[bridge]]`, the profile;
  - `send_receipts`, `read_delay`, `[[display.read_rule]]`;
  - `typing`, `send_typing`, `unread`, `unread_line`;
  - `skin_tone`, `[display.emoji]`, `[display.reactions]`, `[display.threads]`,
    `[display.rooms]`;
  - `show_archived`, `network_colors`, `color_messages`;
  - `[display.media]` policy keys: `auto`, `cache*`, `anonymize`, `download_*`, rules,
    audio speed;
  - `[composer] markdown` and `confirm_sms`, `[spell]`, `[complete]`, `[assist]`.
- **TUI-shaped, ignored by the GUI:**
  - `terminal`, `[keys]` and its tables, `fps`, `mouse`, `hyperlinks`, `row_numbers`,
    `open_in_insert_mode`;
  - `[display.theme]` (a terminal palette);
  - media `mode`, `detail`, `max_*`, `viewer`, `video_player`, `opener`;
  - `[composer] editor`;
  - `[clipboard]` (OSC 52, focus and raise commands).
- **`[notifications]`** belongs to `kith-notify`. The desktop shows no notifications of its
  own, so a popup never appears twice.
- **GUI-only** (decision 5): theme (system, light or dark, plus accent), zoom and font size,
  window bounds, tray behaviour, shortcut overrides. These go in Electron's own JSON in
  `userData`, written by main, not in `device.toml`. The TUI never reads them, and the
  config refuses unknown keys, so a `[gui]` table would make Go own a schema only
  TypeScript reads.

## Repo layout and toolchain

```text
desktop/
  go.mod                 # a stub: Go tooling never walks node_modules (flatted ships a .go file)
  package.json  package-lock.json  .npmrc    # ignore-scripts=true
  forge.config.ts  vite.*.config.ts  tsconfig*.json  eslint.config.js
  src/main/  src/preload/  src/renderer/  src/shared/  src/gen/ (committed)
  e2e/
cmd/kith-relay/          # Go; a depguard rule like the TUI's: no daemon-side package
api/proto/device/v1/     # DeviceService
tools/protoc-gen-es/     # the pinned generator
```

- **Node 24.** This is CI's version, and Electron 45 bundles Node 24.22
  ([release](https://releases.electronjs.org/release/v45.1.0)). `engines` and `.nvmrc`
  both say 24.
- **npm (decision 3)**, as markdownlint's tree already uses: one package manager in the
  repo. `.npmrc` sets `ignore-scripts=true`, so no dependency's install script runs; the
  one that must (electron's binary download) is run by name in the install step, and a
  new one needs its reason in the PR. `npm ci` against the lock everywhere.
- **Not vendored.** markdownlint's tree is committed (about 1.3k files). Electron's would be
  hundreds of MB with per-platform binaries. Instead:
  - the lock's integrity hashes;
  - `@electron/get` checking Electron's SHASUMS256;
  - a CI cache keyed on the lock;
  - `ELECTRON_SKIP_BINARY_DOWNLOAD=1` for every gate but e2e.
- **Audit.** `scripts/npm-audit.sh` takes a directory and a lock, and the same
  allowlist-with-expiry applies.
  - The **runtime tree** (what goes into the ASAR) blocks in `vuln`. The runtime
    dependencies are kept to react, react-dom, zustand, react-virtuoso,
    `@connectrpc/connect`(`-node`) and `@bufbuild/protobuf`. A new one needs its reason in
    the PR.
  - The dev tree is audited weekly and at release, where it reports without blocking
    merges: its advisories churn in build tools that never ship.
- **Build: Electron Forge (decision 4)**, the Electron project's own toolchain, with its
  Vite plugin for main, preload and renderer. The plugin is marked experimental
  ([docs](https://www.electronforge.io/config/plugins/vite)); its versions are pinned and a
  break shows in `desktop-check`. Makers: deb, rpm and zip first; AppImage only through a
  community maker, so it is not in the first release. Rejected: electron-vite with
  electron-builder (the owner chose Forge).
- **Lint and test.**
  - `tsc --noEmit` (strict), ESLint (typescript-eslint, react-hooks), and
    `no-restricted-imports` per directory as depguard is: the renderer never imports
    connect-node or Node built-ins, and the preload only the IPC types. Prettier `--check`.
  - vitest for logic and conformance tables, with fast-check where a test generates
    interleavings.
  - Playwright's Electron mode (1.64; experimental upstream) for a few e2e flows under
    xvfb, against a fake `BackendService` served by `connectNodeAdapter` on a socket in
    the test.
- **Gates.**
  - `make desktop-check` runs install, typecheck, lint, format, unit, `electron-forge package`
    and the runtime audit. It joins `check`, `quick` (about a minute with a warm store) and
    CI. `ci-parity` holds both lists, and ci-ok depends on the new job.
  - `desktop-e2e` is a CI job inside ci-ok from the timeline PR on.
  - Node becomes a prerequisite of `check`, as it already is of `docs-check`. Nothing is
    skipped for want of a tool.
  - `secrets` excludes `desktop/node_modules`, because packages ship test keys.
- **Release (decision 6).**
  - It runs from the same tag as the Go binaries. A job after goreleaser runs
    `electron-forge make` for linux amd64 and arm64, with that tag's `kith-relay` from
    goreleaser's dist in `extraResource`, so the relay always matches the app. Artifacts
    go on the same GitHub release with a SHA256SUMS.
  - There is no auto-update: it would call home, and the packages update themselves.
  - Later: an AUR `kith-desktop` on the system `electron45`, and a Nix derivation on
    nixpkgs' Electron of the same major.
  - macOS needs a Developer ID and notarization, and Windows Authenticode or Trusted
    Signing. Both are an account and a secret in the release job, so they are not built
    until wanted.

## Plan

Each PR lands working. Go prerequisites land in the PR whose feature first calls them, not
before (no exported call without its caller).

1. **feat: a desktop window reaches kithd through `kith-relay`.**
   - `cmd/kith-relay` (local socket only) and `DeviceService` (where, profile, settings
     read).
   - `desktop/` skeleton with the security baseline (CSP, `app://`, navigation and window
     handlers, fuses, IPC sender checks).
   - Codegen, `tools/protoc-gen-es`, `proto-check` over both templates, `desktop-check` in
     `check`, `quick`, CI and ci-ok, the stub `go.mod`, gitleaks' path, the audit.
   - The window shows connection state and `Status`.
   - **Failure review** (concurrent; enforces scope):
     - the relay outliving a crashed window;
     - kithd restarting mid-stream through the proxy (frames flushed, streams reattach);
     - a stream frame held in a proxy buffer;
     - a renderer calling a method off the allowlist, or from another frame;
     - malformed request bytes;
     - a second app instance.
2. **feat: rail and rooms; kithd serves the rail.**
   - `Rail` per profile in kithd (on the profile table), and the TUI built from it.
   - Desktop: profile picker, seat per profile with take-over, rail, room list,
     `UnreadStream` badges, invites, tray count, reload on `Changed`.
   - **Failure review** (caches a derived result; enforces scope):
     - the rail cache over any sequence of tag, space, config and unread updates (the
       row-cache test's generator);
     - `Rail` added to the profile generator, so no room outside the profile appears;
     - a reload removing the profile;
     - the TUI's memo against the server's.
3. **feat: the timeline, read-only, resuming.**
   - Pages (`CachedTimeline`, `Timeline`, `MessageHistory`) and the `Messages` stream
     resumed by `(epoch, change)` in main, with `read_again` leading to a reload.
   - Replace by ID and order (conformance table), spans to elements (byte offsets), replies,
     edits, deletions, reactions, `kith-media://` pictures and downloads, unread line, read
     policy and `MarkRead`.
   - **Failure review** (merges writers; concurrent; caches media):
     - fast-check interleavings of history pages and stream frames (a page older than a
       frame landing after it, an edit of a row outside the loaded window, a redacted reply
       target, a frame for a room not open), asserting the shown order equals the cache's;
     - `read_again` while scrolled back;
     - kithd restarting mid-burst;
     - a renderer reload;
     - a picture replaced by an edit (the media cache's key holds the content);
     - a read rule that fails to resolve never sending a public receipt.
4. **feat: the composer; kithd rebases drafts.**
   - `ReplaceDraft` with a base, rebased in kithd, and the TUI on it.
   - Desktop: send with markdown, reply, edit, delete, reactions with the quick palette,
     emoji `:shortcodes:` and the picker (generated tables), mentions, attachments in
     chunks from main (dialog, drop, paste), typing, `confirm_sms`.
   - **Failure review** (merges writers; concurrent):
     - kith-mcp appending between the RPCs of one save;
     - a TUI and the desktop in one room on one profile (one seat; the other steps aside);
     - a tie;
     - a send while a save is in flight;
     - a retried send after its acknowledgement was lost;
     - an upload cut midway or abandoned when the window closes.
5. **feat: search, jump, threads, and links.** The search pane and jump, the ctrl+K
   switcher, back and forward, the thread pane (read and reply), `FollowStream` for `kith
   --open` and notification clicks, single instance.
   - **Failure review** (concurrent): a link arriving while a profile switch waits for its
     seat; a link for a room outside the profile.
6. **chore: desktop packages.** Forge's deb, rpm and zip with the relay
   bundled, the release job, checksums, `docs/desktop.md`, SECURITY.md's renderer model.
   **This is the first usable release.**
7. **feat: the relay dials a remote bridge.** This needs Phase 2a step 5's `daemon.Dial`.
   `DeviceService.UpdateDeviceSettings` with a revision, and a desktop settings page for
   the shared keys. (The relay half moved up, 2026-10-30, and landed after PR 4: #410.
   `UpdateDeviceSettings` and the settings page landed 2026-10-31.)
   - **Failure review** (merges writers; enforces scope):
     - the TUI's editor and the desktop writing at once;
     - a hand edit between read and write;
     - an address answering with another key;
     - a key unpaired mid-stream.
8. **feat: the desktop's look: themes, colors, and a layout that scales.** (Asked
   2026-10-30, not urgent.) The MVP's screens are functional, not designed; this PR
   gives the window a design taking after Slack and Element: a dense left column
   (profile, rail, rooms) with clear selection and unread weight, a message list with
   avatars or initials, sender names in their colors, grouped consecutive messages,
   hover actions, and a composer docked at the bottom.
   - **What the MVP gets wrong** (a screenshot beside Slack's, 2026-10-30): content sits
     in a fixed narrow column with the window's sides empty; the page scrolls as one, so
     the room list runs past the window and the composer floats mid-page instead of
     docked at the bottom; no header names the open room; every message repeats its
     sender and full date (no grouping, avatars or day dividers); rooms show no unread
     weight, network or avatar, and phone numbers raw; the rail is plain text with an
     outline for selection; the composer is a bare textarea with browser buttons.
   - **What to take from Slack and Element:** full-height panes, each scrolling on its
     own; a header for the open room; day dividers, grouped messages with the time on
     hover, avatars, reaction chips and a hover toolbar per message; a composer docked
     at the bottom with a formatting bar; a thread pane at the side (with PR 5); a
     search bar across the top (PR 5).
   - **Themes:** light, dark, and following the system, plus free color choice for the
     accent and every role a theme names (background, surface, text, muted, link,
     selection, unread, mention), on a settings page.
   - **Colors per account and per person**, each picked freely, with the TUI's
     per-network colors (`[display.theme.networks]`) and its sender colors as the
     defaults until picked. They live in the mesh, a device's own overriding it (below).
   - **Scaling:** the layout follows the window's size (columns narrow or collapse below
     set widths, the message list reflows, nothing clips or scrolls sideways), with zoom
     and font size as knobs.
   - Everything above under settings knobs: no value hard-coded that a person might
     want to change.
   - **Split in two (2026-10-31).** **8a** is the layout and design: full-height panes,
     the room header, day dividers, grouped messages, hover actions, reaction chips,
     the docked composer with its formatting bar, unread weight and network marks,
     columns that narrow and fold, zoom and text size (Electron's store, `scale.json`),
     and every color, size and space as a CSS custom property in one place, with one
     built-in palette following the system's light or dark. **8b** feeds those
     properties: themes (light, dark, system), free role colors, colors per account
     and per person, and their settings page. 8b's storage: the shared look in
     `config.toml`, and a `device.toml` table of the same keys overriding it key by
     key; the TUI keeps its own `[display.theme]`. Avatars are colored initials for
     now; pictures are a later PR of their own. (8b landed 2026-10-31: `[gui.theme]`, for
     graphical clients, holds `mode`, `[gui.theme.light]` and `[gui.theme.dark]` (accent,
     background, surface, text, muted, link, selection, unread, mention),
     `[gui.theme.accounts]` by `domain.AccountRooms` and `[gui.theme.people]` by ID, in
     both files; `Theme`/`UpdateTheme` on `DeviceService`.)

After the MVP, each with its own PR and review:

- polls (landed 2026-10-31: drawn with their tallies, voted in, who voted, asked from
  the composer);
- spell and completion;
- scheduled send and slash commands;
- network logins (QR);
- SAS verification;
- the settings editor (it needs a structured config call; `GetConfig` is TOML text);
- people and phonebook (needs an RPC);
- tags and filing;
- pairing UI;
- a local partial cache for offline reading. That makes the desktop a replica in the mesh
  CRDT (design-clients.md decision 12), so it waits for Phase 2b;
- macOS and Windows.

## Decisions (2026-10-29)

1. **The bridge link is `kith-relay`**, a small Go child process on the client machine
   holding the device key, pinning, `device.toml`, pairing and later failover, and
   serving `BackendService` to Electron on a local socket. One implementation of each
   security and merge path covers every client; Electron carries no native module.
   Rejected: TLS, keyring and TOML written again in TypeScript. Its cost, one more
   process, is accepted: whoever wants the fewest resources runs the TUI, which needs no
   relay.
2. **The rail and draft rebase move into kithd**, and the TUI moves with them, rather
   than about 2k lines of Go ported to TypeScript and again to Kotlin. Name labels follow
   with S4.
3. **npm**, with install scripts off, rather than pnpm: one package manager in the repo.
4. **Electron Forge**, rather than electron-vite with electron-builder.
5. **GUI-only settings in Electron's own store**, shared keys in `device.toml` through the
   relay.
6. **Same tag, same release**: the desktop ships with kithd and bundles its own relay.
7. **The MVP is chat first**: no logins, verification or settings editor in the first
   release; the TUI keeps them.

## Left open

- **Where the theme and the per-account and per-person colors live** (decided
  2026-10-30): in both. The mesh holds the shared values, so every device agrees by
  default (Phase 2b, a config-like register); a device's own setting overrides the
  mesh's, key by key. This amends decision 5 for theme and colors: they leave
  Electron's store for the device's settings beside the mesh's. Which device file holds
  a GUI theme key the TUI does not read is PR 8's to settle (settled for 8b: a
  `device.toml` table, under plan item 8).

- **`kith --open` with no window running** starts a terminal TUI today. Whether it starts
  the desktop instead is a device setting, decided when the desktop ships.
- **Several bridges in one window** (design-clients.md open question 7). The relay is
  where merging would happen, so nothing here blocks it.
- **Keyboard-first use**: whether a vim-style navigation layer like the TUI's is wanted,
  or conventional shortcuts are enough.
- **Version skew with a remote bridge**: the relay matches the app, but a kithd on a
  server may be older. A version in `Status`, refused on mismatch, is shared with the
  remote TUI (Phase 2a).
