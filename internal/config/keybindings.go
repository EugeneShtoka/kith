package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Jump is one chord and where it goes.
type Jump struct {
	Chord  string `toml:"chord"`
	Target string `toml:"target"`
}

// Jumps is the list of jump bindings.
type Jumps []Jump

// Validate refuses a chord bound twice.
func (j Jumps) Validate() error {
	seen := make(map[string]string, len(j))
	for _, entry := range j {
		if entry.Chord == "" {
			return fmt.Errorf("keys.jump: a binding with no chord, going to %q", entry.Target)
		}
		if first, dup := seen[entry.Chord]; dup {
			return fmt.Errorf("keys.jump: %q is bound twice — to %q and to %q; a chord goes to one place",
				entry.Chord, first, entry.Target)
		}
		seen[entry.Chord] = entry.Target
	}
	return nil
}

// TagKey is one chord and the tag it puts the room in, or takes it out of.
type TagKey struct {
	Chord string `toml:"chord"`
	Tag   string `toml:"tag"`
}

// TagKeys is the list of tag bindings.
type TagKeys []TagKey

// Map is the bindings as the keymap wants them: chord to tag name.
func (t TagKeys) Map() map[string]string {
	out := make(map[string]string, len(t))
	for _, entry := range t {
		if entry.Chord != "" && entry.Tag != "" {
			out[entry.Chord] = entry.Tag
		}
	}
	return out
}

// Map is the bindings as the keymap wants them.
func (j Jumps) Map() map[string]string {
	out := make(map[string]string, len(j))
	for _, entry := range j {
		if entry.Chord != "" && entry.Target != "" {
			out[entry.Chord] = entry.Target
		}
	}
	return out
}

// Keys are kith's configurable keybindings. Bindings holds each action's keys by path
// ("timeline.reply" is [keys.timeline] reply), as a comma-separated list ("k,up"); the
// paths, defaults and descriptions live in keySections.
type Keys struct {
	Bindings map[string]string `toml:"-"`
	Jump     Jumps             `toml:"jump"`
	Tag      TagKeys           `toml:"tag"`
}

// UnmarshalTOML reads [keys]: each binding by its path, and the jump and tag lists.
// A path keySections does not name is refused, as an unknown key is everywhere else.
func (k *Keys) UnmarshalTOML(data any) error {
	table, ok := data.(map[string]any)
	if !ok {
		return errors.New("keys: want a table")
	}
	k.Bindings = map[string]string{}
	var unknown []string
	set := func(path string, value any) error {
		if _, known := keyByPath[path]; !known {
			unknown = append(unknown, "keys."+path)
			return nil
		}
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("keys.%s: want a string of keys, like \"k,up\"", path)
		}
		k.Bindings[path] = s
		return nil
	}
	for name, value := range table {
		var err error
		switch sub, isTable := value.(map[string]any); {
		case name == "jump":
			k.Jump, err = chordEntries(name, value, "target", func(c, t string) Jump { return Jump{c, t} })
		case name == "tag":
			k.Tag, err = chordEntries(name, value, "tag", func(c, t string) TagKey { return TagKey{c, t} })
		case isTable:
			for action, keys := range sub {
				if err = set(name+"."+action, keys); err != nil {
					break
				}
			}
		default:
			err = set(name, value)
		}
		if err != nil {
			return err
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return fmt.Errorf("unknown keys: %s", strings.Join(unknown, ", "))
	}
	return nil
}

// chordEntries reads [[keys.<list>]]: tables of a chord and one other string, field.
func chordEntries[T any](list string, value any, field string, entry func(chord, other string) T) ([]T, error) {
	var entries []map[string]any
	switch v := value.(type) {
	case []map[string]any:
		entries = v
	case []any: // [] or inline tables
		for _, e := range v {
			entry, ok := e.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("keys.%s: want tables of chord and %s", list, field)
			}
			entries = append(entries, entry)
		}
	default:
		return nil, fmt.Errorf("keys.%s: want tables of chord and %s", list, field)
	}
	out := make([]T, 0, len(entries))
	for _, table := range entries {
		var pair [2]string
		for name, v := range table {
			i := slices.Index([]string{"chord", field}, name)
			s, ok := v.(string)
			switch {
			case i < 0:
				return nil, fmt.Errorf("unknown keys: keys.%s.%s", list, name)
			case !ok:
				return nil, fmt.Errorf("keys.%s.%s: want a string", list, name)
			}
			pair[i] = s
		}
		out = append(out, entry(pair[0], pair[1]))
	}
	return out, nil
}

// bindingsTOML renders the bindings that differ from defaults: [keys]' own as values,
// the rest as [keys.*] tables, in keySections' order.
func (k Keys) bindingsTOML(defaults Keys) (values, tables string) {
	var own, rest strings.Builder
	for _, section := range keySections {
		var b strings.Builder
		for _, bind := range section.binds {
			if got := k.Bindings[bind.path]; got != defaults.Bindings[bind.path] {
				fmt.Fprintf(&b, "%s = %s\n", bind.path[strings.LastIndex(bind.path, ".")+1:], quoteTOML(got))
			}
		}
		switch {
		case b.Len() == 0:
		case section.table == "":
			own.WriteString(b.String())
		default:
			rest.WriteString("\n[keys." + section.table + "]\n" + b.String())
		}
	}
	return own.String(), rest.String()
}

// DefaultKeys are the built-in bindings, from keySections.
func DefaultKeys() Keys {
	return Keys{Bindings: maps.Clone(defaultKeys.Bindings), Tag: slices.Clone(defaultKeys.Tag)}
}

var defaultKeys = func() Keys {
	k := Keys{Bindings: map[string]string{}}
	for path, b := range keyByPath {
		k.Bindings[path] = b.def
	}
	k.Tag = starterTagKeys()
	return k
}()

// Binding is the value at path ("timeline.reply"); false for an unknown path.
func (k Keys) Binding(path string) (string, bool) {
	_, known := keyByPath[path]
	return k.Bindings[path], known
}

// KeyDoc is the one-line description of the binding at path; false for an unknown path.
func KeyDoc(path string) (string, bool) {
	b, ok := keyByPath[path]
	return b.doc, ok
}

// keyByPath is keySections by path. defaultKeys reads it, so it is built first: the
// starter config defaultKeys decodes checks its paths against it.
var keyByPath = func() map[string]keyBinding {
	out := map[string]keyBinding{}
	for _, section := range keySections {
		for _, b := range section.binds {
			out[b.path] = b
		}
	}
	return out
}()

// FillDefaults sets every unset binding to its built-in value.
func (k *Keys) FillDefaults() {
	if k.Bindings == nil {
		k.Bindings = map[string]string{}
	}
	for path, def := range defaultKeys.Bindings {
		if k.Bindings[path] == "" {
			k.Bindings[path] = def
		}
	}
	if k.Tag == nil {
		k.Tag = DefaultKeys().Tag // unset takes the starter's; decode keeps an explicit []
	}
}

// KeyTable is one [keys.*] table as the settings screen lists it: its name ("" is
// [keys] itself), its prose, and its bindings in order.
type KeyTable struct {
	Name, Intro string
	Bindings    []KeyBindingDoc
}

// KeyBindingDoc is one binding: its path ("timeline.reply"), its default, what it does,
// and default.toml's further note on it.
type KeyBindingDoc struct {
	Path, Default, Doc, Note string
}

// KeyTables is every key table, in default.toml's order.
func KeyTables() []KeyTable {
	out := make([]KeyTable, 0, len(keySections))
	for _, section := range keySections {
		t := KeyTable{Name: section.table, Intro: section.intro}
		for _, b := range section.binds {
			t.Bindings = append(t.Bindings, KeyBindingDoc{Path: b.path, Default: b.def, Doc: b.doc, Note: b.note})
		}
		out = append(out, t)
	}
	return out
}

// SetBinding writes the binding at path; "" restores its default.
func (k *Keys) SetBinding(path, value string) error {
	if _, known := keyByPath[path]; !known {
		return fmt.Errorf("config: no key binding %q", path)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = defaultKeys.Bindings[path]
	}
	if k.Bindings == nil {
		k.Bindings = map[string]string{}
	}
	k.Bindings[path] = value
	return nil
}
