package config

import (
	"slices"
	"strings"
)

// GUI is [gui]: what graphical clients (the desktop window, later Android) read, in
// both files: the bridge's config.toml holds what every device shares, a device's
// device.toml the same keys for that device alone, each set there over the shared one.
type GUI struct {
	Theme GUITheme `toml:"theme"`
}

// GUITheme is [gui.theme]: the graphical clients' theme. The TUI draws with its own
// [display.theme] and reads none of it. A color is "#rrggbb"; an empty value is unset.
type GUITheme struct {
	Mode  string   `toml:"mode"` // GUIThemeModes; empty is "system"
	Light GUIRoles `toml:"light"`
	Dark  GUIRoles `toml:"dark"`
	// Accounts is a network account's color, by its rooms' owner (domain.AccountRooms).
	Accounts map[string]string `toml:"accounts"`
	// People is a person's color, by their ID.
	People map[string]string `toml:"people"`
}

// GUIRoles is one mode's colors, a role each; an empty one is the window's built-in.
type GUIRoles struct {
	Accent     string `toml:"accent"`
	Background string `toml:"background"`
	Surface    string `toml:"surface"`
	Text       string `toml:"text"`
	Muted      string `toml:"muted"`
	Link       string `toml:"link"`
	Selection  string `toml:"selection"`
	Unread     string `toml:"unread"`
	Mention    string `toml:"mention"`
}

// GUIThemeModes are the modes [gui.theme] mode takes.
func GUIThemeModes() []string { return []string{"system", "light", "dark"} }

// GUIThemeRoles are the roles a mode colors, in GUIRoles' order.
func GUIThemeRoles() []string {
	return []string{"accent", "background", "surface", "text", "muted", "link", "selection", "unread", "mention"}
}

// role is the field of r named name; nil for no role.
func (r *GUIRoles) role(name string) *string {
	switch name {
	case "accent":
		return &r.Accent
	case "background":
		return &r.Background
	case "surface":
		return &r.Surface
	case "text":
		return &r.Text
	case "muted":
		return &r.Muted
	case "link":
		return &r.Link
	case "selection":
		return &r.Selection
	case "unread":
		return &r.Unread
	case "mention":
		return &r.Mention
	}
	return nil
}

// The tables of [gui.theme] whose keys are free: an account's, a person's.
const (
	GUIThemeAccounts = "accounts"
	GUIThemePeople   = "people"
)

// Get is the value of the key at path in [gui.theme] ("mode", "dark.accent",
// "people.<id>"), "" where unset; false for a path that names no key.
func (l GUITheme) Get(path string) (string, bool) {
	at, ok := l.at(path)
	if !ok {
		return "", false
	}
	return at.get(), true
}

// Set sets the key at path to value, an empty value unsetting it; false for a path
// that names no key.
func (l *GUITheme) Set(path, value string) bool {
	at, ok := l.at(path)
	if !ok {
		return false
	}
	at.set(value)
	return true
}

// Keys are the paths of every key l sets, sorted.
func (l GUITheme) Keys() []string {
	var out []string
	if l.Mode != "" {
		out = append(out, "mode")
	}
	for mode, roles := range map[string]*GUIRoles{"light": &l.Light, "dark": &l.Dark} {
		for _, name := range GUIThemeRoles() {
			if *roles.role(name) != "" {
				out = append(out, mode+"."+name)
			}
		}
	}
	for table, m := range map[string]map[string]string{GUIThemeAccounts: l.Accounts, GUIThemePeople: l.People} {
		for key := range m {
			out = append(out, table+"."+key)
		}
	}
	slices.Sort(out)
	return out
}

// themeKey is one key of a GUITheme, to read and write.
type themeKey struct {
	get func() string
	set func(string)
}

// at is the key path names in l.
func (l *GUITheme) at(path string) (themeKey, bool) {
	table, key, _ := strings.Cut(path, ".")
	field := func(p *string) themeKey { return themeKey{func() string { return *p }, func(v string) { *p = v }} }
	entry := func(m *map[string]string) themeKey {
		return themeKey{
			get: func() string { return (*m)[key] },
			set: func(v string) {
				if v == "" {
					delete(*m, key)
					if len(*m) == 0 {
						*m = nil // as a file without the table decodes
					}
					return
				}
				if *m == nil {
					*m = map[string]string{}
				}
				(*m)[key] = v
			},
		}
	}
	switch {
	case path == "mode":
		return field(&l.Mode), true
	case table == "light" && l.Light.role(key) != nil:
		return field(l.Light.role(key)), true
	case table == "dark" && l.Dark.role(key) != nil:
		return field(l.Dark.role(key)), true
	case table == GUIThemeAccounts && key != "":
		return entry(&l.Accounts), true
	case table == GUIThemePeople && key != "":
		return entry(&l.People), true
	}
	return themeKey{}, false
}
