package main

/*
#cgo pkg-config: x11
#include <X11/Xlib.h>
*/
import "C"

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// player is one attachment playing in mpv: a video in a child X window of the Gio
// window (mpv --wid), a voice note with no window. All of it runs on the frame loop's
// goroutine but the fetch, which only fills file or err under ui.mu, and what [keys.player]
// asks of mpv, over its IPC socket on a goroutine of its own (control).
type player struct {
	msg   domain.Message
	file  string // the attachment's bytes, in a private temp file; "" while fetching
	err   error
	dpy   *C.Display
	win   C.Window
	at    image.Rectangle
	mpv   *exec.Cmd
	video bool
	ipc   string  // mpv's IPC socket
	speed float64 // what it plays at, as the keys set it

	talk sync.Mutex // one exchange with mpv at a time
	conn net.Conn
	read *bufio.Reader
}

// The player keys' speeds, as the desktop's and the TUI's (audio.MinSpeed, MaxSpeed,
// speedStep).
const (
	minSpeed  = 0.25
	maxSpeed  = 4.0
	speedStep = 0.25
)

// play fetches m's attachment and starts it once the next frame places it.
func (u *ui) play(ctx context.Context, m domain.Message) {
	u.stopLocked()
	p := &player{msg: m, video: m.Media.Type == domain.MediaVideo, speed: u.device.Display.Media.Audio.PlaySpeed()}
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
	p.ipc = p.file + ".sock"
	p.mpv = exec.Command("mpv", "--no-terminal", "--keep-open=yes", "--input-ipc-server="+p.ipc,
		"--speed="+strconv.FormatFloat(p.speed, 'f', -1, 64), arg, p.file)
	return p.mpv.Start()
}

// playerKey is a [keys.player] action while something plays, as the desktop's: false
// when nothing does, so the key does what it does elsewhere.
func (u *ui) playerKey(path string) bool {
	p := u.player
	if p == nil {
		return false
	}
	if path == "player.stop" {
		u.stopLocked()
		return true
	}
	if p.mpv == nil {
		return false // fetching, or it could not start
	}
	audio := u.device.Display.Media.Audio
	speed := func(f float64) [][]any {
		p.speed = min(max(f, minSpeed), maxSpeed)
		return [][]any{{"set_property", "speed", p.speed}}
	}
	var asked [][]any
	switch path {
	case "player.play_pause":
		asked = nil // decided on mpv's answer (control)
	case "player.back", "player.forward":
		step := audio.SkipStep().Seconds()
		if path == "player.back" {
			step = -step
		}
		asked = [][]any{{"seek", step, "relative"}}
	case "player.slower":
		asked = speed(p.speed - speedStep)
	case "player.faster":
		asked = speed(p.speed + speedStep)
	case "player.normal_speed":
		asked = speed(audio.PlaySpeed())
	default:
		return false
	}
	go func() {
		if err := p.control(asked); err != nil {
			u.say("The player did not take it: " + err.Error())
		}
	}()
	return true
}

// control sends mpv the commands asked, or with none toggles pause, from the start again
// once it has played to the end (--keep-open holds the last frame).
func (p *player) control(asked [][]any) error {
	p.talk.Lock()
	defer p.talk.Unlock()
	if asked == nil {
		ended, err := p.ask([]any{"get_property", "eof-reached"})
		if err != nil {
			return err
		}
		asked = [][]any{{"cycle", "pause"}}
		if ended == true {
			asked = [][]any{{"seek", 0, "absolute"}, {"set_property", "pause", false}}
		}
	}
	for _, c := range asked {
		if _, err := p.ask(c); err != nil {
			return err
		}
	}
	return nil
}

// ask sends mpv one command and waits for its answer: its data, or its error. Events
// mpv sends meanwhile are passed over.
func (p *player) ask(command []any) (any, error) {
	if p.conn == nil {
		conn, err := net.DialTimeout("unix", p.ipc, time.Second)
		if err != nil {
			return nil, fmt.Errorf("mpv is not listening yet: %w", err)
		}
		p.conn, p.read = conn, bufio.NewReader(conn)
	}
	fail := func(err error) (any, error) {
		_ = p.conn.Close()
		p.conn = nil
		return nil, err
	}
	line, err := json.Marshal(map[string]any{"command": command, "request_id": 1})
	if err != nil {
		return nil, err
	}
	_ = p.conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := p.conn.Write(append(line, '\n')); err != nil {
		return fail(err)
	}
	for {
		text, err := p.read.ReadBytes('\n')
		if err != nil {
			return fail(err)
		}
		var answer struct {
			Error     string `json:"error"`
			Data      any    `json:"data"`
			RequestID *int   `json:"request_id"`
		}
		if json.Unmarshal(text, &answer) != nil || answer.RequestID == nil {
			continue // an event
		}
		if answer.Error != "success" {
			return nil, errors.New(answer.Error)
		}
		return answer.Data, nil
	}
}

// speedNote is how fast the player plays, "" at its speed for the place.
func (p *player) speedNote(audio config.Audio) string {
	if p.speed == audio.PlaySpeed() {
		return ""
	}
	return " at " + strconv.FormatFloat(p.speed, 'f', -1, 64) + "×"
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
	go func() {
		p.talk.Lock() // after any exchange under way
		defer p.talk.Unlock()
		if p.conn != nil {
			_ = p.conn.Close()
		}
		if p.ipc != "" {
			os.Remove(p.ipc)
		}
	}()
	if p.dpy != nil {
		C.XDestroyWindow(p.dpy, p.win)
		C.XCloseDisplay(p.dpy)
	}
	if p.file != "" {
		os.Remove(p.file)
	}
}
