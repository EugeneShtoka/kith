# The desktop window

`kith-desktop` is a window for kith: the same daemon, profiles and rules as the
terminal client, drawn with pictures at full size, drag and drop, and a native file
picker. It is a client of `kithd`, as `kith` is, so everything the daemon keeps (the
accounts, the cache, the keys, the drafts) is the same in both, and you can use either.

This first release reads and writes messages:

- the rail and room list, with unread counts, and invites;
- the timeline, with replies, edits, reactions, pictures and files;
- the composer, with markdown, mentions, emoji, attachments and drafts;
- search, the ctrl+K switcher, and threads beside the room;
- a settings page for the settings it shares with the terminal client
  ([where settings live](#where-settings-live)).

Logins, verification, the full settings editor, tags and filing stay in the terminal client
and the command line for now. Links in messages show where they go, but the window does
not open them yet.

## Install

The desktop ships with every kith release, built from the same tag as `kithd`. Get the
files from the [releases page](https://github.com/EugeneShtoka/kith/releases):

| System | File |
| --- | --- |
| Debian, Ubuntu | `kith-desktop_<version>_amd64.deb` or `_arm64.deb` |
| Fedora, RHEL, openSUSE | `kith-desktop-<version>-1.x86_64.rpm` or `.arm64.rpm` |
| Any other Linux | `kith-desktop_<version>_linux_amd64.zip` or `_arm64.zip` |

The window needs a `kithd` to reach, so install kith itself too, from the same release
([getting started](getting-started.md#install)). The `.deb` recommends the `kith`
package, so `apt` installs both. The `.rpm` cannot say so, so install both files:

```sh
sudo apt install ./kith_*_amd64.deb ./kith-desktop_*_amd64.deb     # Debian, Ubuntu
sudo dnf install ./kith-*.x86_64.rpm ./kith-desktop-*.x86_64.rpm   # Fedora, RHEL
sudo zypper install ./kith-*.x86_64.rpm ./kith-desktop-*.x86_64.rpm # openSUSE
```

The packages install:

| Path | What |
| --- | --- |
| `/usr/lib/kith-desktop/` | The app: Electron, the window's code in `resources/app.asar`, and `resources/kith-relay` |
| `/usr/bin/kith-desktop` | A link to the app |
| `/usr/share/applications/kith-desktop.desktop` | The menu entry, named kith |

The zip holds the same app directory, `kith-linux-<arch>/`. Unpack it anywhere you own
and run `kith-linux-<arch>/kith-desktop`. On Ubuntu 24.04 and later the zip needs one
more step, under [Troubleshooting](#the-window-does-not-start-the-suid-sandbox-helper).

macOS and Windows are not built yet.

### Verify a download

The desktop's files have their own checksum file, signed as kith's `checksums.txt` is,
and each has a build attestation:

```sh
gh attestation verify kith-desktop_*_amd64.deb --repo EugeneShtoka/kith
cosign verify-blob kith-desktop_SHA256SUMS \
  --bundle kith-desktop_SHA256SUMS.sigstore.json \
  --certificate-identity-regexp '^https://github.com/EugeneShtoka/kith/\.github/workflows/release\.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --check --ignore-missing kith-desktop_SHA256SUMS
```

## First run

1. Set kith up as the [getting started](getting-started.md) guide says: log in to an
   account and run `kithd` (the systemd unit, or `kith` starts it).
2. Open **kith** from the application menu, or run `kith-desktop`.
3. Pick a [profile](configuration.md#profiles). A profile has one window at a time: if
   a terminal client holds it, the window offers to take it over, and that client steps
   aside.

A second start brings the first window forward rather than opening another.
Closing the window ends it; there is no background process to quit.

Notifications are not the window's: `kith-notify` shows them, as it does for the
terminal client, so a message never pops up twice.

## The window

The window fills its height, and each part scrolls on its own:

- **Across the top**, the search bar, back and forward (alt+← and alt+→), **Go to**
  (ctrl+K) and **Settings**.
- **On the left**, the profile menu (when the config has more than one), the rail of
  places, and the room list beside it. A room with something unread is in bold with
  its count; a red count with an @ means something there names you. Each room shows its
  network's mark (WA, TG, SL, …) where the network is known. **Connection**, at the
  bottom, shows the cache's state and each account's.
- **In the middle**, the open room: its name, network and topic above, its messages,
  and the composer under them. Messages are split by day, and one sender's messages a
  few minutes apart are drawn as one group, with the time of each in the margin when
  you point at it. Pointing at a message shows its actions: react, reply, reply in a
  thread, and edit or delete your own. The composer's bar puts Markdown around what is
  selected (bold, italic, code, a link); it is there only while **Send Markdown
  rendered** is on.
- **On the right**, a thread, when one is open.

People and rooms are drawn as their initials, each in a color of their own that stays
the same everywhere: a person in the color the terminal client gives them in that room,
until you pick one ([Theme](#theme)). Phone numbers are shown as the network gives them.

As the window narrows, the columns narrow with it. Under about 960 pixels the rail
folds away behind the **☰** button in the top bar, and a thread opens over the room
instead of beside it; under about 640 pixels the whole left column folds away too, and
picking a room or a place folds it again.

**Zoom** with ctrl+= and ctrl+-, and ctrl+0 to go back to 100%. **Text size** is on the
settings page, under **This window**. Both are kept by the window for the next start,
not in `device.toml`, so the terminal client is not affected.

The colors follow the system's light or dark setting, unless the [theme](#theme) says
otherwise.

## Theme

**Settings → Theme** sets how the window looks: its mode (as the system is, light or
dark), each mode's colors (accent, background, surface, text, muted, link, selection,
unread and mention), each account's color, and the colors you picked for people. To
pick someone's color, point at a message of theirs and press **◐** in its actions.

Each row is written for every device at once: it goes into the bridge's `config.toml`
(`[gui.theme]`), through `kithd`, so a machine that [joined a bridge](remote.md) writes the
bridge's. Turn on a row's **only this device** and its color is kept in this machine's
`device.toml` instead, over every device's; turning it off takes it out again, and every
device's shows. **Reset** takes a color out of the file it is in.

Until you pick them, an account is drawn in its network's color and a person in their
color in the terminal client's room (its `[display.theme.networks]` and sender colors);
someone the terminal client has not colored yet is drawn in the window's own color for
them. The terminal client keeps its own palette, `[display.theme]`, and reads none of
this. The keys are in [configuration.md](configuration.md#the-graphical-clients-theme).

The terminal client's settings screen, another window, or an editor may change either
file while the page is open. The window then reads the theme again and writes your
change onto it. If the same color was changed there, the window keeps that one and says
so. A `[gui.theme]` edited by hand into one that does not check (a color that is not
`#rrggbb`) is refused, as `kithd` refuses any bad config: the window keeps drawing the
theme it had.

## How it finds kithd

The window never talks to `kithd` itself. It starts `kith-relay`, a small Go program
shipped inside the app (`resources/kith-relay`) and built from the same release, as a
child process, and reaches `kithd` only through it:

- The relay reads your config (`~/.config/kith/config.toml` and `device.toml` beside
  it) as `kith` does, finds the daemon's socket, and forwards the window's calls to it.
- On a machine that [joined a bridge](remote.md) (`kith join`), it dials that bridge
  instead, over pinned TLS with this machine's device key from the keyring.
- Its own socket is `relay.sock` in a fresh directory under `$XDG_RUNTIME_DIR/kith`,
  which only you can open. It prints the path to the window, and it exits when the
  window does, crashed or not.

Until the daemon answers, the window says that `kithd` is not reachable and tries
again, so starting the window first and the daemon second is fine.

## Links

`matrix:`, `tg:`, `whatsapp:` and `sgnl:` links keep going to `kith --open`, the
handler the kith package installs
([getting started](getting-started.md#open-matrix-tg-whatsapp-and-signal-links-in-kith)).
It hands the link to the client `kithd` has attached: the window with that room open,
else the one used last whose profile holds it. When that is the desktop window, it
comes forward and opens the room. When no client is attached, `kith --open` still
starts the terminal client.

## Where settings live

| What | Where |
| --- | --- |
| Accounts, rooms, rules: the bridge's config | `~/.config/kith/config.toml`, read by `kithd` |
| This machine's settings, shared with the terminal client (the profile in use, read receipts, typing, emoji, media) | `~/.config/kith/device.toml` |
| Pictures the window has shown | the media cache, `[display.media] cache_dir` in `device.toml` |
| The window's theme: every device's, and this device's over it | `[gui.theme]` in `config.toml`, and in `device.toml` ([Theme](#theme)) |
| The window's zoom and text size | `~/.config/kith-desktop/scale.json` |
| Electron's own store (its caches and the single-window lock) | `~/.config/kith-desktop/` |

**Settings**, in the window's header, opens a page for the shared settings the window
follows: read receipts and the read receipt rules, the unread line, Markdown, asking
before an SMS, sending typing, the emoji skin tone, the emoji set and your own
shortcodes, and how the reaction palette is ranked and the emoji it starts from. There
is no Save button: each change is written to `device.toml` at once, and the open room
follows it. A rule's `delay` is the terminal client's, so the page does not show it, and
a rule you change keeps it. **This window**, at the end of the page, holds the text
size, which the window keeps itself ([above](#the-window)).

The terminal client's settings screen, or an editor, may change `device.toml` while the
page is open. The window then reads the file again and writes your change onto it, so
both are kept. If the same setting was changed there, the window keeps that value, and
the page says so and shows it. Every other setting is changed with kith's settings
screen, or in `device.toml`.

## Troubleshooting

### "kith-relay exited … before it listened"

The relay could not start, and the window closes after saying so. Run it by hand to see
why; it prints its socket's path when it works (press ctrl+D to end it):

```sh
/usr/lib/kith-desktop/resources/kith-relay
```

The usual reasons are a config that does not read (it names the line), or, on a
machine that joined a bridge, a keyring that is locked
([Keyring unavailable](troubleshooting.md#keyring-unavailable)).

### "kithd is not reachable"

The relay runs, but no daemon answers on the socket. Start it, and see its log:

```sh
systemctl --user status kithd
journalctl --user -u kithd -n 50
```

Socket errors are covered in
[troubleshooting](troubleshooting.md#socket-errors-permission-denied-or-connection-refused).

### The window does not start: the SUID sandbox helper

Run from a terminal, it says "The SUID sandbox helper binary was found, but is not
configured correctly". Chromium sandboxes the window with unprivileged user namespaces, and falls back to a
setuid helper, `chrome-sandbox`, where they are restricted, as on Ubuntu 24.04 and
later. The `.deb` installs the helper setuid root. The zip cannot, so give it that mode
yourself:

```sh
sudo chown root:root kith-linux-x64/chrome-sandbox
sudo chmod 4755 kith-linux-x64/chrome-sandbox
```

The `.rpm` installs the helper without the setuid bit and counts on user namespaces,
which Fedora and openSUSE allow. If yours turns them off
(`user.max_user_namespaces = 0`), set the mode as above on
`/usr/lib/kith-desktop/chrome-sandbox`. Do not run the window with `--no-sandbox`: it
draws what other people send you.

### Seeing what the window says

Run `kith-desktop` from a terminal: what the window and the relay log goes there.

## Uninstall

```sh
sudo apt remove kith-desktop   # or: sudo dnf remove kith-desktop
rm -rf ~/.config/kith-desktop  # Electron's store; your kith config and data stay
```
