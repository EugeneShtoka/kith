# Command-line reference

This page lists every flag and subcommand of `kith`, `kithd` and `kith-mcp`, the
environment variables they read, and the Makefile targets you use to install and run
them.

Flags follow Go's conventions: `-flag` and `--flag` both work, and a value can be
given as `--flag value` or `--flag=value`. `--help` prints the flag list for any of
the binaries and exits.

## kith

```text
kith [flags]
kith login [--config path] [--profile name]
kith login whatsapp [--config path] [--profile name] [account]
kith login slack [--config path] [--profile name] [account]
kith login telegram [--config path] [--profile name] [account]
```

With no flags, `kith` loads the config, attaches to the daemon (starting it if
needed) and opens the interface over whatever is logged in. A network account that is
not logged in is named on the status line, with the command that logs it in.

### kith login

```sh
kith login [--profile name]
```

Logs in to Matrix with your password. kith reads the password, starts the daemon if
it isn't running, and hands the password to it over its socket. The daemon logs in,
stores the session in the OS keyring and starts Matrix on it at once.

- The password is read from the terminal with echo off. It is never shown, never
  written to disk, and never on a command line. Standard input must be a terminal.
- `homeserver` and `user` must be set in the config, or in the chosen `[[profile]]`.
  The daemon logs in as the account its own config names. If it was started before
  the config had one, restart it first.
- If the daemon already runs Matrix on a session, the new one is saved and used from
  the daemon's next start; the running session is never replaced.
- Each login creates a new Matrix device. Logging in again on the same machine
  replaces the stored session with a new device, and that device needs its room keys
  restored before it can read old encrypted history (see
  [encryption.md](encryption.md)).

| Flag | Meaning |
| --- | --- |
| `--config path` | Config file to read. |
| `--profile name` | Which `[[profile]]` account to log in. The default is the first one. |

### kith login whatsapp

```sh
kith login whatsapp [account]
```

Links one `[[whatsapp.account]]` to kith as one of that phone's linked devices. The
daemon owns the WhatsApp store, so it does the pairing: this command asks it, prints
the code WhatsApp gives, and waits (up to ten minutes) while you type the code on the
phone under Settings → Linked devices → Link a device → "Link with phone number
instead". Once the phone accepts, the account's groups appear in kith.

- The account must be in the config (`:login whatsapp` in kith writes it).
- An account added to the config needs no restart: this command has kithd re-read
  the config first. Removing one disconnects it on the next re-read; its chats stay
  in kith, readable.
- `account` is the `name` of a `[[whatsapp.account]]`; it can be left out when there
  is only one.
- An account already linked is refused: unlink kith on the phone first.
- A code typed on another number's phone is refused, and that link removed.

### kith login slack

```sh
kith login slack [account]
```

Signs one `[[slack.account]]` in. kith uses the session a browser signed in to the
workspace holds: the command says where to copy its token (a line to paste into the
browser console) and its `d` cookie (the browser's cookie storage), reads both without
echoing them, and has the daemon check them with Slack, keep them in the system keyring
and connect. The workspace's channels then appear in kith. See [Slack](slack.md).

- The account must be in the config (`:login slack` in kith writes it).
- An account added to the config needs no restart: this command has kithd re-read the
  config first.
- `account` is the `name` of a `[[slack.account]]`; it can be left out when there is
  only one.
- A session for another workspace than the account's `workspace` is refused.
- Signing in again replaces the session kept for the account.

### kith login telegram

```sh
kith login telegram [account]
```

Logs one `[[telegram.account]]` in. It asks first for the app to log in through: your
own app's `api_id` and `api_hash` (from my.telegram.org → API development tools; the
hash is read without echoing), or nothing, for kith's own app when the build carries
one. kithd then has Telegram send a code, to the Telegram app where the account is
logged in or by SMS; the command reads it, and the account's two-step verification
password when it has one. A wrong code or password is asked for again. kithd keeps
the session in the system keyring and connects. See [Telegram](telegram.md).

- The account must be in the config (`:login telegram` in kith writes it).
- `account` is the `name` of a `[[telegram.account]]`; it can be left out when there
  is only one.
- Logging in again replaces the session kept for the account; one begun while another
  is under way ends the first.

### General flags

| Flag | Meaning |
| --- | --- |
| `--config path` | Config file to read. Default: `$XDG_CONFIG_HOME/kith/config.toml`. If it doesn't exist, an annotated default is written there and kith exits. |
| `--profile name` | Which `[[profile]]` account to run as. The default is the first one. It also chooses which daemon to attach to or start (`kithd@<name>.service`). |
| `--version` | Print the version, commit and build date, then exit. |
| `--print-config` | Print the fully documented default configuration, then exit. It needs no config or session. |
| `--log-level level` | How much to log: `debug`, `info`, `warn` or `error`. Overrides `KITH_LOG_LEVEL` and `[log] level`. See [troubleshooting.md](troubleshooting.md#logs). |
| `--log-target target` | Where to log: `journal` (tagged `kith`, or `kith-<profile>`), `file` (`$XDG_STATE_HOME/kith/kith.log`), or `auto`, the journal when there is one. Overrides `KITH_LOG_TARGET` and `[log] target`. Never the terminal. See [troubleshooting.md](troubleshooting.md#where-the-log-goes). |

### Maintenance flags

These run against the daemon before the interface opens. They can be combined, and
run in this order: `--clear-cache`, `--bootstrap-keys`, `--restore-keys`,
`--export-keys`, `--import-keys`. `--bootstrap-keys` and `--export-keys` exit when
they finish. The others continue into the client, so you see the result right away.
Every secret is read from the terminal with echo off and passed to the daemon over its
0600 socket; none of them is ever taken on the command line.

| Flag | Meaning |
| --- | --- |
| `--clear-cache` | Ask the daemon to empty the local message cache and rewind to a full initial sync. The encryption store is untouched. See [troubleshooting.md](troubleshooting.md#rebuild-the-cache). |
| `--restore-keys` | Prompt for the account's recovery key or passphrase and import room keys from the server-side key backup. |
| `--bootstrap-keys` | Set up cross-signing, secret storage and a key backup for an account that has none. It prompts for the account password, prints the recovery key once, and exits. It refuses to run if the account already has secret storage or a backup. |
| `--export-keys file` | Write this device's room keys to `file`, encrypted with a passphrase you enter twice, and exit. The file is created with mode 0600 and never overwrites an existing one. |
| `--import-keys file` | Add the room keys from a Matrix key export (for example, one from Element), after prompting for its passphrase. It reports how many keys were new out of how many were in the file. |

Details and workflows: [encryption.md](encryption.md).

### Local flags

These do one local job and exit. They need no session, no daemon and no network
beyond the download they perform.

| Flag | Meaning |
| --- | --- |
| `--add-dictionary tag` | Install a spelling dictionary, such as `en_US` or `he_IL`, into `~/.local/share/kith/hunspell/`. `list` shows what is available, with sizes and what is already installed. |
| `--add-frequencies tag` | Install word-frequency counts for a language, by dictionary tag. The `[spell] flag_rare_words` check reads them. `list` shows what is available. |
| `--add-model tag` | Install the local completion model's weights, checked against a pinned SHA-256. `[complete.model]` reads them. `list` shows what is available. |
| `--set-model-key ref` | Store an API key for the `[assist]` endpoint in the OS keyring under `ref`, the name `[assist] key_ref` refers to. It reads from the terminal, or from standard input when that isn't a terminal. An empty answer removes the key. |
| `--agent-log` | Print everything an assistant has written on your behalf through `kith-mcp` (sent, queued, drafted, or refused by `[agent.write]`/`[agent.read]`), oldest first, and exit. It reads the config only to find the account. |

See [composer.md](composer.md) for spelling and completion, [assist.md](assist.md)
for the model endpoint, and [mcp.md](mcp.md) for the assistant ledger.

### Link flags

| Flag | Meaning |
| --- | --- |
| `--open uri` | Hand a `matrix:` or `https://matrix.to/#/…` link to the `kith` already attached to the daemon, and exit. If no client is attached, it starts one in a terminal (see `terminal` in the config). This is what the desktop's `matrix:` handler runs. Anything that isn't a Matrix link is refused. |
| `--force` | Take over from an `kith` already open on this account. The daemon serves one window at a time: without `--force` a second one is refused, and says where the first one runs (its process, terminal and start time). With it, the first window saves its drafts and closes, printing `opened in another window`. One that does not answer within a few seconds loses the seat anyway: its later draft writes are refused, and it closes when it next hears from the daemon. |
| `--follow uri` | Start the client and go to this link once it has loaded. `--open` uses it when it has to open a terminal. You rarely need it yourself. |

### Exit status

| Status | When |
| --- | --- |
| `0` | Normal exit, including `--help`, and a first run that only wrote the default config. |
| `1` | An error. The message is printed to stderr prefixed with `kith:`. For example: the daemon can't be started or never becomes ready, or the config is invalid. |
| `2` | A flag kith doesn't know, or a flag missing its value. |

When the daemon isn't running, `kith` starts it with
`systemctl --user start kithd.service` (or `kithd@<profile>.service`). For a config
other than the default one, it first writes that config's own unit,
`~/.config/systemd/user/kithd-<instance>.service`: `kithd --config path`, under the
same sandbox as `kithd.service`, allowed to write only the directories its
`[storage]` names. kith rewrites the unit when those change; `systemctl --user enable
kithd-<instance>` starts it at login. If systemd can't start the daemon, or the
config's files are somewhere a unit can't name (a path with whitespace, quotes, a
backslash or `$`, or under `/tmp`, which the sandbox hides), kith falls back to
`setsid kithd`, then waits up to 60 seconds for the daemon's first sync.

## kithd

```text
kithd [--config path] [--profile name] [--log-level level] [-v] [--log-target target] [--log-file path] [--version]
```

The daemon. It runs in the foreground and logs to stderr, which the journal captures
under systemd. Started by `kith` without systemd, it logs to the journal itself,
or to `--log-file` when there is none. It runs logged in to nothing too: a network
with no session waits for `kith login` (or `kith login whatsapp`, `kith login slack`, `kith login telegram`), which logs it in
through the daemon and starts it without a restart.

| Flag | Meaning |
| --- | --- |
| `--config path` | Config file to read. The daemon re-reads this same path when a client asks it to reload. A config other than the default one gets its own daemon: its files are wherever its `[storage]` says, named by its instance, and kith starts that daemon through a systemd unit it writes for that config (`kithd-<instance>.service`, see above). |
| `--profile name` | Which `[[profile]]` account to serve. The default is the first one. |
| `--log-level level` | How much to log: `debug`, `info`, `warn` or `error`. Overrides `KITH_LOG_LEVEL` and `[log] level`. |
| `-v` | Short for `--log-level debug`. |
| `--log-target target` | Where to log when not a systemd unit: `journal` (tagged `kithd`, or `kithd-<profile>`), `file` (the `--log-file`), or `auto`, the journal when there is one and the `--log-file` otherwise. Overrides `KITH_LOG_TARGET` and `[log] target`. A unit always logs to its stderr, which is the journal. |
| `--log-file path` | Log to this file (capped at 1 MB, one old copy kept) instead of stderr when there is no journal, or always with `--log-target file`. `kith` passes it when it starts the daemon itself, so the daemon never writes over the interface. |
| `--version` | Print version information and exit. |

Behavior worth knowing:

- **One daemon per account.** The daemon takes an exclusive lock before it opens any
  store. If another `kithd` already serves the account, the new one **exits 0
  without printing anything**. When two clients race to start a daemon, whichever
  loses has nothing to report, because someone else got there first.
- **An unreachable homeserver doesn't stop it.** It serves cached history, logs
  `cannot reach the homeserver; serving cached history and connecting when it is back`,
  and retries with a backoff capped at 30 seconds.
- **Neither does a rejected session.** An invalid or revoked token leaves Matrix logged
  out, with the reason in the daemon's status and log, and the daemon waits for
  `kith login`. (A token rejected later, after the homeserver was first unreachable,
  needs a restart after the login.)
- **Optional parts fail soft.** Encryption that can't be enabled (for example, with no
  keyring) or scheduled messages that can't be set up each log one `level=ERROR` line,
  and the daemon carries on without that part. The cache is not optional: one that
  can't be opened stops the daemon, naming the file and why.
- **It shuts down cleanly on `SIGINT` and `SIGTERM`.** It stops accepting clients,
  waits for its workers, and removes its socket.

| Status | When |
| --- | --- |
| `0` | Clean shutdown, or another daemon already holds the lock. |
| `1` | A fatal error: no config, an invalid config (one naming no network at all), no network that could be set up, a cache or a socket that can't be opened. |

## kith-mcp

```text
kith-mcp [--config path] [--profile name] [--log-level level] [--version]
```

An MCP server over stdio: newline-delimited JSON-RPC on stdin and stdout, with logs on
stderr. An MCP client, the assistant's host application, starts it and talks to it.
You don't normally run it by hand. It attaches to the running daemon's socket and
doesn't start one itself, so keep `kithd` running.

| Flag | Meaning |
| --- | --- |
| `--config path` | Config file to read. `[agent.read]` and `[agent.write]` decide what may be read and written. |
| `--profile name` | Which `[[profile]]` account to serve. The default is the first one. |
| `--log-level level` | How much to log to stderr: `debug`, `info`, `warn` or `error`. Overrides `KITH_LOG_LEVEL` and `[log] level`. |
| `--version` | Print version information and exit. |

It exits 0 when the client closes stdin, and 1 on a startup error, such as an
invalid config or no account set. Tools, scope and the write policy are covered in
[mcp.md](mcp.md).

## emoji-probe

A diagnostic that ships in the repository but not in release archives. It checks that
every emoji kith offers takes up as many columns in *your* terminal as kith
expects. Run it from a clone:

```sh
go run -tags goolm ./cmd/emoji-probe                       # the configured set and tone
go run -tags goolm ./cmd/emoji-probe -set complete -tone light
go run -tags goolm ./cmd/emoji-probe -text 'a line that leaves residue'
```

| Flag | Meaning |
| --- | --- |
| `-set name` | `curated`, `standard` or `complete`. The default is the config's `[display.emoji] set`. |
| `-tone name` | `none`, `light`, `medium-light`, `medium`, `medium-dark` or `dark`. The default is the config's. |
| `-text string` | Measure this text instead of the emoji set. `-` reads standard input. |

It needs a real terminal, prints one line for each glyph whose width disagrees, and
exits non-zero if there are any. See
[troubleshooting.md](troubleshooting.md#emoji-break-the-layout).

## Environment variables

| Variable | Effect |
| --- | --- |
| `XDG_CONFIG_HOME` | Where `kith/config.toml` is found. |
| `XDG_DATA_HOME` | Cache, crypto store, dictionaries and models (`kith/` under it). |
| `XDG_STATE_HOME` | Scheduled-message queue, assistant ledger, opt-in token file. |
| `XDG_CACHE_HOME` | Media cache (`kith/media/`), unless `[display.media] cache_dir` is set. |
| `XDG_RUNTIME_DIR` | The daemon's socket and lock (`kith/<hash>.sock`). The client and the daemon must agree on it. |
| `XDG_DOWNLOAD_DIR` | Where `s` saves attachments when `[display.media] download_dir` is empty. The fallback is `~/Downloads`. |
| `DBUS_SESSION_BUS_ADDRESS` | The session bus, used for the keyring (Secret Service), desktop notifications and the file-chooser portal. |
| `WAYLAND_DISPLAY`, `DISPLAY` | Needed by the daemon's clipboard command for `[clipboard] auto_copy`. |
| `VISUAL`, `EDITOR` | The external editor when `[composer] editor` is empty, tried in that order, with `vi` as the last resort. |
| `DICPATH` | Extra directories searched for hunspell dictionaries. |

The systemd units forward `WAYLAND_DISPLAY`, `DISPLAY` and `DBUS_SESSION_BUS_ADDRESS`
from the user manager, which only has them if something imported them. `make deploy`
imports them for you.

## Makefile targets

These are the targets you need to install and run kith from a clone. Development
targets (`test`, `lint`, `check`, …) are covered in
[CONTRIBUTING.md](../CONTRIBUTING.md).

| Target | What it does |
| --- | --- |
| `make build` | Build `./kith`, `./kithd` and `./kith-mcp` in the repository root. |
| `make install` | Build, then install the binaries to `$(BINDIR)`, both systemd user units (`kithd.service` and the per-profile `kithd@.service`) to `~/.config/systemd/user/`, and the `matrix:` handler to `~/.local/share/applications/`. It starts nothing. |
| `make deploy` | `install`, then import the desktop environment variables, enable and restart the daemon's unit, and wait for its socket. With `PROFILE=<name>`, the unit is `kithd@<name>.service`. |
| `make undeploy` | Stop and disable `kithd.service` and every `kithd@<profile>` instance, remove both units and every `kithd-<instance>.service` kith wrote for a config of its own, the `kith`, `kithd` and `kith-mcp` binaries, the desktop entry, and the `matrix:` handler association if it still names kith. Config, cache, crypto store and keyring are left alone. `PROFILE` is ignored: every profile's daemon goes. |
| `make daemon-status` | `systemctl --user status` for the unit `PROFILE` selects. |
| `make daemon-logs` | Print that unit's last 100 journal lines. |
| `make daemon-restart` | Restart that unit, for example after an upgrade. |
| `make daemon-stop` | Stop that unit. |
| `make run` | Run the client from source (`go run`). |
| `make run-daemon` | Run the daemon from source in the foreground. Stop the unit first, or it exits at once because the lock is held. |

| Variable | Default | Meaning |
| --- | --- | --- |
| `PREFIX` | `~/.local` | Install prefix. |
| `BINDIR` | `$(PREFIX)/bin` | Where the binaries go. The units' `ExecStart` and the desktop entry's `Exec` are rewritten to match. `sudo` is used only if this directory isn't writable. |
| `UNITDIR` | `~/.config/systemd/user` | Where the units go. |
| `APPDIR` | `~/.local/share/applications` | Where the desktop entry goes. |
| `PROFILE` | empty | Which daemon `deploy` and the daemon-* targets act on: empty is `kithd.service`, a name is `kithd@<name>.service`. Letters, digits, `.`, `-` and `_` only, like a profile name. |
| `LAUNCHAGENTDIR` | `~/Library/LaunchAgents` | macOS only: where `deploy` writes the LaunchAgent. |

For example, a system-wide copy of the binaries, and a second account's daemon:

```sh
make deploy PREFIX=/usr/local
make deploy PROFILE=work
```

A profile's socket is named after a hash of its Matrix ID, which the Makefile can't
read from the config. So `deploy` waits for whichever socket appears after the
restart, which is the new daemon's.

Without `systemctl`, `install` and `deploy` skip the systemd steps and print what to
do instead. On macOS, `deploy` renders `packaging/macos/io.github.eugeneshtoka.kithd.plist`
into `LAUNCHAGENTDIR` (as `io.github.eugeneshtoka.kithd.<name>` with `PROFILE=`),
loads it with `launchctl bootstrap`, and logs to `~/Library/Logs/kithd.log`
(`kithd.<name>.log` with `PROFILE=`).
`undeploy` unloads and removes every such agent. The daemon-* targets are systemd-only.
