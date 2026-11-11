package setup

import (
	"fmt"
	"regexp"
	"slices"

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

// HiddenInputs is the config's part of who is hidden (db.Cache.UseHidden): the muted
// tags' people, the muted tags (whose person verdicts hide people too), and the
// identity groups linking IDs. Identities sharing an alias are one group, as the TUI
// holds them.
func HiddenInputs(cfg config.Config) (marked, muted []string, groups [][]string, err error) {
	tags, _, err := Tags(cfg)
	if err != nil {
		return nil, nil, nil, err
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
	return tags.MutedPeople(), tags.MutedTags(), groups, nil
}

// identityGroups are the identities' IDs, those sharing an alias as one group as the TUI
// holds them, each with the picture it names (the first of the group's that names one).
func identityGroups(ids []config.Identity) (groups [][]string, pictures []string) {
	byAlias := map[string]int{}
	for _, id := range ids {
		at, ok := byAlias[id.Alias]
		if !ok || id.Alias == "" {
			at = len(groups)
			groups, pictures = append(groups, nil), append(pictures, "")
			byAlias[id.Alias] = at
		}
		for _, user := range id.IDs {
			if !slices.Contains(groups[at], user) {
				groups[at] = append(groups[at], user)
			}
		}
		if pictures[at] == "" {
			pictures[at] = id.Picture
		}
	}
	return groups, pictures
}

// FacesOf is, for each ID a [[display.identity]] names, the IDs whose picture stands for
// theirs, the first that has one winning: the one its picture names, then their own,
// then the rest of theirs in the order written.
func FacesOf(ids []config.Identity) map[string][]string {
	groups, pictures := identityGroups(ids)
	out := map[string][]string{}
	for i, group := range groups {
		for _, user := range group {
			var chain []string
			for _, id := range append([]string{pictures[i], user}, group...) {
				if id != "" && !slices.Contains(chain, id) {
					chain = append(chain, id)
				}
			}
			out[user] = chain
		}
	}
	return out
}

// savedPicture is a picture kithd keeps (local.SavePicture): "file:" and its sha256.
var savedPicture = regexp.MustCompile(`^file:[0-9a-f]{64}$`)

// identityPictures refuses an identity's picture that is neither one of its people's IDs
// nor a picture kithd keeps.
func identityPictures(ids []config.Identity) error {
	groups, pictures := identityGroups(ids)
	for i, p := range pictures {
		if p != "" && !slices.Contains(groups[i], p) && !savedPicture.MatchString(p) {
			return fmt.Errorf("display.identity: picture %q is none of its ids, nor a saved picture", p)
		}
	}
	return nil
}
