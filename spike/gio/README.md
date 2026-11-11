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
gets a private read receipt while the window has focus. Ctrl+K filters every room. The
composer takes Enter as a newline and Ctrl/Alt+Enter or Send to send ([keys.insert]'s
default), and Attach sends a file from the portal's file chooser.

Messages draw kith's formatting (bold, italic, code in monospace, quotes, headings) and
links, theirs and bare URLs, which open in the browser (web and mail links only). A reply
names what it answers, and clicking that scrolls to it. Reactions show as pills that
follow kithd's stream, and who is typing shows over the composer. A thread's summary opens
it beside the timeline, where a reply goes into the thread. Clicking an image shows it over
the window (Esc or a click closes it), a right-click copies a message, and the room list
reorders as messages arrive.

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
- **Formatted text cannot be selected.** Gio's rich text (`gioui.org/x/richtext`) draws
  spans and clickable links but has no selection, so a message with formatting or a link is
  drawn rich and the rest stay selectable; a right-click copies any message whole. It has no
  underline, strikethrough or span background either, so those are dropped.
- **Slack shortcodes** are converted here for now; kithd's Slack adapter is to do it for
  every client, after which `shortcodes()` goes.
- **Gio does not report the desktop's color scheme**, so the theme's "system" mode is dark here.
