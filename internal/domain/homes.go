package domain

import "strings"

// A room's homes are where it lives, for everything that picks one: its spaces, and
// the tags holding it that the person's [display] priority names (as tag:<name>), in
// that priority. The first decides which name rule applies, a download's {space}, a
// notification's {space}, and which place rule ranks as the room's space. A tag is a
// home only when named there: most tags are broad (every room, every DM) and would
// otherwise become the home of every room in no space.

// Homes is a room's spaces and its tags that priority names, in priority order; a tag
// is TagEntry(name), so a tag and a space of the same name stay apart.
func Homes(spaces, tags, priority []string) []string {
	named := make(map[string]bool, len(priority))
	for _, p := range priority {
		if name, ok := TagOf(p); ok {
			named[strings.ToLower(name)] = true
		}
	}
	homes := make([]string, 0, len(spaces)+len(tags))
	homes = append(homes, spaces...)
	for _, tag := range tags {
		if named[strings.ToLower(strings.TrimSpace(tag))] {
			homes = append(homes, TagEntry(tag))
		}
	}
	return OrderSpaces(homes, priority)
}

// HomeLabel is a home as a person reads it: a tag by its name.
func HomeLabel(home string) string {
	if name, ok := TagOf(home); ok {
		return name
	}
	return strings.TrimSpace(home)
}
