package setup

import (
	"fmt"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/phonebook"
)

// Profiles is the [[profile]]s in config order; a config has at least one, and each
// names its rooms ("*" is every room). A name is unique in any case; entries are
// places, and a tag whose rule reads a room's state is refused: a profile names rooms,
// so a room must not enter and leave it as it is read.
func Profiles(cfg config.Config) ([]domain.Profile, error) {
	if len(cfg.Profiles) == 0 {
		return nil, fmt.Errorf("setup: the config has no [[profile]]; kith runs in one (include = [%q] holds every room)", everyRoom)
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
		include := slices.DeleteFunc(slices.Clone(p.Include), func(entry string) bool { return strings.TrimSpace(entry) == everyRoom })
		if len(p.Include) == 0 {
			return nil, fmt.Errorf("setup: profile %q: include is empty; name its rooms (%q is every room)", name, everyRoom)
		}
		if len(include) < len(p.Include) {
			include = nil // every room, whatever else it names
		}
		for what, entries := range map[string][]string{"include": include, "exclude": p.Exclude} {
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
		people, err := profilePeople(name, p.People.Exclude)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.Profile{
			Name: name, Rooms: domain.PlaceFilter{Include: include, Exclude: p.Exclude}, People: people,
		})
	}
	return out, nil
}

// ProfileExcluded is whom filter hides over cards: its IDs, and whoever's card (found
// by any ID the person goes by) holds one of its field terms word for word
// (phonebook.Card.Holds). kithd, the TUI and kith-mcp read a profile's people alike.
func ProfileExcluded(filter domain.PeopleFilter, cards phonebook.Index) domain.ExcludedPeople {
	out := domain.ExcludedPeople{IDs: filter.IDs}
	var terms []phonebook.Term
	for _, field := range filter.Fields {
		if term, ok := phonebook.ParseTerm(field); ok {
			terms = append(terms, term)
		}
	}
	if len(terms) == 0 {
		return out
	}
	out.Card = func(ids []string) bool {
		card, ok := cards.Find(phonebook.Identifiers(domain.CardIdentifiers(ids)))
		return ok && slices.ContainsFunc(terms, card.Holds)
	}
	return out
}

// profilePeople reads a profile's people.exclude: an entry whose key is a network's
// prefix (or a bare Matrix ID) is a person ID, never a card field; anything else must be
// a phonebook field term, key:value.
func profilePeople(profile string, entries []string) (domain.PeopleFilter, error) {
	var out domain.PeopleFilter
	for _, entry := range entries {
		typed := domain.TypedUserID(entry)
		key, _, _ := strings.Cut(typed, ":")
		switch {
		case strings.TrimSpace(entry) == "":
			return domain.PeopleFilter{}, fmt.Errorf("setup: profile %q: people.exclude has an empty entry", profile)
		case domain.IsNetworkPrefix(key):
			if !domain.IsUserID(typed) {
				return domain.PeopleFilter{}, fmt.Errorf("setup: profile %q: people.exclude: %q is not a person's ID", profile, entry)
			}
			out.IDs = append(out.IDs, typed)
		default:
			if _, ok := phonebook.ParseTerm(strings.TrimSpace(entry)); !ok {
				return domain.PeopleFilter{}, fmt.Errorf("setup: profile %q: people.exclude: %q is neither a person's ID "+
					"(whatsapp:+15550100) nor a phonebook field (role:colleagues)", profile, entry)
			}
			out.Fields = append(out.Fields, strings.TrimSpace(entry))
		}
	}
	return out, nil
}

// everyRoom is the include entry holding every room, as in a tag's rule.
const everyRoom = "*"

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
