# Getting started

This guide takes you from nothing to a running kith: install the binaries, log in,
run the daemon, and open the client for the first time.

kith is three programs that work together:

| Binary | What it does |
| --- | --- |
| `kithd` | The daemon. It owns the Matrix session, the sync loop, the local cache, the end-to-end encryption store and desktop notifications. It keeps running when no terminal is open. |
| `kith` | The terminal client. It holds no session of its own and attaches to the daemon over a unix socket. It also provides `kith login` and the other one-shot commands. |
| `kith-mcp` | An optional [Model Context Protocol](https://modelcontextprotocol.io) server that lets an AI assistant read (and, under your policy, write to) the account through the same daemon. See [mcp.md](mcp.md). |

The client has no built-in fallback: if the daemon cannot start, `kith` does not
run. This is on purpose. Two processes sharing one device's encryption state would
corrupt it, so exactly one process, the daemon, owns it.

## Requirements

### Required

- **Linux, macOS or FreeBSD**, on amd64 or arm64. Those are the platforms release
  builds are published for. Linux gets the most: systemd units, desktop notifications
  and the file chooser are Linux (D-Bus) features.
- **Go 1.26.3 or newer**, only if you build from source. `go.mod` pins toolchain
  1.26.9, and with the default `GOTOOLCHAIN=auto` the go command downloads it for you.
- **A terminal with truecolor support.** Any modern terminal emulator works.
- **An OS secret store**, to hold the access token and the key that encrypts the
  crypto store at rest. On Linux that means a Secret Service provider (GNOME Keyring,
  KeePassXC, KWallet with its Secret Service bridge) on the D-Bus session bus. On
  macOS it is the Keychain. A headless machine can do without one, with trade-offs;
  see [Where files live](#where-files-live).

The binaries are pure Go, built with `CGO_ENABLED=0`, so you don't need libolm,
SQLite or any other C library.

### Optional

Nothing below is needed to chat. Each one turns on a feature, and kith tells you
when a feature has nothing to run with instead of failing quietly.

| Dependency | Used for |
| --- | --- |
| systemd (user instance) | Keeping `kithd` running and starting it at login. Without systemd, `kith` starts the daemon itself with `setsid`. |
| A freedesktop notification daemon (dunst, mako, GNOME Shell, KDE Plasma, …) | Desktop notifications, sent over D-Bus. You can also use `[notifications] command` to run your own script instead. |
| `mpv` or VLC | Playing voice notes (`[display.media.audio] player`). |
| An image viewer (`nsxiv`, `sxiv`, `imv`, `feh`, `eog`, `gthumb`, `qimgv`, `loupe`, `ristretto`) | Showing pictures at full size. kith uses the first one it finds unless `[display.media] viewer` names one. |
| `xdg-open` | Opening links (`[clipboard] open_command`) and videos. |
| The XDG desktop portal | The file chooser for attachments. Without it you can still type a path. |
| `wl-copy` or `xclip` | Only for `[clipboard] command`: needed when your terminal lacks OSC 52 clipboard support, and for the daemon's unattended `auto_copy`. |
| `hunspell` with dictionaries | Spell-checking. `kith --add-dictionary list` can install dictionaries for you. |
| `llama-server` from llama.cpp | Local word completion. `kith --add-model list` installs the weights. See [composer.md](composer.md). |
| `setsid` | Starting the daemon on demand when systemd is not available. It comes with util-linux on Linux. macOS doesn't ship it. |

## Install

Pick the line for your system. Every option installs the same three binaries.

| System | Install |
| --- | --- |
| Arch Linux | `yay -S kith-bin` (prebuilt), `kith` (built from the release source) or `kith-git` (latest `main`) |
| Debian, Ubuntu | the `.deb` from the release page |
| Fedora, RHEL, openSUSE | the `.rpm` from the release page |
| Alpine | the `.apk` from the release page |
| macOS | `brew install --cask eugeneshtoka/tap/kith` |
| Nix, NixOS | `nix profile install github:EugeneShtoka/kith`, or the flake's modules |
| FreeBSD | the `freebsd` tarball from the release page |
| Anything else | [a release archive](#from-a-release-archive) or [from source](#from-source) |

Release assets are on the
[releases page](https://github.com/EugeneShtoka/kith/releases). How to check a
download before you run it is under [Verify a download](#verify-a-download).

### Linux packages

The `.deb`, `.rpm`, `.apk` and Arch packages all install the same files:

| Path | What |
| --- | --- |
| `/usr/bin/kith`, `kithd`, `kith-mcp` | The binaries. |
| `/usr/lib/systemd/user/kithd.service`, `kithd@.service` | The systemd user units, with `ExecStart=/usr/bin/kithd`. |
| `/usr/share/applications/kith.desktop` | The `matrix:` link handler. |
| `/usr/share/doc/kith/` | README, ARCHITECTURE, these docs and `config.toml.example`, the annotated default config. |

```sh
sudo apt install ./kith_*_amd64.deb                # Debian, Ubuntu
sudo dnf install ./kith-*.x86_64.rpm               # Fedora, RHEL
sudo zypper install ./kith-*.x86_64.rpm            # openSUSE
sudo apk add --allow-untrusted ./kith_*_x86_64.apk # Alpine (the package is unsigned)
sudo pacman -U ./kith-*-x86_64.pkg.tar.zst         # Arch, without the AUR
```

The packages have no hard dependencies, because the binaries are static. The `.deb`
and `.rpm` recommend the [optional](#optional) tools each feature uses, so `apt` and
`dnf` pull in what your system is missing. The Arch packages list them as optional
dependencies. Alpine has no weak dependencies, so install what you want yourself.

A package can't start a service in your session, so nothing is enabled for you. The
package prints the next steps; they are [First login](#first-login), then:

```sh
systemctl --user daemon-reload
systemctl --user import-environment WAYLAND_DISPLAY DISPLAY DBUS_SESSION_BUS_ADDRESS
systemctl --user enable --now kithd
```

After an upgrade, restart the daemon so the new binary takes over:
`systemctl --user restart kithd`.

### Arch Linux (AUR)

Three AUR packages exist. Install one of them; they conflict with each other.

| Package | Builds |
| --- | --- |
| `kith-bin` | Nothing. It repackages the release's prebuilt binaries. |
| `kith` | The tagged release, from its source tarball. Needs `go`. |
| `kith-git` | The latest commit on `main`. Needs `go` and `git`. |

```sh
yay -S kith-bin     # or paru, or: git clone https://aur.archlinux.org/kith-bin.git && cd kith-bin && makepkg -si
```

They install the same files as the other [Linux packages](#linux-packages).

### macOS (Homebrew)

```sh
brew install --cask eugeneshtoka/tap/kith
```

The cask installs the binaries. `brew services` works only with formulae, so it can't
run the daemon, and macOS has no systemd and no `setsid` for `kith` to start it with.
Load the LaunchAgent that ships in the cask once, after
[logging in](#first-login):

```sh
mkdir -p ~/Library/LaunchAgents
cp "$(brew --caskroom)"/kith/*/packaging/macos/io.github.eugeneshtoka.kithd.plist ~/Library/LaunchAgents/
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/io.github.eugeneshtoka.kithd.plist
```

The agent starts `kithd` at login and restarts it if it fails. Its log is
`~/Library/Logs/kithd.log`. After a `brew upgrade`, restart it with
`launchctl kickstart -k gui/$(id -u)/io.github.eugeneshtoka.kithd`.
`brew uninstall` unloads it.

What doesn't work on macOS yet:

- **Desktop notifications.** They are sent over D-Bus, which macOS doesn't have. Use
  `[notifications] command` to run your own script instead.
- **The file chooser.** It is the XDG desktop portal. Type the path instead.
- **The `matrix:` link handler.** It is a `.desktop` file.
- **Clipboard from the daemon.** Set `[clipboard] command = "pbcopy"`.

The keyring (the macOS Keychain), sync, encryption and everything in the terminal
work as on Linux.

### Nix

The repository is a flake:

```sh
nix run github:EugeneShtoka/kith                 # try it
nix profile install github:EugeneShtoka/kith     # install it
```

The package puts the systemd units in `share/systemd/user`, with `ExecStart` pointing
into the store. The flake also has two modules that wire the daemon up:

```nix
# NixOS (system-wide). The daemon is off by default: it would start in every user's session.
imports = [ inputs.kith.nixosModules.default ];
programs.kith.enable = true;
programs.kith.daemon.enable = true;      # optional

# home-manager (one user). Starts kithd in your session; launchd on macOS.
imports = [ inputs.kith.homeManagerModules.default ];
programs.kith.enable = true;
programs.kith.profiles = [ "work" ];     # optional: kithd@work instead of kithd
```

With home-manager on Linux, run `systemctl --user daemon-reload` after the first
switch.

### FreeBSD

Download `kith_<version>_freebsd_<arch>.tar.gz` and put the binaries on your
`PATH`:

```sh
tar xzf kith_*_freebsd_amd64.tar.gz
install -m755 kith kithd kith-mcp ~/.local/bin/
```

FreeBSD has neither systemd nor `setsid(1)`, so nothing starts the daemon for you.
Start `kithd` before the client, for example from your session's autostart, in a
`tmux` window, or with `daemon -r kithd`.

The FreeBSD build has **no keyring**. The keyring library kith uses reaches the
Secret Service on FreeBSD only in a cgo build, and release builds are cgo-free. So
set `allow_token_file = true` before `kith login` (the token goes in a 0600 file,
see [Where files live](#where-files-live)), and expect the daemon to run with
end-to-end encryption disabled, because the key that protects the crypto store has
nowhere to live. Encrypted rooms show as undecryptable. Desktop notifications and the
file chooser work if a D-Bus session bus with those services is running.

### From a release archive

Every release publishes one `tar.gz` per platform, named
`kith_<version>_<os>_<arch>.tar.gz`, for Linux, macOS and FreeBSD on amd64 and
arm64. It contains all three binaries, the two systemd units, the macOS LaunchAgent,
the `matrix:` desktop entry, `README.md`, `LICENSE` and `ARCHITECTURE.md`.

```sh
tar xzf kith_*_linux_amd64.tar.gz
install -Dm755 kith kithd kith-mcp -t ~/.local/bin/
```

Put the binaries in `~/.local/bin` unless you have a reason not to. The shipped
systemd units expect the daemon at `~/.local/bin/kithd`, and `kith` finds the
daemon through your `PATH` when it has to start one.

### Verify a download

Every archive and Linux package has a build attestation, and the checksum file is
signed:

```sh
gh attestation verify kith_*_linux_amd64.tar.gz --repo EugeneShtoka/kith
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/EugeneShtoka/kith/\.github/workflows/release\.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --check --ignore-missing checksums.txt
```

Each archive also ships with an SBOM (`*.sbom.json`) that lists every dependency
built into it.

### From source

Clone the repository and use the Makefile:

```sh
git clone https://github.com/EugeneShtoka/kith.git
cd kith
make install     # build, then install binaries, the systemd units and the matrix: handler
```

`make install` does four things:

1. Builds `kith`, `kithd` and `kith-mcp`.
2. Copies them to `~/.local/bin` (override this with `PREFIX=` or `BINDIR=`; it uses
   `sudo` only if the target directory isn't writable).
3. Installs `kithd.service` and the per-profile `kithd@.service` into
   `~/.config/systemd/user/`, with `ExecStart` set to wherever the binary went, and
   runs `systemctl --user daemon-reload`.
4. Installs `kith.desktop` into `~/.local/share/applications/` and registers it as
   the `matrix:` link handler with `xdg-mime`.

`make install` doesn't start anything. `make deploy` does everything `make install`
does and then brings the daemon up (see
[Run the daemon under systemd](#run-the-daemon-under-systemd)). Without `systemctl`
both targets skip the systemd steps and say so. On macOS, `make deploy` installs and
loads the LaunchAgent instead, and skips the desktop entry.

To build without the Makefile, you must pass the `goolm` build tag. That tag selects
the pure-Go encryption backend. Without it, the build needs libolm and cgo, and it
fails under `CGO_ENABLED=0`:

```sh
CGO_ENABLED=0 go build -tags goolm -o kith ./cmd/kith
CGO_ENABLED=0 go build -tags goolm -o kithd ./cmd/kithd
CGO_ENABLED=0 go build -tags goolm -o kith-mcp ./cmd/kith-mcp
```

`go install github.com/EugeneShtoka/kith/cmd/...@latest` doesn't work. The module's
`go.mod` contains a `replace` directive, and the go command refuses to install such a
module by version. Build from a clone instead.

## First login

### 1. Create the config

Run `kith`. Because no config exists yet, it writes a fully documented default file
and starts with no account:

```text
$ kith
kith: wrote a default config to /home/alice/.config/kith/config.toml; set an account up inside kith with :login.
```

Inside it, `:login` sets an account up and signs it in — Matrix, WhatsApp, Slack or
Telegram —
asking for what each needs and explaining where to find it. The rest of this section
is the same done by hand.

Open the file and fill in the two fields at the top for Matrix (to use kith only for
WhatsApp, leave them empty and enable `[whatsapp]` instead):

```toml
homeserver = "https://matrix.example.org"
user       = "@alice:example.org"
```

`homeserver` is the URL of the client-server API itself. kith doesn't look up
`.well-known`, so if your Matrix ID is `@alice:example.org` but the server runs at
`matrix.example.org`, use the second one.

### 2. Log in

```sh
kith login
```

`kith login` asks for your password on the terminal with echo off and hands it to the
daemon (starting it if need be), which performs a password login, stores the
resulting access token in the OS keyring and starts syncing. The password itself is
never written anywhere. It must run interactively. Only password login is supported.

Every successful login creates a **new Matrix device**. Log in once per machine and
leave it. If you log in again, the new device can't read encrypted history until you
[give it the room keys](#set-up-encryption-keys).

For WhatsApp (see [WhatsApp](whatsapp.md) first, for the risks), run
`kith login whatsapp` instead (or as well): it shows a pairing code
to type on the phone, under WhatsApp → Linked devices → Link a device → "Link with
phone number instead". `kith` opens whatever is logged in; an account that is not
says so on the status line, with the command that logs it in. A WhatsApp community
shows as a space holding its groups (its announcement group among them); what is in
it is the community's admins' to decide, so kith does not offer to file rooms into it.
A channel you follow is a room too, with its latest posts fetched the first time;
only its admins can post in it, and reacting to channel posts is not supported yet.

## Run the daemon under systemd

`kith` starts the daemon itself when none is running, and prints a note when it
does:

```text
kith: kithd was not running — started it. Anything that arrived while it was down was not notified.
```

This fallback works, but the point of the daemon is that it keeps running when no
terminal is open. That's what lets notifications, scheduled messages and the send
queue keep working. Let systemd own it.

### With the Makefile

```sh
make deploy                 # kithd.service
make deploy PROFILE=work    # kithd@work.service, for a [[profile]] account
```

`make deploy` runs `make install`, then does the setup the unit can't do for itself:

- Imports `WAYLAND_DISPLAY`, `DISPLAY` and `DBUS_SESSION_BUS_ADDRESS` into the systemd
  user manager, if they are set. The daemon needs them for notifications and
  clipboard access.
- Enables the unit and restarts it, so a new binary actually takes over.
- Waits up to a minute for the daemon's socket to appear, then tells you whether the
  daemon came up.

With `PROFILE=<name>` it does the same for `kithd@<name>.service`. The daemon-*
targets take `PROFILE=` too, for example `make daemon-logs PROFILE=work`.

### By hand

This is the path to use with a release archive:

```sh
install -Dm644 packaging/systemd/kithd.service ~/.config/systemd/user/kithd.service
systemctl --user daemon-reload
systemctl --user import-environment WAYLAND_DISPLAY DISPLAY DBUS_SESSION_BUS_ADDRESS
systemctl --user enable --now kithd
```

With a Linux package the unit is already installed; start at `daemon-reload`.

The unit mounts your home directory read-only and allows writes only to kith's own
directories (`~/.local/share/kith`, `~/.local/state/kith`, `~/.cache/kith`).
Those must exist before the sandbox is built, or the unit fails before the daemon
even starts (see
[troubleshooting](troubleshooting.md#the-unit-fails-with-status-226namespace)). The
unit creates them itself, in an `ExecStartPre=+mkdir` step that runs outside the
sandbox, so you no longer need to. A unit installed before that line existed still
needs the `mkdir -p` by hand.
Only import variables that are set in your shell: importing an unset one clears the
value the user manager already has.

Log in before you enable the unit. A daemon with no stored session exits right away,
and systemd keeps restarting it every five seconds until you do.

The unit runs with a hardened sandbox: `UMask=0077`, so the cache and crypto store are
readable only by you, a read-only home directory apart from kith's own directories,
no capabilities, and a system-call filter. `Restart=on-failure` and
`StartLimitIntervalSec=0` mean a homeserver that is down for hours doesn't make
systemd give up on the daemon.

### Everyday commands

| Task | Command |
| --- | --- |
| Status | `make daemon-status` or `systemctl --user status kithd` |
| Last 100 log lines | `make daemon-logs` |
| Follow the log | `journalctl --user -u kithd -f` |
| Restart after an upgrade | `make daemon-restart` or `systemctl --user restart kithd` |
| Stop | `make daemon-stop` |
| Run in the foreground instead | `systemctl --user stop kithd`, then `kithd` |

### Without systemd

On macOS, use the LaunchAgent: `make deploy` installs and loads it, and
[Homebrew users load it by hand](#macos-homebrew). With `PROFILE=<name>`, `make deploy`
installs `io.github.eugeneshtoka.kithd.<name>`, which runs `kithd --profile <name>`.

On any other system without a systemd user instance, you have two choices. Run
`kithd` yourself, for example in a spare terminal or from your session's autostart.
Or let `kith` start the daemon on demand, which requires `setsid` on your `PATH`.
The daemon stops cleanly on `SIGINT` or `SIGTERM`.

## First launch

```sh
kith
```

The client connects to the daemon's socket and waits until the daemon has received
its first sync response from the homeserver. It waits up to 60 seconds, because
before that first sync the cache is cold and an account with rooms would look empty.
Then the three panes appear: the spaces rail, the room list, and the timeline with
the composer.

On the first run for an account, the daemon:

- Creates the local cache and the encryption store, and sets up end-to-end encryption
  for the new device.
- Performs a full initial sync, which fills the room list, room state and recent
  messages, and caches all of it.
- Starts serving from the cache. Later starts show the room list instantly from disk
  while a background refresh catches up. Older history is fetched from the
  homeserver when you ask for it and cached as it arrives.

If the homeserver is unreachable when the daemon starts, it serves your cached
history anyway and keeps retrying (every 2 to 30 seconds) until the homeserver is
back.

A few pointers for the first minutes:

- Press `?` for the list of keybindings. It is generated from your live keymap. See
  [keybindings.md](keybindings.md).
- Press `,` for the settings screen. See [configuration.md](configuration.md).
- Notifications ship **off**. Turn them on with `[notifications] enabled = true` or
  from the settings screen. See [notifications.md](notifications.md).
- [usage.md](usage.md) walks through the panes and everyday tasks.

## Set up encryption keys

A new device can decrypt messages sent after it joined, but not older encrypted
history: the keys for those messages live on your other devices. Messages it can't
decrypt yet show as `🔒 Encrypted message — no decryption key`. There are three ways
to fix that. Pick the one that matches your account:

| Situation | Command |
| --- | --- |
| The account has a server-side key backup (you have a recovery key or passphrase) | `kith --restore-keys` |
| You have a key export file from another client (for example, Element's "Export E2E room keys") | `kith --import-keys keys.txt` |
| The account has never set up key backup | `kith --bootstrap-keys` |

`--bootstrap-keys` sets up cross-signing, secret storage and a key backup, then
prints a recovery key **once**. Write the key down. `--bootstrap-keys` refuses to run
on an account that already has secret storage or a backup, because running it again
would replace the account's cross-signing identity. Once a backup exists, the daemon
uploads new room keys to it as they arrive.

Device verification, key export and the details are covered in
[encryption.md](encryption.md).

## Open matrix: links in kith

`make install` registers kith as the handler for `matrix:` links. When you click
one in a browser or another app, the desktop runs:

```sh
kith --open 'matrix:r/room:example.org'
```

`--open` accepts `matrix:` URIs and `https://matrix.to/#/…` links. It doesn't draw
anything. It hands the link to the `kith` that is already attached to the daemon,
which then goes there. If no client is attached, it starts one in a terminal, as a new
tab if the terminal supports it. It prefers wezterm, kitty, konsole or gnome-terminal
(which can open tabs), then foot, alacritty, ghostty and xterm. To choose one yourself,
set `terminal` at the top of the config, either to one of those names or to any
command that accepts `-e`:

```toml
terminal = "kitty"
```

Release archives don't include the desktop entry. To register the handler by hand,
copy `packaging/desktop/kith.desktop` from the repository, then run:

```sh
install -Dm644 kith.desktop ~/.local/share/applications/kith.desktop
xdg-mime default kith.desktop x-scheme-handler/matrix
```

If `kith` isn't on the desktop session's `PATH`, change the entry's `Exec=` line to
the binary's absolute path.

## Multiple accounts

To use more than one account, replace the top-level `homeserver` and `user` with
named `[[profile]]` blocks:

```toml
[[profile]]
name       = "personal"
homeserver = "https://matrix.example.org"
user       = "@alice:example.org"

[[profile]]
name       = "work"
homeserver = "https://matrix.work.example"
user       = "@alice:work.example"
```

Keep the account in one place only. A file with both a top-level account and
profiles is refused. Profile names may contain letters, digits, `.`, `-` and `_`.

A profile carries **only the account**. Keybindings, display settings and
notification rules are shared by all profiles. Everything stateful is kept per
account, keyed by a hash of the Matrix ID: the keyring entry, the cache, the crypto
store, the daemon, its socket and its lock. Two accounts can't interfere with each
other.

```sh
kith login --profile work
systemctl --user enable --now kithd@work
kith --profile work
```

Or, from a clone, `make deploy PROFILE=work` does the enable, restart and wait in
one step.

Without `--profile`, every command uses the first profile in the file. The templated
unit, `kithd@.service`, is installed by `make install` and by every Linux package.
From a release archive, install it once yourself:

```sh
install -Dm644 packaging/systemd/kithd@.service ~/.config/systemd/user/kithd@.service
systemctl --user daemon-reload
```

`kithd@<name>.service` runs `kithd --profile <name>`, and `kith --profile <name>`
starts that unit when its daemon isn't running. The plain `kithd.service` serves the
first profile. Enable either it or `kithd@<first-name>` for that account, not both.
The second one would find the lock taken and exit.

There is no combined view across accounts. Each `kith` shows one account.

## Where files live

On Linux, kith follows the XDG base directory spec, and each directory can be moved
with its `XDG_*` variable. `<hash>` is a short hash of the Matrix ID, so file names
don't reveal who is logged in.

| What | Path | Notes |
| --- | --- | --- |
| Config | `~/.config/kith/config.toml` | Holds no secrets. The first settings-screen save leaves `config.toml.bak` beside it. |
| Your commands | `~/.config/kith/commands/` | Default `[commands] dir`. See [commands.md](commands.md). |
| Message cache | `~/.local/share/kith/cache-<hash>.db` | SQLite. A copy of what the homeserver has, so it is safe to rebuild. See [database.md](database.md). |
| Crypto store | `~/.local/share/kith/crypto-<hash>.db` | Olm/Megolm keys and the sync position. **Not** a cache: don't delete it. |
| Old caches | `~/.local/share/kith/cache-<hash>.db.pre-schema-<date>-<time>` | Kept when a schema change rebuilt the cache; the two newest are kept, older ones are deleted at the next rebuild. Delete them when you no longer need them. |
| Dictionaries, word lists, models | `~/.local/share/kith/hunspell/`, `~/.local/share/kith/models/`, … | Written by `--add-dictionary`, `--add-frequencies` and `--add-model`. |
| Scheduled messages | `~/.local/state/kith/scheduled-<hash>.toml` | The send-later queue. |
| Assistant ledger | `~/.local/state/kith/agent-sends-<hash>.jsonl` | What `kith-mcp` wrote on your behalf. Read it with `kith --agent-log`. |
| Token file (opt-in only) | `~/.local/state/kith/session-<hash>.toml` | Only with `allow_token_file = true`. Mode 0600, one per account. An older single `session.toml` is still read for the account it belongs to and renamed on first use. |
| Media cache | `~/.cache/kith/media/` | Attachments and their rendered previews, capped at 1 GiB by default (`[display.media] cache_dir`, `cache_max_mb`). |
| Socket and lock | `$XDG_RUNTIME_DIR/kith/<hash>.sock` and `<hash>.lock` | Usually under `/run/user/<uid>/`. The socket is mode 0600 in a 0700 directory. |

On macOS, the config, data and state files all go under
`~/Library/Application Support/kith/`, the socket too unless `XDG_RUNTIME_DIR` is
set, and the media cache goes under `~/Library/Caches/kith/media/`. Setting the
`XDG_*` variables overrides these locations on macOS as well.

### The keyring

kith stores these entries under the keyring service `kith`:

| Entry | Holds |
| --- | --- |
| `<your Matrix ID>` | The session: homeserver, device ID and access token. |
| `<your Matrix ID>\|pickle` | The key that encrypts the crypto store at rest. It is generated on first use and never written to disk. |
| `secret\|<ref>` | API keys stored with `kith --set-model-key <ref>`. |

Without a keyring, `kith login` can't store the session and says so. On a headless
machine you can set `allow_token_file = true` to keep the token in a 0600 file
instead. End-to-end encryption still needs the keyring for the pickle key. Without
one, the daemon runs with encryption disabled and logs
`encryption disabled`, with the reason in `err=`.

## Uninstall

```sh
make undeploy
```

`make undeploy` reverses `make install` and `make deploy`. It:

- stops and disables `kithd.service` and every `kithd@<profile>` instance, running
  or only enabled;
- removes `kithd.service` and `kithd@.service` from `~/.config/systemd/user/`;
- deletes `kith`, `kithd` and `kith-mcp` from `BINDIR`;
- deletes `~/.local/share/applications/kith.desktop` and removes the
  `x-scheme-handler/matrix` association from `mimeapps.list`, but only where it
  names `kith.desktop`. A handler you picked since is left alone.

With a package manager, remove the package instead (`apt remove kith`,
`pacman -R kith-bin`, `brew uninstall --cask kith`, …), after
`systemctl --user disable --now kithd` (and any `kithd@<profile>`): the package
can't reach your user session to do it for you. On macOS, `make undeploy` also
unloads and removes the LaunchAgents.

Both leave your data in place on purpose. To remove everything:

```sh
rm -rf ~/.config/kith ~/.local/share/kith ~/.local/state/kith ~/.cache/kith
secret-tool clear service kith                    # Linux, Secret Service
```

**Export your room keys first** (`kith --export-keys file`) unless another device
or a server-side backup has them. The crypto store may hold the only copy. Removing
local files doesn't sign the device out on the server; remove it from your account's
session list in another client.
