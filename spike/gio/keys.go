package main

import (
	"strconv"
	"strings"
	"unicode"

	"gioui.org/io/key"

	"github.com/EugeneShtoka/kith/internal/config"
)

// The window's keys, from the device's [keys]: each press named as the TUI names it, and
// sequences ("g g") held until they complete. A port of the desktop's keymap.ts.

// named are Gio's names for the keys [keys] names by word.
var named = map[key.Name]string{
	key.NameReturn: "enter", key.NameEnter: "enter", key.NameEscape: "esc", key.NameTab: "tab",
	key.NameSpace: "space", key.NameDeleteBackward: "backspace", key.NameDeleteForward: "delete",
	key.NameUpArrow: "up", key.NameDownArrow: "down", key.NameLeftArrow: "left", key.NameRightArrow: "right",
	key.NameHome: "home", key.NameEnd: "end", key.NamePageUp: "pgup", key.NamePageDown: "pgdown",
}

// shiftedDigits are a US layout's shifted digit row, so ctrl+shift+1 is named by its key.
// shortcut: US layout only; another layout's shifted digits name as their symbol.
const shiftedDigits = ")!@#$%^&*("

// keyName is e as [keys] names it ("ctrl+k", "G", "shift+tab"), or "" for a modifier
// alone. Gio names a character key by the symbol the layout gives it with shift applied,
// a letter in upper case: unchorded it is that symbol, a letter cased by shift; chorded,
// the unshifted key, so ctrl+shift+k is not ctrl+K.
func keyName(e key.Event) string {
	word, isWord := named[e.Name]
	if !isWord && strings.HasPrefix(string(e.Name), "F") {
		if n, err := strconv.Atoi(string(e.Name[1:])); err == nil && n >= 1 && n <= 20 {
			word, isWord = strings.ToLower(string(e.Name)), true
		}
	}
	r := []rune(string(e.Name))
	if !isWord && len(r) != 1 {
		return "" // Ctrl, Shift, Alt, Super, …
	}
	shift := e.Modifiers.Contain(key.ModShift)
	chord := e.Modifiers&(key.ModCtrl|key.ModAlt|key.ModSuper) != 0
	base := word
	switch {
	case isWord:
	case !chord:
		if unicode.IsLetter(r[0]) && !shift {
			return string(unicode.ToLower(r[0]))
		}
		return string(r)
	default:
		base = string(unicode.ToLower(r[0]))
		if i := strings.IndexRune(shiftedDigits, r[0]); i >= 0 {
			base = strconv.Itoa(i)
		}
	}
	var b strings.Builder
	for _, m := range []struct {
		mod  key.Modifiers
		name string
	}{{key.ModCtrl, "ctrl+"}, {key.ModAlt, "alt+"}, {key.ModShift, "shift+"}, {key.ModSuper, "super+"}} {
		if e.Modifiers.Contain(m.mod) && (m.mod != key.ModShift || isWord || chord) {
			b.WriteString(m.name)
		}
	}
	return b.String() + base
}

// action is one the window binds: its [keys] path, and whether it acts while a field
// takes typing.
type action struct {
	path     string
	anywhere bool
}

// countLimit bounds a count typed before a motion ("12j"), as the TUI's does.
const countLimit = 9999

// keymap resolves presses to actions: every action bound to the press, in priority
// order, as panes bind one key apart. A sequence is held while it can still complete; a
// press that continues none ends it and is judged alone. Digits that start no sequence
// are a count, which the next action takes and any other press drops. While a field
// takes typing only anywhere actions are consulted, each by a press that types no text.
type keymap struct {
	bound    map[string][]action
	prefixes map[string]bool
	held     []string
	typed    int
	count    int // what the actions press last returned were typed with; 0 for none
}

func newKeymap(keys config.Keys, actions []action) *keymap {
	k := &keymap{bound: map[string][]action{}, prefixes: map[string]bool{}}
	for _, a := range actions {
		binding, _ := keys.Binding(a.path)
		for _, seq := range config.SplitKeys(binding) {
			k.bound[seq] = append(k.bound[seq], a)
			steps := strings.Split(seq, " ")
			for i := 1; i < len(steps); i++ {
				k.prefixes[strings.Join(steps[:i], " ")] = true
			}
		}
	}
	return k
}

// press is one named press: the paths of the actions it completes, in priority order.
func (k *keymap) press(name string, typing bool) []string {
	k.count = 0
	if typing {
		k.held, k.typed = nil, 0
		var out []string
		if len([]rune(name)) == 1 {
			return nil // it types itself
		}
		for _, a := range k.bound[name] {
			if a.anywhere {
				out = append(out, a.path)
			}
		}
		return out
	}
	seq := strings.Join(append(k.held, name), " ")
	if actions, ok := k.bound[seq]; ok {
		k.held, k.count, k.typed = nil, k.typed, 0
		out := make([]string, len(actions))
		for i, a := range actions {
			out[i] = a.path
		}
		return out
	}
	if k.prefixes[seq] {
		k.held = append(k.held, name)
		return nil
	}
	if len(k.held) > 0 {
		k.held = nil
		return k.press(name, false)
	}
	if len(name) == 1 && name[0] >= '0' && name[0] <= '9' && (k.typed > 0 || name != "0") {
		k.typed = min(k.typed*10+int(name[0]-'0'), countLimit)
		return nil
	}
	k.typed = 0
	return nil
}

// pending is the count and the sequence typed so far ("12", "g"), or "".
func (k *keymap) pending() string {
	var parts []string
	if k.typed > 0 {
		parts = append(parts, strconv.Itoa(k.typed))
	}
	return strings.Join(append(parts, k.held...), " ")
}

// moved is where a [keys.nav] motion takes a cursor at `at` among n rows, page of them
// on screen, count typed before it (0: none); false for a path that is no motion. The
// ends with a count are that row ("12G"), as the TUI's are.
func moved(path string, count, at, page, n int) (int, bool) {
	by := func(delta int) int { return max(0, min(n-1, at+delta)) }
	half := max(1, page/2)
	switch path {
	case "nav.up":
		return by(-max(count, 1)), true
	case "nav.down":
		return by(max(count, 1)), true
	case "nav.page_up":
		return by(-max(page, 1)), true
	case "nav.page_down":
		return by(max(page, 1)), true
	case "nav.half_page_up":
		return by(-half), true
	case "nav.half_page_down":
		return by(half), true
	case "nav.select_oldest", "nav.scroll_oldest":
		if count > 0 {
			return by(count - 1 - at), true
		}
		return 0, true
	case "nav.select_newest", "nav.scroll_newest":
		if count > 0 {
			return by(count - 1 - at), true
		}
		return n - 1, true
	}
	return 0, false
}
