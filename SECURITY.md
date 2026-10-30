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
  device that every peer must pair again. The device list holds no secrets, but it
  is the authorization: it is written `0600`, atomically, and a list that does not
  read keeps kithd from listening at all.

## Supported versions

This is a pre-1.0 project; only the latest release is supported. Please upgrade
to the newest release before reporting an issue.
