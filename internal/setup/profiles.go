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
	for i := range cfg.Profiles {
		p := &cfg.Profiles[i]
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return nil, fmt.Errorf("setup: profile %d: name is empty", i+1)
		}
		if seen[strings.ToLower(name)] {
			return nil, fmt.Errorf("setup: profile %q is defined twice", name)
		}
		seen[strings.ToLower(name)] = true
		profile, err := profileOf(cfg, tags, p, name)
		if err != nil {
			return nil, err
		}
		out = append(out, profile)
	}
	return out, nil
}

// profileOf is one [[profile]], named name, checked: its rooms judged with the tags as
// it has them (shared, else its own), and its rail.
func profileOf(cfg config.Config, shared domain.TagSet, p *config.Profile, name string) (domain.Profile, error) {
	include := slices.DeleteFunc(slices.Clone(p.Include), func(entry string) bool { return strings.TrimSpace(entry) == everyRoom })
	if len(p.Include) == 0 {
		return domain.Profile{}, fmt.Errorf("setup: profile %q: include is empty; name its rooms (%q is every room)", name, everyRoom)
	}
	if len(include) < len(p.Include) {
		include = nil // every room, whatever else it names
	}
	own, err := profileTags(cfg, p, name)
	if err != nil {
		return domain.Profile{}, err
	}
	judged := shared
	if own != nil {
		judged = *own
	}
	for what, entries := range map[string][]string{"include": include, "exclude": p.Exclude} {
		what = fmt.Sprintf("profile %q: %s", name, what)
		if err := domain.ValidateEntries(what, entries); err != nil {
			return domain.Profile{}, fmt.Errorf("setup: %w", err)
		}
		if err := stateFree(judged, what, entries); err != nil {
			return domain.Profile{}, err
		}
	}
	if err := checkLists(railRows(fmt.Sprintf("profile %q: rail", name), p.Rail)); err != nil {
		return domain.Profile{}, err
	}
	people, peopleErr := profilePeople(name, p.People.Exclude)
	if peopleErr != nil {
		return domain.Profile{}, peopleErr
	}
	return domain.Profile{Name: name, Rooms: domain.PlaceFilter{Include: include, Exclude: p.Exclude}, Tags: own, People: people}, nil
}

// profileTags is the tags as profile p has them, where its archive differs from the
// shared one; nil where it does not.
func profileTags(cfg config.Config, p *config.Profile, name string) (*domain.TagSet, error) {
	if !p.Archive.Overrides() {
		return nil, nil
	}
	if len(archiveTagNames(cfg)) == 0 {
		return nil, fmt.Errorf("setup: profile %q: archive: there is no archive tag to set "+
			"(a [[tag]] %q, or one a [<network>.archive] tag names)", name, config.DefaultArchiveTag)
	}
	own, _, err := Tags(withProfile(cfg, p))
	if err != nil {
		return nil, fmt.Errorf("setup: profile %q: archive.rooms: %w", name, err)
	}
	return &own, nil
}

// ProfileConfig is cfg as the named profile runs it ("" the first): each archive tag
// with the profile's archive rooms and push, and [display.rail] its rail, where it
// sets them. A name no profile has, or a profile setting neither, is cfg itself.
func ProfileConfig(cfg config.Config, name string) config.Config {
	i := slices.IndexFunc(cfg.Profiles, func(p config.Profile) bool {
		return strings.EqualFold(strings.TrimSpace(p.Name), strings.TrimSpace(name))
	})
	if strings.TrimSpace(name) == "" && len(cfg.Profiles) > 0 {
		i = 0
	}
	if i < 0 || (!cfg.Profiles[i].Archive.Overrides() && !cfg.Profiles[i].Rail.Overrides()) {
		return cfg
	}
	return withProfile(cfg, &cfg.Profiles[i])
}

// withProfile is a copy of cfg with profile p's archive and rail in place of the
// shared ones.
func withProfile(cfg config.Config, p *config.Profile) config.Config {
	out := cfg.Clone()
	if p.Rail.Overrides() {
		out.Display.Rail = config.Rail{
			Order: slices.Clone(p.Rail.Order), Hidden: slices.Clone(p.Rail.Hidden),
			HideWhenEmpty: slices.Clone(p.Rail.HideWhenEmpty), NetworkColors: p.Rail.NetworkColors,
		}
	}
	if !p.Archive.Overrides() {
		return out
	}
	archives := archiveTagNames(cfg)
	for i := range out.Tags {
		if !slices.ContainsFunc(archives, func(n string) bool { return sameTag(n, out.Tags[i].Name) }) {
			continue
		}
		if len(p.Archive.Rooms) > 0 {
			out.Tags[i].Rule = slices.Clone(p.Archive.Rooms)
		}
		if p.Archive.Push != nil {
			out.Tags[i].PushArchive = *p.Archive.Push
		}
	}
	return out
}

// archiveTagNames is every existing archive tag, as ArchiveTags has them, from the
// config alone.
func archiveTagNames(cfg config.Config) []string {
	var out []string
	add := func(name string) {
		exists := slices.ContainsFunc(cfg.Tags, func(t config.Tag) bool { return sameTag(t.Name, name) })
		if exists && !slices.ContainsFunc(out, func(n string) bool { return sameTag(n, name) }) {
			out = append(out, name)
		}
	}
	add(config.DefaultArchiveTag)
	for _, archive := range cfg.Archives() {
		add(archive.TagName())
	}
	return out
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
