package main

/*
#cgo pkg-config: x11
#include <X11/Xlib.h>
*/
import "C"

import (
	"context"
	"fmt"
	"image"
	"os"
	"os/exec"
	"strconv"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// player is one attachment playing in mpv: a video in a child X window of the Gio
// window (mpv --wid), a voice note with no window. All of it runs on the frame loop's
// goroutine but the fetch, which only fills file or err under ui.mu.
type player struct {
	msg   domain.Message
	file  string // the attachment's bytes, in a private temp file; "" while fetching
	err   error
	dpy   *C.Display
	win   C.Window
	at    image.Rectangle
	mpv   *exec.Cmd
	video bool
}

// play fetches m's attachment and starts it once the next frame places it.
func (u *ui) play(ctx context.Context, m domain.Message) {
	u.stopLocked()
	p := &player{msg: m, video: m.Media.Type == domain.MediaVideo}
	u.player = p
	go func() {
		data, err := u.backend.LoadImage(ctx, m.RoomID, m.ID)
		var file string
		if err == nil {
			file, err = private(data)
		}
		u.mu.Lock()
		p.file, p.err = file, err
		if u.player != p && file != "" {
			os.Remove(file) // stopped while fetching
		}
		u.mu.Unlock()
		u.w.Invalidate()
	}()
}

// private writes data to a file only this user can read, in the runtime dir.
func private(data []byte) (string, error) {
	f, err := os.CreateTemp(os.Getenv("XDG_RUNTIME_DIR"), "kith-gio-media-*")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// place puts the video's window at r (window pixels) and starts mpv the first time;
// a voice note starts with no window.
func (u *ui) place(r image.Rectangle) {
	p := u.player
	if p == nil || p.file == "" || p.err != nil {
		return
	}
	if !p.video {
		if p.mpv == nil {
			p.err = p.start("--no-video")
		}
		return
	}
	if u.view == 0 {
		p.err = fmt.Errorf("no X11 window to play in (Wayland needs a subsurface)")
		return
	}
	if p.dpy == nil {
		// A connection of its own: Gio's Display is not ours to use off its thread.
		p.dpy = C.XOpenDisplay(nil)
		if p.dpy == nil {
			p.err = fmt.Errorf("XOpenDisplay failed")
			return
		}
		p.win = C.XCreateSimpleWindow(p.dpy, C.Window(u.view), C.int(r.Min.X), C.int(r.Min.Y),
			C.uint(r.Dx()), C.uint(r.Dy()), 0, 0, 0)
		C.XMapWindow(p.dpy, p.win)
		C.XFlush(p.dpy)
		p.at = r
		p.err = p.start("--wid=" + strconv.FormatUint(uint64(p.win), 10))
		return
	}
	if r != p.at {
		C.XMoveResizeWindow(p.dpy, p.win, C.int(r.Min.X), C.int(r.Min.Y), C.uint(r.Dx()), C.uint(r.Dy()))
		C.XFlush(p.dpy)
		p.at = r
	}
}

func (p *player) start(arg string) error {
	p.mpv = exec.Command("mpv", "--no-terminal", "--keep-open=yes", arg, p.file)
	return p.mpv.Start()
}

// stopLocked ends the player, its window and its file.
func (u *ui) stopLocked() {
	p := u.player
	if p == nil {
		return
	}
	u.player = nil
	if p.mpv != nil && p.mpv.Process != nil {
		_ = p.mpv.Process.Kill()
		go func() { _ = p.mpv.Wait() }()
	}
	if p.dpy != nil {
		C.XDestroyWindow(p.dpy, p.win)
		C.XCloseDisplay(p.dpy)
	}
	if p.file != "" {
		os.Remove(p.file)
	}
}
