package tui

import (
	"strconv"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
)

// Every config property (config.Properties) has a settings row: hand-written in
// settingsList where a setting needs more than its key, otherwise generated from the
// property's kind, with default.toml's text shown beneath the list.

// propertyGroup puts properties under prefix in group; base is what labels leave off
// ("display.media." makes display.media.audio.speed "Audio · speed").
type propertyGroup struct{ prefix, group, base string }

// propertyGroups is matched in order, first prefix wins, so narrow ones come first.
// Network sections (networkGroups) overlap no other prefix.
var propertyGroups = append([]propertyGroup{
	{"notifications.", "notifications", "notifications."},
	{"display.media.", "media", "display.media."},
	{"display.theme.", "look", "display.theme."},
	{"display.fps", "look", "display."},
	{"display.network_colors", "look", "display."},
	{"display.mouse", "look", "display."},
	{"display.hyperlinks", "look", "display."},
	{"display.row_numbers", "look", "display."},
	{"display.time_format", "look", "display."},
	{"display.long_date_format", "look", "display."},
	{"display.short_date_format", "look", "display."},
	{"terminal", "look", ""},
	{"display.emoji.", "emoji", "display."},
	{"display.skin_tone", "emoji", "display."},
	{"display.reactions.", "emoji", "display."},
	{"display.rooms.", "rooms", "display."},
	{"phonebook.", "names", ""},
	{"display.show_archived", "rooms", "display."},
	{"display.rail.", "rooms", "display."},
	{"display.priority", "rooms", "display."},
	{"display.filing_spaces", "rooms", "display."},
	{"display.threads.", "rooms", "display."},
	{"display.direction.", "rooms", "display."},
	{"display.open_in_insert_mode", "composer", "display."},
	{"display.max_name_length", "names", "display."},
	{"display.room_name_rules", "names", "display."},
	{"display.identity", "names", "display."},
	{"display.name", "names", "display."},
	{"display.space_rule", "names", "display."},
	{"composer.", "composer", "composer."},
	{"display.", "display", "display."},
	{"spell.", "spelling", "spell."},
	{"complete.", "completion", "complete."},
	{"assist.", "assist", "assist."},
	{"codes.", "codes", "codes."},
	{"clipboard.", "codes", ""},
	{"filter", "filters", ""},
	{"agent.", "agent", "agent."},
	{"allow_token_file", "networks", ""},
	{"bridge_contacts", "networks", ""},
	{"commands.", "advanced", ""},
	{"command", "advanced", ""},
	{"log.", "advanced", ""},
	{"schedule.", "advanced", ""},
	{"keys.", "keys", "keys."},
}, networkGroups()...)

func networkGroups() []propertyGroup {
	var out []propertyGroup
	for _, n := range (config.Config{}).Networks() {
		section, _, _ := strings.Cut(n.Table(), ".")
		out = append(out, propertyGroup{section + ".", "networks", ""})
	}
	return out
}

// placeProperty is where a property is listed and its label there; false if no group takes it.
func placeProperty(path string) (group, label string, ok bool) {
	for _, g := range propertyGroups {
		if strings.HasPrefix(path, g.prefix) {
			return g.group, propertyLabel(strings.TrimPrefix(path, g.base)), true
		}
	}
	return "", "", false
}

// propertyLabel is a key path as words: "audio.close_after" → "Audio · close after".
func propertyLabel(rest string) string {
	parts := strings.Split(rest, ".")
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(p, "_", " ")
	}
	if parts[0] != "" {
		parts[0] = strings.ToUpper(parts[0][:1]) + parts[0][1:]
	}
	return strings.Join(parts, " · ")
}

// buildSettings is the hand-written rows, then a generated row for each property they
// don't cover.
func buildSettings(curated []setting) []setting {
	docs := map[string]string{}
	covered := map[string]bool{}
	for i := range curated {
		covered[curated[i].key] = true
		for _, k := range curated[i].covers {
			covered[k] = true
		}
	}
	all := make([]setting, 0, len(curated)+200)
	props := config.Properties()
	for _, p := range props {
		docs[p.Path] = p.Doc
	}
	for i := range curated {
		s := curated[i]
		if s.doc == "" {
			s.doc = docs[s.key]
		}
		all = append(all, s)
	}
	for _, p := range props {
		if covered[p.Path] {
			continue
		}
		if s, ok := propertySetting(p); ok {
			all = append(all, s)
		}
	}
	all = append(all, recordTableSettings()...)
	return append(all, keyTableSettings()...)
}

func propertySetting(p config.Property) (setting, bool) {
	group, label, ok := placeProperty(p.Path)
	if !ok {
		return setting{}, false
	}
	s := setting{key: p.Path, group: group, label: label, doc: p.Doc}
	s.show = func(c config.Config) string { return propertyWords(c, p) }
	switch p.Kind {
	case config.PropertyBool, config.PropertyOptionalBool:
		s.kind = settingToggle
		s.set = func(c *config.Config, _ string) error { return setProperty(c, p, strconv.FormatBool(!boolNow(*c, p))) }
	case config.PropertyInt, config.PropertyOptionalInt:
		s = propertyNumber(s, p)
	case config.PropertyList:
		s.kind = settingList
		s.set = func(c *config.Config, v string) error { return setProperty(c, p, v) }
		s.entries = func(c config.Config) []string { return listNow(c, p) }
		s.setEntries = func(c *config.Config, e []string) error { return c.SetList(p.Path, e) }
	case config.PropertyFloat, config.PropertyOptionalFloat, config.PropertyText:
		s.kind = settingText
		s.edit = func(c config.Config) string { v, _, _ := c.Value(p.Path); return v }
		s.set = func(c *config.Config, v string) error { return setProperty(c, p, v) }
		s.help = "empty for the default"
	default:
		return setting{}, false
	}
	return s, true
}

// propertyNumber makes s a stepped, typed number row stopping at 0, or at -1 for a
// number that means something there ("never", "until dismissed").
func propertyNumber(s setting, p config.Property) setting {
	s.kind = settingNumber
	if strings.Contains(p.Doc, "-1") {
		s.least = -1
	}
	s.count = func(c config.Config) int { n, _ := strconv.Atoi(effective(c, p)); return n }
	s.edit = func(c config.Config) string { return effective(c, p) }
	s.set = func(c *config.Config, v string) error { return setProperty(c, p, v) }
	s.help = "a whole number — empty for the default"
	return s
}

// listNow is a list property's entries in force: what is set, else default.toml's.
func listNow(c config.Config, p config.Property) []string {
	if list := c.List(p.Path); len(list) > 0 {
		return list
	}
	return p.DefaultList()
}

// effective is a property's value as text: what is set, else an optional's default.
func effective(c config.Config, p config.Property) string {
	v, set, _ := c.Value(p.Path)
	if !set && (p.Kind == config.PropertyOptionalBool || p.Kind == config.PropertyOptionalInt) {
		return strings.Trim(p.Default, `"`)
	}
	return v
}

func boolNow(c config.Config, p config.Property) bool {
	b, _ := strconv.ParseBool(effective(c, p))
	return b
}

// setProperty writes v, unsetting an optional set back to its default so the file
// keeps only what differs.
func setProperty(c *config.Config, p config.Property, v string) error {
	optional := p.Kind == config.PropertyOptionalBool || p.Kind == config.PropertyOptionalInt
	if optional && strings.TrimSpace(v) == strings.Trim(p.Default, `"`) {
		v = ""
	}
	return c.SetValue(p.Path, v) //nolint:wrapcheck // config's error says what is wrong
}

func propertyWords(c config.Config, p config.Property) string {
	switch p.Kind {
	case config.PropertyBool, config.PropertyOptionalBool:
		return onOff(boolNow(c, p))
	case config.PropertyList:
		if list := listNow(c, p); len(list) > 0 {
			return strings.Join(list, ", ")
		}
		return "none"
	case config.PropertyInt, config.PropertyOptionalInt, config.PropertyFloat, config.PropertyOptionalFloat, config.PropertyText:
	}
	if v := effective(c, p); v != "" {
		return v
	}
	if p.Default != "" && !p.Example {
		if d := strings.Trim(p.Default, `"'`); d != "" {
			return d // unset: the default default.toml writes
		}
		return "none" // its default is empty
	}
	return "default"
}
