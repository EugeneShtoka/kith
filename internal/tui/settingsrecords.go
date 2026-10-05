package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Some groups' rows are made from the config as it stands rather than listed: the
// Names group's switch per space and tag, a record table's records, a record's fields.
// They are settings like any other, so a change stays on its row, a number steps, a
// list opens its entries, and esc steps back.
//
// Their keys say where they are: rec:<table> lists a table's records, rec:<table>#<i>
// is one record, rec:<table>#<i>.<field> one of its fields; names:<rail key> is a
// place's first-names switch.

const (
	recordPrefix = "rec:"
	namesPrefix  = "names:"
	recordAdd    = "#add"
	recordRemove = "#remove"
)

// recordTableLabels name the record tables in settings; a table not named here has no
// row (the Names group's switches stand for display.space_rule).
var recordTableLabels = map[string]string{
	"display.identity":     "People (accounts shown as one)",
	"display.name":         "Names you gave",
	"display.read_rule":    "Read rules",
	"display.tracked.rule": "Tracked-word rules",
	"display.rooms.rule":   "Room list order per group",
	"display.threads.rule": "Threads per place",
	"display.media.rule":   "Media per place",
	"spam.filter":          "Spam filters",
	"commands.script":      "Scripts",
	"whatsapp.account":     "WhatsApp accounts",
	"slack.account":        "Slack accounts",
	"telegram.account":     "Telegram accounts",
	"keys.jump":            "Jump shortcuts",
}

// recordTableSettings are the rows leading to the record tables, one per labeled
// table, in the group its path places it. Each opens its records (chooseSetting: a row
// keyed rec:<table> leads there).
func recordTableSettings() []setting {
	var out []setting
	for _, t := range config.RecordTables() {
		label, ok := recordTableLabels[t.Path]
		group, _, placed := placeProperty(t.Path)
		if !ok || !placed {
			continue
		}
		path := t.Path
		out = append(out, setting{
			key: recordPrefix + path, group: group, label: label, kind: settingOpen, doc: t.Doc,
			show: func(c config.Config) string { return showCount(c.Records(path), "none") },
		})
	}
	return out
}

// recordTable is the table a rec: group or key is about.
func recordTable(key string) (config.RecordTable, bool) {
	path, _, _ := strings.Cut(strings.TrimPrefix(key, recordPrefix), "#")
	for _, t := range config.RecordTables() {
		if t.Path == path {
			return t, true
		}
	}
	return config.RecordTable{}, false
}

// groupRows is a group's rows: listed, or made from the config (see above).
func (m Model) groupRows(group string) []setting {
	switch {
	case group == "names":
		return append(m.placeNameRows(), groupSettings(group)...)
	case strings.HasPrefix(group, keysPrefix):
		if t, ok := keyTableOf(group); ok {
			return keyBindingRows(t)
		}
		return nil
	case strings.HasPrefix(group, recordPrefix):
		t, ok := recordTable(group)
		if !ok {
			return nil
		}
		if _, rest, isRecord := strings.Cut(strings.TrimPrefix(group, recordPrefix), "#"); isRecord {
			return m.recordFieldRows(t, atoiSafe(rest))
		}
		return m.recordRows(t)
	}
	return groupSettings(group)
}

// setting is the row keyed key: among the rows of the group open, else listed.
func (m Model) setting(key string) (setting, bool) {
	rows := m.groupRows(m.choosing.settingGroup)
	for i := range rows {
		if rows[i].key == key {
			return rows[i], true
		}
	}
	return findSetting(key)
}

// settingsParent is where esc goes from a group: a record to its table, a table to the
// group it is listed in, any other group to the groups.
func settingsParent(group string) (parent, at string, ok bool) {
	if strings.HasPrefix(group, keysPrefix) {
		return "keys", group, true
	}
	if !strings.HasPrefix(group, recordPrefix) {
		return "", group, false
	}
	table, _, isRecord := strings.Cut(group, "#")
	if isRecord {
		return table, group, true
	}
	t, found := recordTable(group)
	if !found {
		return "", "", false
	}
	inGroup, _, _ := placeProperty(t.Path)
	return inGroup, group, true
}

// settingGroupLabel is a group's words, made ones included.
func (m Model) settingGroupLabel(group string) string {
	if t, ok := keyTableOf(group); ok && strings.HasPrefix(group, keysPrefix) {
		return "Keys · " + keyTableLabel(t.Name)
	}
	if !strings.HasPrefix(group, recordPrefix) {
		return groupLabel(group)
	}
	t, _ := recordTable(group)
	label := recordTableLabels[t.Path]
	if _, rest, isRecord := strings.Cut(strings.TrimPrefix(group, recordPrefix), "#"); isRecord {
		return label + " · " + recordSummary(m.conf.base, t, atoiSafe(rest))
	}
	return label
}

// recordRows are a table's records, each leading to its fields, then a row to add one.
func (m Model) recordRows(t config.RecordTable) []setting {
	n := m.conf.base.Records(t.Path)
	rows := make([]setting, 0, n+1)
	group := recordPrefix + t.Path
	for i := range n {
		key := fmt.Sprintf("%s#%d", group, i)
		rows = append(rows, setting{
			key: key, group: group, kind: settingOpen, doc: t.Doc,
			label: recordSummary(m.conf.base, t, i),
			show:  func(config.Config) string { return "" },
			open:  func(m Model) (Model, tea.Cmd) { return m.settingsIn(key, ""), nil },
		})
	}
	return append(rows, addRecordRow(t, group))
}

// addRecordRow adds a record, typed in place as its first field (a rule's place, a
// person's alias): a record is never empty, which some tables refuse and none needs.
func addRecordRow(t config.RecordTable, group string) setting {
	first := t.Fields[0]
	path := t.Path
	return setting{
		key: group + recordAdd, group: group, kind: settingText, label: "Add one", doc: t.Doc,
		help: "its " + propertyLabel(first.Path) + " — " + strings.TrimSuffix(firstLine(strings.TrimSpace(first.Doc)), "."),
		show: func(config.Config) string { return "" },
		edit: func(config.Config) string { return "" },
		set: func(c *config.Config, v string) error {
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("type its %s first", propertyLabel(first.Path))
			}
			i, err := c.AddRecord(path)
			if err != nil {
				return err //nolint:wrapcheck // config says what is wrong
			}
			return c.SetRecordValue(path, i, first.Path, v)
		},
	}
}

// recordSummary names a record by what it says: its first two set fields.
func recordSummary(c config.Config, t config.RecordTable, i int) string {
	var parts []string
	for _, f := range t.Fields {
		if v, set := c.RecordValue(t.Path, i, f.Path); set && len(parts) < 2 {
			parts = append(parts, v)
		}
	}
	if len(parts) == 0 {
		return "(empty)"
	}
	return strings.Join(parts, " · ")
}

// recordFieldRows are one record's fields, each set as a property is, then a row to
// remove it.
func (m Model) recordFieldRows(t config.RecordTable, i int) []setting {
	group := fmt.Sprintf("%s%s#%d", recordPrefix, t.Path, i)
	rows := make([]setting, 0, len(t.Fields)+1)
	for _, f := range t.Fields {
		rows = append(rows, recordField(t, i, group, f))
	}
	return append(rows, setting{
		key: group + recordRemove, group: group, kind: settingOpen, label: "Remove this one", doc: t.Doc,
		show: func(config.Config) string { return "" },
		open: func(m Model) (Model, tea.Cmd) { return m.removeRecord(t, i) },
	})
}

// recordField is one field's row, set as a property of its kind is.
func recordField(t config.RecordTable, i int, group string, f config.Property) setting {
	path, name := t.Path, f.Path
	doc := f.Doc
	if doc == "" {
		doc = t.Doc
	}
	get := func(c config.Config) string { v, _ := c.RecordValue(path, i, name); return v }
	set := func(c *config.Config, v string) error { return c.SetRecordValue(path, i, name, v) }
	s := setting{key: group + "." + name, group: group, label: propertyLabel(name), doc: doc, set: set, edit: get}
	s.show = func(c config.Config) string { return fieldWords(c, path, i, f) }
	switch f.Kind {
	case config.PropertyBool:
		s.kind = settingToggle
		s.set = func(c *config.Config, _ string) error { return set(c, strconv.FormatBool(get(*c) != "true")) }
	case config.PropertyOptionalBool:
		s.kind, s.choices, s.show = settingChoice, inheritChoices, get // on, off, or unset (inherits)
	case config.PropertyInt, config.PropertyOptionalInt:
		s.kind, s.least, s.help = settingNumber, -1, "a whole number — empty unsets it"
		s.count = func(c config.Config) int { n, _ := strconv.Atoi(get(c)); return n }
	case config.PropertyList:
		s.kind = settingList
		s.entries = func(c config.Config) []string { return c.RecordList(path, i, name) }
		s.setEntries = func(c *config.Config, e []string) error { return c.SetRecordList(path, i, name, e) }
	case config.PropertyFloat, config.PropertyOptionalFloat, config.PropertyText:
		s.kind, s.help = settingText, "empty unsets it"
	}
	return s
}

// fieldWords is a record field's row value: on or off, the value, or that it is unset.
func fieldWords(c config.Config, path string, i int, f config.Property) string {
	v, set := c.RecordValue(path, i, f.Path)
	switch {
	case f.Kind == config.PropertyBool:
		return onOff(v == "true")
	case !set && f.Kind == config.PropertyList:
		return "none"
	case !set:
		return "unset"
	}
	return v
}

// inheritChoices are an optional on/off field's values: a rule that turns it on, off,
// or leaves it to the wider setting (a switch has only two states).
var inheritChoices = []settingChoiceOption{
	{"true", "on", "this rule turns it on"},
	{"false", "off", "this rule turns it off"},
	{"", "unset", "follows the wider setting"},
}

// removeRecord takes record i out of t and shows the table.
func (m Model) removeRecord(t config.RecordTable, i int) (Model, tea.Cmd) {
	cfg := m.conf.base.Clone()
	if err := cfg.RemoveRecord(t.Path, i); err != nil {
		return m.sayErr(recordTableLabels[t.Path], err), nil
	}
	next, cmd := m.applyConfig(cfg, "removed from "+recordTableLabels[t.Path])
	return next.settingsIn(recordPrefix+t.Path, ""), cmd
}

// placeNameRows are a first-names switch for every space and tag on the rail that can
// be a room's home: senders in rooms there are shown by first name
// ([[display.space_rule]]). A tag of state alone (Unread) is nobody's home, and one of
// every room (All) is everywhere.
func (m Model) placeNameRows() []setting {
	var rows []setting
	for _, g := range m.rail.groups {
		if home := isSpaceGroup(g.key) || (isTagGroup(g.key) && m.isHomeTag(g.key)); !home {
			continue
		}
		place := g.key
		rows = append(rows, setting{
			key: namesPrefix + place, group: "names", kind: settingToggle,
			label: "First names only in " + g.label,
			doc:   "Senders in rooms whose home is this " + placeWord(place) + " are shown by their first name. A room in several places follows its home: [display] priority, then the rail's order.",
			show:  func(c config.Config) string { return onOff(firstNamesIn(c, place)) },
			set: func(c *config.Config, _ string) error {
				c.Display.SpaceRules = withFirstNames(c.Display.SpaceRules, place, !firstNamesIn(*c, place))
				return nil
			},
		})
	}
	return rows
}

// isHomeTag reports whether a tag (tag:<name>) holds some room as a place, and not
// every room.
func (m Model) isHomeTag(key string) bool {
	if m.spans(key) {
		return false
	}
	name, _ := domain.TagOf(key)
	for _, f := range m.rail.roomFacts {
		if slices.ContainsFunc(f.Tags, func(t string) bool { return strings.EqualFold(t, name) }) {
			return true
		}
	}
	return false
}

// placeWord is "tag" or "space", for a rail key.
func placeWord(key string) string {
	if isTagGroup(key) {
		return "tag"
	}
	return "space"
}

// firstNamesIn reports whether place (a space's name, or tag:<name>) shows first names.
func firstNamesIn(c config.Config, place string) bool {
	for _, r := range c.Display.SpaceRules {
		if strings.EqualFold(r.Space, place) {
			return r.FirstNameOnly
		}
	}
	return false
}

// withFirstNames is rules with place's first-names switch set to on; a rule switched
// off is dropped, so the file keeps only what is on.
func withFirstNames(rules []config.SpaceRule, place string, on bool) []config.SpaceRule {
	out := make([]config.SpaceRule, 0, len(rules)+1)
	for _, r := range rules {
		if !strings.EqualFold(r.Space, place) {
			out = append(out, r)
		}
	}
	if on {
		out = append(out, config.SpaceRule{Space: place, FirstNameOnly: true})
	}
	return out
}
