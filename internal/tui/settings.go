package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/notify"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// The plain preferences, shown as a settings screen: every row displays its current
// value, since the config file records only what differs from the defaults.

// settingKind decides what choosing a setting does.
type settingKind int

const (
	settingToggle settingKind = iota // flips immediately
	settingChoice                    // opens a picker of the allowed values
	settingText                      // typed in place on its row
	settingNumber                    // typed in place, or stepped with + and -
	settingList                      // a list of entries, edited one at a time
	settingOpen                      // leads somewhere rather than holding a value
)

// setting is one preference.
type setting struct {
	key   string
	label string
	// group is the settingGroups entry it is listed under.
	group string
	// show renders the current value for the row's qualifier.
	show    func(config.Config) string
	kind    settingKind
	choices []settingChoiceOption
	// edit is what a settingText prompt starts out holding, when the displayed form
	// ("no limit") is not typeable back. Empty means unset.
	edit func(config.Config) string
	// set writes a new value; an error is shown to the user.
	set func(*config.Config, string) error
	// count is a settingNumber's value, and least the lowest + and - step to.
	count func(config.Config) int
	least int
	// help says what a typed value may be, while it is typed.
	help string
	// doc is what default.toml says of it, shown beneath the list; covers is the
	// properties a hand-written row stands for besides its own key, which get no row.
	doc    string
	covers []string
	// entries and setEntries are a settingList's entries in force and how to write them.
	entries    func(config.Config) []string
	setEntries func(*config.Config, []string) error
	// open is where a settingOpen row leads.
	open func(Model) (Model, tea.Cmd)
}

// settingChoiceOption is one allowed value of a choice setting, with what it means.
type settingChoiceOption struct {
	value, label, detail string
}

// levelChoices are the notification levels, named by internal/notify.
var levelChoices = func() []settingChoiceOption {
	describe := map[notify.Level]string{
		notify.LevelNone:    "never notify",
		notify.LevelMention: "only when you're named",
		notify.LevelDM:      "mentions and direct messages",
		notify.LevelAll:     "every message",
	}
	levels := notify.Levels()
	out := make([]settingChoiceOption, 0, len(levels))
	for _, l := range levels {
		out = append(out, settingChoiceOption{l.String(), l.String(), describe[l]})
	}
	return out
}()

// mediaModeChoices are built from config's vocabulary so the picker cannot offer a
// mode the validator rejects.
var mediaModeChoices = func() []settingChoiceOption {
	describe := map[string]string{
		config.MediaPlaceholder: "a text chip only; the view key opens it full size",
		config.MediaInline:      "drawn in the timeline with block characters",
	}
	modes := config.MediaModes()
	out := make([]settingChoiceOption, 0, len(modes))
	for _, mode := range modes {
		out = append(out, settingChoiceOption{mode, mode, describe[mode]})
	}
	return out
}()

// ringChoices are the sound levels plus unset, meaning the sound follows the popup.
var ringChoices = append([]settingChoiceOption{
	{ringFollowsShow, "same as notifications", "any popup makes its sound"},
}, levelChoices...)

// ringFollowsShow is the displayed value for an unset ring; it is written back as unset.
const ringFollowsShow = "same"

// The notification rows edit two unscoped rules: the account-wide one and the
// scheduled one (quiet hours), told apart by having a schedule.

// accountRuleAt is the index of the account-wide rule, or -1.
func accountRuleAt(rules []config.Rule) int {
	for i := range rules {
		if rules[i].Match == "" && rules[i].Sender == "" && rules[i].When == "" {
			return i
		}
	}
	return -1
}

// quietRuleAt is the index of the scheduled account-wide rule — quiet hours — or -1.
func quietRuleAt(rules []config.Rule) int {
	for i := range rules {
		if rules[i].Match == "" && rules[i].Sender == "" && rules[i].When != "" {
			return i
		}
	}
	return -1
}

// editAccountRule applies change to the account-wide rule, creating it on first edit.
func editAccountRule(c *config.Config, change func(*config.Rule)) {
	at := accountRuleAt(c.Notifications.Rules)
	if at < 0 {
		c.Notifications.Rules = append(c.Notifications.Rules, config.Rule{Name: "everything"})
		at = len(c.Notifications.Rules) - 1
	}
	change(&c.Notifications.Rules[at])
}

// accountShow is the level in force account-wide.
func accountShow(c config.Config) string {
	if at := accountRuleAt(c.Notifications.Rules); at >= 0 && c.Notifications.Rules[at].Show != "" {
		return c.Notifications.Rules[at].Show
	}
	return notify.LevelMention.String()
}

func setAccountShow(c *config.Config, v string) error {
	editAccountRule(c, func(r *config.Rule) { r.Show = v })
	return nil
}

// accountRing is the sound level account-wide; unset follows the popup.
func accountRing(c config.Config) string {
	if at := accountRuleAt(c.Notifications.Rules); at >= 0 && c.Notifications.Rules[at].Ring != "" {
		return c.Notifications.Rules[at].Ring
	}
	return ringFollowsShow
}

func setAccountRing(c *config.Config, v string) error {
	if v == ringFollowsShow {
		v = ""
	}
	editAccountRule(c, func(r *config.Rule) { r.Ring = v })
	return nil
}

// quietWindowText is the quiet-hours schedule as the prompt accepts it, empty when none.
func quietWindowText(c config.Config) string {
	if at := quietRuleAt(c.Notifications.Rules); at >= 0 {
		return c.Notifications.Rules[at].When
	}
	return ""
}

// setQuietWindow writes the schedule onto the scheduled rule, creating it on first
// edit and removing it when cleared.
func setQuietWindow(c *config.Config, value string) error {
	value = strings.TrimSpace(value)
	at := quietRuleAt(c.Notifications.Rules)
	if value == "" || value == "none" {
		if at >= 0 {
			c.Notifications.Rules = append(c.Notifications.Rules[:at], c.Notifications.Rules[at+1:]...)
		}
		return nil
	}
	// The rules' own parser, so what is accepted here works at the next start.
	if _, err := notify.ParseWhen(value); err != nil {
		return fmt.Errorf("%q is not two HH:MM times — try 22:00-08:00, or empty for none", value)
	}
	if at < 0 {
		c.Notifications.Rules = append(c.Notifications.Rules, config.Rule{Name: "quiet hours", Show: notify.LevelNone.String()})
		at = len(c.Notifications.Rules) - 1
	}
	c.Notifications.Rules[at].When = value
	return nil
}

// quietShow is what still notifies during quiet hours: the scheduled rule's level.
func quietShow(c config.Config) string {
	if at := quietRuleAt(c.Notifications.Rules); at >= 0 {
		return orDefault(c.Notifications.Rules[at].Show, "none")
	}
	return "none"
}

func setQuietShow(c *config.Config, v string) error {
	at := quietRuleAt(c.Notifications.Rules)
	if at < 0 {
		return errors.New("there are no quiet hours yet — set them first")
	}
	c.Notifications.Rules[at].Show = v
	return nil
}

// settingsList is every row, in display order: curatedSettings, then one per property
// they do not cover (settingsgen.go).
var settingsList = buildSettings(curatedSettings)

// curatedSettings are the rows written by hand: a setting that is more than its key.
var curatedSettings = []setting{
	{
		key: "notifications.enabled", group: "notifications", label: "Notifications",
		show: func(c config.Config) string { return onOff(c.Notifications.Enabled) },
		kind: settingToggle,
		set:  func(c *config.Config, _ string) error { c.Notifications.Enabled = !c.Notifications.Enabled; return nil },
	},
	{
		key: "notifications.show", group: "notifications", label: "Notify me for",
		show:    accountShow,
		kind:    settingChoice,
		choices: levelChoices,
		set:     setAccountShow,
	},
	{
		key: "notifications.ring", group: "notifications", label: "Play a sound for",
		show:    accountRing,
		kind:    settingChoice,
		choices: ringChoices,
		set:     setAccountRing,
	},
	{
		key: "notifications.quiet_hours", group: "notifications", label: "Quiet hours",
		show: func(c config.Config) string { return orDefault(quietWindowText(c), "none") },
		kind: settingText,
		edit: quietWindowText,
		set:  setQuietWindow,
		help: "a window like 23:00-08:00 — empty for none",
	},
	{
		key: "notifications.quiet_show", group: "notifications", label: "During quiet hours, notify for",
		show:    quietShow,
		kind:    settingChoice,
		choices: levelChoices,
		set:     setQuietShow,
	},
	{
		key: "notifications.desktop", group: "notifications", label: "Desktop popups",
		show: func(c config.Config) string { return onOff(c.Notifications.Desktop) },
		kind: settingToggle,
		set:  func(c *config.Config, _ string) error { c.Notifications.Desktop = !c.Notifications.Desktop; return nil },
	},
	{
		key: "notifications.rules", group: "notifications", label: "Notification rules",
		show: func(c config.Config) string { return ruleCountNote(len(c.Notifications.Rules)) },
		kind: settingOpen,
		open: Model.openRuleList,
	},
	{
		key: "tags", group: "tags", label: "Tags",
		show: func(c config.Config) string {
			if len(c.Tags) == 0 {
				return "none"
			}
			return strconv.Itoa(len(c.Tags)) + " tags"
		},
		kind: settingOpen,
		open: Model.openTags,
	},
	{
		key: "notifications.sound", group: "notifications", label: "Notification sound",
		show: func(c config.Config) string { return orDefault(c.Notifications.Sound, "silent") },
		kind: settingText,
		edit: func(c config.Config) string { return c.Notifications.Sound },
		set:  func(c *config.Config, v string) error { c.Notifications.Sound = v; return nil },
		help: "a sound file's path — empty for silent",
	},
	{
		key: "notifications.timeout", group: "notifications", label: "Popup stays up for",
		show: func(c config.Config) string { return timeoutNote(c.Notifications) },
		kind: settingNumber,
		edit: func(c config.Config) string {
			if c.Notifications.Timeout == nil {
				return ""
			}
			return strconv.Itoa(*c.Notifications.Timeout)
		},
		set:   setPopupTimeout,
		count: popupSeconds,
		least: -1,
		help:  "seconds — -1 until dismissed, 0 your desktop's default, empty kith's default",
	},
	{
		key: "display.open_in_insert_mode", group: "composer", label: "Open rooms ready to type",
		show: func(c config.Config) string { return onOff(c.Display.OpenInInsertMode()) },
		kind: settingToggle,
		set: func(c *config.Config, _ string) error {
			c.Display.OpenInInsert = flipped(c.Display.OpenInInsertMode())
			return nil
		},
	},
	{
		// Send and newline are a pair, so flipping enter writes both.
		key: "keys.insert.enter", group: "composer", label: "Enter key",
		show: enterKeyShow,
		kind: settingToggle,
		set:  toggleEnterKey,
	},
	{
		key: "display.color_messages", group: "display", label: "Tint message bodies",
		show: func(c config.Config) string { return onOff(c.Display.ColorMessages) },
		kind: settingToggle,
		set:  func(c *config.Config, _ string) error { c.Display.ColorMessages = !c.Display.ColorMessages; return nil },
	},
	{
		key: "display.room_name_rules", group: "names", label: "Name rules apply to room names",
		show: func(c config.Config) string { return onOff(c.Display.ApplyRoomNameRules()) },
		kind: settingToggle,
		set: func(c *config.Config, _ string) error {
			c.Display.RoomNameRules = flipped(c.Display.ApplyRoomNameRules())
			return nil
		},
	},
	{
		key: "display.max_name_length", group: "names", label: "Sender name width",
		show:  func(c config.Config) string { return showCount(c.Display.MaxNameLength, "no limit") },
		kind:  settingNumber,
		edit:  func(c config.Config) string { return countText(c.Display.MaxNameLength) },
		set:   func(c *config.Config, v string) error { return setInt(&c.Display.MaxNameLength, v) },
		count: func(c config.Config) int { return c.Display.MaxNameLength },
		help:  "columns — 0 or empty for no limit",
	},
	{
		key: "display.unread", group: "display", label: "Unread badges count",
		show: func(c config.Config) string { return orDefault(c.Display.Unread, config.UnreadMessages) },
		kind: settingChoice,
		choices: []settingChoiceOption{
			{config.UnreadMessages, config.UnreadMessages, "what you have not read"},
			{config.UnreadNotifications, config.UnreadNotifications,
				"what your push rules would have pinged you about"},
		},
		set: func(c *config.Config, v string) error { c.Display.Unread = v; return nil },
	},
	{
		key: "display.media.mode", group: "media", label: "Images",
		show: func(c config.Config) string {
			return orDefault(c.Display.Media.Mode, config.MediaPlaceholder)
		},
		kind:    settingChoice,
		choices: mediaModeChoices,
		set:     func(c *config.Config, v string) error { c.Display.Media.Mode = v; return nil },
	},
	{
		key: "display.media.max_height", group: "media", label: "Inline image height",
		show:  func(c config.Config) string { return showCount(c.Display.Media.MaxHeight, "default") },
		kind:  settingNumber,
		edit:  func(c config.Config) string { return countText(c.Display.Media.MaxHeight) },
		set:   func(c *config.Config, v string) error { return setInt(&c.Display.Media.MaxHeight, v) },
		count: func(c config.Config) int { return c.Display.Media.MaxHeight },
		help:  "rows — 0 or empty for the default",
	},
	{
		key: "codes.length", group: "codes", label: "Code length",
		show:   func(c config.Config) string { return showRange(c.Codes.Min(), c.Codes.Max()) },
		kind:   settingText,
		edit:   func(c config.Config) string { return showRange(c.Codes.Min(), c.Codes.Max()) },
		set:    setCodeLength,
		covers: []string{"codes.min_length", "codes.max_length"},
		help:   "a range like 4-8, or 6 for exactly six — empty for the default",
	},
	{
		key: "codes.letters", group: "codes", label: "Codes may contain letters",
		show: func(c config.Config) string { return onOff(c.Codes.LettersAllowed()) },
		kind: settingToggle,
		set: func(c *config.Config, _ string) error {
			c.Codes.Letters = flipped(c.Codes.LettersAllowed())
			return nil
		},
	},
	{
		key: "codes.digits", group: "codes", label: "Codes may contain digits",
		show: func(c config.Config) string { return onOff(c.Codes.DigitsAllowed()) },
		kind: settingToggle,
		set:  func(c *config.Config, _ string) error { c.Codes.Digits = flipped(c.Codes.DigitsAllowed()); return nil },
	},
	{
		key: "codes.require_digit", group: "codes", label: "Codes must contain a digit",
		show: func(c config.Config) string { return onOff(c.Codes.DigitRequired()) },
		kind: settingToggle,
		set: func(c *config.Config, _ string) error {
			c.Codes.RequireDigit = flipped(c.Codes.DigitRequired())
			return nil
		},
	},
	{
		// Turning it on refuses unless [clipboard] command and [codes] include are
		// set, rather than reading "on" while doing nothing.
		key: "clipboard.auto_copy", group: "codes", label: "Auto-copy codes as they arrive",
		show: func(c config.Config) string { return onOff(c.Clipboard.AutoCopy) },
		kind: settingToggle,
		set: func(c *config.Config, _ string) error {
			if c.Clipboard.AutoCopy {
				c.Clipboard.AutoCopy = false
				return nil
			}
			flipped := *c
			flipped.Clipboard.AutoCopy = true
			if err := setup.AutoCopyUnavailable(flipped); err != nil {
				return err
			}
			c.Clipboard.AutoCopy = true
			return nil
		},
	},
	{
		key: "codes.symbols", group: "codes", label: "Other characters in a code",
		show: func(c config.Config) string { return orDefault(c.Codes.Symbols, "none") },
		kind: settingText,
		edit: func(c config.Config) string { return c.Codes.Symbols },
		set:  func(c *config.Config, v string) error { c.Codes.Symbols = v; return nil },
		help: "the characters, written together — empty for none",
	},
	{
		key: "display.emoji.set", group: "emoji", label: "Emoji set",
		show: func(c config.Config) string { return orDefault(c.Display.Emoji.Set, emojiCurated) },
		kind: settingChoice,
		choices: []settingChoiceOption{
			{emojiCurated, emojiCurated, fmt.Sprintf("%d hand-picked, browsable end to end", len(emojiShortcodes))},
			{emojiStandard, emojiStandard, fmt.Sprintf("+%d single-glyph emoji from Unicode", len(standardEmoji))},
			{emojiComplete, emojiComplete, fmt.Sprintf("+%d composed: families, professions, flags", len(sequenceEmoji))},
		},
		set: func(c *config.Config, v string) error { c.Display.Emoji.Set = v; return nil },
	},
	{
		key: "display.skin_tone", group: "emoji", label: "Emoji skin tone",
		show:    func(c config.Config) string { return orDefault(c.Display.SkinTone, "none") },
		kind:    settingChoice,
		choices: skinToneChoices(),
		set:     func(c *config.Config, v string) error { c.Display.SkinTone = v; return nil },
	},
	{
		key: "display.reactions.scope", group: "emoji", label: "React palette ranking",
		show: func(c config.Config) string { return orDefault(c.Display.Reactions.Scope, "room") },
		kind: settingChoice,
		choices: []settingChoiceOption{
			{"room", "room", "this conversation, then its space, then everywhere"},
			{"space", "space", "the space, then everywhere"},
			{"global", "global", "one ranking everywhere"},
			{"static", "static", "no ranking; the fixed list"},
		},
		set: func(c *config.Config, v string) error { c.Display.Reactions.Scope = v; return nil },
	},
}

// skinToneChoices lists the tones with a sample of each.
func skinToneChoices() []settingChoiceOption {
	names := skinToneNames()
	out := make([]settingChoiceOption, 0, len(names))
	for _, name := range names {
		sample := "👍"
		if mod := skinToneModifier(name); mod != "" {
			sample = "👍" + mod
		}
		out = append(out, settingChoiceOption{name, name, sample + " 👋" + skinToneModifier(name)})
	}
	return out
}

// The settings screen is two levels: the groups, then one group's settings. A change
// is applied and saved at once and the group stays open on the same row, its value
// updated, so several can be changed in a row; esc steps back a level.

// settingGroup is one entry of the first level.
type settingGroup struct{ key, label string }

var settingGroups = []settingGroup{
	{"notifications", "Notifications"},
	{"names", "Names"},
	{"display", "Messages"},
	{"rooms", "Rail and room list"},
	{"composer", "Composer"},
	{"media", "Images and media"},
	{"emoji", "Emoji and reactions"},
	{"look", "Look"},
	{"spelling", "Spelling"},
	{"completion", "Completion"},
	{"assist", "Assistant"},
	{"codes", "Codes and clipboard"},
	{"spam", "Spam"},
	{"agent", "AI agent access"},
	{"networks", "Accounts and networks"},
	{"advanced", "Commands, logs and schedule"},
	{"tags", "Tags"},
}

// groupSettings is the settings listed under a group, in settingsList order.
func groupSettings(group string) []setting {
	var out []setting
	for i := range settingsList {
		if settingsList[i].group == group {
			out = append(out, settingsList[i])
		}
	}
	return out
}

// groupLabel is a group's words, or its key when it has none.
func groupLabel(group string) string {
	for _, g := range settingGroups {
		if g.key == group {
			return g.label
		}
	}
	return group
}

// openSettings opens the first level.
func (m Model) openSettings() (Model, tea.Cmd) { return m.settingsTop(""), nil }

// settingsTop shows the groups, the cursor on the one named at. A group holding one
// row that leads somewhere (Tags) is that row.
func (m Model) settingsTop(at string) Model {
	items := make([]pickerItem, 0, len(settingGroups))
	for _, g := range settingGroups {
		rows := m.groupRows(g.key)
		detail := fmt.Sprintf("%d settings", len(rows))
		if len(rows) == 1 {
			detail = rows[0].show(m.conf.base)
		}
		items = append(items, pickerItem{label: g.label, detail: detail, value: g.key, match: g.label})
	}
	m.choosing.setting, m.choosing.settingGroup = "", ""
	m.picker = newPicker(pickerSettingGroups, items).at(at)
	return m
}

// settingsIn shows one group's settings, the cursor on the one keyed at.
func (m Model) settingsIn(group, at string) Model {
	rows := m.groupRows(group)
	items := make([]pickerItem, 0, len(rows))
	for i := range rows {
		items = append(items, pickerItem{
			label:  rows[i].label,
			detail: choiceWords(rows[i], rows[i].show(m.conf.base)),
			value:  rows[i].key,
			match:  rows[i].label + " " + rows[i].key,
		})
	}
	m.choosing.settingGroup, m.choosing.setting = group, at
	spec := pickerSpecs[pickerSetting]
	spec.title = "Settings: " + m.settingGroupLabel(group)
	m.picker = newPickerWith(pickerSetting, spec, items).at(at)
	return m
}

// choiceWords is a setting's value as its row shows it: an empty choice by its option's
// words ("unset"), anything else as it is.
func choiceWords(s setting, value string) string {
	if value != "" {
		return value
	}
	for _, c := range s.choices {
		if c.value == "" {
			return c.label
		}
	}
	return value
}

// chooseSettingGroup opens a group; one that is a single row leading somewhere goes
// straight there.
func (m Model) chooseSettingGroup(group string) (Model, tea.Cmd) {
	if rows := m.groupRows(group); len(rows) == 1 && rows[0].kind == settingOpen {
		m.choosing.settingGroup = group
		return rows[0].open(m)
	}
	return m.settingsIn(group, ""), nil
}

// chooseSetting acts on a chosen setting according to its kind. Nothing closes the
// group: a toggle flips in place, a choice returns to it, and a typed value is typed
// on its row.
func (m Model) chooseSetting(key string) (Model, tea.Cmd) {
	s, ok := m.setting(key)
	if !ok {
		return m.settingsTop(m.choosing.settingGroup), nil
	}
	switch s.kind {
	case settingOpen:
		m.choosing.setting = key
		if s.open == nil { // a record table's row: its records
			return m.settingsIn(key, ""), nil
		}
		return s.open(m)
	case settingToggle:
		return m.writeSetting(s, "")
	case settingChoice:
		m.choosing.setting = key
		m.picker = newPicker(pickerSettingValue, settingValueItems(s, s.show(m.conf.base))).at(s.show(m.conf.base))
		return m, nil
	case settingText, settingNumber:
		return m.editSettingInPlace(s, editText(s, m.conf.base)), nil
	case settingList:
		return m.settingEntriesOpen(s.key, ""), nil
	}
	return m, nil
}

// editSettingInPlace types a setting's value on its own row, starting from text, held
// selected so the first key typed replaces it.
func (m Model) editSettingInPlace(s setting, text string) Model {
	m = m.settingsIn(s.group, s.key)
	m = m.openPromptWith(promptSetting, text)
	m.prompt.fresh = text != ""
	return m
}

// stepSetting moves the number under the cursor by delta, no lower than its least.
func (m Model) stepSetting(delta int) (Model, tea.Cmd) {
	item, ok := m.picker.selected()
	if !ok {
		return m, nil
	}
	s, ok := m.setting(item.value)
	if !ok || s.kind != settingNumber {
		return m.say("+ and - change numbers; enter changes this one"), nil
	}
	return m.writeSetting(s, strconv.Itoa(max(s.count(m.conf.base)+delta, s.least)))
}

// settingValueItems lists a choice setting's values, marking the current one.
func settingValueItems(s setting, current string) []pickerItem {
	items := make([]pickerItem, 0, len(s.choices))
	for _, choice := range s.choices {
		detail := choice.detail
		if choice.value == current {
			detail = "current · " + detail
		}
		items = append(items, pickerItem{
			label:  choice.label,
			detail: detail,
			value:  choice.value,
			match:  choice.label + " " + choice.detail,
		})
	}
	return items
}

// chooseSettingValue applies a picked value and returns to the group.
func (m Model) chooseSettingValue(value string) (Model, tea.Cmd) {
	s, ok := m.setting(m.choosing.setting)
	if !ok {
		return m.settingsTop(m.choosing.settingGroup), nil
	}
	return m.writeSetting(s, value)
}

// submitSetting applies a typed value. One that will not parse stays on the row as
// typed, the reason on the status line, to be fixed or abandoned with esc.
func (m Model) submitSetting(value string) (Model, tea.Cmd) {
	s, ok := m.setting(m.choosing.setting)
	if !ok {
		return m, nil
	}
	cfg := m.conf.base.Clone()
	if err := s.set(&cfg, strings.TrimSpace(value)); err != nil {
		m = m.editSettingInPlace(s, value)
		m.prompt.fresh = false
		return m.sayErr(s.label, err), nil
	}
	return m.writeSetting(s, strings.TrimSpace(value))
}

// writeSetting applies one setting, saves, and shows its group again on its row; a
// value that will not parse changes nothing.
func (m Model) writeSetting(s setting, value string) (Model, tea.Cmd) {
	cfg := m.conf.base.Clone()
	if err := s.set(&cfg, value); err != nil {
		return m.settingsIn(s.group, s.key).sayErr(s.label, err), nil
	}
	next, cmd := m.applyConfig(cfg, s.label+": "+s.show(cfg))
	return next.settingsIn(s.group, s.key), cmd
}

// settingsBack is esc in a settings picker: a level up, or false when it closes. A
// setting's value picker returns to its group, a group to the groups, and the tag
// editor's levels to the one above.
func (m Model) settingsBack() (Model, bool) {
	group := m.choosing.settingGroup
	switch m.picker.kind {
	case pickerSettingValue, pickerSettingEntries:
		return m.settingsIn(group, m.choosing.setting), true
	case pickerSetting:
		if parent, at, ok := settingsParent(group); ok {
			return m.settingsIn(parent, at), true
		}
		return m.settingsTop(group), true
	case pickerTags:
		return m.settingsTop("tags"), true
	case pickerTagEdit:
		return m.tagsOpen(), true
	case pickerTagEntries:
		return m.tagOpen(m.choosing.tag.tag), true
	default:
		return m, false
	}
}

// settingHelp is what the setting being typed may be.
func (m Model) settingHelp() string {
	if m.prompt.kind == promptSettingEntry {
		return "the entry — empty removes it"
	}
	s, ok := m.setting(m.choosing.setting)
	if !ok || s.help == "" {
		return "type the new value"
	}
	return s.help
}

// popupSeconds is the popup timeout as + and - step it: -1 until dismissed, 0 the
// desktop's default, else seconds.
func popupSeconds(c config.Config) int {
	if t := c.Notifications.Timeout; t != nil && *t <= 0 {
		return *t
	}
	return int(c.Notifications.PopupTimeout().Seconds())
}

// findSetting looks a setting up by key.
func findSetting(key string) (setting, bool) {
	for i := range settingsList {
		if settingsList[i].key == key {
			return settingsList[i], true
		}
	}
	return setting{}, false
}

// timeoutNote reads the popup timeout back in words.
func timeoutNote(n config.Notifications) string {
	if n.Timeout == nil {
		return strconv.Itoa(int(n.PopupTimeout().Seconds())) + "s"
	}
	switch {
	case *n.Timeout < 0:
		return "until dismissed"
	case *n.Timeout == 0:
		return "your desktop's default"
	}
	return strconv.Itoa(int(n.PopupTimeout().Seconds())) + "s"
}

// setPopupTimeout reads a number of seconds; empty restores the default.
func setPopupTimeout(c *config.Config, v string) error {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		c.Notifications.Timeout = nil
		return nil
	}
	seconds, err := strconv.Atoi(trimmed)
	if err != nil {
		return fmt.Errorf(
			"%q is not a number of seconds (-1 keeps it up until dismissed, 0 leaves the length to your desktop)", v)
	}
	// Any negative is "never", normalized to the config's one spelling.
	if seconds < 0 {
		seconds = -1
	}
	c.Notifications.Timeout = &seconds
	return nil
}

// ruleCountNote is the rules row's qualifier.
func ruleCountNote(n int) string {
	switch n {
	case 0:
		return "none"
	case 1:
		return "1 rule"
	default:
		return strconv.Itoa(n) + " rules"
	}
}

// onOff renders a boolean the way the row reads it.
func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// orDefault names the default when v is unset. Undecorated, because the shown value
// is compared against the choices to mark the current one.
func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// showCount renders a number, naming what zero means rather than showing "0".
func showCount(n int, zero string) string {
	if n == 0 {
		return zero
	}
	return strconv.Itoa(n)
}

// showRange renders a length range, collapsing a range of one.
func showRange(lo, hi int) string {
	if lo == hi {
		return strconv.Itoa(lo)
	}
	return strconv.Itoa(lo) + "-" + strconv.Itoa(hi)
}

// setCodeLength parses "4-8", or a single number for an exact length.
func setCodeLength(c *config.Config, value string) error {
	if value == "" {
		c.Codes.MinLength, c.Codes.MaxLength = 0, 0
		return nil
	}
	lo, hi, ranged := strings.Cut(value, "-")
	if !ranged {
		hi = lo
	}
	var min, max int
	if err := setInt(&min, strings.TrimSpace(lo)); err != nil {
		return err
	}
	if err := setInt(&max, strings.TrimSpace(hi)); err != nil {
		return err
	}
	if min < 1 || max < min {
		return fmt.Errorf("%q is not a length range — try 4-8, or 6 for exactly six", value)
	}
	c.Codes.MinLength, c.Codes.MaxLength = min, max
	return nil
}

// editText is what the prompt starts with.
func editText(s setting, cfg config.Config) string {
	if s.edit != nil {
		return s.edit(cfg)
	}
	return s.show(cfg)
}

// countText is a count as the parser writes it: empty when unset.
func countText(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// setInt parses a count; empty means unset (zero).
func setInt(target *int, value string) error {
	if value == "" {
		*target = 0
		return nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return fmt.Errorf("%q is not a number", value)
	}
	*target = n
	return nil
}

// enterKeyShow names the send and newline keys.
func enterKeyShow(c config.Config) string {
	return fmt.Sprintf("%s sends · %s new line",
		firstKey(c.Keys.Insert.Send), firstKey(c.Keys.Insert.Newline))
}

// toggleEnterKey swaps the send and newline bindings, so no key is claimed twice.
func toggleEnterKey(c *config.Config, _ string) error {
	c.Keys.Insert.Send, c.Keys.Insert.Newline = c.Keys.Insert.Newline, c.Keys.Insert.Send
	return nil
}

// firstKey is a binding list's preferred key — what the app would show for it.
func firstKey(list string) string {
	if keys := splitKeys(list); len(keys) > 0 {
		return spellSequence(keys[0])
	}
	return "(unbound)"
}

// flipped is a pointer to !v, for toggling an optional boolean.
func flipped(v bool) *bool {
	v = !v
	return &v
}
