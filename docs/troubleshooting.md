# Troubleshooting

This page lists common problems with the error messages that go with them, and how to
fix each one. Most problems start in the daemon, so [read its log](#logs) first.

## Logs

All three programs write the same kind of log: one line per event, as
`level=WARN msg="mark room read failed" op="mark room read" room=matrix:4f1c9e2a7b3d5c60/!abc:example.org err="…"`.
Lines name rooms, events and users by ID. They never contain message text, access
tokens, passwords or keys; anything shaped like one is replaced with `[REDACTED]`.

| Program | Where its log goes |
| --- | --- |
| `kithd` under systemd | The journal, through its stderr: `journalctl --user -u kithd -f`, or `make daemon-logs` for the last 100 lines. Under journald the lines carry no timestamp of their own, because the journal adds one. |
| `kithd` in the foreground | The terminal you started it in (stderr), unless `--log-target journal` |
| `kithd` started by `kith` without systemd | The journal, tagged `kithd`. Without one: `$XDG_STATE_HOME/kith/kithd.log`, so it never draws over the interface |
| `kith` | The journal, tagged `kith`. Without one: `$XDG_STATE_HOME/kith/kith.log` (usually `~/.local/state/kith/kith.log`). The interface owns the terminal, so failures show briefly in the status line, with their reason, and stay in the log. |
| `kith-mcp` | Its stderr (stdout carries the protocol). Your MCP client decides where that ends up. |

Follow the client and the daemon together:

```sh
journalctl --user -t kith -t kithd -f
```

`-t` matches the tag, which is how `kith` and a daemon it started log; add
`-u kithd` to include the systemd unit. In the journal, each attribute of a line is a field
of its own, upper-cased: `room=` is `ROOM=`, `op=` is `OP=`, `err=` is `ERR=`, and a
group's attributes are joined with `_`. Filter on them:

```sh
journalctl --user ROOM='matrix:4f1c9e2a7b3d5c60/!abc:example.org'
journalctl --user -t kith PRIORITY=4 -o verbose   # warnings, with every field
```

(The unit's own lines arrive as plain text through stderr, so filter those with
`--grep` instead.)

### Where the log goes

`[log] target` chooses, for the client and a daemon it started:

- `auto` (the default): the journal when there is one, the file otherwise
- `journal`: the journal; with none, the file, with a note at its top saying why
- `file`: always the file

`--log-target` and `KITH_LOG_TARGET` override it. A daemon run as a systemd unit
always logs through its stderr, which is the journal already.

There is no journal on macOS, FreeBSD or Windows, or on Linux without systemd, so
there the log is always the file: `$XDG_STATE_HOME/kith/kith.log` for the client
and `kithd.log` beside it for the daemon. `$XDG_STATE_HOME` defaults to
`~/.local/state` on Linux and FreeBSD, `~/Library/Application Support` on macOS, and
`%LOCALAPPDATA%` on Windows.

Log files are capped at 1 MB. When one fills up it's renamed to `….log.1`, replacing
the previous one, so each file takes at most 2 MB.

Every failed request a client makes to the daemon is logged by the daemon at `WARN`
with the call's name (`op=MarkRoomsRead`) and the reason, even when the client shows
only a short line. Background work that fails, such as a cache write during sync or a
receipt the homeserver refuses, is logged at `WARN` too.

### Raise the log level

The levels are `debug`, `info` (the default), `warn` and `error`. From highest to
lowest precedence, set the level with:

- the `--log-level` flag, which all three programs take (`kithd -v` is short for
  `--log-level debug`)
- the `KITH_LOG_LEVEL` environment variable
- `[log] level` in the config file

`debug` adds best-effort failures that are normally harmless, and the Matrix
library's own request log. To debug the daemon under systemd, set the level in the
config and restart the unit, or run it by hand:

```sh
systemctl --user stop kithd
KITH_LOG_LEVEL=debug kithd
```

The daemon picks up a changed `[log] level` when a client asks it to reload its
config.

## An account stopped

An account that had a session and stops (its network refuses the session, the phone
unlinks kith, or it fails at start) raises one desktop notification, in every profile, naming the
account and why. While it stays down, its account row in the rail is marked ⚠, and
with the cursor on that row the status line says why, usually with the
`kith login <network> <account>` that brings it back. A network still retrying keeps
quiet: only an account that has stopped is told. An account you never logged in is
not "stopped".

## Startup

### "Matrix personal is logged out: no saved session; :login matrix personal in kith, or run `kith login matrix personal`"

The keyring holds no session for this account, so log it in. kith opens meanwhile with whatever else is
logged in, the daemon keeps running with Matrix logged out, and Matrix starts as soon
as the login is done. Until then the account's cached rooms stay listed and readable,
with their unread counts; nothing new arrives in them and nothing can be sent.

### "the OS secret store could not be read (is the keyring unlocked?)"

A session may well exist, but the keyring couldn't be reached. The keyring might be
locked, or the D-Bus session bus might be missing or timing out. **Don't log in
again.** Doing so would replace a working session with a new device. Instead:

- Unlock the keyring (log in to the desktop session, or unlock it in your keyring
  app).
- Under systemd, make sure the daemon can see the session bus:
  `systemctl --user show-environment | grep DBUS`. If it's missing, run
  `systemctl --user import-environment DBUS_SESSION_BUS_ADDRESS`, then restart the
  unit.

### "saved session for … is unusable (run `kith login` again)"

The homeserver rejected the stored token. Usually the device was signed out from
another client, or the token was revoked. Run `kith login` again; the daemon, which
kept running with Matrix logged out (its cached rooms still listed and readable),
starts on the new session. The new device
needs its room keys; see [Undecryptable messages](#undecryptable-messages).

### The daemon cannot start, so kith does not run

This is by design: the client has no mode of its own without the daemon. The error
says which step failed:

| Message | Meaning and fix |
| --- | --- |
| `attach to kithd: daemon: kithd did not become ready in 1m: …` | The daemon started but never completed its first sync. The text after the colon is the daemon's own last error. Check the log. |
| `daemon: kithd is not on PATH` | `kith` fell back to starting the daemon itself and couldn't find it. Install `kithd` next to `kith`, or install the systemd unit. |
| `daemon: cannot detach kithd without setsid` | There's no systemd and no `setsid` (macOS or FreeBSD, for example). Start `kithd` yourself before running `kith`; on macOS, load the LaunchAgent (`make deploy`, or see [getting-started.md](getting-started.md#macos-homebrew)). |
| `daemon: start kithd.service: …; and …` | `systemctl --user start` failed, and so did the fallback. Both reasons are printed. |

Once the daemon is up, `kith` attaches to it right away. If the daemon exits while
you're using the client, the status line shows `lost the daemon — reconnecting`, then
`daemon back — catching up` once systemd has restarted it.

### The unit fails with status 226/NAMESPACE

The unit sandboxes the daemon (`ProtectSystem=strict`, `ProtectHome=read-only`) and
names the directories it may write to. systemd can't set up that sandbox when one of
those directories is missing, and the error it reports mentions namespacing, not the
directory. Create them and restart the unit:

```sh
mkdir -p ~/.local/share/kith ~/.local/state/kith ~/.cache/kith
systemctl --user restart kithd
```

Current units create them themselves, in an `ExecStartPre=+mkdir` step that runs
outside the sandbox, so this happens only with a unit installed before that step
existed, or one edited to write somewhere new without adding the directory to both
`ReadWritePaths=` and that `mkdir` line. Reinstall the unit (`make install`, or copy
it from a current release) to pick up the step.

### The unit is inactive, but kith works

Another `kithd` is already serving the account. Typically, `kith` started one with
`setsid` before the unit was installed. The daemon takes an exclusive lock
(`$XDG_RUNTIME_DIR/kith/<hash>.lock`), and a second copy that finds the lock taken
**exits 0 without a word**, which systemd records as a clean stop. For the same
reason, `kithd` run in a terminal returns immediately while the unit is running.

To hand the account back to systemd:

```sh
pgrep -a kithd          # find the stray daemon
pkill -x kithd          # stop it; it shuts down cleanly on SIGTERM
systemctl --user restart kithd
```

### Socket errors: permission denied or connection refused

The daemon listens on `$XDG_RUNTIME_DIR/kith/<hash>.sock`, with mode 0600 in a
0700 directory. The file permissions are the only access control, so only the user
who runs the daemon can connect. Check:

- **The same user.** `kith` under `sudo`, or as another user, can't reach your
  daemon's socket.
- **The same runtime directory.** The client and the daemon must agree on
  `XDG_RUNTIME_DIR`, which is normally `/run/user/<uid>`. Compare
  `echo $XDG_RUNTIME_DIR` with `ls -l /run/user/$(id -u)/kith/`.
- **A runtime directory that exists and is yours.** Without a login session (cron,
  some ssh setups, containers) `/run/user/<uid>` may not exist. kith never falls
  back to `/tmp`: set `XDG_RUNTIME_DIR` to a directory you own. It refuses a
  `kith/` directory that another user owns or that is a symlink, and makes one
  you own `0700` if it was looser.
- **A daemon is actually running.** A socket left behind by a crashed daemon is
  removed and replaced when the next daemon starts. If there's a socket file but no
  process (`pgrep -a kithd`), start the unit.

### "kith is open in another window: pid 4312 on /dev/pts/3 (laptop), since 10:41"

The daemon serves one window per [profile](configuration.md#profiles), and another one runs in yours (`kith --profile <name>` runs in another). Go to that window (the pid and terminal say where), or run `kith --force` to take over: the other window saves its drafts and closes. A window that crashed or was closed never blocks you: the daemon frees its seat the moment it goes.

### Configuration errors

The client, the daemon and `kith-mcp` all validate the whole config before starting, and
a bad value stops them with a message that names the key. Common ones:

| Message | Fix |
| --- | --- |
| `config: matrix.account[0] (personal): user "…" is no Matrix ID` | Write the Matrix ID whole, as `@you:matrix.org`. |
| `config: matrix.account[0] (personal): homeserver "…" is no URL` | Write the server's address, as `https://matrix.org`. |
| `config: …: unknown keys: notifications.on, …` | The key is misspelled, in the wrong section, or no longer exists. `kith --print-config` shows every valid key. |

Keys kith doesn't know are refused at startup, naming each one. See
[configuration.md](configuration.md#validation).

### The homeserver is down

The daemon starts anyway and serves your cached history:

```text
level=WARN msg="cannot reach the homeserver; serving cached history and connecting when it is back" err=…
```

It keeps retrying, every 30 seconds at most, and logs
`homeserver reachable again; syncing` when it succeeds. Messages you send in the
meantime are queued and sent later. If the homeserver rejects the token rather than
being unreachable, the daemon says so and stops retrying (see above).

Once running, a sync that fails the same way five times in a row (a response kith
cannot read, say) is shown on the account's status line as `sync keeps failing`, with
the error, not only in the log. The daemon keeps retrying; the next sync that succeeds
puts the account back online.

## The cache

### Rebuild the cache

The message cache is a copy of what the homeserver has, so rebuilding it is safe. To
clear it and fetch everything again:

```sh
kith --clear-cache
```

This asks the running daemon to empty the cache and rewind to a full initial sync,
then opens the client. The encryption store isn't touched, so no keys are lost. A
large account can take a while to refill.

Don't delete a `crypto-<instance>-<account>.db`. It isn't a cache: it holds this device's identity and
room keys.

### Old caches with a `.pre-schema-` suffix

When a new version of kith finds a cache written with a schema it doesn't use (older
or newer), it doesn't migrate it. It copies the file aside, rebuilds from scratch and
re-syncs. The copies stay in `~/.local/share/kith/`:

```text
cache-<hash>.db.pre-schema-20260901-101500
```

They're kept because they may contain history the homeserver can no longer serve (for
example, from a bridge that has purged its backfill). They're encrypted with the
cache's key, as the cache is (a copy from before that is encrypted at the next start).
Nothing reads them, and nothing
deletes them automatically, so remove them once you're sure you don't need them. If
the copy couldn't be made, the daemon logs
`the previous cache could not be kept` and starts with an empty cache
anyway.

Switching back and forth between two builds with different schemas rebuilds the
cache on each switch. That's slow, but nothing is corrupted.

### "open the cache …: …"

The daemon couldn't open its cache file (for example, a permissions problem or a full
disk), and it does not run without one: every network writes into it and every client
reads from it. Fix the cause shown in the message. Under systemd the daemon retries
every few seconds by itself; otherwise start it again.

### "the cache is locked: unlock the keyring"

The cache is encrypted with a key kept only in the keyring
([encryption.md](encryption.md#where-the-keys-live)), so while the keyring is locked
(a boot with nothing to ask for its password, say) **no history shows, from any
network**. Every client says so at once: `kith` prints `kithd is waiting: the cache is
locked: unlock the keyring` and opens by itself once the daemon can start (Ctrl-C
quits); one-shot commands such as `kith export` stop with that line instead of waiting;
`kith-notify` shows it once as a desktop notification; and the assistant's tools answer
with it. The daemon is not stuck: it waits, tries again every few seconds (up to every
30), and once the keyring answers opens the cache and then every network, WhatsApp's,
Signal's and Messenger's stores included. Unlock the keyring
(see [Keyring unavailable](#keyring-unavailable)); no restart is needed.

`the cache is locked: the keyring is not answering; unlock it` is the same wait for a
keyring that takes no calls at all: a Secret Service D-Bus is still trying to start,
or one waiting on a password prompt nothing shows. Each try waits 5 seconds for it.

On the first start of a kith that encrypts the cache, the journal says
`encrypting the cache, one time` with its size, then `the cache is encrypted` with how
long it took: about 15 seconds for 500 MB, during which kith waits for the daemon.

### "the keyring's key does not open the cache"

The keyring gives a key, but not the one the cache was encrypted with: the entry
(`<instance>|cache-store`) was replaced or deleted and made again. The daemon waits as
for a locked keyring and never replaces the cache. Put the old entry back if you can,
and it opens by itself. Otherwise move `cache-<instance>.db` aside (or
[restore a backup](usage.md#backing-up-and-moving-to-a-new-machine), which needs your
backup key, not this one); the daemon then starts an empty cache and fills it from what
the networks still hold.

### "Backups are off until you run `kith backup` once"

kithd's daily backups are encrypted to your backup key, and there is none yet: the
first `kith backup`, run in a terminal, makes it and shows you its private half. kithd
never makes one itself, since nobody would see that half and the backups could never be
opened. Run `kith backup` once; the daily ones start within the hour. Keep the key it
shows somewhere other than this machine: without it no backup opens
([your backup key](usage.md#backing-up-and-moving-to-a-new-machine)).

### "The daily backup failed: …" or "is put off: …"

kithd tried today's backup into `[storage] export_dir/backups/` and could not. *Put
off* is a disk without room for it (about the cache's size free beside the cache, and
the cache's and pictures' size in the backups folder); it tries again every hour. A
failure says why: often the keyring is locked (`unlock the keyring`), or the export
folder is one kithd may not write. Under the systemd unit kithd writes only its own
folders, so an `export_dir` outside `~/.local/share/kith` needs a `ReadWritePaths=`
for it in a drop-in. Each is said once, and again only after a backup has worked.

### "the key does not open this backup"

`kith restore` was given a key the backup was not made to: a key from another machine,
or one made again after the first was lost. Each backup opens only with the key that
was current when it was made. A text that is not a key at all (it should start
`AGE-SECRET-KEY-1`) is refused the same way. Nothing was changed. The backup key is not
the keyring's cache key; a restore needs only the backup key. If it is lost, see
[if you lose the key](usage.md#backing-up-and-moving-to-a-new-machine).

## Encryption

### Undecryptable messages

Messages this device holds no key for show as:

```text
🔒 Encrypted message — no decryption key
```

That's normal on a new device: history from before it joined was encrypted for your
other devices.

kith asks for each missing key by itself, once per key: from the key backup when this
device holds the backup key (a verified device shares it after verification), otherwise
from your other devices and the sender's. If the key comes within ten minutes, the
message replaces its placeholder where it is shown. If it never comes, the next time
the message is shown asks again. To give this device every key at once:

| You have | Run |
| --- | --- |
| A server-side key backup and its recovery key or passphrase | `kith --restore-keys` |
| A key export file from another client (for example, Element's "Export E2E room keys") | `kith --import-keys keys.txt` |
| Neither: the account has never set up key backup | `kith --bootstrap-keys`, which starts backing up keys from now on. History this device never had keys for stays unreadable unless another device exports them. |

After a restore or import, reopen the affected rooms. Possible errors:

| Message | Meaning |
| --- | --- |
| `this account has no room-key backup on the server to restore from` | Use `--import-keys` with an export, or set up a backup with `--bootstrap-keys`. |
| `that recovery key or passphrase is not correct` | Check it and try again. |
| `could not read that export: wrong passphrase, or the file is not a Matrix key export` | The import file or its passphrase is wrong. |
| `this account already has secret storage or a key backup` | `--bootstrap-keys` refuses to replace an existing setup. Use `--restore-keys` with the existing recovery key. |
| `encryption is not enabled for this device …` | The daemon started without encryption. See the next section. |

More in [encryption.md](encryption.md).

### "encryption disabled: …" in the daemon log

The daemon couldn't set up end-to-end encryption, and it keeps running for unencrypted
rooms only. The usual cause is the keyring: the key that unlocks the crypto store
lives only there. See [Keyring unavailable](#keyring-unavailable). Restart the daemon
once the keyring works.

`device verification unavailable: …` is a separate, milder warning. Encryption works,
but interactive device verification doesn't.

### "WhatsApp cannot connect: its store's key cannot be had from the keyring"

WhatsApp's store (and Messenger's, logged as "Messenger's encrypted chats are off", and
Signal's, as "Signal cannot connect") is encrypted with a key kept only in the keyring,
so without the keyring it stays closed. The cached chats stay listed and readable; the
account shows as failed and nothing new arrives or can be sent.
See [Keyring unavailable](#keyring-unavailable), then restart the daemon. If the log
says the key in the keyring is corrupt, or `the key does not open this store`, restore
the entry if you can; otherwise delete the entry (`<instance>|whatsapp-store` or
`|messenger-store`) and the store file, and link the account again. Your history stays:
it is in the message cache.

## Keyring unavailable

kith keeps the access token and the crypto store's unlock key in the OS secret
store, never in a file by default.

- **Linux:** a Secret Service provider must be running on the session bus (GNOME
  Keyring, KeePassXC with Secret Service enabled, or KWallet with its Secret Service
  bridge). Check that one is on the bus with
  `busctl --user list | grep org.freedesktop.secrets`.
- **systemd:** the unit gets the bus address from the user manager. If it isn't
  there, import it with
  `systemctl --user import-environment DBUS_SESSION_BUS_ADDRESS`.
- **At login:** if nothing can store the token, `kith login` fails with
  `no OS secret store available; token not persisted (set allow_token_file to persist to a 0600 file)`,
  followed by the reason.

On a headless machine with no keyring, `allow_token_file = true` stores the token in
`~/.local/state/kith/session-<instance>-<account>.toml` (mode 0600), one file per account. A
`session.toml` from an older version still loads for the account it belongs to and is
renamed to the new name. End-to-end encryption still needs a
keyring, so without one the daemon runs with encryption disabled.

## Notifications don't show

Work through these in order:

1. **Are they on?** Notifications ship with `[notifications] enabled = false`. Turn them
   on in the file or from the settings screen (`,`).
2. **Does a rule let the message through?** By default only mentions notify. Press `W`
   in the room to see every rule in force there and which one decided. See
   [notifications.md](notifications.md).
3. **Is do-not-disturb on?** `ctrl+n` hides notifications and `alt+n` mutes their
   sound. Either key turns do-not-disturb off again. Restarting the daemon also clears
   it.
4. **Is `kith-notify` running, and listening to the room's profile?** kithd only
   decides; `kith-notify` shows. `systemctl --user status kith-notify` (or
   `pgrep -a kith-notify`) says whether it runs and with which `--profile`s. A room
   only a profile it doesn't listen to holds is never shown. Its log says when it
   cannot reach kithd: `journalctl --user -u kith-notify`.
5. **Can `kith-notify` reach the desktop?** Desktop popups go over D-Bus to
   `org.freedesktop.Notifications`, and a failed delivery is only logged. Check
   that the unit has the bus address (`systemctl --user show-environment`), and that
   `notify-send test` works from a terminal. Import the variable and restart
   `kith-notify` if it's missing.
6. **Is it the catch-up batch?** Messages that arrived while the daemon was down are
   cached as history but not announced. Only messages the daemon receives live
   notify, or that it fetches into a Matrix room after losing its connection while
   running (those older than ten minutes as one count per room). Your own messages
   never notify.
7. **Is the room rate-limited?** With `max_per_room` set, a busy room turns its
   notifications into one summary popup per `rate_window`.

Without a freedesktop notification daemon (macOS, for example), set
`[notifications] command` to a script instead. It receives the text in environment
variables such as `$KITH_TITLE` and `$KITH_BODY`.

## Images don't render

- **By default an image is a text chip.** `[display.media] mode = "placeholder"` shows
  the file name, size and dimensions. Press the view key to open the picture in an
  image viewer. Set `mode = "inline"` to draw images in the timeline.
- **Inline images need truecolor.** They are drawn from block characters colored in
  24-bit color. If they look banded or wrong, check that your terminal (and tmux, if
  you use it) supports truecolor.
- **Empty boxes instead of pictures** mean your font lacks the Unicode 13 "sextant"
  block characters. Set `[display.media] detail = "half"`, which uses half-blocks that
  every font has.
- **No viewer opens.** kith looks for `nsxiv`, `sxiv`, `imv`, `feh`, `eog`, `gthumb`,
  `qimgv`, `loupe` and `ristretto`, in that order. Install one, or set
  `[display.media] viewer`.
- **Only some rooms show pictures.** Per-room `[[display.media.rule]]` entries can turn
  fetching (`auto`) or caching off for a place.

## Emoji break the layout

When a row with an emoji overflows its pane, or leaves stray characters on screen, the
terminal and kith disagree about how many columns that emoji takes. Measure it:

```sh
go run -tags goolm ./cmd/emoji-probe            # from a clone of the repository
go run -tags goolm ./cmd/emoji-probe -text '👍🏽 the line that misdraws'
```

`emoji-probe` prints each glyph whose width in *your* terminal differs from what
kith expects. See [cli.md](cli.md#emoji-probe). Then:

- Stay on, or go back to, `[display.emoji] set = "curated"`. The `complete` set adds
  composed emoji (families, professions, flags) that draw correctly only when the font
  has the ligature. Without it, they come out wider than their cell.
- Try a different emoji font, or update your terminal. Width tables differ between
  terminals and versions.
- Press `ctrl+l` to redraw the screen after a glitch.

## Copying doesn't work

- `c` copies with the OSC 52 escape, which sets the clipboard of the terminal you are
  looking at, even over SSH. Inside tmux, add `set -g set-clipboard on`. Some
  terminals turn OSC 52 off or cap its size. If yours does, set
  `[clipboard] command = "wl-copy"` (or `xclip -selection clipboard`, `pbcopy`).
- `[clipboard] auto_copy` runs in the daemon, which has no terminal, so it needs
  `command`, a graphical session (`WAYLAND_DISPLAY` or `DISPLAY` in the unit's
  environment) and a `[codes] include` list. If one is missing, the daemon logs
  `auto-copy is on but cannot run: …` at startup.

## Spell-checking does nothing

kith runs `hunspell` (or the engine in `[spell] command`) as a subprocess, and the
engine needs dictionaries, which are separate packages. When it has nothing to check
with, it says so. Install a dictionary through your distribution, or let kith fetch
one:

```sh
kith --add-dictionary list
kith --add-dictionary en_US
```

## The local model runs on the CPU only, or will not start

When the daemon runs as the packaged systemd unit (`packaging/systemd/kithd.service`), its sandbox also covers the `llama-server` it starts for [assist](assist.md). `PrivateDevices=true` hides `/dev/dri` and `/dev/nvidia*`, so the model cannot reach a GPU. `MemoryDenyWriteExecute=true` stops code that is compiled while it runs (a CUDA JIT, for one). A GPU build of `llama-server` then falls back to the CPU, or exits.

To give it the GPU, override those two lines for your user, then restart the daemon:

```sh
systemctl --user edit kithd.service
# [Service]
# PrivateDevices=false
# MemoryDenyWriteExecute=false
systemctl --user restart kithd.service
```

This weakens the daemon's sandbox as well: it holds your keys. Keep the defaults if the CPU is fast enough.

## Still stuck

Collect the version (`kith --version`, `kithd --version`) and the relevant log
lines, remove anything personal, and open an issue. For anything that looks like a
security problem, follow [SECURITY.md](../SECURITY.md) instead.
