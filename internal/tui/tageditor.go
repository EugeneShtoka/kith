package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// Editing a tag, from its page in Settings → Places (places.go): its name, and its rule,
// picked and excluded rooms an entry at a time, since room names may hold any
// separator. Each change is applied and saved at once. Renaming rewrites every
// reference (setup.RenameTag).

type tagEditing struct {
	tag string // the tag's name; "" while creating one
	// fileRoom is the room a tag created from the filing picker goes on; "" from settings.
	fileRoom domain.RoomID
}

// A tag's lists, and the filing pickers' "New tag" row (no tag can be named so).
const (
	tagRule     = "rule"
	tagPicked   = "picked"
	tagExcluded = "excluded"
	tagPeople   = "people"
	tagNew      = "\x00new"
)

type tagProperty struct {
	key, label string
	get        func(config.Tag) bool
	set        func(*config.Tag, bool)
}

// flag is a property kept in one bool field.
func boolProperty(key, label string, field func(*config.Tag) *bool) tagProperty {
	return tagProperty{key, label,
		func(t config.Tag) bool { return *field(&t) },
		func(t *config.Tag, on bool) { *field(t) = on }}
}

// exclusiveFrom is one word of the exclusive list.
func exclusiveFrom(what, label string) tagProperty {
	return tagProperty{"exclusive." + what, label,
		func(t config.Tag) bool { return t.ExclusiveFrom(what) },
		func(t *config.Tag, on bool) { t.Exclusive = t.WithExclusive(what, on) }}
}

var tagProperties = []tagProperty{
	exclusiveFrom(config.ExclusiveTags, "Takes its rooms from other tags"),
	exclusiveFrom(config.ExclusiveSpaces, "Takes its rooms from other spaces"),
	exclusiveFrom(config.ExclusiveOrigin, "Takes its rooms from their own space"),
	boolProperty("sticky", "Keeps the open room listed", func(t *config.Tag) *bool { return &t.Sticky }),
	boolProperty("hide_when_empty", "Hidden while empty", func(t *config.Tag) *bool { return &t.HideWhenEmpty }),
	boolProperty("first", "At the top of the rail", func(t *config.Tag) *bool { return &t.First }),
	boolProperty("count_in_label", "Says how many rooms it holds", func(t *config.Tag) *bool { return &t.CountInLabel }),
	boolProperty("hidden", "Hidden from the rail", func(t *config.Tag) *bool { return &t.Hidden }),
	boolProperty("push_archive", "Archives its rooms on their network", func(t *config.Tag) *bool { return &t.PushArchive }),
}

// countsUnreadKey and notifiesKey are counts_unread's and notifies' rows: *bools whose
// nil is true.
const (
	countsUnreadKey = "counts_unread"
	notifiesKey     = "notifies"
)

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

// configTag is the index of the named tag in the running config, or -1.
func (m Model) configTag(name string) int {
	for i := range m.conf.base.Tags {
		if strings.EqualFold(strings.TrimSpace(m.conf.base.Tags[i].Name), strings.TrimSpace(name)) {
			return i
		}
	}
	return -1
}

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

// entryName is a room entry as shown: a room by its display name.
func (m Model) entryName(entry string) string {
	if name, ok := strings.CutPrefix(entry, "room:"); ok {
		return isolate(name)
	}
	if room, ok := m.roomByID(domain.RoomID(entry)); ok {
		return isolate(m.roomName(room))
	}
	return entry
}

// claimedFrom names the tags that take rooms out of tag at's row: the earlier tags
// exclusive from tags if it is one (first configured wins), else every one. "" for none.
func (m Model) claimedFrom(at int) string {
	tags := m.conf.base.Tags
	this := tags[at]
	var names []string
	for i := range tags {
		t := &tags[i]
		if i == at {
			continue
		}
		if t.ExclusiveFrom(config.ExclusiveTags) && (!this.ExclusiveFrom(config.ExclusiveTags) || i < at) {
			names = append(names, isolate(t.Name))
		}
	}
	return strings.Join(names, ", ")
}

// tagsOpen is Settings → Places; tagOpen is the named tag's page, or Places when it is
// gone (renamed or deleted meanwhile).
func (m Model) tagsOpen() Model { return m.settingsIn("places", "") }

func (m Model) tagOpen(name string) Model {
	at := m.configTag(name)
	if at < 0 {
		return m.tagsOpen()
	}
	return m.settingsIn(placePage(tagGroupKey(m.conf.base.Tags[at].Name)), "")
}

// applyTagConfig applies s and reopens the page of the tag now named name.
func (m Model) applyTagConfig(s config.Settings, name, done string) (Model, tea.Cmd) {
	next, cmd := m.applySettings(s, done)
	return next.tagOpen(name), cmd
}

// submitTagName names a new tag, or renames the edited one everywhere it is referenced.
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
		// Renaming onto another tag's name combines the two, once confirmed.
		m.confirm = confirmState{action: pendingCombineTags, group: from, address: m.conf.base.Tags[at].Name}
		return m, nil
	}
	renamed := config.Settings{
		Bridge: setup.RenameTag(m.conf.base, from, name), Device: setup.RenameTagOnDevice(m.conf.device, from, name),
	}
	next, cmd := m.applyTagConfig(renamed, name,
		"renamed "+isolate(from)+" to "+isolate(name)+", everywhere it is named")
	return next, tea.Batch(cmd, next.retagCmd(from, name))
}

// combineTags folds tag from into tag into, keeping the rooms each holds now.
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
		return ok && view.tagsOf(byID[facts.ID]).In[i]
	}
	combined := config.Settings{
		Bridge: setup.CombineTags(m.conf.base, from, into, rooms, held), Device: setup.RenameTagOnDevice(m.conf.device, from, into),
	}
	next, cmd := m.applyTagConfig(combined, into,
		"combined "+isolate(from)+" into "+isolate(into))
	return next, tea.Batch(cmd, next.retagCmd(from, into))
}

// createTag adds an empty tag (no rule: it holds the rooms put in it) and applies the
// config. A refusal is reported on the status line and the tag is absent.
func (m Model) createTag(name, done string) (Model, tea.Cmd) {
	cfg := m.conf.base.Clone()
	cfg.Tags = append(cfg.Tags, config.Tag{Name: name})
	return m.applyConfig(cfg, done)
}

// fileInNewTag puts room in the named tag, creating it if needed: the filing picker's
// New tag row and /tag with a new name.
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
	if view.tagsOf(room).In[i] {
		return m.say(m.roomName(room) + " is already in " + isolate(view.tags.At(i).Name)), made
	}
	next, filed := m.fileTags(room, []tagFiling{{tag: i, label: view.tags.At(i).Name, in: true}})
	return next, tea.Batch(made, filed)
}

// deleteTag removes the tag and every entry naming it that can go (setup.DeleteTag);
// the status line names what still does, matching no room.
func (m Model) deleteTag(name string) (Model, tea.Cmd) {
	deleted := config.Settings{Bridge: setup.DeleteTag(m.conf.base, name), Device: setup.DeleteTagOnDevice(m.conf.device, name)}
	next, cmd := m.applySettings(deleted, "deleted the tag "+isolate(name))
	if next.configTag(name) >= 0 {
		return next.tagOpen(name), cmd // refused: the reason is on the status line
	}
	return next.tagsOpen(), tea.Batch(cmd, next.retagCmd(name, ""))
}

// retagCmd moves tag from's filter moves to tag to (renamed or combined), or drops them
// (deleted), once the config holds the change: from is gone and to, if any, exists.
func (m Model) retagCmd(from, to string) tea.Cmd {
	if m.configTag(from) >= 0 || (to != "" && m.configTag(to) < 0) {
		return nil // refused: the config is as it was
	}
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		return verdictsWrittenMsg{failed: "the rooms filters moved into " + from + " did not follow", err: backend.RetagVerdicts(ctx, from, to)}
	}
}
