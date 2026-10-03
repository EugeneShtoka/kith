package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Authoring notification rules from what you are looking at: a rule is keyed on a
// room ID or MXID the UI does not show, so you point at the thing and pick a preset.

// ruleTarget is what a rule being authored applies to, held as values rather than as
// a pointer into the config so a list that changes underneath cannot redirect it.
type ruleTarget struct {
	match  string // the rule's Match: a room ID, a space entry, or empty for "anywhere"
	sender string // MXID, empty for a place-only rule
	what   string // names the target in prompts and confirmations
}

// rulePreset is one offered rule shape.
type rulePreset struct {
	key           string
	label, detail string
	apply         func(*config.Rule) // nil for presets handled specially
}

// rulePresets are deliberately few; the config file expresses the rest. A scoped rule
// is narrower than quiet hours or DND, so `show = all` already gets through both.
var rulePresets = []rulePreset{
	{
		key: "all", label: "Notify me for everything",
		detail: "show = all · gets through quiet hours and do-not-disturb too",
		apply:  func(r *config.Rule) { r.Show = "all" },
	},
	{
		key: "mentions", label: "Only when I'm mentioned",
		detail: "show = mention",
		apply:  func(r *config.Rule) { r.Show = "mention" },
	},
	{
		key: "mute", label: "Mute — never notify",
		detail: "show = none",
		apply:  func(r *config.Rule) { r.Show = "none" },
	},
	{
		key: "silent", label: "Seen but not heard",
		detail: "ring = none · the popup still appears",
		apply:  func(r *config.Rule) { r.Ring = "none" },
	},
	{
		key: "sound", label: "Give it its own sound…",
		detail: "asks for a file",
	},
	{
		key: "name", label: "Name this rule…",
		detail: "so the status line can say which rule silenced you",
	},
	{
		key: "remove", label: "Remove the rule",
		detail: "back to the account-wide rule",
	},
}

// openRuleForSender starts the flow from a message by asking for the scope.
func (m Model) openRuleForSender() (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	if !ok {
		return m, nil
	}
	room, found := m.roomByID(msg.RoomID)
	if !found {
		return m, nil
	}
	// senderName: the names are isolated inside sentences of ours (see isolate).
	who := m.senderName(msg)
	m.aimedAt.ruleScopes = []ruleTarget{
		{match: string(room.ID), sender: msg.Sender, what: who + " in " + m.roomName(room)},
		{match: "", sender: msg.Sender, what: who + ", anywhere"},
		{match: string(room.ID), what: "everyone in " + m.roomName(room)},
	}
	for _, home := range m.homesOf(room.ID) {
		entry, shown := homeEntry(home), isolate(domain.HomeLabel(home))
		m.aimedAt.ruleScopes = append(m.aimedAt.ruleScopes,
			ruleTarget{match: entry, sender: msg.Sender, what: who + " in " + shown},
			ruleTarget{match: entry, what: "everything in " + shown})
	}
	m.picker = newPicker(pickerRuleScope, ruleScopeItems(m.aimedAt.ruleScopes))
	return m, nil
}

// openRuleForRoom starts the flow from the room list: the room and its spaces.
func (m Model) openRuleForRoom() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok || room.IsInvite() {
		return m, nil
	}
	m.aimedAt.ruleScopes = []ruleTarget{{match: string(room.ID), what: m.roomName(room)}}
	for _, home := range m.homesOf(room.ID) {
		m.aimedAt.ruleScopes = append(m.aimedAt.ruleScopes, ruleTarget{match: homeEntry(home), what: "everything in " + domain.HomeLabel(home)})
	}
	m.picker = newPicker(pickerRuleScope, ruleScopeItems(m.aimedAt.ruleScopes))
	return m, nil
}

// openRuleForGroup starts the flow from the rail. Synthetic groups are not places.
func (m Model) openRuleForGroup() (Model, tea.Cmd) {
	entry, ok := m.currentGroup()
	if !ok {
		return m, nil
	}
	if !isSpaceGroup(entry.key) && !isTagGroup(entry.key) {
		m = m.say("rules apply to spaces, tags and rooms, not to " + entry.label)
		return m, nil
	}
	// A space's rail key is its bare name, not yet a place entry; a tag's is one.
	m.aimedAt.ruleScopes = []ruleTarget{{match: homeEntry(entry.key), what: "everything in " + entry.label}}
	return m.chooseRuleScope(0)
}

// ruleScopeItems lists the scopes in words, with the config terms as detail.
func ruleScopeItems(scopes []ruleTarget) []pickerItem {
	items := make([]pickerItem, 0, len(scopes))
	for i, scope := range scopes {
		items = append(items, pickerItem{
			label:  scope.what,
			detail: scope.describe(),
			value:  fmt.Sprint(i),
			match:  scope.what,
		})
	}
	return items
}

// describe is the scope in the config's own terms.
func (t ruleTarget) describe() string {
	switch {
	case t.match != "" && t.sender != "":
		return t.sender + " in " + t.match
	case t.sender != "":
		return t.sender
	default:
		return t.match
	}
}

// chooseRuleScope moves from the scope to the preset.
func (m Model) chooseRuleScope(index int) (Model, tea.Cmd) {
	if index < 0 || index >= len(m.aimedAt.ruleScopes) {
		return m.closePicker(), nil
	}
	m.aimedAt.rule = m.aimedAt.ruleScopes[index]
	m.picker = newPicker(pickerRulePreset, rulePresetItems(m.existingRule(m.aimedAt.rule)))
	return m, nil
}

// rulePresetItems lists the presets, marking those in effect; "remove" only when a rule exists.
func rulePresetItems(existing *config.Rule) []pickerItem {
	items := make([]pickerItem, 0, len(rulePresets))
	for _, preset := range rulePresets {
		if preset.key == "remove" && existing == nil {
			continue
		}
		detail := preset.detail
		if existing != nil && presetInEffect(preset, *existing) {
			detail = "currently set · " + detail
		}
		items = append(items, pickerItem{
			label:  preset.label,
			detail: detail,
			value:  preset.key,
			match:  preset.label,
		})
	}
	return items
}

// presetInEffect reports whether a rule already carries what a preset would set.
func presetInEffect(preset rulePreset, rule config.Rule) bool {
	switch preset.key {
	case "all", "mentions", "mute":
		return rule.Show == presetLevel(preset.key)
	case "silent":
		return rule.Ring == "none"
	case "sound":
		return rule.Sound != ""
	case "name":
		return rule.Name != ""
	default:
		return false
	}
}

// presetLevel is the level a level-setting preset sets.
func presetLevel(key string) string {
	switch key {
	case "all":
		return "all"
	case "mentions":
		return "mention"
	case "mute":
		return "none"
	default:
		return ""
	}
}

// chooseRulePreset applies a preset, branching for the ones that need more input.
func (m Model) chooseRulePreset(key string) (Model, tea.Cmd) {
	m = m.closePicker()
	switch key {
	case "remove":
		return m.removeRule()
	case "sound":
		m = m.openPromptWith(promptRuleSound, m.existingRuleField(func(r config.Rule) string { return r.Sound }))
		return m, nil
	case "name":
		m = m.openPromptWith(promptRuleName, m.existingRuleField(func(r config.Rule) string { return r.Name }))
		return m, nil
	}
	for _, preset := range rulePresets {
		if preset.key == key && preset.apply != nil {
			return m.applyRule(preset.apply, preset.label)
		}
	}
	return m, nil
}

// submitRuleSound finishes the sound preset; an empty path clears it.
func (m Model) submitRuleSound(path string) (Model, tea.Cmd) {
	path = strings.TrimSpace(path)
	return m.applyRule(func(r *config.Rule) { r.Sound = path }, soundNote(path))
}

// submitRuleName finishes the naming preset; an empty name clears it.
func (m Model) submitRuleName(name string) (Model, tea.Cmd) {
	name = strings.TrimSpace(name)
	// A name alone would be dropped by ruleIsEmpty; say so instead.
	if m.existingRule(m.aimedAt.rule) == nil {
		m.aimedAt.rule, m.aimedAt.ruleScopes = ruleTarget{}, nil
		m = m.say("a name alone changes nothing — set what the rule does first")
		return m, nil
	}
	what := "named it " + name
	if name == "" {
		what = "removed the name"
	}
	return m.applyRule(func(r *config.Rule) { r.Name = name }, what)
}

func soundNote(path string) string {
	if path == "" {
		return "no sound"
	}
	return "sound: " + path
}

// applyRule writes the change onto the target's rule, creating one, and applies it live.
func (m Model) applyRule(change func(*config.Rule), what string) (Model, tea.Cmd) {
	target := m.aimedAt.rule
	m.aimedAt.rule, m.aimedAt.ruleScopes = ruleTarget{}, nil
	if target.match == "" && target.sender == "" {
		return m, nil
	}
	notifs := m.conf.base.Clone().Notifications
	notifs.Rules = upsertRule(notifs.Rules, target, change)
	return m.applyNotifications(notifs, what+" for "+target.what)
}

// removeRule drops the target's rule entirely, returning it to the global settings.
func (m Model) removeRule() (Model, tea.Cmd) {
	target := m.aimedAt.rule
	m.aimedAt.rule, m.aimedAt.ruleScopes = ruleTarget{}, nil
	notifs := m.conf.base.Clone().Notifications
	notifs.Rules = removeRuleFor(notifs.Rules, target)
	return m.applyNotifications(notifs, "removed the rule for "+target.what)
}

// upsertRule applies change to the rule for target, adding one if it does not exist.
// One rule per (match, sender) pair, rather than several resolved by file order.
func upsertRule(rules []config.Rule, target ruleTarget, change func(*config.Rule)) []config.Rule {
	out := make([]config.Rule, 0, len(rules)+1)
	found := false
	for i := range rules {
		rule := rules[i]
		if rule.Match == target.match && rule.Sender == target.sender {
			found = true
			change(&rule)
			if !ruleIsEmpty(rule) {
				out = append(out, rule)
			}
			continue
		}
		out = append(out, rule)
	}
	if !found {
		rule := config.Rule{Match: target.match, Sender: target.sender}
		change(&rule)
		if !ruleIsEmpty(rule) {
			out = append(out, rule)
		}
	}
	return out
}

// removeRuleFor drops the rule matching target exactly.
func removeRuleFor(rules []config.Rule, target ruleTarget) []config.Rule {
	out := make([]config.Rule, 0, len(rules))
	for i := range rules {
		if rules[i].Match == target.match && rules[i].Sender == target.sender {
			continue
		}
		out = append(out, rules[i])
	}
	return out
}

// ruleIsEmpty reports whether a rule sets nothing. A name alone does not count.
func ruleIsEmpty(rule config.Rule) bool {
	return rule.Show == "" && rule.Ring == "" && rule.When == "" && rule.Sound == ""
}

// existingRule is the rule already set for a target, or nil.
func (m Model) existingRule(target ruleTarget) *config.Rule {
	for i := range m.conf.base.Notifications.Rules {
		rule := m.conf.base.Notifications.Rules[i]
		if rule.Match == target.match && rule.Sender == target.sender {
			return &rule
		}
	}
	return nil
}

// existingRuleField prefills a prompt from the aimed-at rule, empty when there is none.
func (m Model) existingRuleField(field func(config.Rule) string) string {
	if rule := m.existingRule(m.aimedAt.rule); rule != nil {
		return field(*rule)
	}
	return ""
}

// Reading rules back: a rule's IDs say nothing in the config file, so the list
// resolves each scope back to names.

// openRuleList shows every rule; choosing one reopens it at the preset picker.
func (m Model) openRuleList() (Model, tea.Cmd) {
	rules := m.conf.base.Notifications.Rules
	if len(rules) == 0 {
		m = m.closePicker()
		m = m.say("no notification rules yet — press " +
			m.keys.keyHint(scopeTimeline, actNotifyRule) + " on a message, a room or a space to write one")
		return m, nil
	}
	m.aimedAt.ruleScopes = make([]ruleTarget, 0, len(rules))
	for i := range rules {
		m.aimedAt.ruleScopes = append(m.aimedAt.ruleScopes, ruleTarget{
			match: rules[i].Match, sender: rules[i].Sender, what: m.describeRuleScope(rules[i]),
		})
	}
	m.picker = newPicker(pickerRuleList, ruleListItems(m.aimedAt.ruleScopes, rules))
	return m, nil
}

// describeRuleScope names what a rule applies to in the UI's words. An ID that
// resolves to nothing is shown as itself, so a rule for a left room stays removable.
func (m Model) describeRuleScope(rule config.Rule) string {
	who := ""
	if rule.Sender != "" {
		// Alias when they have one, else the MXID; isolated, as this is a sentence.
		who = isolate(m.inviterName(rule.Sender))
	}
	where := ""
	if rule.Match != "" {
		where = m.placeName(rule.Match)
	}
	switch {
	case who != "" && where != "":
		return who + " in " + where
	case who != "":
		return who + ", anywhere"
	default:
		return where
	}
}

// placeName resolves a rule's match to a room or space name, else the match itself.
func (m Model) placeName(match string) string {
	if room, ok := m.roomByID(domain.RoomID(match)); ok {
		return m.roomName(room)
	}
	if space, ok := domain.SpaceOf(match); ok {
		return isolate(space)
	}
	return isolate(match)
}

// ruleListItems lists the rules, each with what it does as the qualifier.
func ruleListItems(scopes []ruleTarget, rules []config.Rule) []pickerItem {
	items := make([]pickerItem, 0, len(scopes))
	for i, scope := range scopes {
		// A named rule is listed by the name the status line uses for it.
		label, detail := scope.what, describeRule(rules[i])
		match := scope.what
		if name := rules[i].Name; name != "" {
			label = isolate(name)
			detail = scope.what + " · " + detail
			match = name + " " + scope.what
		}
		items = append(items, pickerItem{
			label:  label,
			detail: detail,
			value:  fmt.Sprint(i),
			match:  match,
		})
	}
	return items
}

// describeRule says what a rule does, in the config's own terms.
func describeRule(rule config.Rule) string {
	var parts []string
	if rule.Show != "" {
		parts = append(parts, "show = "+rule.Show)
	}
	if rule.Ring != "" {
		parts = append(parts, "ring = "+rule.Ring)
	}
	if rule.When != "" {
		parts = append(parts, "when "+rule.When)
	}
	if rule.Sound != "" {
		parts = append(parts, "sound "+rule.Sound)
	}
	return strings.Join(parts, " · ")
}
