package setup

import (
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Aliases is the person's own name for each user ID their [[display.identity]] blocks
// name, for domain.People.Alias; an identity with no alias names nobody.
func Aliases(ids []config.Identity) map[string]string {
	out := map[string]string{}
	for _, id := range ids {
		if id.Alias == "" {
			continue
		}
		for _, user := range id.IDs {
			out[user] = id.Alias
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

// PeopleOf is People with these aliases and phone book.
func PeopleOf(aliases map[string]string, book domain.PhoneBook) domain.People {
	return domain.People{Alias: func(user string) string { return aliases[user] }, Book: book}
}
