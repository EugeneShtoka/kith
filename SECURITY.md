# Security Policy

## Reporting a vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

Report privately via GitHub's
[Private vulnerability reporting](https://github.com/EugeneShtoka/kith/security/advisories/new)
(the **Security → Report a vulnerability** button on the repository). This keeps
the report confidential until a fix is available.

Please include, as far as you can:

- the affected version (`kith --version`),
- a description of the issue and its impact,
- steps to reproduce or a proof of concept,
- any suggested remediation.

You can expect an initial acknowledgement within a few days. Once a fix is
released, we're happy to credit reporters who wish to be named.

## Scope and things to keep in mind

kith is a local, single-user chat client for Matrix, WhatsApp, Telegram and Slack.
Because it holds every account's credentials and end-to-end-encryption key material, a few areas are especially
security-relevant — please flag anything in these areas:

- **Access token & credentials at rest.** The client stores a Matrix access
  token (and device ID) in the OS keyring, or in a `0600` file only when
  `allow_token_file` is set. A Telegram session and a Slack token and cookie are
  kept in the keyring too. A WhatsApp link — its device keys and Signal sessions —
  is a `0600` SQLite file in the data directory that only the daemon opens: whoever
  has it reads and sends as that account. Report any path that leaks any of these,
  logs them, or writes them with overly-permissive file modes.
- **E2EE key material.** The Olm/Megolm crypto store (device keys, session keys,
  cross-signing keys) and the keyring-held pickle key that encrypts it are
  sensitive. Report anything that could exfiltrate them, weaken verification, or
  cause the client to send to unverified/unexpected devices when it shouldn't.
- **The daemon socket.** `kithd` serves every client on its machine over a unix
  socket under `$XDG_RUNTIME_DIR`, and its `0600` permissions are the authorization
  there. Report any way another local user could reach it, or any request that
  reaches beyond the account the daemon serves.
- **A listening kithd** (`[remote] listen`): see [below](#a-listening-kithd). Report
  any way to be served without a paired key, to stay connected or reconnect after
  being unpaired, to pair without the confirmation on a trusted device, or to exhaust
  the listener from one address.
- **Assistant and model features.** `kith-mcp` lets an assistant read the cache and
  send or draft messages, within the rooms `[agent.read]` and `[agent.write]` allow; the
  optional model features send message context to a configured endpoint, within the
  rooms `[assist]` allows. Report anything that sends or reveals more than that
  configuration permits.
- **Server trust & TLS.** Report any case where the client would talk to a
  homeserver, or any network's servers, over an unvalidated TLS connection, or follow a redirect/`.well-known`
  discovery to an unexpected host without the user's intent.
- **Untrusted event content.** Message bodies, room names, and member display
  names are attacker-controllable. Report any rendering path where such content
  could inject terminal escape sequences, corrupt the display, or trigger
  unintended behavior.
- **Secrets in logs.** Report any case where tokens, keys, or decrypted message
  content end up on stderr or in the daemon's journal
  (`journalctl --user -u kithd.service`).
- **The desktop window** (`kith-desktop`): see [below](#the-desktop-window). Report
  any way content from a message, a name or a picture runs script in the window,
  navigates it, or reaches a call, a file or a stream the window's allowlist does not
  name; and any way to reach `kith-relay`'s socket from another user.

## A listening kithd

`[remote] listen` lets clients on other machines reach kithd over the network. The
threat model:

- **Off by default.** With `listen` empty, kithd opens no network port; the socket is
  all there is. It listens only on the addresses written, and refuses a bare `:port`,
  so every interface (`0.0.0.0`, `[::]`) is only ever taken when written out.
- **Pinned mutual TLS.** TLS 1.3 only. Each side presents a self-signed certificate
  for its device key (ECDSA P-256); the device ID is the SHA-256 of the key's
  SubjectPublicKeyInfo. kithd requires a client certificate and admits it only if
  its key's ID is in the device list (kept as ops signed by kithd's device key in
  `mesh-<instance>.db`, written out as `devices-<instance>.toml`, both in the data
  directory), checked in the handshake, before any call is read. No CA is involved;
  certificates are reissued at will, keys are what is pinned. Session tickets are
  off, so no session outlives an unpairing; the check runs on every handshake.
- **Paired = full authority.** A paired device may do everything the socket allows:
  read and send on every account, edit the config, pair and unpair devices. There
  are no per-device roles. Unpairing closes that device's live connections at once,
  and its next handshake is refused. Pairing is on an already-trusted device (the
  socket, or a paired device): by pasting an ID, or by QR code (below).
- **Pairing by QR code.** `kith pair` asks kithd for a token: 128 random bits, good
  for one pairing, for ten minutes, and only while the `kith pair` that asked waits.
  The QR code (or `kith://pair` URI) carries it with kithd's addresses and device ID,
  so the new device pins kithd's key from its very first connection: there is no
  trust on first use for an attacker in the middle to take. While a token is live,
  kithd completes the handshake with an unpaired key, but serves that connection
  only `Pair`; every other call is refused, and the connection is cut once no pairing
  is open. Tokens are compared in constant time; one device may answer a token, and
  any other is refused.
- **The confirmation digits.** The device that answers is held, not paired, until a
  person confirms it on the trusted device. Both screens show six digits derived from
  kithd's ID, the new device's ID and the token (SHA-256), so a QR code photographed
  or a URI copied, and used first by someone else, shows digits that do not match the
  person's own device: refused, it is never paired. A refusal, the ten minutes passing,
  or either side going away spends the token, drops the waiting key and cuts its
  connection. The digits guard the confirmation, not the token: whoever confirms
  without comparing them pairs whoever answered.
- **Bridges syncing (the mesh).** A kithd paired as a bridge (`kith join --bridge`) is
  in the device list like any device, with the bridge role and its public key. The sync
  call is served over `[remote] listen` only, and only to a connection whose key the
  list has as a bridge: the socket, a device's key, an unpaired key and a key let in to
  answer a pairing are refused before a frame is read. Every change in the mesh store
  is signed by the bridge that made it (ECDSA P-256 over one canonical encoding) and
  checked by every bridge it reaches, relayed or not: a change to the device list counts
  only if its signer was a bridge of the list as of what that change had seen, so a
  bridge joining checks the list back to its first entry rather than take the sender's
  word, and no bridge can write as another. Unpairing a bridge is unpairing a device: its
  connections are cut and its key is refused by every bridge that holds the removal; a
  change it signed before it saw its removal still counts everywhere, and nothing it
  writes after does. A bridge found signing two different changes under one number is
  removed by the bridge that finds it. Paired = full authority holds between bridges
  too: a bridge may pair and unpair anything, on every bridge. A peer whose clock is
  more than 60 seconds off is refused, and a change dated further ahead waits.
- **Before TLS.** Each remote address (an IPv6 /64 counting as one) gets a burst of
  10 handshakes, then one per two seconds; at most 32 handshakes run at once; a
  handshake, and each request's headers, must arrive within 10 seconds. A throttled
  connection is closed before a byte of TLS.
- **What an attacker on the network sees.** An open port that speaks TLS 1.3 and asks
  for a client certificate; kithd's certificate, which carries its device ID (public:
  it names a key, it grants nothing) and no account names. Without a paired key,
  nothing past the handshake. A refused handshake is logged at debug level only.
- **The client side.** A machine that joined a bridge dials only the addresses its
  `[[bridge]]` entry names and takes the connection only if the peer's key has the
  pinned ID, checked in the handshake (`VerifyConnection`, run on resumed sessions
  too); an address answering with any other key is passed over, never trusted. It
  presents its own `client-device` key. kithd's config travels to it over that
  connection, as everything else a paired device may read does. Idle connections are
  pinged (HTTP/2 PING) and dropped when unanswered, so a connection a network change
  left behind is not kept.
- **Links stay on their device.** `kith --open` hands a link through kithd to a
  window on the same device only, by the device each connection authenticated as, not
  by anything a client says of itself; the socket counts as kithd's own machine.
- **Key storage.** Device keys live in the OS keyring (account
  `<instance>|device` for kithd, `<instance>|client-device` for a client machine),
  or, only with `allow_token_file` and no keyring, in a `0600` file in the state
  directory. A corrupt key is reported, never silently replaced: a new key is a new
  device that every peer must pair again. kithd's key can be written out as a recovery
  key ([docs/remote.md](docs/remote.md#kithds-recovery-key)): only by `kith devices recovery-key` on kithd's own machine,
  reading the keyring there; no RPC carries it, over the socket or the network. A
  backup holds no device key. The device list holds no secrets, but it
  is the authorization: it is written `0600`, atomically, and a list that does not
  read keeps kithd from listening at all.

## The desktop window

The desktop window ([docs/desktop.md](docs/desktop.md)) draws what other people wrote,
in Electron. Its renderer, the page, is treated as the part an attacker reaches first,
and holds the least:

- **The renderer is sandboxed.** `sandbox`, `contextIsolation` and no
  `nodeIntegration`, set explicitly although they are Electron's defaults. It has no
  Node, no file system and no network: the page is served from `app://kith`, not
  `file://`, under the policy `default-src 'none'; script-src 'self'; style-src 'self';
  img-src 'self' blob: kith-media:; media-src kith-media:; connect-src 'none'`. Every
  browser permission (camera, notifications, clipboard reads) is refused.
- **No HTML, ever.** Message bodies arrive from kithd already parsed and sanitized
  into text and spans; the page builds React elements from them, and a lint rule bans
  `dangerouslySetInnerHTML`, `innerHTML`, `outerHTML` and `insertAdjacentHTML`. Names
  are text.
- **No remote pictures.** A picture is `kith-media://<room>/<event>`, which main
  answers from kithd (`LoadImage`), so drawing one never fetches a URL a sender chose.
- **The window does not leave its page.** Navigation is refused and every
  `window.open` denied. Links show their target and open nothing yet.
- **The preload is the whole reach.** The page may call kithd's RPCs by name, in
  protobuf binary, and main serves only the names on an allowlist of what reading
  and writing messages needs, with the settings page and logging accounts in and out.
  Calls beyond that (config edits, key export, pairing) are left off until a feature
  needs one and its review adds it. A login's calls reach only a login the window
  began itself, as its current step allows (one answer at a time, its new account
  written once, through the relay); a login the window stops leading (it reloads or
  closes) is ended in kithd; and sign-out reaches only an account whose own space is
  on the profile's rail. What is typed into a login is not logged. Main checks
  that each request comes from the window's own `app://kith` frame, decodes it against
  the method's schema, and refuses anything else. Files go through main too: a file
  is read where the system's dialog, a drop or a paste put it, and the page holds only
  a token for it; an attachment is saved where the person picks in a save dialog.
  A queued message is canceled or retried only by an ID kithd showed the window, or
  queued for it, in the profile it runs in.
- **Your own commands run on this machine, named only.** `kith-relay`, not the page,
  runs the commands of the device's commands folder (the terminal client runs the same
  ones). The page names a command, a room and a message, and nothing else: the relay
  refuses a name that is not one executable file in that folder (no `/`, no `..`), a
  room outside the window's profile (main names the profile, the page cannot), and a
  room outside the command's `in` places. The program is given only what the relay
  looks up itself from kithd (the message, its link, the room's history) and its
  declared secrets from the keyring: no argument, text, path or variable of the page's,
  and no secret ever reaches the page. Output marked `send` is sent by the relay into
  the room it ran in, and `clipboard` is copied by main; the rest comes back to the
  page as text. One runs at a time, `[commands] timeout` bounds it, and it is killed
  when the window reloads, closes or the relay exits.
- **kith-relay.** The window reaches kithd only through `kith-relay`, a Go child
  process bundled with it. It listens on a socket in a new `0700` directory under
  `$XDG_RUNTIME_DIR/kith`, so only the same user can reach it, and exits when the
  window's end of its stdin closes, so it never outlives the window. On a machine that
  joined a bridge it holds the device key and pins the bridge's
  ([above](#a-listening-kithd)); the window's code never sees the key.
- **Fuses.** The packaged Electron binary has `RunAsNode`, `NODE_OPTIONS` and
  `--inspect` turned off, and loads its code only from its ASAR. The fuse that checks
  the ASAR against a hash embedded in the binary is on, but Electron enforces it on
  macOS and Windows only. On Linux the app's integrity is its files': the deb and rpm
  install them owned by root and writable by no one else, and the zip unpacks as the
  user who owns it.
- **What a release checks.** `scripts/desktop-package-check.sh` unpacks each deb, rpm
  and zip the release makes and refuses it unless its `kith-relay` is, byte for byte,
  the one GoReleaser built from the tag (and which says it was built from the tag's
  commit), the fuses read as above, the app is its ASAR alone, and no file in it is
  writable by group or others. The desktop's checksums are signed keyless, as kith's
  are, and each package has a build attestation. There is no auto-update: the window
  never calls home.

## Supported versions

This is a pre-1.0 project; only the latest release is supported. Please upgrade
to the newest release before reporting an issue.
