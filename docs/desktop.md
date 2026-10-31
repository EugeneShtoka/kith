# The desktop window

`kith-desktop` is a window for kith: the same daemon, profiles and rules as the
terminal client, drawn with pictures at full size, drag and drop, and a native file
picker. It is a client of `kithd`, as `kith` is, so everything the daemon keeps (the
accounts, the cache, the keys, the drafts) is the same in both, and you can use either.

This first release reads and writes messages:

- the rail and room list, with unread counts, and invites;
- the timeline, with replies, edits, reactions, polls, pictures and files;
- the composer, with markdown, mentions, emoji, attachments and drafts, and
  [spelling and word completion](#spelling-and-completion), and
  [sending later and commands](#sending-later-and-commands);
- search, the ctrl+K switcher, and threads beside the room;
- a settings page for the settings it shares with the terminal client
  ([where settings live](#where-settings-live));
- [accounts](#accounts): logging a network account in, setting a new one up, and
  signing one out;
- [verifying this session](#verifying-this-session) against another of your own;
- [filing a room](#filing-a-room) into tags and spaces.

The full settings editor and the tag editor stay in the terminal client and
the command line for now. Links in messages show where they go, but the window does
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

A **poll** shows its question and each answer with its votes and share, your own
answer marked. Press an answer to vote for it, or, where the poll takes several, tick
them and press **Vote**; **Take my vote back** takes it back, and an ended poll takes no
more votes. **Who voted** lists who chose each answer, as far as the network names them
(Telegram may name them only once you have voted). **Poll**, under the composer, asks
one in the open room: a question, two answers or more, and whether voters may choose
several. You vote wherever the terminal client's `P` does (Matrix, WhatsApp, Telegram
and Signal); asking a poll works in Matrix rooms, as the terminal's `/poll` does, and
elsewhere the window says the network cannot.

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
color in the terminal client's room (its `[tui.theme.networks]` and sender colors);
someone the terminal client has not colored yet is drawn in the window's own color for
them. The terminal client keeps its own palette, `[tui.theme]`, and reads none of
this. The keys are in [configuration.md](configuration.md#the-graphical-clients-theme).

The terminal client's settings screen, another window, or an editor may change either
file while the page is open. The window then reads the theme again and writes your
change onto it. If the same color was changed there, the window keeps that one and says
so. A `[gui.theme]` edited by hand into one that does not check (a color that is not
`#rrggbb`) is refused, as `kithd` refuses any bad config: the window keeps drawing the
theme it had.

## Accounts

**Settings → Accounts** lists every network `kithd` can log in to, with its accounts.
**Sign in** logs an account in again; **New account** sets one up. As with the terminal
client's `:login`, the network leads the way a step at a time: it asks for what it needs
(a phone number, a code it sent, a password), shows a code to type on your phone, or
draws a QR code for your phone to scan, with another way to link when it offers one
(**Link with a code typed on the phone instead**, say). A new account is written into
the bridge's `config.toml` through `kithd`, as the terminal client writes it, and `kithd`
starts it. **Cancel** ends the login.

What you type goes straight to `kithd` and is not kept: a password shows as dots once
sent, and its field is emptied. A login waits 15 minutes at most. Reloading or
closing the window ends a login it was leading, so none is left waiting on a phone.
Beginning a login of an account the terminal client (or another window) is logging in
ends that one, as `kithd` allows one login per account at a time; the other says so.

A login that needs `kithd` restarted first (Matrix, the first time) says so: restart
`kithd`, then sign in again. The window does not restart it, as a terminal client attached
to a [bridge](remote.md) does not.

**Signed in** lists each account whose own space is on the rail. **Sign out** asks first,
and whether to forget its rooms and their history here too: its session ends on the
network, and **Sign in** brings it back. This is the terminal client's leaving an
account's space from the rail.

## Verifying this session

A new session reads your encrypted history once another of your sessions (Element, a
phone) vouches for it. **Settings → Verify this session** asks your other sessions, as
the terminal client's `:verify` does; with several Matrix accounts and none the
default, each has its own **Verify**. Accept on the other session, and both show seven
emoji, each with its name: **They match** if they are the same in the same order, **They
don't match** if not. A request from another of your sessions opens over the window at
any time, with **Accept** and **Decline**; **Cancel** calls off your own.

Every client of `kithd` is shown each request and its emoji, so the terminal client and
the window can both answer one. `kithd` takes the first answer to each step and refuses
the other, and the other client says the step was answered elsewhere and waits.

The window verifies your own sessions only. A request from someone else is not shown
(the terminal client shows it), and the window answers only a verification it was
shown or asked for. If the window loses `kithd` mid-verification (a restart forgets
it), reloads, or switches profile, the verification ends there: start it again. One
the other session cancels, or that times out after ten minutes, says so.

## Spelling and completion

The composer checks your spelling and finishes your words as the terminal client does,
with the same settings (`[spell]` and `[complete]` in `device.toml`,
[composer.md](composer.md#spell-checking)).

- **Spelling** is checked by `kithd`, after a pause in typing, with the dictionaries
  and your own words the terminal client uses: a word you add in one is known in the
  other. A misspelling is underlined (`underline`: curly, line or none), a rare word
  more quietly (`rare_underline`), and never the word you are still typing. The
  window's own browser spellchecker is off, so a word is marked once and no word leaves
  for a spelling service.
- **Right-click** a marked word, or press **alt+S**, for the correction walk: its
  suggestions (**1**–**9**), **Add to dictionary** (**a**; for a rare word, **It's
  fine**), **Ignore** for this session (**i**), **Skip** (**s**), **Ignore all** (**I**),
  **Skip all** (**S**) and **Stop** (**esc**). A suggestion taken puts right the word and
  its later copies.
- With `check_before_send`, a send with a word marked opens the walk first, and the
  message goes once you are through it (`rare_before_send`: rare words too).
  `autocorrect` puts right a word as you finish it, only when the fix is certain.
- **Completion** draws the word you are typing, dimmed after the caret, when one
  clearly leads; **Tab** takes it, **→** takes it too (**←** in a right-to-left word),
  and Tab with nothing clearly leading lists the candidates. At a word's end the next
  words are offered, from the completion model on the `kithd` machine and from what
  the room said before: drawn as ghost text, numbered above the composer when there
  are several, taken with **alt+1**…**alt+5**. While the model answers, the composer
  shows ⌂ and its name.

The first time the window has rooms, it offers the dictionaries, word counts and
completion model your own writing calls for, as the terminal client does. Ticked rows
are installed (each checked against its pinned hash); an unticked row is written to
`declined`, `frequencies_declined` or `[complete.model] declined` and not offered
again; **Not now** asks again next time the window starts.

## Sending later and commands

A message can go later, as the terminal client's `/at` and `/in` send it
([scheduled messages](composer.md#scheduled-messages)). `kithd` keeps the queue and
sends from it, so it goes on time whether the window is open or not.

- **Later**, beside **Send**, asks when: **At** a clock time (`09:00`, `1504`, `9pm`,
  `3:45pm`; one already past today means tomorrow) or **In** a delay (`30m`, `2h`,
  `1h30m`). The words in the composer go then, with the message they reply to, the
  thread open and their mentions, and the composer empties as a send empties it.
  `/at 09:00 good morning` and `/in 2h see you then` do the same from the composer.
- **Queued here** (in the Later form), or `/scheduled`, lists what waits to go out in
  this room; **Every room** lists the whole queue. Each row has **Cancel**, and a message
  that failed or was refused has **Try again** and says why. The list is read again
  while it is open, so a message that goes out meanwhile leaves it. Canceling one whose
  time came first says it was no longer waiting, rather than that it was canceled.
- A message `kithd` did not take (it was not reachable) stays above the composer,
  **Not queued**, with **Queue again**: it is asked for under the same transaction ID,
  so it is queued once even when the first answer was lost.

A `/` at the start of the composer lists the commands, as the terminal client's does
([commands](commands.md#composer-commands-)): `/me`, `/plain`, `/at`, `/in`,
`/scheduled`, `/search`, `/upload` (it opens the file dialog), `/poll`, `/topic` (shown in
full) and `/replacement` work in the window; the rest say they are in the terminal
client and keep your words. `//` starts a message with one slash.

Your own commands ([your own commands](commands.md#your-own-commands): the executables
of the commands folder, with their `[[command]]` settings) are listed after them, those
whose `in` places hold the open room. `kith-relay` runs them on this machine, as the
terminal client does: by name only, in a room of the window's profile, given the
message (the one you reply to, else the room's newest), its link and the room's history
as `kithd` holds them, and their secrets from the keyring. The window passes the
command no words of its own: `/zoom tomorrow` is refused, and `/zoom` runs it. Its
output goes where `output` says: into the composer, sent to the room, a status line,
a panel to read, or the clipboard (main copies it). `[commands] timeout` bounds a run,
one runs at a time, and closing or reloading the window stops it. Key bindings
(`keys`) and setting up a command's secrets stay in the terminal client.

## Filing a room

**Tags and spaces**, in the open room's header, or a right-click on a room in the list,
opens the filing picker, as the terminal client's `S` does
([putting a room in a tag by hand](filters.md#putting-a-room-in-a-tag-by-hand)): the
spaces you made, then the tags you file rooms into by hand, in the same order, the
ones holding the room ticked. `[display] filing_spaces` in `device.toml` names the
spaces offered here as it does there. Tags their rule fills (Unread, DMs, All) are not
offered, nor a bridge's own space.

Tick or untick, then **Apply**: only what you changed is filed. `kithd` writes the
tags into `config.toml` itself, on the file as it is when it writes, so another window,
the terminal client or a hand edit landing at the same moment is kept, not overwritten.
As in the terminal client, taking a room out of a tag a filter moved it into records a
release so no filter moves it back, and filing a chat into or out of a network's
[mirrored archive](configuration.md#follow-a-networks-own-archive) archives or
unarchives it on the network. One line above the window says where the room went, and
what a network or a space refused. The rail follows when `kithd` says it changed.

A room of another profile cannot be filed from this one. Making a new tag or space,
the tag editor and the Places settings stay in the terminal client.

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
shortcodes, how the reaction palette is ranked and the emoji it starts from, how
misspellings and rare words are marked and corrected, and word completion. There
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
