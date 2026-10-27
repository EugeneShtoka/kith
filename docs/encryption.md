# Encryption

kith supports end-to-end encrypted rooms: sending, receiving, device verification, server-side key backup and key export files. The crypto is [mautrix-go](https://github.com/mautrix/go)'s pure-Go Olm/Megolm implementation (goolm). Release binaries are built with `CGO_ENABLED=0 -tags goolm`, with no libolm and no C toolchain. The crypto store uses a pure-Go SQLite driver.

Encrypted rooms work without any extra setup. Everything below is about **trust** (proving that this device is really you) and **history** (reading messages sent before this device existed).

## One process owns the keys

Only `kithd` touches the crypto store. `kith` and `kith-mcp` attach to it over a unix socket and never open the store themselves. Even `kith login` only hands the password to the daemon, which logs in and opens the store itself.

The reason is that two processes sharing one Matrix device's Olm/Megolm state can corrupt it, and restarting does not repair that. So before opening any store, the daemon takes an exclusive `flock` on a lock file next to its socket. The kernel settles who owns the store. A second daemon for the same account sees the lock, exits successfully and silently, and the client uses the daemon that is already running.

For the same reason, the client has **no in-process fallback**. If no daemon is running, `kith` starts one and attaches to it. It never opens the stores itself "just this once". See [ARCHITECTURE.md](../ARCHITECTURE.md) for the rest of the daemon design.

## Where the keys live

| What | Where | Protection |
| --- | --- | --- |
| Crypto store (Olm account, Megolm sessions, device lists, cross-signing state) | `$XDG_DATA_HOME/kith/crypto-<instance>-<account>.db`, one per Matrix account | Encrypted at rest with the pickle key |
| Pickle key (32 random bytes) | OS keyring, service `kith`, account `<instance>\|matrix\|<account>\|pickle` | Keyring only, never written to disk |
| Access token and device ID | OS keyring, service `kith`, account `<instance>\|matrix\|<account>` | Keyring. A `0600` file only if you opt in with `allow_token_file = true` |
| WhatsApp's and Messenger's key stores (each linked device's identity keys, prekeys, sessions, sender keys) | `$XDG_DATA_HOME/kith/whatsapp-<hash>.db`, `messenger-<hash>.db` | Encrypted at rest, every page and the write-ahead log, with the store's key |
| Each store's key (32 random bytes) | OS keyring, service `kith`, account `<hash>\|whatsapp-store`, `<hash>\|messenger-store` | Keyring only, never written to disk |
| Message cache (decrypted text, full-text index) | `$XDG_DATA_HOME/kith/cache-<hash>.db` | Encrypted SQLite (every page and its log), with a key of its own |
| The cache's key (32 random bytes) | OS keyring, service `kith`, account `<hash>\|cache-store` | Keyring only, never written to disk |
| Downloaded attachments and rendered previews | `$XDG_CACHE_HOME/kith/` | File permissions |
| Socket and lock | `$XDG_RUNTIME_DIR/kith/<hash>.sock`, `.lock` | Socket `0600`, directory `0700` |

`<account>` is derived from the account's MXID, so your user ID does not appear in directory listings, and each account gets its own files.

**The pickle key requires an OS secret store**: a Secret Service provider such as GNOME Keyring on Linux, or Keychain on macOS. If no keyring can be read, the daemon logs `encryption disabled` (with the reason) and keeps running for unencrypted rooms only. It will not generate a key it would have to store in plaintext. On a headless machine, run a Secret Service provider before starting the daemon.

**A corrupt pickle key is reported, never replaced.** If the keyring entry is not a valid key, the daemon logs `encryption disabled` with `the pickle key in the keyring is corrupt`, naming the entry. A new key could not open the crypto store the old one encrypted, so replacing it would silently cost this device its identity and every room key it holds. Restore the entry if you can. Otherwise delete both the entry and the crypto store file, log in again with `kith login` (a new device: the server already holds the old device's keys, which a fresh crypto store cannot claim), then restore your room keys from the key backup (`kith --restore-keys`).

**WhatsApp's and Messenger's key stores are encrypted the same way.** Whoever holds one could act as that account and read its new messages, so each is encrypted at rest with a key of its own, kept only in the keyring. A store written by an older kith is encrypted at its first open, once: it is copied, the copy checked, and only then the plain file deleted; an interrupted migration is taken up again at the next start. Without a keyring, WhatsApp (and Messenger's encrypted chats) stay off, logged as `its store's key cannot be had from the keyring`; kith never keeps the key on disk instead. A corrupt key is reported naming its entry and never replaced. If the key is lost, the store cannot be opened again: delete the entry and the store file, and link the account again. That costs the link, not your history, which is in the message cache.

**The message cache is encrypted too**, under its own key in the keyring, so a copy of the disk, or of the file alone, holds no readable history. A cache written by an older kith is encrypted at the daemon's first start, the same way and once; it needs as much free space again while it is copied (a few seconds for a few hundred MB). The copies a rebuild sets aside, and the cache inside a backup, are encrypted as well. The price: **while the keyring is locked, all history is hidden**, every network's, until it is unlocked. kithd waits for it rather than start without the cache, and opens everything once the keyring answers (see [troubleshooting](troubleshooting.md#the-cache-is-locked-unlock-the-keyring)). If the cache's key is lost, the cache cannot be opened again: move the file aside and kithd starts an empty one, filled again from what the networks still hold.

## Verifying this device

Verification proves to your other sessions, and to the people you talk to, that this device is really you. It uses **emoji SAS** (short authentication string). Neither QR code method is implemented.

### Starting from kith

Type `:verify`. Your other sessions (for example Element on your phone) receive a request. Accept it there, and both sides show the same emoji sequence.

### Answering a request

When another session asks to verify, kith shows an overlay with the requesting user and device:

| Key | Action |
| --- | --- |
| `y` | Accept the request, and later confirm that the emoji match |
| `n` / `esc` | Reject, or cancel on a mismatch |

The keys are configurable in `[keys.verify]` (see [keybindings](keybindings.md)).

**Compare the emoji on both screens before pressing `y`.** Only confirm if they match exactly.

### After verification

Once verification succeeds, kith asks the verified device for the **key backup secret** (secret gossip). If it arrives within 30 seconds, the daemon imports the whole server-side backup, and the status line says "restored N room keys from backup — reopen rooms to see history". If no answer comes, or the account has no backup, the device is still verified, and you can use `--restore-keys` instead.

If the verification machinery cannot be set up, encryption keeps working. The daemon logs `device verification unavailable` and `:verify` says why.

## Trust model

kith uses mautrix-go's default policies:

- **Outgoing room keys** are shared with every device of every room member, verified or not. kith does not block sending to unverified devices, and the timeline has no per-device trust markers.
- **Room-key requests** are answered only for devices that are at least cross-signed (trusted on first use).
- **Key backup uploads** only go to a backup version signed by your master cross-signing key or by a trusted device. A backup version created by someone else, whose public key you cannot verify, would be a copy of every message you can read, so kith refuses to upload to one.
- **Cross-signing** is used whenever the account has it. `--restore-keys` with a recovery key signs this device with it. `--bootstrap-keys` creates cross-signing from nothing.

## Key backup

The server-side key backup holds your room keys, encrypted to a key that only your recovery key can unlock. With it, a new device can read old history.

### Restore from an existing backup

```sh
kith --restore-keys
```

This prompts (without echo) for the **recovery key or passphrase**, unlocks secret storage, and imports every room key from the backup. kith detects which of the two you typed. With a recovery key, it also cross-signs this device. The client then starts as usual. Reopen rooms to see decrypted history.

| Error | Meaning |
| --- | --- |
| "this account has no room-key backup on the server" | Nothing to restore. Import a key file, or use `--bootstrap-keys` |
| "that recovery key or passphrase is not correct" | Nothing was changed |
| "encryption is not enabled for this device" | The daemon could not enable E2EE. Check its log for the keyring error |

### Set up a backup for the first time

```sh
kith --bootstrap-keys
```

This is for an account that has **no** encryption setup yet. It prompts for your **account password**, because publishing cross-signing keys needs interactive authentication, which the access token alone does not satisfy. It then:

1. creates cross-signing keys and signs this device with them,
2. creates secret storage with a new **recovery key**,
3. creates a backup version signed by your master key and stores its private key in secret storage,
4. uploads every room key this device holds.

The recovery key is **printed once and stored nowhere**. Write it down before doing anything else. The command exits afterwards so the TUI cannot scroll the key off the screen.

It **refuses** if the account already has secret storage or a backup version. Running it again would replace your cross-signing identity, invalidate every verification made against the old one, and orphan the recovery key you wrote down. Use `--restore-keys` on such an account instead.

### Keeping the backup filled

The daemon uploads new room keys to the backup by itself: once at startup and then every five minutes. It needs no secret for this, because the backup is encrypted to a public key. Each pass uploads only the sessions that have not been uploaded yet. If `--bootstrap-keys` could not finish uploading, the next pass continues from where it stopped.

## Key export files

For an account without a server-side backup, you can move keys between devices as a file:

```sh
kith --export-keys ~/room-keys.txt   # prompts for a passphrase twice, writes the file, exits
kith --import-keys ~/room-keys.txt   # prompts for the passphrase, imports, then starts the client
```

The file uses the **Matrix key export format** defined in the spec. A file exported here opens in Element, and an export from Element imports here. This is useful when the only device that can read your old history is a different client.

- The export is created `0600`, and kith **refuses to overwrite** an existing file.
- It is encrypted with the passphrase. That passphrase is the only thing protecting every message those keys unlock, so an empty passphrase is refused and you must type it twice.
- Import reports `imported N of M room keys`. `0 of M` means the file was fine and you already had every key in it.
- The client reads and writes the file itself. The daemon runs with `ProtectHome=read-only` and cannot reach your home directory, so only already-encrypted bytes cross the socket.

## Messages that will not decrypt

A message whose Megolm session this device never received is shown as:

```text
🔒 Encrypted message — no decryption key
```

This placeholder is never cached. As soon as the key arrives, the next time the room is opened the real message appears. To recover missing keys:

1. **Verify this device** (`:verify`) against a session that has the keys. The backup is restored automatically after verification when the account has one.
2. Otherwise run `kith --restore-keys` with the recovery key or passphrase.
3. If the account has no backup, export keys from a device that can read the history (in Element, *Export E2E room keys* in its security settings) and use `kith --import-keys FILE`.
4. Reopen the affected rooms. If rows still show the placeholder, `kith --clear-cache` empties the cache and fetches history again.

Messages sent to the room before your account joined, or while no device of yours existed, may never have been shared with you. No client can decrypt those.

## Security considerations

- **The message cache is encrypted; some things beside it are not.** Decrypted messages are kept in an encrypted SQLite file, so that search, mentions and the MCP server work offline. Pictures kith has shown (`$XDG_CACHE_HOME/kith/media`) and exports are written in the clear: for those, full-disk encryption remains the protection, and never loosen their file permissions.
- **File permissions.** The daemon creates `$XDG_DATA_HOME/kith/` as `0700` when it does not exist yet. The shipped systemd units also set `UMask=0077`, so the cache and crypto store are created owner-only. If you run `kithd` some other way, keep a `077` umask, or check that `~/.local/share/kith/*.db` is not readable by group or others.
- **Hardened unit.** The unit in `packaging/systemd/` runs the daemon with `ProtectSystem=strict`, `ProtectHome=read-only`, a short list of writable paths, `NoNewPrivileges`, `MemoryDenyWriteExecute`, `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6`, `SystemCallFilter=@system-service` and an empty capability bounding set.
- **What stays visible to the homeserver.** In an encrypted room, messages, attachments and reactions are encrypted. A reaction's link to the message it annotates stays in the clear, as the spec requires, so the server can count reactions, but the emoji itself is encrypted. Redactions are not encrypted, and neither is a redaction's reason. Room names, topics and membership are room state, which Matrix does not encrypt. When the crypto machine is unavailable, kith refuses to send into an encrypted room rather than send in the clear.
- **Secrets are never on argv or disk.** Passwords, recovery keys and export passphrases are read from the terminal without echo, sent in a request body over the `0600` socket, and discarded. A secret prompt requires an interactive terminal.
- **Encrypted rooms stay local by default.** The optional language-model layer (`[assist] encrypted = false`) and the MCP server (`[agent.read] encrypted = false`, and separately `[agent.write] encrypted = false` for writing) exclude encrypted rooms unless you opt in. See [assist](assist.md) and [mcp](mcp.md).
- **Reporting.** Credential and key-material handling, TLS and homeserver trust, terminal-escape injection and secrets in logs are all in scope. See [SECURITY.md](../SECURITY.md) for how to report privately.
