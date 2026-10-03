package setup

import (
	"fmt"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// PlaceEntries refuses a place entry that declares no kind, in every list that takes
// one, plus the naming checks (jump chords, name targets).
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
		"spam.rooms":            cfg.Spam.Rooms,
		"spam.except":           cfg.Spam.Except,
		"assist.rooms":          cfg.Assist.Rooms,
		"assist.except":         cfg.Assist.Except,
	}
	for i := range cfg.Display.Tracked.Rules {
		lists[fmt.Sprintf("tracked.rule[%d].in", i)] = cfg.Display.Tracked.Rules[i].In
		lists[fmt.Sprintf("tracked.rule[%d].except", i)] = cfg.Display.Tracked.Rules[i].Except
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
	return nil
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

// TrackedRules is the tracked word list as the domain takes it: the `rule` blocks, plus
// the bare `words` desugared into one rule that admits everywhere.
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

// naming is the two checks about giving things names: that a target says what kind of
// thing it is, and that a chord goes to one place.
func naming(cfg config.Config) error {
	if err := cfg.Keys.Jump.Validate(); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	return NameTargets(cfg.Display)
}
