package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/app"
)

// fakeMPV is mpv's IPC socket: it answers every command, the end reached as ended says,
// with an event before each answer, and hands each command on as text.
func fakeMPV(t *testing.T, ended bool) (string, chan string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mpv.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan string, 16)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		lines := bufio.NewScanner(conn)
		for lines.Scan() {
			var asked struct {
				Command []any `json:"command"`
			}
			if json.Unmarshal(lines.Bytes(), &asked) != nil {
				return
			}
			got <- strings.TrimSpace(fmt.Sprintln(asked.Command...))
			fmt.Fprintf(conn, "{\"event\":\"playback-restart\"}\n{\"request_id\":1,\"error\":\"success\",\"data\":%v}\n", ended)
		}
	}()
	return path, got
}

// sent is the next command mpv was sent.
func sent(t *testing.T, got chan string) string {
	t.Helper()
	select {
	case c := <-got:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("mpv was sent nothing")
		return ""
	}
}

// The player keys go to mpv while something plays: a skip by the step, the speed by its
// step within the bounds and back, and play/pause, from the start once it has ended;
// with nothing playing they pass the key on.
func TestThePlayerKeysGoToMPV(t *testing.T) {
	path, got := fakeMPV(t, false)
	u := &ui{w: new(app.Window), player: &player{mpv: new(exec.Cmd), ipc: path, speed: 1}}
	u.playerKey("player.back")
	if c := sent(t, got); c != "seek -10 relative" {
		t.Errorf("back: %q", c)
	}
	u.playerKey("player.faster")
	if c := sent(t, got); c != "set_property speed 1.25" {
		t.Errorf("faster: %q", c)
	}
	for range 8 {
		u.playerKey("player.slower")
		sent(t, got)
	}
	if u.player.speed != minSpeed {
		t.Errorf("slowest %v, want %v", u.player.speed, minSpeed)
	}
	u.playerKey("player.normal_speed")
	if c := sent(t, got); c != "set_property speed 1" {
		t.Errorf("normal: %q", c)
	}
	u.playerKey("player.play_pause")
	if c := sent(t, got) + "; " + sent(t, got); c != "get_property eof-reached; cycle pause" {
		t.Errorf("play/pause: %q", c)
	}

	path, got = fakeMPV(t, true)
	u.player = &player{mpv: new(exec.Cmd), ipc: path, speed: 1}
	u.playerKey("player.play_pause")
	var asked []string
	for range 3 {
		asked = append(asked, sent(t, got))
	}
	if c := strings.Join(asked, "; "); c != "get_property eof-reached; seek 0 absolute; set_property pause false" {
		t.Errorf("play/pause at the end: %q", c)
	}

	u.player = nil
	if u.playerKey("player.play_pause") {
		t.Error("taken with nothing playing")
	}
}
