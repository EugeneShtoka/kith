# Gio desktop spike

kith's desktop window drawn by [Gio](https://gioui.org) instead of Electron, to measure what
a native window costs. It attaches to the local kithd as the terminal client does
(`daemon.Remote`, no kith-relay) and has a room list (newest first), a timeline with inline
images, scrollback paging, live messages (not seen arriving yet), a composer (never sent from),
and video and voice notes played by
mpv, a video in a child X window of the Gio window (`mpv --wid`). Not meant to merge.

## Build and run (NixOS)

```sh
nix-shell -p pkg-config wayland libxkbcommon xorg.libX11 xorg.libXcursor xorg.libXfixes \
  xorg.libxcb libGL vulkan-headers
export GOFLAGS='-tags=goolm -ldflags=-X=github.com/ncruces/go-sqlite3/driver.driverName='
go build -o /dev/shm/kith-gio . && /dev/shm/kith-gio            # kithd must be running
/dev/shm/kith-gio -measure                                       # timings and memory, then exit
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
- **No shortcodes, markup, links, reactions or threads yet**, all of which kith's Go code
  already does for the TUI (`internal/emoji`, `internal/richtext`, `internal/markdown`) and
  a Go window would import rather than port.
