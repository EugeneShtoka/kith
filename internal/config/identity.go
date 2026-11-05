package config

import "slices"

// MergeIdentity adds id to the identity named alias (making it if there is none) and
// takes it out of any other, dropping one left with nobody in it, and pins color to the
// identity. An identity whose picture was id's own lets it go with id.
func MergeIdentity(display Display, alias, id, color string) Display {
	identities := make([]Identity, 0, len(display.Identities)+1)
	found := false
	for _, ident := range display.Identities {
		ident.IDs = slices.DeleteFunc(slices.Clone(ident.IDs), func(x string) bool { return x == id })
		if ident.Picture == id && ident.Alias != alias {
			ident.Picture = "" // its picture left with the account
		}
		if ident.Alias == alias {
			found = true
			ident.IDs = append(ident.IDs, id)
			ident.Color = color
		}
		if ident.Alias == alias || len(ident.IDs) > 0 {
			identities = append(identities, ident)
		}
	}
	if !found {
		identities = append(identities, Identity{Alias: alias, Color: color, IDs: []string{id}})
	}
	display.Identities = identities
	return display
}
