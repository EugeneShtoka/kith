package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/widget"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// folderWith is a commands folder holding greet, which prints its argument and room,
// and a note that is not executable.
func folderWith(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "greet"), []byte("#!/bin/sh\necho \"hi $1 in $KITH_ROOM_ID\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("not a command"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// ranOnce types words, waits for the command to come back and puts its output where it goes.
func ranOnce(t *testing.T, u *ui, words string) {
	t.Helper()
	u.composer.SetText(words)
	if !u.command(context.Background(), &u.composer, "", words) {
		t.Fatalf("%q: taken as a message", words)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		u.mu.Lock()
		ran := u.ran != nil
		u.mu.Unlock()
		if ran {
			break
		}
		if u.running == "" || time.Now().After(deadline) {
			t.Fatalf("%q: never ran; said %q", words, u.note)
		}
		time.Sleep(10 * time.Millisecond)
	}
	u.landCommand(context.Background(), C{})
}

// A command of the folder runs with its argument, and what it printed goes to the
// composer, or as its block's output says; only the folder's executables are offered.
func TestAFolderCommandRunsAndLands(t *testing.T) {
	room := domain.RoomID("matrix:a/!r")
	u := &ui{w: new(app.Window), cmds: commandsAt{dir: folderWith(t)}, current: room, mentions: map[*widget.Editor][]domain.Mention{}}
	if got := u.commandsHere(); !slices.Equal(got, []string{"greet"}) {
		t.Errorf("offered %q, want greet", got)
	}
	ranOnce(t, u, "/greet bob")
	if got := u.composer.Text(); got != "hi bob in "+string(room) {
		t.Errorf("composer %q", got)
	}
	u.device.UserCommands = []config.Command{{Name: "greet", Output: "status"}}
	ranOnce(t, u, "/greet ann")
	if got := u.composer.Text(); got != "" || u.note != "/greet: hi ann in "+string(room) {
		t.Errorf("status: composer %q, said %q", got, u.note)
	}
}

// Outside its places a command is refused and its words kept; a slash word the folder has
// no command of is a message.
func TestAFolderCommandOutsideItsPlaces(t *testing.T) {
	u := &ui{cmds: commandsAt{dir: folderWith(t)}, current: "matrix:a/!r"}
	u.device.UserCommands = []config.Command{{Name: "greet", In: []string{"work"}}}
	u.composer.SetText("/greet")
	if !u.command(context.Background(), &u.composer, "", "/greet") || u.note != "/greet is only for work" || u.running != "" {
		t.Errorf("said %q, running %q", u.note, u.running)
	}
	if u.composer.Text() != "/greet" {
		t.Errorf("words %q, want kept", u.composer.Text())
	}
	if len(u.commandsHere()) != 0 {
		t.Errorf("offered %q where it does not run", u.commandsHere())
	}
	for _, words := range []string{"/note.txt", "/nothing here", "/../greet"} {
		if u.command(context.Background(), &u.composer, "", words) {
			t.Errorf("%q: taken as a command", words)
		}
	}
}
