# Design: the Android client, phase one (step 1d)

Status: drafted 2026-11-25, nothing built; the decisions at the end wait on Eugene. It
narrows design-clients.md's Phase 5 to a first phase and leaves its Phase 6 (a bridge on
the phone) for later. It changes nothing in [design-clients.md](design-clients.md),
[design-desktop-cache.md](design-desktop-cache.md) or
[design-failover.md](design-failover.md); the steps below follow them.

## Goal

An Android app that reads and writes through a kithd running somewhere else (the
elitedesk, a VPS, a laptop in the mesh), paired with it the way `kith join` pairs a
laptop ([docs/remote.md](../docs/remote.md)). Every network stays logged in on that
kithd; the phone holds no network session, only its own device key and a copy of what
the bridge answered.

Not goals in phase one:

- **A bridge on the phone** (design-clients.md Phase 6): no kithd, no gomobile build, no
  linked-device slot spent on the phone. The spikes in the roadmap row (WhatsApp, Signal
  and Google Messages linked beside their official apps on one phone) answer only
  whether that bridge can work, so they move to phase two.
- **Logging in to a network from the phone.** Accounts are added on the bridge, from the
  terminal or the desktop window. Matrix single sign-on from a remote client is a
  phase-two item: its callback lands on the bridge's machine, not on the phone.
- **Writing offline** (design-clients.md, Mobile: no offline writes). A send needs a live
  connection; without one it fails where the user sees it.
- **Settings, pairing other devices, the mesh, verification, filing.** The phone reads,
  sends and is notified; everything else stays on the desktop and the terminal until
  someone asks for it on the phone.

## What the wire already gives

Nothing on kithd's side has to change for the phone to connect. It is one more device:

- **The device key is ECDSA P-256** (`internal/devicekey`), its ID the SHA-256 of the
  public key's SubjectPublicKeyInfo. Android Keystore makes and holds P-256 keys that
  never leave it, and signs a TLS handshake with them.
- **TLS 1.3 with the key pinned, not a certificate.** The app sends its key in a
  self-signed certificate made at start, sends the bridge's ID as the server name
  (`ID.ServerName()`: the ID in lower case under `.kith`), and checks that the key the
  bridge presents has the ID it paired with. OkHttp takes a custom `X509TrustManager`
  and `X509KeyManager` for that. Android enables TLS 1.3 from API 29.
- **Connect over HTTP/2 or HTTP/1.1** (`NextProtos` `h2`, `http/1.1`): connect-kotlin on
  OkHttp speaks it, server streams included (`Messages`, `NotificationStream`,
  `UnreadStream`, `Pair`).
- **Pairing by QR**: `kith pair` (or Settings → Devices → Pair a device) shows a
  `kith://pair?…` QR; the app scans it, dials the addresses in order, calls `Pair` with
  the token, and shows the six digits `pairing.Digits` derives from both IDs and the
  token. The user confirms on the trusted machine.
- **Profiles**: the app names one with the `Kith-Profile` header, and kithd refuses
  anything outside it (`internal/daemon/named.go`). A phone is a profile like a laptop.

So phase one is the app, plus three things on kithd's side: the `buf breaking` check
(the wire now has a client that is not built from the same commit), the push sink, and a
backlog for `NotificationStream` ([Notifications](#notifications)).

## The app

- **Kotlin, Jetpack Compose**, one activity, `minSdk` 29 (TLS 1.3 without a bundled
  provider). In this repo under `android/`, as the desktop window is under `desktop/`:
  its wire types are generated from `api/proto` by buf, as `desktop/src/gen` is, and
  `make proto-check` holds them in sync.
- **The device key in Android Keystore**: P-256, non-exportable, in StrongBox when the
  phone has one, made at first start. No recovery key: a lost phone is removed on the
  bridge (`kith devices remove`) and the next one paired, as remote.md says for any
  device. `kith devices rotate` works from the phone later, not in phase one.
- **The bridges it paired with**, each an address list and an ID, as `[[bridge]]` in
  `device.toml`, kept in the app's private storage.
- **Its own SQLite cache** of the bridge's answers (rail, rooms, unread, each room's
  recent timeline), so the app starts on the last screen it showed and reads offline.
  It mirrors kith-proxy's store (#632: a cache of answers, rooms by integer key, deleted
  when the bridge refuses this device) without sharing its code, which is Go. Offline
  search and image previews follow 1c's steps 3 and 4, not phase one.
- **Sending only when connected**: the composer's send is off while no bridge answers.
- **Failing over** across its bridges per 1b once 1b's heartbeat exists; until then it
  dials the first bridge that answers, as `kith join` does.

## Notifications

- **While the app is open**, it holds `NotificationStream` on its connection and posts
  what arrives, as kith-notify does on a desktop.
- **While it is closed**, a UnifiedPush wake (design-clients.md, Push notifications,
  decided 2026-10-28 and 2026-11-18): the app registers with the phone's distributor,
  hands the bridge the endpoint URL and its RFC 8291 keys for its profile, and kithd's
  push sink in `internal/notify` POSTs an encrypted, empty wake to it for every
  notification that profile would show. The app wakes, connects for a moment as a
  foreground service, pulls the notifications it missed and posts them.
- **No distributor on the phone**: the app offers to hold the connection open as a
  foreground service instead, with its persistent notification.

Two changes on kithd's side:

- **A call registering (and dropping) a device's endpoint for a profile**, kept on the
  bridge beside the device's pairing. The endpoint is a secret and never logged.
- **A backlog.** `NotificationStream` sends only what is decided while it is open
  (`NotificationStreamRequest` is empty), so a woken app would find nothing. It gains a
  `since`: kithd keeps each profile's notifications of the last day and replays those
  after it, then streams on. Working them out on the phone from unread counts instead
  would skip kithd's notification rules and quiet hours.

## Steps

Each its own PR, with a failure review where CONTRIBUTING.md asks for one (the push sink
and the cache do).

1. **`buf breaking`** against `origin/main` in CI's proto job, so a change that breaks a
   released app's wire fails before merge.
2. **The app's frame**: `android/` with Gradle, generated wire types, the device key in
   Keystore and its ID, checked against IDs `internal/devicekey` computes; a CI job that
   builds the APK and runs its unit tests.
3. **Join**: scan or paste a `kith://pair` URI, `Pair`, the six digits, the bridge kept;
   the pinned TLS dial; choosing a profile.
4. **Read**: the rail, rooms with unread counts, a room's timeline and new messages as
   they arrive; marking read.
5. **Send**: the composer, text and replies, only when connected.
6. **The cache**: start from it, read offline, delete it when the bridge refuses the
   device.
7. **Notifications while open**, then **the backlog** (`since`), then **the push sink
   and UnifiedPush** (the new call, `internal/notify`'s sink, the app's receiver), then
   the foreground-service fallback.
8. **Failover** across bridges, after 1b's heartbeat and the proxy's failover.

Phase two, not planned yet: the spikes, then a bridge on the phone (Phase 6), Matrix
single sign-on from a remote client, settings on the phone.

## Decisions for Eugene

Each has a recommendation; the steps above assume it.

1. **Where the app lives.** (a) `android/` in this repo, generated from `api/proto` with
   the rest *(recommended: one schema, one proto check, as the desktop)*; (b) a repo of
   its own, pinning kith's protos by commit.
2. **The cache's sealing.** kithd's cache and kith-proxy's store are sealed
   (`internal/sealeddb`) because a Linux home directory is readable by every process of
   the user. (a) Android's app sandbox and file-based encryption only, with backups off
   (`allowBackup="false"`) *(recommended: no other app can read it, and it adds no
   dependency)*; (b) SQLCipher with a key wrapped by Keystore.
3. **Push in phase one.** (a) Step 7 as written: the sink and UnifiedPush are part of
   phase one *(recommended: a phone that notifies only while open is not one to rely
   on)*; (b) phase one ends at the cache, push in phase two.
4. **Where it builds.** No JDK or Android SDK is installed here. (a) CI builds and tests
   it, and a JDK 17 and the SDK's command-line tools go in `~/.local` here for running
   it on a phone over adb *(recommended)*; (b) CI only, APKs from CI's artifacts.
