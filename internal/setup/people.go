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

// FirstNameSpaces is each space whose names are first names only ([[display.space_rule]]).
func FirstNameSpaces(d config.Display) map[string]bool {
	out := map[string]bool{}
	for _, r := range d.SpaceRules {
		if r.FirstNameOnly {
			out[r.Space] = true
		}
	}
	return out
}

// PeopleOf is People with these aliases and directory.
func PeopleOf(aliases map[string]string, dir domain.Directory) domain.People {
	return domain.People{Alias: func(user string) string { return aliases[user] }, Dir: dir}
}
