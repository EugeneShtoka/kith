package setup

import (
	"fmt"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// PlaceEntries refuses place entries that declare no kind, plus the naming checks.
func PlaceEntries(cfg config.Config) error {
	if err := naming(cfg); err != nil {
		return err
	}
	lists := map[string][]string{
		"codes.include":         cfg.Codes.Include,
		"codes.exclude":         cfg.Codes.Exclude,
		"display.direction.rtl": cfg.Display.Direction.RTL,
		"display.direction.ltr": cfg.Display.Direction.LTR,
		"agent.read.rooms":      cfg.Agent.Read.Rooms,
		"agent.read.except":     cfg.Agent.Read.Except,
		"agent.write.rooms":     cfg.Agent.Write.Rooms,
		"agent.write.except":    cfg.Agent.Write.Except,
		"agent.write.send":      cfg.Agent.Write.Send,
		"assist.rooms":          cfg.Assist.Rooms,
		"assist.except":         cfg.Assist.Except,
	}
	for i := range cfg.Display.Tracked.Rules {
		lists[fmt.Sprintf("tracked.rule[%d].in", i)] = cfg.Display.Tracked.Rules[i].In
		lists[fmt.Sprintf("tracked.rule[%d].except", i)] = cfg.Display.Tracked.Rules[i].Except
	}
	for i := range cfg.Filters {
		lists[fmt.Sprintf("filter[%d].in", i)] = cfg.Filters[i].In
	}
	// An empty notification match is the account-wide rule, not a mistyped place.
	for i := range cfg.Notifications.Rules {
		if match := cfg.Notifications.Rules[i].Match; match != "" {
			lists[fmt.Sprintf("notifications.rule[%d].match", i)] = []string{match}
		}
	}
	for what, entries := range lists {
		if err := domain.ValidateEntries(what, entries); err != nil {
			return fmt.Errorf("setup: %w", err)
		}
		if err := knownTags(cfg, what, entries); err != nil {
			return err
		}
	}
	for _, entry := range cfg.Display.Direction.RTL {
		if slices.Contains(cfg.Display.Direction.LTR, entry) {
			return fmt.Errorf("setup: display.direction: %q is in both rtl and ltr", entry)
		}
	}
	return homeEntries(cfg)
}

// What a list naming rail rows may hold besides space:, tag: and place: entries.
const (
	homePrefixes  = "space:, tag:, place:"
	railWords     = "* or -"
	ruleWords     = "a room ID"
	filingPrefix  = "space:, place:"
	filingWords   = "a space's ID"
	jumpPrefixes  = "room:, space:, tag:, place:"
	railSeparator = "-"
)

// placeList is one setting's entries and the forms it takes.
type placeList struct {
	what, prefixes, words string
	entries               []string
	ok                    func(string) bool
}

// homeEntries refuses an entry naming rail rows without a prefix: a bare name could be
// a space, a tag or a row's label.
func homeEntries(cfg config.Config) error {
	for _, l := range slices.Concat(railLists(cfg.Display), ruleLists(cfg)) {
		for _, entry := range l.entries {
			if !l.ok(entry) {
				return fmt.Errorf("setup: %w", domain.NeedsPrefix(l.what, entry, l.prefixes, l.words))
			}
		}
	}
	return nil
}

func isHomeEntry(entry string) bool { _, _, ok := domain.ParseHomeEntry(entry); return ok }

// isIDEntry reports a room or event ID: complete on its own.
func isIDEntry(entry string) bool {
	entry = strings.TrimSpace(entry)
	return strings.HasPrefix(entry, "!") || strings.HasPrefix(entry, "$") || domain.IsRoomID(entry)
}

// railLists are the lists of rail rows: priority, the rail's own, filing_spaces.
func railLists(d config.Display) []placeList {
	return []placeList{
		{"display.priority", homePrefixes, "", d.Priority, isHomeEntry},
		{"display.rail.order", homePrefixes, railWords, d.Rail.Order, func(e string) bool {
			e = strings.TrimSpace(e)
			return isHomeEntry(e) || e == "*" || (e != "" && strings.Trim(e, railSeparator) == "")
		}},
		{"display.rail.hidden", homePrefixes, "", d.Rail.Hidden, isHomeEntry},
		{"display.rail.hide_when_empty", homePrefixes, "", d.Rail.HideWhenEmpty, isHomeEntry},
		{"display.filing_spaces", filingPrefix, filingWords, d.FilingSpaces, func(e string) bool {
			_, isTag := domain.TagOf(e)
			return isIDEntry(e) || (isHomeEntry(e) && !isTag)
		}},
	}
}

// ruleLists are the one-entry settings of rules and jumps. An empty match is a
// sender-only rule (or none), an empty group RoomList's to refuse.
func ruleLists(cfg config.Config) []placeList {
	d := cfg.Display
	var out []placeList
	add := func(what, prefixes, words, entry string, ok func(string) bool) {
		if strings.TrimSpace(entry) != "" {
			out = append(out, placeList{what, prefixes, words, []string{entry}, ok})
		}
	}
	match := func(e string) bool { return isHomeEntry(e) || isIDEntry(e) }
	for i, r := range d.SpaceRules {
		add(fmt.Sprintf("display.space_rule[%d].space", i), homePrefixes, "", r.Space, isHomeEntry)
	}
	for i, r := range d.Rooms.Rules {
		add(fmt.Sprintf("display.rooms.rule[%d].group", i), homePrefixes, "", r.Group, isHomeEntry)
	}
	for i, r := range d.ReadRules {
		add(fmt.Sprintf("display.read_rule[%d].match", i), homePrefixes, ruleWords, r.Match, match)
	}
	for i, r := range d.Threads.Rules {
		add(fmt.Sprintf("display.threads.rule[%d].match", i), homePrefixes, ruleWords, r.Match, match)
	}
	for i := range d.Media.Rules {
		add(fmt.Sprintf("display.media.rule[%d].match", i), homePrefixes, ruleWords, d.Media.Rules[i].Match, match)
	}
	for _, j := range cfg.Keys.Jump {
		add(fmt.Sprintf("keys.jump %q", j.Chord), jumpPrefixes, "", j.Target, domain.HasJumpPrefix)
	}
	return out
}

// knownTags refuses a tag:<name> entry that names no [[tag]].
func knownTags(cfg config.Config, what string, entries []string) error {
	for _, entry := range entries {
		name, ok := domain.TagOf(entry)
		if !ok {
			continue
		}
		if !slices.ContainsFunc(cfg.Tags, func(t config.Tag) bool { return strings.EqualFold(strings.TrimSpace(t.Name), name) }) {
			return fmt.Errorf("setup: %s: %q names no [[tag]]", what, entry)
		}
	}
	return nil
}

// HomeNamer reports, for one home (a space's name, or tag:<name>), whether a home entry
// names it, given the label its rail row shows under the display's renames.
func HomeNamer(d config.Display, home string) func(entry string) bool {
	label := ""
	if _, isTag := domain.TagOf(home); !isTag {
		label = d.SpaceLabels()[strings.TrimSpace(home)]
	}
	return func(entry string) bool { return domain.NamesHome(entry, home, label) }
}

// FirstNamesIn reports whether home's [[display.space_rule]] shows first names only.
func FirstNamesIn(d config.Display, home string) bool {
	if home == "" {
		return false
	}
	r, ok := d.SpaceRule(HomeNamer(d, home))
	return ok && r.FirstNameOnly
}

// Place adapts the client's place vocabulary to the ranking notify does.
type Place struct{ Room domain.RoomFacts }

// Reach reports how broadly an entry reaches into this room.
func (p Place) Reach(entry string) notify.Breadth {
	kind, ok := p.Room.Match(entry)
	if !ok {
		return notify.NoPlace
	}
	switch kind {
	case domain.EntryRoom:
		return notify.OneRoom
	case domain.EntryClass:
		return notify.ClassOfRooms
	case domain.EntryInvalid:
		return notify.NoPlace
	default:
		return notify.NoPlace
	}
}

// TrackedRules is the `rule` blocks plus bare `words` as one rule that admits
// everywhere.
func TrackedRules(cfg config.Tracked) []domain.TrackedRule {
	out := make([]domain.TrackedRule, 0, len(cfg.Rules)+1)
	if len(cfg.Words) > 0 {
		out = append(out, domain.TrackedRule{Words: cfg.Words})
	}
	for _, rule := range cfg.Rules {
		out = append(out, domain.TrackedRule{
			Words:  rule.Words,
			Where:  domain.PlaceFilter{Include: rule.In, Exclude: rule.Except},
			From:   rule.From,
			Notify: rule.Notify,
		})
	}
	return out
}

// naming checks that name targets declare a kind and each jump chord goes to one place.
func naming(cfg config.Config) error {
	if err := cfg.Keys.Jump.Validate(); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	return NameTargets(cfg.Display)
}
