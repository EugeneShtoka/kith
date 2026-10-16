package domain

import "strings"

// A home entry names rail rows: `space:<name>` a space by its name, `tag:<name>` a
// tag, `place:<name>` every row shown as <name> (its label, or its name). A bare name
// names nothing: a space, a tag and a room can share one.

// entryPlace names rows by what the rail shows.
const entryPlace = "place:"

// ParseHomeEntry splits a home entry into its prefix (space:, tag: or place:,
// lower-cased) and name; false for anything else, or no name.
func ParseHomeEntry(entry string) (prefix, name string, ok bool) {
	entry = strings.TrimSpace(entry)
	for _, p := range []string{entrySpace, termTag, entryPlace} {
		if hasPrefixFold(entry, p) {
			name = strings.TrimSpace(entry[len(p):])
			return p, name, name != ""
		}
	}
	return "", "", false
}

// HomeEntry spells a home (a space's name, or tag:<name>) as an entry.
func HomeEntry(home string) string {
	if _, ok := TagOf(home); ok {
		return strings.TrimSpace(home)
	}
	return SpaceEntry(strings.TrimSpace(home))
}

// NamesHome reports whether a home entry names home (a space's name, or tag:<name>)
// whose rail row is labeled label ("" when it shows its name).
func NamesHome(entry, home, label string) bool {
	prefix, want, ok := ParseHomeEntry(entry)
	if !ok {
		return false
	}
	name, isTag := TagOf(home)
	if !isTag {
		name = strings.TrimSpace(home)
	}
	switch prefix {
	case entrySpace:
		return !isTag && strings.EqualFold(name, want)
	case termTag:
		return isTag && strings.EqualFold(name, want)
	default:
		return strings.EqualFold(name, want) || strings.EqualFold(strings.TrimSpace(label), want)
	}
}

// IsPlaceEntry reports whether entry is `place:<name>`.
func IsPlaceEntry(entry string) bool {
	prefix, _, ok := ParseHomeEntry(entry)
	return ok && prefix == entryPlace
}
