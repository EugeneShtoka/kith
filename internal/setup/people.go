package setup

import (
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Aliases maps each user ID in [[display.identity]] to its alias. A number ID (e.g.
// WhatsApp) also maps the bare number, so the alias reaches every linked account.
func Aliases(ids []config.Identity) map[string]string {
	out := map[string]string{}
	for _, id := range ids {
		if id.Alias == "" {
			continue
		}
		for _, user := range id.IDs {
			out[user] = id.Alias
			if digits := domain.PhoneOf(user); digits != "" {
				out[domain.PhoneID(digits)] = id.Alias
			}
		}
	}
	return out
}

// PeopleOf is People with these aliases and directory.
func PeopleOf(aliases map[string]string, dir domain.Directory) domain.People {
	return domain.People{Alias: func(user string) string { return aliases[user] }, Dir: dir}
}

// HiddenInputs is the config's part of who is hidden (db.Cache.UseHidden): the muted
// tags' people, and the identity groups linking IDs. Identities sharing an alias are
// one group, as the TUI holds them.
func HiddenInputs(cfg config.Config) (marked []string, groups [][]string, err error) {
	tags, _, err := Tags(cfg)
	if err != nil {
		return nil, nil, err
	}
	byAlias := map[string]int{}
	for _, id := range cfg.Display.Identities {
		at, ok := byAlias[id.Alias]
		if !ok || id.Alias == "" {
			at = len(groups)
			groups = append(groups, nil)
			byAlias[id.Alias] = at
		}
		groups[at] = append(groups[at], id.IDs...)
	}
	return tags.MutedPeople(), groups, nil
}
