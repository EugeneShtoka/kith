package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// The tag editor, from settings: the tags, then one tag's name, rule, picked and
// excluded rooms, properties, and what other tags take out of it. Each change is
// applied and saved at once, like any setting, and the editor reopens on the same
// tag. Rule, picked and excluded are lists edited an entry at a time — room names may
// hold any separator. Renaming rewrites every reference to the tag (setup.RenameTag).

// tagEditing is the editor's place: the tag, and the list being edited in it.
type tagEditing struct {
	tag   string // the tag's name; "" while creating one
	list  string // tagRule, tagPicked or tagExcluded
	entry int    // the entry a prompt edits; -1 adds one
	// fileRoom is the room a tag being created from the filing picker goes on; ""
	// when it is created from settings.
	fileRoom domain.RoomID
}

// The editor's rows that are not properties.
const (
	tagName     = "name"
	tagRule     = "rule"
	tagPicked   = "picked"
	tagExcluded = "excluded"
	tagClaimed  = "claimed"
	tagFirst    = "first_names" // its rooms' senders by first name ([[display.space_rule]])
	tagDelete   = "delete"
	tagNew      = "\x00new" // the list's "New tag" row; no tag can be named this
	tagAdd      = "add"
)

// tagProperty is one on/off property row: its config key, its words, and its field.
type tagProperty struct {
	key, label string
	field      func(*config.Tag) *bool
}

var tagProperties = []tagProperty{
	{"exclusive", "Shows its rooms alone", func(t *config.Tag) *bool { return &t.Exclusive }},
	{"space_exclusive", "Takes its rooms out of spaces you made", func(t *config.Tag) *bool { return &t.SpaceExclusive }},
	{"sticky", "Keeps the open room listed", func(t *config.Tag) *bool { return &t.Sticky }},
	{"hide_when_empty", "Hidden while empty", func(t *config.Tag) *bool { return &t.HideWhenEmpty }},
	{"first", "At the top of the rail", func(t *config.Tag) *bool { return &t.First }},
	{"count_in_label", "Says how many rooms it holds", func(t *config.Tag) *bool { return &t.CountInLabel }},
	{"hidden", "Hidden from the rail", func(t *config.Tag) *bool { return &t.Hidden }},
}

// countsUnreadKey is counts_unread's row: a *bool whose nil is true.
const countsUnreadKey = "counts_unread"

// openTags lists the tags, and a row to make one; it is the settings row's way in.
func (m Model) openTags() (Model, tea.Cmd) { return m.tagsOpen(), nil }

// tagsOpen is the model showing the list of tags.
func (m Model) tagsOpen() Model {
	tags := m.conf.base.Tags
	items := make([]pickerItem, 0, len(tags)+1)
	for _, t := range tags {
		items = append(items, pickerItem{label: isolate(t.Name), detail: tagSummary(t), value: t.Name, match: t.Name})
	}
	items = append(items, pickerItem{label: "New tag", value: tagNew, match: "new tag"})
	m.choosing.tag = tagEditing{}
	m.picker = newPicker(pickerTags, items)
	return m
}

// tagSummary is a tag in a line: its rule and how many rooms it picks and excludes.
func tagSummary(t config.Tag) string {
	parts := []string{ruleWords(t.Rule)}
	if n := len(t.Picked); n > 0 {
		parts = append(parts, fmt.Sprintf("%d picked", n))
	}
	if n := len(t.Excluded); n > 0 {
		parts = append(parts, fmt.Sprintf("%d excluded", n))
	}
	return strings.Join(parts, " · ")
}

func ruleWords(rule []string) string {
	if len(rule) == 0 {
		return "no rule: only rooms put in it"
	}
	return strings.Join(rule, ", ")
}

// chooseTag opens a tag's editor, or prompts for a new tag's name.
func (m Model) chooseTag(value string) (Model, tea.Cmd) {
	m = m.closePicker()
	if value == tagNew {
		m.choosing.tag = tagEditing{}
		return m.openPrompt(promptTagName), nil
	}
	return m.tagOpen(value), nil
}

// configTag is the index of the named tag in the running config; -1 when none is.
func (m Model) configTag(name string) int {
	for i := range m.conf.base.Tags {
		if strings.EqualFold(strings.TrimSpace(m.conf.base.Tags[i].Name), strings.TrimSpace(name)) {
			return i
		}
	}
	return -1
}

// tagOpen shows one tag's rows; a tag that is gone (renamed or deleted under it)
// falls back to the list.
func (m Model) tagOpen(name string) Model {
	at := m.configTag(name)
	if at < 0 {
		return m.tagsOpen()
	}
	t := m.conf.base.Tags[at]
	m.choosing.tag = tagEditing{tag: t.Name}
	items := []pickerItem{
		{label: "Name", detail: isolate(t.Name), value: tagName},
		{label: "Rule", detail: ruleWords(t.Rule), value: tagRule},
		{label: "Picked", detail: m.entriesWords(t.Picked), value: tagPicked},
		{label: "Excluded", detail: m.entriesWords(t.Excluded), value: tagExcluded},
		{label: "Counts as unread", detail: onOff(t.Counts()), value: countsUnreadKey},
		{label: "First names only", detail: onOff(firstNamesIn(m.conf.base, domain.TagEntry(t.Name))), value: tagFirst},
	}
	for _, p := range tagProperties {
		items = append(items, pickerItem{label: p.label, detail: onOff(*p.field(&t)), value: p.key})
	}
	if claimed := m.claimedFrom(at); claimed != "" {
		items = append(items, pickerItem{label: "Also excludes", detail: claimed, value: tagClaimed})
	}
	items = append(items, pickerItem{label: "Delete this tag", value: tagDelete})
	for i := range items {
		items[i].match = items[i].label
	}
	spec := pickerSpecs[pickerTagEdit]
	spec.title = "Tag: " + t.Name
	m.picker = newPickerWith(pickerTagEdit, spec, items)
	return m
}

// entriesWords is a picked or excluded list in a line, rooms by name.
func (m Model) entriesWords(entries []string) string {
	if len(entries) == 0 {
		return "none"
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, m.entryName(e))
	}
	return fmt.Sprintf("%d: %s", len(entries), strings.Join(names, ", "))
}

// entryName is a room entry as a person reads it: a room by the name shown for it.
func (m Model) entryName(entry string) string {
	if name, ok := strings.CutPrefix(entry, "room:"); ok {
		return isolate(name)
	}
	if room, ok := m.roomByID(domain.RoomID(entry)); ok {
		return isolate(m.roomName(room))
	}
	return entry
}

// claimedFrom names the tags that take rooms out of tag at's row: the exclusive tags
// before it if it is exclusive itself (the first configured wins), every exclusive tag
// if not, and the space-exclusive tags unless it is one. "" for none.
func (m Model) claimedFrom(at int) string {
	tags := m.conf.base.Tags
	this := tags[at]
	var names []string
	for i, t := range tags {
		if i == at {
			continue
		}
		switch {
		case t.Exclusive && (!this.Exclusive || i < at):
			names = append(names, isolate(t.Name)+" (exclusive)")
		case t.SpaceExclusive && !this.SpaceExclusive && !this.Exclusive:
			names = append(names, isolate(t.Name)+" (space-exclusive)")
		}
	}
	return strings.Join(names, ", ")
}

// chooseTagRow acts on one of a tag's rows.
func (m Model) chooseTagRow(value string) (Model, tea.Cmd) {
	name := m.choosing.tag.tag
	m = m.closePicker()
	switch value {
	case tagName:
		return m.openPromptWith(promptTagName, name), nil
	case tagRule, tagPicked, tagExcluded:
		return m.tagEntriesOpen(name, value), nil
	case tagClaimed:
		m = m.say("an exclusive tag shows its rooms alone, and a space-exclusive one takes them out of other tags")
		return m.tagOpen(name), nil
	case tagDelete:
		m.confirm = confirmState{action: pendingDeleteTag, group: name}
		return m, nil
	case tagFirst:
		place := domain.TagEntry(name)
		now := !firstNamesIn(m.conf.base, place)
		cfg := m.conf.base.Clone()
		cfg.Display.SpaceRules = withFirstNames(cfg.Display.SpaceRules, place, now)
		return m.applyTagConfig(cfg, name, isolate(name)+": first names only: "+onOff(now))
	case countsUnreadKey:
		now := !m.conf.base.Tags[m.configTag(name)].Counts()
		return m.editTag(name, func(t *config.Tag) {
			t.CountsUnread = nil // unset is on
			if !now {
				t.CountsUnread = &now
			}
		}, "counts as unread: "+onOff(now))
	}
	for _, p := range tagProperties {
		if p.key == value {
			now := !*p.field(&m.conf.base.Tags[m.configTag(name)])
			return m.editTag(name, func(t *config.Tag) { *p.field(t) = now }, strings.ToLower(p.label)+": "+onOff(now))
		}
	}
	return m.tagOpen(name), nil
}

// editTag applies a change to the named tag and reopens its editor.
func (m Model) editTag(name string, change func(*config.Tag), done string) (Model, tea.Cmd) {
	cfg := m.conf.base.Clone()
	at := m.configTag(name)
	if at < 0 {
		return m.tagsOpen(), nil
	}
	change(&cfg.Tags[at])
	return m.applyTagConfig(cfg, cfg.Tags[at].Name, isolate(name)+": "+done)
}

// applyTagConfig applies cfg and reopens the editor on the tag now named name.
func (m Model) applyTagConfig(cfg config.Config, name, done string) (Model, tea.Cmd) {
	next, cmd := m.applyConfig(cfg, done)
	return next.tagOpen(name), cmd
}

// tagEntriesOpen lists one of a tag's lists, each entry editable, and a row to add.
func (m Model) tagEntriesOpen(name, list string) Model {
	at := m.configTag(name)
	if at < 0 {
		return m.tagsOpen()
	}
	entries := tagList(&m.conf.base.Tags[at], list)
	items := make([]pickerItem, 0, len(*entries)+1)
	for i, e := range *entries {
		label := e
		if list != tagRule {
			label = m.entryName(e)
		}
		items = append(items, pickerItem{label: label, detail: e, value: fmt.Sprint(i), match: label + " " + e})
	}
	add := map[string]string{tagRule: "Add a term", tagPicked: "Add a room", tagExcluded: "Exclude a room"}[list]
	items = append(items, pickerItem{label: add, value: tagAdd, match: add})
	m.choosing.tag = tagEditing{tag: m.conf.base.Tags[at].Name, list: list}
	spec := pickerSpecs[pickerTagEntries]
	spec.title = m.conf.base.Tags[at].Name + ": " + list
	m.picker = newPickerWith(pickerTagEntries, spec, items)
	return m
}

// tagList is the named list of a tag.
func tagList(t *config.Tag, list string) *[]string {
	switch list {
	case tagPicked:
		return &t.Picked
	case tagExcluded:
		return &t.Excluded
	default:
		return &t.Rule
	}
}

// chooseTagEntry prompts to edit an entry, or to add one.
func (m Model) chooseTagEntry(value string) (Model, tea.Cmd) {
	m = m.closePicker()
	editing := m.choosing.tag
	at := m.configTag(editing.tag)
	if at < 0 {
		return m.tagsOpen(), nil
	}
	if value == tagAdd {
		m.choosing.tag.entry = -1
		return m.openPrompt(promptTagEntry), nil
	}
	i := atoiSafe(value)
	entries := *tagList(&m.conf.base.Tags[at], editing.list)
	if i < 0 || i >= len(entries) {
		return m.tagEntriesOpen(editing.tag, editing.list), nil
	}
	m.choosing.tag.entry = i
	return m.openPromptWith(promptTagEntry, entries[i]), nil
}

// removeTagEntry takes the entry under the cursor out of the tag's list, as emptying it
// would.
func (m Model) removeTagEntry() (Model, tea.Cmd) {
	item, ok := m.picker.selected()
	if !ok || item.value == tagAdd {
		return m, nil
	}
	m.choosing.tag.entry = atoiSafe(item.value)
	return m.submitTagEntry("")
}

// submitTagEntry writes the prompt's entry: empty removes the one being edited.
func (m Model) submitTagEntry(input string) (Model, tea.Cmd) {
	editing := m.choosing.tag
	at := m.configTag(editing.tag)
	if at < 0 {
		return m.tagsOpen(), nil
	}
	input = strings.TrimSpace(input)
	cfg := m.conf.base.Clone()
	entries := tagList(&cfg.Tags[at], editing.list)
	var done string
	switch {
	case editing.entry < 0 && input == "":
		return m.tagEntriesOpen(editing.tag, editing.list), nil
	case editing.entry < 0:
		*entries, done = append(*entries, input), "added "+input
	case editing.entry >= len(*entries):
		return m.tagEntriesOpen(editing.tag, editing.list), nil
	case input == "":
		done = "removed " + (*entries)[editing.entry]
		*entries = append((*entries)[:editing.entry:editing.entry], (*entries)[editing.entry+1:]...)
	default:
		(*entries)[editing.entry], done = input, "set "+input
	}
	next, cmd := m.applyConfig(cfg, isolate(editing.tag)+" "+editing.list+": "+done)
	return next.tagEntriesOpen(editing.tag, editing.list), cmd
}

// submitTagName names a new tag, or renames the one being edited everywhere it is
// referred to.
func (m Model) submitTagName(input string) (Model, tea.Cmd) {
	name, from := strings.TrimSpace(input), m.choosing.tag.tag
	switch {
	case name == "" && m.choosing.tag.fileRoom != "":
		m.choosing.tag = tagEditing{}
		return m, nil
	case name == "" && from == "":
		return m.tagsOpen(), nil
	case name == "" || name == from:
		return m.tagOpen(from), nil
	case from == "" && m.choosing.tag.fileRoom != "":
		room, ok := m.roomByID(m.choosing.tag.fileRoom)
		m.choosing.tag = tagEditing{}
		if !ok {
			return m, nil
		}
		return m.fileInNewTag(name, room)
	case from == "":
		next, cmd := m.createTag(name, "made the tag "+isolate(name)+" — put rooms in it with S, or give it a rule")
		return next.tagOpen(name), cmd
	}
	if at := m.configTag(name); at >= 0 && !strings.EqualFold(strings.TrimSpace(from), name) {
		// The name is another tag's: renaming to it is combining the two, once asked.
		m.confirm = confirmState{action: pendingCombineTags, group: from, address: m.conf.base.Tags[at].Name}
		return m, nil
	}
	return m.applyTagConfig(setup.RenameTag(m.conf.base, from, name), name,
		"renamed "+isolate(from)+" to "+isolate(name)+", everywhere it is named")
}

// combineTags folds the tag from into the tag into, as the tags hold rooms now.
func (m Model) combineTags(from, into string) (Model, tea.Cmd) {
	view := m.unreadView()
	rooms := make([]domain.RoomFacts, 0, len(m.rooms.all))
	byID := make(map[string]domain.Room, len(m.rooms.all))
	for i := range m.rooms.all {
		rooms = append(rooms, view.factsOf(m.rooms.all[i]))
		byID[string(m.rooms.all[i].ID)] = m.rooms.all[i]
	}
	held := func(tag string, facts domain.RoomFacts) bool {
		i, ok := view.tags.Index(tag)
		return ok && view.tagsOf(byID[facts.ID]).in[i]
	}
	return m.applyTagConfig(setup.CombineTags(m.conf.base, from, into, rooms, held), into,
		"combined "+isolate(from)+" into "+isolate(into))
}

// createTag adds an empty tag named name (no rule: it holds the rooms put in it) and
// applies the config. A config that refuses it says why on the status line, and the
// tag is then absent.
func (m Model) createTag(name, done string) (Model, tea.Cmd) {
	cfg := m.conf.base.Clone()
	cfg.Tags = append(cfg.Tags, config.Tag{Name: name})
	return m.applyConfig(cfg, done)
}

// fileInNewTag puts room in the tag named name, making the tag first when there is
// none: the filing picker's New tag row and /tag with a new name.
func (m Model) fileInNewTag(name string, room domain.Room) (Model, tea.Cmd) {
	var made tea.Cmd
	if _, ok := m.unreadView().tags.Index(name); !ok {
		m, made = m.createTag(name, "made the tag "+isolate(name))
	}
	i, ok := m.unreadView().tags.Index(name)
	if !ok {
		return m, made // refused: the reason is on the status line
	}
	view := m.unreadView()
	if view.tagsOf(room).in[i] {
		return m.say(m.roomName(room) + " is already in " + isolate(view.tags.At(i).Name)), made
	}
	next, filed := m.fileTags(room, []tagFiling{{tag: i, label: view.tags.At(i).Name, in: true}})
	return next, tea.Batch(made, filed)
}

// deleteTag removes the tag, and it from the rail's lists and priority; a rule still
// naming it refuses the change, saying where.
func (m Model) deleteTag(name string) (Model, tea.Cmd) {
	next, cmd := m.applyConfig(setup.DeleteTag(m.conf.base, name), "deleted the tag "+isolate(name))
	if next.configTag(name) >= 0 {
		return next.tagOpen(name), cmd // refused: the reason is on the status line
	}
	return next.tagsOpen(), cmd
}
