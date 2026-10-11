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

// Some groups' rows are made from the config rather than listed: the Names group's
// per-place switches, a record table's records, a record's fields. They behave like
// any setting.
//
// Keys: rec:<table> lists a table's records, rec:<table>#<i> is one record,
// rec:<table>#<i>.<field> one field; names:, colors: and archived:<rail key> are a
// place's first-names, network-colors and archived-rooms rows.

const (
	recordPrefix = "rec:"
	namesPrefix  = "names:"
	colorsPrefix = "colors:"
	archivedKey  = "archived:"
	recordAdd    = "#add"
	recordRemove = "#remove"
)

// recordTableLabels name the record tables, every network's accounts among them; an
// unnamed table has no row (the Names switches stand for display.space_rule).
var recordTableLabels = withNetworkTables(map[string]string{
	"display.identity":     "People (accounts shown as one)",
	"display.name":         "Names you gave",
	"display.read_rule":    "Read rules",
	"display.tracked.rule": "Tracked-word rules",
	"display.rooms.rule":   "Room list order per group",
	"display.threads.rule": "Threads per place",
	"display.media.rule":   "Media per place",
	"spam.filter":          "Spam filters",
	"commands.script":      "Scripts",
	"keys.jump":            "Jump shortcuts",
})

func withNetworkTables(labels map[string]string) map[string]string {
	for _, n := range (config.Config{}).Networks() {
		labels[n.Table()] = n.Network() + " accounts"
	}
	return labels
}

// recordTableSettings are the rows leading to each labeled record table, in the group
// its path places it.
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

// groupRows is a group's rows: listed, or made from the config.
func (m Model) groupRows(group string) []setting {
	switch {
	case group == "names":
		return append(m.placeNameRows(), groupSettings(group)...)
	case group == "look":
		return append(groupSettings(group), m.placeColorRows()...)
	case group == "rooms":
		return append(groupSettings(group), m.placeArchivedRows()...)
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

// setting is the row keyed key: among the open group's rows, else listed.
func (m Model) setting(key string) (setting, bool) {
	rows := m.groupRows(m.choosing.settingGroup)
	for i := range rows {
		if rows[i].key == key {
			return rows[i], true
		}
	}
	return findSetting(key)
}

// settingsParent is where esc goes from a group: a record to its table, a table to its
// group, any other group to the groups.
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

// recordRows are a table's records, each leading to its fields, then an add row.
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

// addRecordRow adds a record typed in place as its first field (a rule's place, a
// person's alias), so a record is never empty, which some tables refuse.
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

// recordSummary names a record by its first two set fields.
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

// recordFieldRows are one record's fields, then a row to remove it.
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

// inheritChoices are an optional on/off field's values: on, off, or inherit the wider
// setting (a switch has only two states).
var inheritChoices = []settingChoiceOption{
	{"true", "on", "this rule turns it on"},
	{"false", "off", "this rule turns it off"},
	{"", "unset", "follows the wider setting"},
}

func (m Model) removeRecord(t config.RecordTable, i int) (Model, tea.Cmd) {
	cfg := m.conf.base.Clone()
	if err := cfg.RemoveRecord(t.Path, i); err != nil {
		return m.sayErr(recordTableLabels[t.Path], err), nil
	}
	next, cmd := m.applyConfig(cfg, "removed from "+recordTableLabels[t.Path])
	return next.settingsIn(recordPrefix+t.Path, ""), cmd
}

// placeNameRows are a first-names switch ([[display.space_rule]]) for every rail
// space and tag that can be a room's home. A state tag (Unread) is nobody's home and
// one of every room (All) is everywhere.
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

// placeColorRows are a network-colors choice for every rail space and tag: follow
// [display] network_colors, or force on or off ([[display.space_rule]]).
func (m Model) placeColorRows() []setting {
	var rows []setting
	for _, g := range m.rail.groups {
		if !isSpaceGroup(g.key) && !isTagGroup(g.key) {
			continue
		}
		place := g.key
		rows = append(rows, setting{
			key: colorsPrefix + place, group: "look", kind: settingChoice,
			label:   "Network colors in " + g.label,
			doc:     "Rooms in this " + placeWord(place) + "'s room list are named in their network's color, as [display.theme.networks] sets: a bridged room in its bridge's network's. As everywhere follows [display] network_colors.",
			choices: placeColorChoices,
			show:    func(c config.Config) string { return placeColorsShown(c, place) },
			set: func(c *config.Config, value string) error {
				c.Display.SpaceRules = withNetworkColors(c.Display.SpaceRules, place, placeColorValues[value])
				return nil
			},
		})
	}
	return rows
}

// placeColorChoices are a place's network-colors choices and placeColorValues what
// each writes: nil follows [display] network_colors.
var (
	placeColorChoices = []settingChoiceOption{
		{"everywhere", "as everywhere", "as [display] network_colors says"},
		{"on", "on", "named in their networks' colors whatever [display] network_colors says"},
		{"off", "off", "in the row's own color whatever [display] network_colors says"},
	}
	placeColorValues = map[string]*bool{"everywhere": nil, "on": new(true), "off": new(false)}
)

// placeColorsShown is a place's network-colors choice: its own, or the inherited one.
func placeColorsShown(c config.Config, place string) string {
	if r, ok := c.Display.SpaceRule(place); ok && r.NetworkColors != nil {
		return onOff(*r.NetworkColors)
	}
	return "as everywhere (" + onOff(c.Display.NetworkColors) + ")"
}

// placeArchivedRows are an archived-rooms choice for every rail space and tag but an
// archive's own: follow [display] show_archived, or force show or hide.
func (m Model) placeArchivedRows() []setting {
	var rows []setting
	for _, g := range m.rail.groups {
		if name, isTag := domain.TagOf(g.key); (!isSpaceGroup(g.key) && !isTagGroup(g.key)) || (isTag && m.isArchiveTag(name)) {
			continue
		}
		place := g.key
		rows = append(rows, setting{
			key: archivedKey + place, group: "rooms", kind: settingChoice,
			label:   "Archived rooms in " + g.label,
			doc:     "Whether this " + placeWord(place) + "'s room list lists the rooms the archive tag holds. As everywhere follows [display] show_archived; the archived key flips it until kith restarts.",
			choices: placeArchivedChoices,
			show:    func(c config.Config) string { return placeArchivedShown(c, place) },
			set: func(c *config.Config, value string) error {
				c.Display.SpaceRules = withSpaceRule(c.Display.SpaceRules, place, func(r *config.SpaceRule) { r.ShowArchived = placeArchivedValues[value] })
				return nil
			},
		})
	}
	return rows
}

// placeArchivedChoices are a place's archived-rooms choices and placeArchivedValues
// what each writes: nil follows [display] show_archived.
var (
	placeArchivedChoices = []settingChoiceOption{
		{"everywhere", "as everywhere", "as [display] show_archived says"},
		{"shown", "shown", "listed whatever [display] show_archived says"},
		{"hidden", "hidden", "left out whatever [display] show_archived says"},
	}
	placeArchivedValues = map[string]*bool{"everywhere": nil, "shown": new(true), "hidden": new(false)}
)

// placeArchivedShown is a place's archived-rooms choice: its own, or the inherited one.
func placeArchivedShown(c config.Config, place string) string {
	word := map[bool]string{true: "shown", false: "hidden"}
	if r, ok := c.Display.SpaceRule(place); ok && r.ShowArchived != nil {
		return word[*r.ShowArchived]
	}
	return "as everywhere (" + word[c.Display.ShowsArchived()] + ")"
}

// isHomeTag reports whether a tag (tag:<name>) holds some rooms, but not every room.
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

func withFirstNames(rules []config.SpaceRule, place string, on bool) []config.SpaceRule {
	return withSpaceRule(rules, place, func(r *config.SpaceRule) { r.FirstNameOnly = on })
}

// withNetworkColors sets place's network-colors choice: nil follows [display] network_colors.
func withNetworkColors(rules []config.SpaceRule, place string, on *bool) []config.SpaceRule {
	return withSpaceRule(rules, place, func(r *config.SpaceRule) { r.NetworkColors = on })
}

// withSpaceRule applies change to place's rule; a rule left setting nothing is
// dropped, so the file keeps only what is set.
func withSpaceRule(rules []config.SpaceRule, place string, change func(*config.SpaceRule)) []config.SpaceRule {
	rule := config.SpaceRule{Space: place}
	out := make([]config.SpaceRule, 0, len(rules)+1)
	for _, r := range rules {
		if strings.EqualFold(r.Space, place) {
			rule = r
			continue
		}
		out = append(out, r)
	}
	change(&rule)
	if !rule.Empty() {
		out = append(out, rule)
	}
	return out
}
