package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// /shortcut, as the TUI's and the desktop's: keys become the sequence that goes to the
// open room, a [[keys.jump]] in the device file, the room's earlier one replaced; with
// none it asks, starting from the room's own, and empty unbinds it. A sequence already
// bound, or one that starts or is started by a bound one, is refused.
// shortcut: the room only; the TUI's binds the rail's space or tag too when the rail has
// the focus.

// shortcut binds keys to room, or asks which with none.
func (u *ui) shortcut(room domain.Room, keys string) {
	if strings.TrimSpace(keys) != "" {
		u.bindShortcut(room, keys)
		return
	}
	current := ""
	for _, j := range u.keyConf.Jump {
		if jumpsTo(j, room.ID) {
			current = j.Chord
		}
	}
	u.prompting = u.newPrompt("Shortcut for "+u.roomName(room), "Keys as [keys] names them, like g a; empty unbinds", current,
		func(keys string) { u.bindShortcut(room, keys) })
}

// jumpsTo is whether j goes to room.
func jumpsTo(j config.Jump, room domain.RoomID) bool {
	to, ok := domain.ParseJump(j.Target)
	return ok && to.Kind == domain.JumpRoom && to.Name == string(room)
}

// bindShortcut writes keys as room's jump, or unbinds it for none, and says what came of it.
func (u *ui) bindShortcut(room domain.Room, keys string) {
	name := u.roomName(room)
	say := func(note string) { u.note, u.noteAt = note, time.Now() }
	seq := config.NormalizeSequence(strings.TrimSpace(keys))
	jumps := slices.DeleteFunc(slices.Clone(u.keyConf.Jump), func(j config.Jump) bool { return jumpsTo(j, room.ID) })
	if seq != "" {
		others := u.keyConf
		others.Jump = jumps
		if !config.SequenceValid(seq) {
			say(fmt.Sprintf("%q is not a key name", keys))
			return
		}
		if why := shortcutConflict(others, seq); why != "" {
			say(seq + ": " + why)
			return
		}
		jumps = append(jumps, config.Jump{Chord: seq, Target: domain.JumpTarget{Kind: domain.JumpRoom, Name: string(room.ID)}.String()})
	}
	if len(jumps) == 0 {
		jumps = nil // the writer then leaves the section out
	}
	was, now := config.Device{}, config.Device{}
	was.Keys.Jump, now.Keys.Jump = u.keyConf.Jump, jumps
	written, err := config.UpdateDevice(u.devicePath, was, now)
	if err != nil {
		say(name + " was not bound: " + err.Error())
		return
	}
	u.keyConf.Jump = written.Keys.Jump
	u.jumps = written.Keys.Jump
	u.keymap = newKeymap(u.keyConf, actions)
	if seq == "" {
		say("Unbound " + name)
	} else {
		say(seq + " now goes to " + name)
	}
}

// shortcutConflict is why seq cannot be a jump among keys' bindings, "" when it can: it
// is bound, the start of a binding, or a binding starts it (the TUI's jumpConflict).
func shortcutConflict(keys config.Keys, seq string) string {
	paths := slices.Sorted(maps.Keys(keys.Bindings))
	all := make([]action, len(paths))
	for i, path := range paths {
		all[i] = action{path: path}
	}
	k := newKeymap(keys, all)
	for _, t := range keys.Tag {
		k.bind(t.Chord, action{path: "tag." + t.Tag})
	}
	means := func(seq string) string {
		path := k.bound[seq][0].path
		if tag, ok := strings.CutPrefix(path, "tag."); ok {
			return "putting rooms in " + tag + " ([[keys.tag]])"
		}
		if strings.HasPrefix(path, "jump.") {
			return "a jump ([[keys.jump]])"
		}
		return path
	}
	if len(k.bound[seq]) > 0 {
		return "already bound to " + means(seq)
	}
	if k.prefixes[seq] {
		return "the start of a longer binding, which would become unreachable"
	}
	steps := strings.Split(seq, " ")
	for i := 1; i < len(steps); i++ {
		if prefix := strings.Join(steps[:i], " "); len(k.bound[prefix]) > 0 {
			return fmt.Sprintf("%s already means %s on its own, so the rest would never arrive", prefix, means(prefix))
		}
	}
	return ""
}
