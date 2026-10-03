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
	settingText                      // prompts for a typed value
	settingOpen                      // leads somewhere rather than holding a value
)

// setting is one preference.
type setting struct {
	key   string
	label string
	// show renders the current value for the row's qualifier.
	show    func(config.Config) string
	kind    settingKind
	choices []settingChoiceOption
	// edit is what a settingText prompt starts out holding, when the displayed form
	// ("no limit") is not typeable back. Empty means unset.
	edit func(config.Config) string
	// set writes a new value; an error is shown to the user.
	set func(*config.Config, string) error
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

// settingsList is the whole table, in display order.
var settingsList = []setting{
	{
		key: "notifications.enabled", label: "Notifications",
		show: func(c config.Config) string { return onOff(c.Notifications.Enabled) },
		kind: settingToggle,
		set:  func(c *config.Config, _ string) error { c.Notifications.Enabled = !c.Notifications.Enabled; return nil },
	},
	{
		key: "notifications.show", label: "Notify me for",
		show:    accountShow,
		kind:    settingChoice,
		choices: levelChoices,
		set:     setAccountShow,
	},
	{
		key: "notifications.ring", label: "Play a sound for",
		show:    accountRing,
		kind:    settingChoice,
		choices: ringChoices,
		set:     setAccountRing,
	},
	{
		key: "notifications.quiet_hours", label: "Quiet hours",
		show: func(c config.Config) string { return orDefault(quietWindowText(c), "none") },
		kind: settingText,
		edit: quietWindowText,
		set:  setQuietWindow,
	},
	{
		key: "notifications.quiet_show", label: "During quiet hours, notify for",
		show:    quietShow,
		kind:    settingChoice,
		choices: levelChoices,
		set:     setQuietShow,
	},
	{
		key: "notifications.desktop", label: "Desktop popups",
		show: func(c config.Config) string { return onOff(c.Notifications.Desktop) },
		kind: settingToggle,
		set:  func(c *config.Config, _ string) error { c.Notifications.Desktop = !c.Notifications.Desktop; return nil },
	},
	{
		key: "notifications.rules", label: "Notification rules",
		show: func(c config.Config) string { return ruleCountNote(len(c.Notifications.Rules)) },
		kind: settingOpen,
		open: Model.openRuleList,
	},
	{
		key: "tags", label: "Tags",
		show: func(c config.Config) string { return showCount(len(c.Tags), "none") },
		kind: settingOpen,
		open: Model.openTags,
	},
	{
		key: "notifications.sound", label: "Notification sound",
		show: func(c config.Config) string { return orDefault(c.Notifications.Sound, "silent") },
		kind: settingText,
		edit: func(c config.Config) string { return c.Notifications.Sound },
		set:  func(c *config.Config, v string) error { c.Notifications.Sound = v; return nil },
	},
	{
		key: "notifications.timeout", label: "Popup stays up for",
		show: func(c config.Config) string { return timeoutNote(c.Notifications) },
		kind: settingText,
		edit: func(c config.Config) string {
			if c.Notifications.Timeout == nil {
				return ""
			}
			return strconv.Itoa(*c.Notifications.Timeout)
		},
		set: setPopupTimeout,
	},
	{
		key: "display.open_in_insert_mode", label: "Open rooms ready to type",
		show: func(c config.Config) string { return onOff(c.Display.OpenInInsertMode()) },
		kind: settingToggle,
		set: func(c *config.Config, _ string) error {
			c.Display.OpenInInsert = flipped(c.Display.OpenInInsertMode())
			return nil
		},
	},
	{
		// Send and newline are a pair, so flipping enter writes both.
		key: "keys.insert.enter", label: "Enter key",
		show: enterKeyShow,
		kind: settingToggle,
		set:  toggleEnterKey,
	},
	{
		key: "display.color_messages", label: "Tint message bodies",
		show: func(c config.Config) string { return onOff(c.Display.ColorMessages) },
		kind: settingToggle,
		set:  func(c *config.Config, _ string) error { c.Display.ColorMessages = !c.Display.ColorMessages; return nil },
	},
	{
		key: "display.room_name_rules", label: "Name rules apply to room names",
		show: func(c config.Config) string { return onOff(c.Display.ApplyRoomNameRules()) },
		kind: settingToggle,
		set: func(c *config.Config, _ string) error {
			c.Display.RoomNameRules = flipped(c.Display.ApplyRoomNameRules())
			return nil
		},
	},
	{
		key: "display.max_name_length", label: "Sender name width",
		show: func(c config.Config) string { return showCount(c.Display.MaxNameLength, "no limit") },
		kind: settingText,
		edit: func(c config.Config) string { return countText(c.Display.MaxNameLength) },
		set:  func(c *config.Config, v string) error { return setInt(&c.Display.MaxNameLength, v) },
	},
	{
		key: "display.unread", label: "Unread badges count",
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
		key: "display.media.mode", label: "Images",
		show: func(c config.Config) string {
			return orDefault(c.Display.Media.Mode, config.MediaPlaceholder)
		},
		kind:    settingChoice,
		choices: mediaModeChoices,
		set:     func(c *config.Config, v string) error { c.Display.Media.Mode = v; return nil },
	},
	{
		key: "display.media.max_height", label: "Inline image height",
		show: func(c config.Config) string { return showCount(c.Display.Media.MaxHeight, "default") },
		kind: settingText,
		edit: func(c config.Config) string { return countText(c.Display.Media.MaxHeight) },
		set:  func(c *config.Config, v string) error { return setInt(&c.Display.Media.MaxHeight, v) },
	},
	{
		key: "codes.length", label: "Code length",
		show: func(c config.Config) string { return showRange(c.Codes.Min(), c.Codes.Max()) },
		kind: settingText,
		edit: func(c config.Config) string { return showRange(c.Codes.Min(), c.Codes.Max()) },
		set:  setCodeLength,
	},
	{
		key: "codes.letters", label: "Codes may contain letters",
		show: func(c config.Config) string { return onOff(c.Codes.LettersAllowed()) },
		kind: settingToggle,
		set: func(c *config.Config, _ string) error {
			c.Codes.Letters = flipped(c.Codes.LettersAllowed())
			return nil
		},
	},
	{
		key: "codes.digits", label: "Codes may contain digits",
		show: func(c config.Config) string { return onOff(c.Codes.DigitsAllowed()) },
		kind: settingToggle,
		set:  func(c *config.Config, _ string) error { c.Codes.Digits = flipped(c.Codes.DigitsAllowed()); return nil },
	},
	{
		key: "codes.require_digit", label: "Codes must contain a digit",
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
		key: "clipboard.auto_copy", label: "Auto-copy codes as they arrive",
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
		key: "codes.symbols", label: "Other characters in a code",
		show: func(c config.Config) string { return orDefault(c.Codes.Symbols, "none") },
		kind: settingText,
		edit: func(c config.Config) string { return c.Codes.Symbols },
		set:  func(c *config.Config, v string) error { c.Codes.Symbols = v; return nil },
	},
	{
		key: "display.emoji.set", label: "Emoji set",
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
		key: "display.skin_tone", label: "Emoji skin tone",
		show:    func(c config.Config) string { return orDefault(c.Display.SkinTone, "none") },
		kind:    settingChoice,
		choices: skinToneChoices(),
		set:     func(c *config.Config, v string) error { c.Display.SkinTone = v; return nil },
	},
	{
		key: "display.reactions.scope", label: "React palette ranking",
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

// openSettings offers the plain preferences, each showing what it is currently set to.
func (m Model) openSettings() (Model, tea.Cmd) {
	m.picker = newPicker(pickerSetting, m.settingItems())
	return m, nil
}

// settingItems renders the table, current values included.
func (m Model) settingItems() []pickerItem {
	items := make([]pickerItem, 0, len(settingsList))
	for _, s := range settingsList {
		items = append(items, pickerItem{
			label:  s.label,
			detail: s.show(m.conf.base),
			value:  s.key,
			match:  s.label + " " + s.key,
		})
	}
	return items
}

// chooseSetting acts on a chosen setting according to its kind.
func (m Model) chooseSetting(key string) (Model, tea.Cmd) {
	s, ok := findSetting(key)
	if !ok {
		return m.closePicker(), nil
	}
	switch s.kind {
	case settingOpen:
		return s.open(m)
	case settingToggle:
		m = m.closePicker()
		return m.writeSetting(s, "")
	case settingChoice:
		m.choosing.setting = key
		m.picker = newPicker(pickerSettingValue, settingValueItems(s, s.show(m.conf.base)))
		return m, nil
	case settingText:
		m.choosing.setting = key
		m = m.closePicker()
		m = m.openPromptWith(promptSetting, editText(s, m.conf.base))
		return m, nil
	}
	return m.closePicker(), nil
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

// chooseSettingValue applies a picked value.
func (m Model) chooseSettingValue(value string) (Model, tea.Cmd) {
	s, ok := findSetting(m.choosing.setting)
	m = m.closePicker()
	if !ok {
		return m, nil
	}
	return m.writeSetting(s, value)
}

// submitSetting applies a typed value.
func (m Model) submitSetting(value string) (Model, tea.Cmd) {
	s, ok := findSetting(m.choosing.setting)
	if !ok {
		return m, nil
	}
	return m.writeSetting(s, strings.TrimSpace(value))
}

// writeSetting applies one setting and saves; a value that will not parse changes nothing.
func (m Model) writeSetting(s setting, value string) (Model, tea.Cmd) {
	m.choosing.setting = ""
	cfg := m.conf.base.Clone()
	if err := s.set(&cfg, value); err != nil {
		m = m.sayErr(s.label, err)
		return m, nil
	}
	return m.applyConfig(cfg, "", s.label+": "+s.show(cfg))
}

// findSetting looks a setting up by key.
func findSetting(key string) (setting, bool) {
	for _, s := range settingsList {
		if s.key == key {
			return s, true
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
