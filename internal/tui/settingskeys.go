package tui

import (
	"errors"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
)

// The Keys group: a row per key table, each opening its bindings, plus the
// [[keys.jump]] records. A binding is typed in place as comma-separated keys; empty
// restores the default, "-" binds nothing, and a clash is refused with the keymap's reason.

const keysPrefix = "keys:"

// keyTableLabels name the key tables; [keys] itself is "Global".
var keyTableLabels = map[string]string{
	"": "Global", "rail": "Rail", "nav": "Moving around", "rooms": "Room list", "sort": "Sorting the room list",
	"search": "Search", "timeline": "Timeline", "insert": "Writing", "edit": "Editing text",
	"completion": "Completion", "spell": "Spelling", "emoji": "Emoji", "composer": "Composer",
	"picker": "Lists and choosers", "react": "Reacting", "prompt": "Prompts", "confirm": "Questions",
	"verify": "Verification", "player": "Player",
}

// keyTableKey is a key table's group key; [keys] itself is keys:global.
func keyTableKey(name string) string {
	if name == "" {
		return keysPrefix + "global"
	}
	return keysPrefix + name
}

// keyTableSettings are the rows leading to each table's bindings (opened like a
// record table's row, see chooseSetting).
func keyTableSettings() []setting {
	var out []setting
	for _, t := range config.KeyTables() {
		n := len(t.Bindings)
		out = append(out, setting{
			key: keyTableKey(t.Name), group: "keys", label: keyTableLabel(t.Name), kind: settingOpen, doc: t.Intro,
			show: func(config.Settings) string { return showCount(n, "none") + " keys" },
		})
	}
	return out
}

func keyTableLabel(name string) string {
	if label, ok := keyTableLabels[name]; ok {
		return label
	}
	return propertyLabel(name)
}

// keyTableOf is the key table a keys: group names.
func keyTableOf(group string) (config.KeyTable, bool) {
	name := strings.TrimPrefix(group, keysPrefix)
	if name == "global" {
		name = ""
	}
	for _, t := range config.KeyTables() {
		if t.Name == name {
			return t, true
		}
	}
	return config.KeyTable{}, false
}

func keyBindingRows(t config.KeyTable) []setting {
	group := keyTableKey(t.Name)
	rows := make([]setting, 0, len(t.Bindings))
	for _, b := range t.Bindings {
		path := b.Path
		doc := b.Doc
		if b.Note != "" {
			doc += ". " + b.Note
		}
		doc += "\nDefault: " + spellBinding(b.Default)
		rows = append(rows, setting{
			key: keysPrefix + path, group: group, kind: settingText,
			label: strings.ToUpper(b.Doc[:1]) + b.Doc[1:], doc: doc,
			help: "keys, comma-separated (k,up; g g for a sequence) — empty restores " + spellBinding(b.Default) + ", - binds nothing",
			show: func(c config.Settings) string { v, _ := c.Device.Keys.Binding(path); return spellBinding(v) },
			edit: func(c config.Settings) string { v, _ := c.Device.Keys.Binding(path); return v },
			set:  func(c *config.Settings, v string) error { return setBinding(c, path, v) },
		})
	}
	return rows
}

// spellBinding is a binding as its row reads; "none" when bound to nothing.
func spellBinding(v string) string {
	if strings.TrimSpace(v) == unbind || strings.TrimSpace(v) == "" {
		return "none"
	}
	return v
}

// setBinding writes a binding, refusing one that adds a keymap problem (a clash, a
// shadowed sequence, a key the terminal never reports) with its reason.
func setBinding(c *config.Settings, path, value string) error {
	before := keymapFor(c.Device).issues
	if err := c.Device.Keys.SetBinding(path, value); err != nil {
		return err //nolint:wrapcheck // config says what is wrong
	}
	var added []string
	for _, issue := range keymapFor(c.Device).issues {
		if !slices.Contains(before, issue) {
			added = append(added, issue)
		}
	}
	if len(added) > 0 {
		return errors.New(strings.Join(added, "; "))
	}
	return nil
}
