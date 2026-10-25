package setup

import (
	"fmt"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Profiles is the [[profile]]s in config order, or the one default profile (every
// room) when none is written. A name is unique in any case; entries are places, and a
// tag whose rule reads a room's state is refused: a profile names rooms, so a room
// must not enter and leave it as it is read.
func Profiles(cfg config.Config) ([]domain.Profile, error) {
	if len(cfg.Profiles) == 0 {
		return []domain.Profile{{Name: domain.DefaultProfile}}, nil
	}
	tags, _, err := Tags(cfg)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Profile, 0, len(cfg.Profiles))
	seen := make(map[string]bool, len(cfg.Profiles))
	for i, p := range cfg.Profiles {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return nil, fmt.Errorf("setup: profile %d: name is empty", i+1)
		}
		if seen[strings.ToLower(name)] {
			return nil, fmt.Errorf("setup: profile %q is defined twice", name)
		}
		seen[strings.ToLower(name)] = true
		for what, entries := range map[string][]string{"include": p.Include, "exclude": p.Exclude} {
			what = fmt.Sprintf("profile %q: %s", name, what)
			if err := domain.ValidateEntries(what, entries); err != nil {
				return nil, fmt.Errorf("setup: %w", err)
			}
			if err := knownTags(cfg, what, entries); err != nil {
				return nil, err
			}
			if err := stateFree(tags, what, entries); err != nil {
				return nil, err
			}
		}
		out = append(out, domain.Profile{Name: name, Rooms: domain.PlaceFilter{Include: p.Include, Exclude: p.Exclude}})
	}
	return out, nil
}

// stateFree refuses a tag: or place: entry naming a tag that reads a room's state.
func stateFree(tags domain.TagSet, what string, entries []string) error {
	for _, entry := range entries {
		_, name, ok := domain.ParseHomeEntry(entry)
		if _, space := domain.SpaceOf(entry); !ok || space {
			continue
		}
		if tags.ReadsState(name) {
			return fmt.Errorf("setup: %s: %q reads whether a room is unread, mentions you, has a draft "+
				"or is an invitation; a profile names rooms, not their state", what, entry)
		}
	}
	return nil
}
