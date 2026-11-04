package config

import (
	"strconv"
	"strings"
	"unicode"
)

// Key names and bindings as [keys] writes them, read alike by every client.

// namedKeys are the non-character key names Bubble Tea reports (tea.KeyPressMsg.String()), which
// the window's names follow too;
// f1–f20 match by pattern.
var namedKeys = map[string]bool{
	"enter": true, "esc": true, "tab": true, "space": true, "backspace": true,
	"delete": true, "insert": true, "up": true, "down": true, "left": true,
	"right": true, "home": true, "end": true, "pgup": true, "pgdown": true,
	"begin": true, "menu": true, "find": true, "select": true, "pause": true,
	"capslock": true, "numlock": true, "scrolllock": true, "printscreen": true,
	"plus": true, "minus": true, "comma": true, "period": true,
}

// modifiers are the prefixes a key name may carry, in any combination.
var modifiers = []string{"ctrl+", "alt+", "shift+", "meta+", "super+", "hyper+"}

// ValidKey reports whether name is modifiers over a single character or a named key.
func ValidKey(name string) bool {
	base := name
	for stripped := true; stripped; {
		stripped = false
		for _, mod := range modifiers {
			if rest, cut := strings.CutPrefix(base, mod); cut && rest != "" {
				base, stripped = rest, true
				break
			}
		}
	}
	if len([]rune(base)) == 1 {
		return true
	}
	if namedKeys[base] {
		return true
	}
	if digits, isFn := strings.CutPrefix(base, "f"); isFn {
		if n, err := strconv.Atoi(digits); err == nil && n >= 1 && n <= 20 {
			return true
		}
	}
	return false
}

// SplitKeys parses a comma-separated binding list, dropping blanks. "minus" names the - key
// ("-" alone unbinds) and "comma" names ",", which the separator makes unwritable.
func SplitKeys(list string) []string {
	parts := strings.Split(list, ",")
	keys := make([]string, 0, len(parts))
	for _, p := range parts {
		switch key := strings.TrimSpace(p); key {
		case "":
		case "comma":
			keys = append(keys, ",")
		case "minus":
			keys = append(keys, "-")
		default:
			keys = append(keys, NormalizeSequence(key))
		}
	}
	return keys
}

// shorthandSteps is the longest run-together sequence accepted without spaces ("gg").
const shorthandSteps = 3

// NormalizeSequence canonicalizes a binding to steps joined by one space. "gg" is accepted as
// "g g" only for 2–3 letters/digits that are not a key name, so a typo like "escape" stays
// for validation to report.
func NormalizeSequence(binding string) string {
	if fields := strings.Fields(binding); len(fields) > 1 {
		return strings.Join(fields, " ")
	}
	if ValidKey(binding) {
		return binding
	}
	runes := []rune(binding)
	if len(runes) < 2 || len(runes) > shorthandSteps {
		return binding
	}
	steps := make([]string, len(runes))
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return binding
		}
		steps[i] = string(r)
	}
	return strings.Join(steps, " ")
}

// SequenceValid reports whether every step of a binding is a valid key.
func SequenceValid(binding string) bool {
	for step := range strings.SplitSeq(binding, " ") {
		if !ValidKey(step) {
			return false
		}
	}
	return true
}
