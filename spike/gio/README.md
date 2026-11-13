# Gio desktop spike

kith's desktop window drawn by [Gio](https://gioui.org) instead of Electron, to measure what
a native window costs and what building one takes. Not meant to merge.

It attaches to the local kithd as the terminal client does (`daemon.Remote`, no kith-relay)
and looks like the desktop: its theme from `[gui.theme]` (the device's over the shared), the
top bar (rail toggle, back and forward through the rooms opened, Go to), kithd's rail with
each row's unread, the room list with faces and counts, the room's header, and a timeline
of sender runs with their pictures (initials on their hue otherwise, as `faces.ts`), day
dividers, inline images, scrollback paging, thread summaries, "Message deleted", and files
as Save buttons (to `~/Downloads`). Video and voice notes play in mpv, a video in a child X
window of the Gio window (`mpv --wid`). Unread badges follow kithd's stream; the open room
gets a private read receipt while the window has focus. The composer takes Enter as a
newline and [keys.insert] send (Ctrl/Alt+Enter) or Send to send, and Attach sends a file
from the portal's file chooser.

Messages draw kith's formatting (bold, italic, code in monospace on a background,
underline, strikethrough, quotes, headings), selectable, and
links, theirs and bare URLs, which open in the browser (web and mail links only). A reply
names what it answers, and clicking that scrolls to it. Reactions show as pills that
follow kithd's stream, and who is typing shows over the composer. A thread's summary opens
it beside the timeline, where a reply goes into the thread. Clicking an image shows it over
the window (Esc or a click closes it), a right-click copies a message, and the room list
reorders as messages arrive.

The device's `[keys]` drive the window, as they drive the desktop (`keys.go` ports its
keymap: sequences such as `g g`, counts such as `12j`, and typing wins). The rail, the
room list and the timeline each have a cursor that `[keys.nav]` moves; Tab or
Ctrl+Shift+1..4 move between panes, Ctrl+K goes to a room, Ctrl+O and Ctrl+I back and
forward, each `[[keys.jump]]` chord to its room or rail row, and `?` lists the keys bound
now. The title counts unread, where the desktop has its tray badge. On the selected message: reply, edit (your own), delete (asks first), react (a
picker that searches emoji by name, your most used first), copy its text or link, open
a link (with several, a chooser asks which, as the desktop's does; opening hands it to
kithd, so a chat link opens in the window kithd picks and a web one in the browser), save,
view, play, open or start its thread, and jump by mention, attachment,
day, or to what a reply answers. `m` / `M` / Ctrl+M mark a room read or unread from the
list, and `m` on a rail row marks the row read (asks first). The composer completes
`:name` into emoji and `@name` into a mention (people as kithd offers them: Matrix,
LinkedIn and Instagram rooms), and turns a typed `:name:` into its emoji; the popup's keys
are `[keys.completion]`'s. A room's unsent text, reply and edit are kithd's draft
(`drafts.go`, the desktop's `drafts.ts` in less), which any number of windows and kith-mcp
write: written over the version last read after a pause in typing, and read at start, when
the window gets the focus back, and whenever kithd says drafts changed (`CHANGE_DRAFTS`).
Words another window or the assistant added come back in the write's answer and go after
the window's own, typed meanwhile or not, and the window says so; a read older than one of
its own writes is not taken (`drafts_test.go` walks every interleaving of two windows and
kith-mcp).

kithd can hand it links (`kith --open`, a link opened from any window): daemon.Remote holds
its link stream under a client id kept for the run, and the window says which room it has
open (Present) as that changes and when it gets the focus, so a link goes to it when it
has the link's chat open or was used last. A link it is handed opens the chat it names
among the profile's rooms (the first, as the desktop takes), at the message it names once
loaded, and raises the window (X11; Wayland has no raise without xdg-activation).

Search, People and Settings are drawn but do nothing; "system" theme mode is dark.

## Build and run

```sh
# NixOS
nix-shell -p pkg-config wayland libxkbcommon xorg.libX11 xorg.libXcursor xorg.libXfixes \
  xorg.libxcb libGL vulkan-headers
# Arch
sudo pacman -S --needed go pkgconf wayland libxkbcommon-x11 libx11 libxcursor libxfixes \
  mesa vulkan-headers mpv noto-fonts-emoji

export GOFLAGS='-tags=goolm -ldflags=-X=github.com/ncruces/go-sqlite3/driver.driverName='
go build -o /dev/shm/kith-gio . && /dev/shm/kith-gio            # kithd must be running
/dev/shm/kith-gio -measure                                       # timings and memory, then exit
/dev/shm/kith-gio -profile work                                  # another [[profile]]
/dev/shm/kith-gio -room ROOM -play EVENT                         # play an attachment at once
go run ./findmedia                                               # cached videos and voice notes
```

## Measured 2026-11-11

Xvfb 1280×800, Mesa llvmpipe (software GL) for both, same kithd, 307 rooms.

|                          | Gio spike                     | Electron desktop (packaged)           |
| ------------------------ | ----------------------------- | ------------------------------------- |
| processes                | 1                             | 7 (main, 2 zygotes, GPU, utility, renderer, kith-relay) |
| PSS                      | 156 MB                        | 320 MB                                |
| RSS (summed)             | 183 MB                        | 661 MB                                |
| Go runtime               | ~50 MB (heap in use 24–41 MB) | —                                     |
| first frame              | 85–125 ms                     | under 1 s (window up by the 1 s shot) |
| rooms listed             | 110–140 ms                    | under 1 s after the profile is picked |
| first room drawn         | 300–700 ms (kithd's Timeline call) | — (opens no room)                |

Most of the Gio process beyond the Go runtime is Mesa's llvmpipe; on a GPU it would be less.
With the rail, faces and theme added the Gio numbers barely moved: RSS 188 MB, Go runtime
54 MB, first frame ~90 ms, rooms ~220 ms.

## Found

- **Emoji are tofu by default.** go-text's `sans-serif` fallback names no emoji family, so
  the theme's face is `"sans-serif, emoji"`. Bitmap emoji (Noto Color Emoji) then draw, but
  each is followed by a gap several characters wide (its advance is mis-scaled).
- **Video in place works on X11** with a child window and `mpv --wid`, positioned from the
  layout. It is a dock above the timeline, not inline in the scrolling list: Gio does not
  give a widget's window position, so inline would mean tracking offsets through the list.
  **Wayland has no `--wid`**: in place there needs a subsurface, or libmpv's render API
  drawing into a texture Gio can show, which Gio does not expose.
- **Selection is per message.** `widget.Selectable` selects inside one label; selecting
  across messages, as a browser does, has to be written.
- **Gio's rich text could not select, and dropped color emoji.** `gioui.org/x/richtext`
  sits on `x/styledtext`, which shapes each styled run apart and keeps no glyph positions,
  so there is nothing to hit-test; it also paints only glyph outlines, so bitmap emoji
  vanish, and a run has no background, underline or strikethrough. No issue or plan for
  this was found upstream. `seltext/` is styledtext's layout (Unlicense OR MIT) keeping
  each grapheme cluster's bytes and box: drag to select (across lines and styles),
  Ctrl+C / Ctrl+A, clickable links, backgrounds, underline, strikethrough, bitmap emoji.
  Every message draws with it; written so it could go upstream to gio-x. Selection is
  per message still, and right-to-left text selects by cluster boxes, not yet visually.
- **Slack shortcodes** are converted here for now; kithd's Slack adapter is to do it for
  every client, after which `shortcodes()` goes.
- **Gio does not report the desktop's color scheme**, so the theme's "system" mode is dark here.
- **Gio gives a key to the first reader in the frame whose filter matches**, not to the most
  specific filter, so the window-wide key reader runs last: anything earlier (an editor's
  arrows and Backspace, the picker's Esc) keeps its own keys.
- **A notification has no click.** `kith-notify` posts its popups with no actions and no
  room in them, so clicking one reaches no window, the desktop's included; what reaches a
  window is a link opened elsewhere (`kith --open`, kithd's Follow). Making a popup open
  its room needs kith-notify to carry the room and handle the default action.
- **Not done here:** the thread composer's draft is not kept; a linked message older than
  the pages loaded is not fetched (the desktop reads the room around it); a link handed
  over picks the first chat it names where the TUI asks which account.
