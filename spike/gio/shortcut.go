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

// /shortcut, as the TUI's and the desktop's: keys become the sequence that goes to a
// place, a [[keys.jump]] in the device file, the place's earlier one replaced; with none
// it asks, starting from the place's own, and empty unbinds it. The place is the open
// room, or the rail's selected space or tag, which Tab in the prompt switches to (the
// TUI's bindingTargets, from the composer; the desktop binds rooms only). A sequence
// already bound, or one that starts or is started by a bound one, is refused.

// shortcut binds keys to the first place shortcutTargets offers, or asks with none.
func (u *ui) shortcut(room domain.Room, keys string) {
	targets := u.shortcutTargets(room)
	if len(targets) == 0 {
		u.note, u.noteAt = "Nothing to bind a shortcut to: open a room, or select a space or tag", time.Now()
		return
	}
	if strings.TrimSpace(keys) != "" {
		u.bindShortcut(targets[0], keys)
		return
	}
	u.askShortcut(targets, 0, "")
}

// askShortcut asks for the keys of targets[at], starting from typed, or with none its
// own; Tab asks for the next target, keeping what was typed.
func (u *ui) askShortcut(targets []domain.JumpTarget, at int, typed string) {
	to := targets[at]
	if typed == "" {
		typed = u.sequenceTo(to)
	}
	hint := "Keys like g a; empty unbinds"
	if len(targets) > 1 {
		hint += "; Tab: " + u.targetName(targets[(at+1)%len(targets)])
	}
	p := u.newPrompt("Shortcut for "+u.targetName(to), hint, typed, func(keys string) { u.bindShortcut(to, keys) })
	if len(targets) > 1 {
		p.tab = func() { u.askShortcut(targets, (at+1)%len(targets), p.field.Text()) }
	}
	u.prompting = p
}

// shortcutTargets are the places /shortcut binds here: the open room, then the rail's
// selected space or tag.
func (u *ui) shortcutTargets(room domain.Room) []domain.JumpTarget {
	var out []domain.JumpTarget
	if room.ID != "" && !room.IsInvite() {
		out = append(out, domain.JumpTarget{Kind: domain.JumpRoom, Name: string(room.ID)})
	}
	if u.rail.sel < len(u.rail.rows) {
		if row := u.rail.rows[u.rail.sel]; row.Space != "" || strings.HasPrefix(row.Key, "tag:") {
			out = append(out, domain.JumpTarget{Kind: domain.JumpHome, Name: domain.HomeEntry(row.Key)})
		}
	}
	return out
}

// railRowOf is the first rail row a home entry names, -1 for none.
func (u *ui) railRowOf(entry string) int {
	return slices.IndexFunc(u.rail.rows, func(r domain.RailRow) bool { return domain.NamesHome(entry, r.Key, r.Label) })
}

// targetName is a place as the window shows it.
func (u *ui) targetName(t domain.JumpTarget) string {
	if t.Kind == domain.JumpRoom {
		if r, ok := u.roomOf(domain.RoomID(t.Name)); ok {
			return u.roomName(r)
		}
		return t.Name
	}
	if row := u.railRowOf(t.Name); row >= 0 {
		return u.rail.rows[row].Label
	}
	return t.Name
}

// samePlace is whether a and b go to the same place: a home entry by the rail row it
// names, as the TUI's samePlace.
func (u *ui) samePlace(a, b domain.JumpTarget) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Kind == domain.JumpHome {
		row := u.railRowOf(a.Name)
		return row >= 0 && row == u.railRowOf(b.Name)
	}
	return a.Name == b.Name
}

// jumpsTo is whether j goes to the place to.
func (u *ui) jumpsTo(j config.Jump, to domain.JumpTarget) bool {
	at, ok := domain.ParseJump(j.Target)
	return ok && u.samePlace(at, to)
}

// sequenceTo is the sequence that goes to the place to now, "" for none.
func (u *ui) sequenceTo(to domain.JumpTarget) string {
	current := ""
	for _, j := range u.keyConf.Jump {
		if u.jumpsTo(j, to) {
			current = j.Chord
		}
	}
	return current
}

// bindShortcut writes keys as the jump to a place, or unbinds it for none, and says
// what came of it.
func (u *ui) bindShortcut(to domain.JumpTarget, keys string) {
	name := u.targetName(to)
	say := func(note string) { u.note, u.noteAt = note, time.Now() }
	seq := config.NormalizeSequence(strings.TrimSpace(keys))
	jumps := slices.DeleteFunc(slices.Clone(u.keyConf.Jump), func(j config.Jump) bool { return u.jumpsTo(j, to) })
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
		jumps = append(jumps, config.Jump{Chord: seq, Target: to.String()})
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
