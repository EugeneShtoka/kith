# Design: remote clients (Phase 2a of design-clients.md)

Status: planned 2026-10-28. Phase 2a of [design-clients.md](design-clients.md): kithd on
one machine, its clients on others. Phase 2b (bridges pairing with each other, and the
mesh CRDT) follows when a second bridge exists; the push sink moved to Phase 5, the
first client that needs a wake.

## Goal

The TUI, `kith-notify` and `kith-mcp` run on a laptop against a kithd on a home server
or a VPS: the "central" shape. Today every client reaches kithd only through a unix
socket on the same machine, whose `0600` permissions are the only authorization
(SECURITY.md).

What stands in the way, besides a listener:

- **No device has an identity.** Nothing generates a keypair; no TLS anywhere.
- **The TUI reads kithd's config file from disk**, and that file holds the device's
  own settings (theme, keys, media display) beside the bridge's (rooms, rules,
  profiles). A TUI on another machine has no such file.
- **Files cross as paths.** `SendFile` sends a local path for kithd to open; the TUI
  opens attachments from kithd's media cache directory (`Display.Media.CacheDir`).
- **The socket speaks HTTP/1.1** (no `Protocols` set on either side). Every RPC today
  is unary or server-streaming, which HTTP/1.1 carries; TLS brings HTTP/2 by ALPN.

## A device

Every client machine and every kithd has a **device key**: ECDSA P-256 (Electron and
Android do not reliably take ed25519 certificates), generated on first use, the
private half in the OS keyring like the cache's key (`internal/session`), a file only
under `allow_token_file`. A **device ID** is the SHA-256 of the public key's
SubjectPublicKeyInfo, shown as groups of base32. The TLS certificate is self-signed and
long-lived; the key, not the certificate, is what is pinned, so a certificate may be
reissued freely.

**Device settings** live in `device.toml` beside each client, never on the bridge:

- which bridges this device dials (`[[bridge]]`: name, addresses, pinned ID) and which
  profile it runs;
- what only a client reads: theme, keys, media display, time formats, mouse, FPS.

The rule that sorts every key: **kithd reads it → the bridge's `config.toml`; only a
client reads it → `device.toml`.** A key both read stays on the bridge. (Decided
2026-10-28: a device file on each device, with every key only a client reads, so a
phone and a laptop may behave differently too, not only look so.)

Applied 2026-10-28 by following every read of each config field through the config
accessors and setup helpers to the package that reads it (validation and encoding
left out), against kithd's dependency graph. A table keeps its name in both files and
each key sits in the file the rule puts it in; three judgment calls:

- **A list of tables whose element is mixed stays on the bridge whole**
  (`[[display.space_rule]]`, `[[display.identity]]`): one rule cannot be split across
  files.
- **`[agent.*]` stays on the bridge whole**: what an assistant may read and write is
  policy, not a device's taste.
- **`[display.media]` moves in step 2**, with the media cache it names.

Device keys:

| table | keys |
| --- | --- |
| top level | `terminal`, `[keys]`, `[composer]`, `[[command]]` |
| `[display]` | `color_messages`, `direction`, `emoji`, `fps`, `filing_spaces`, `hyperlinks`, `max_name_length`, `mouse`, `network_colors`, `open_in_insert_mode`, `reactions`, `read_delay`, `read_rule`, `rooms`, `row_numbers`, `send_receipts`, `send_typing`, `show_archived`, `skin_tone`, `theme`, `threads`, `typing`, `unread`, `unread_line` |
| `[notifications]` | how this device shows one: `enabled`, `desktop`, `command`, `sound_command`, `timeout`, `title`, `body` |
| `[clipboard]` | all but `auto_copy` |
| `[commands]` | `timeout` |
| `[complete]` | `enabled`, `ghost`, `ratio`, `scope`, `sources`; `[complete.model]` `declined` |
| `[spell]` | `autocorrect`, `check_before_send`, `declined`, `frequencies_declined`, `rare_before_send`, `rare_underline`, `underline` |
| `[assist]` | `name_threads`, `name_threads_after` |

Today's file is split once, by whichever of kithd and the client opens it first: the
device keys move into `device.toml` beside it (a copy of the old file kept, as every
write keeps one). They cannot stay behind: the config refuses unknown keys, so kithd
would refuse its own file. The TUI's settings editor writes each key to its own file
from then on.

## The listener

`[remote] listen = ["192.168.1.5:7443", "100.64.0.3:7443"]`: off by default, and only
the addresses named, never `0.0.0.0` unless written. Same `BackendService` as the
socket, over TLS 1.3 with HTTP/2 (`ServeTLS`; the client sets `ForceAttemptHTTP2`).

- **Mutual TLS, pinned.** The server asks for any client certificate
  (`RequireAnyClientCert`), the client skips CA checks (`InsecureSkipVerify`), and both
  check the peer's key ID against what they pinned in `VerifyConnection`, which runs on
  resumed sessions too (`VerifyPeerCertificate` does not, and a session ticket would
  outlive an unpairing). Unpairing closes that device's live connections.
- **Unpaired keys are refused in the handshake**, except while a pairing token is
  live, and then only the `Pair` call is served (an interceptor beside the profile
  gate).
- **Before TLS**, a per-address token bucket and a cap on concurrent handshakes; the
  handshake is bounded by `ReadHeaderTimeout`.
- **The roster** of paired devices (name, ID, added when and by whom) is a file in
  kithd's data directory, not the config (it is not something one edits by hand into
  sense) and not the cache (a rebuild must not unpair everyone).

**A paired device may do everything**, as the socket allows today: send, delete,
edit the config, pair and unpair others. (Decided 2026-10-28; roles per device were
the alternative, a permission check on every call.) Any paired device can unpair a
lost one.

## Pairing

1. On a device kithd already trusts (its own machine over the socket, or a paired
   one), `kith pair` asks kithd for a pairing token: 128 random bits, one use, ten
   minutes. It shows a QR (`internal/qrtext`) and the same as a `kith://pair?…` URI:
   kithd's addresses, its device ID, the token, the expiry.
2. The new device scans or pastes it (`kith join <uri>`), pins kithd's ID from it, so
   its very first connection is authenticated, and calls `Pair` with the token over
   TLS with its own key.
3. kithd holds the new key as pending and both screens show six digits derived from
   both IDs and the token. The person confirms on the trusted device; kithd adds the
   device to the roster. A QR photographed and used first shows digits nobody
   confirms.
4. The new device writes the bridge into its `device.toml`.

Rejected: a PAKE (the code is scanned or pasted, so it can be long; Go's PAKE
libraries are not mature) and Noise (it gives up TLS, HTTP/2 and Connect).

## Clients

`daemon.Dial` picks the transport: the unix socket when this machine runs the kithd
named, else the first `[[bridge]]` address that answers with the pinned ID (pinned
priority with fallback across a bridge's addresses; across bridges is Phase 3).
`kith-notify` and `kith-mcp` dial the same way. On a device without a local kithd the
TUI does not spawn one, and `kith --open` hands links to the local window as today.

**Files cross as bytes.** Downloads already do (`LoadImage`), into the media cache,
which is the client's (`internal/media`). The one path that crosses is `SendFile`'s,
which every adapter opens inside kithd. It becomes an upload in chunks by unary calls
(`StartUpload`, `PutUpload` at an offset, then `SendFile` naming the upload), which
work over the HTTP/1.1 socket and TLS alike, hold one chunk in memory, and resume
after a cut; kithd stages the file in its own cache and hands the adapter a path as
today. `Remote.SendFile` keeps its signature, the path now the client's. On the socket
the same calls are used: one path, not two. `[display.media]` moves to `device.toml`;
kithd's backup takes the media folder on its own machine.

Left with shared folders after step 2: the phonebook (the TUI edits cards, kithd reads
them: design-clients.md open question 10), a notification `sound` (a path on kithd's
machine; a remote `kith-notify` plays its own default), and `kith --add-dictionary`
(writes kithd's data folder from the CLI).

**Liveness** is HTTP/2's own PING (`ReadIdleTimeout`) on the client; the
`Heartbeat(network, caught_up_as_of)` of design-clients.md is for choosing among bridges
and lands with failover (Phase 3).

## Plan

Each PR lands working.

1. **feat: device settings in `device.toml`.** `config.Device` beside `config.Config`,
   the device keys moved out of the bridge's types so kithd reading one does not
   compile; device validation moved to the client; the one-time split; the TUI and
   `kith-notify` reading device keys locally and the bridge's through `GetConfig`; the
   settings editor writing each to its own file. **Failure review** (merges writers):
   kithd and the client splitting at once, a split interrupted midway, a device key
   written into the bridge's file by hand after the split, two windows editing at once.
2. **feat: files cross as bytes.** Uploads in chunks; `[display.media]` to the device.
   **Failure review** (concurrent): a large file, an upload cut off and resumed, chunks
   out of order or repeated, two uploads at once, an upload never sent (staging
   cleaned), the staging folder full.
3. **feat: device keys, and kithd listens over TLS.** Keys and IDs, `[remote] listen`,
   pinned mutual TLS, the roster, rate limiting, `kith devices` (list, unpair, and add
   by pasted ID, as Syncthing does, before QR pairing exists). **Failure review**
   (enforces scope): an unpaired key, a key unpaired mid-stream, a resumed session
   after unpairing, a flood of handshakes, a roster file edited by hand or corrupt.
4. **feat: pairing by QR.** Tokens, `Pair`, the six digits, `kith pair` / `kith join`.
   **Failure review** (enforces scope, concurrent): an expired or reused token, two
   devices racing one token, digits never confirmed, the trusted device going away
   mid-pairing.
5. **feat: clients dial a remote bridge.** `daemon.Dial` over the socket or a
   `[[bridge]]`; the TUI, `kith-notify`, `kith-mcp`; HTTP/2 PING; SECURITY.md's threat
   model for a listening kithd. **Failure review** (concurrent): an address that
   answers with another key, the bridge restarting mid-stream (the message stream
   resumes, #387), a network change under a laptop.

## Left open

- **WAN reach** (design-clients.md open question 4): pasting addresses and Tailscale
  work with this design; an embedded tailnet node (`tsnet`) is a heavy dependency.
- **Key rotation**: re-pairing for now; the old key signing the new one belongs with
  the roster's CRDT (Phase 2b).
- **HTTP/2 on the socket** (h2c): needed by the first bidirectional call, which is the
  CRDT's sync in Phase 2b.
