package domain

import "strings"

// A room's homes are where it lives, for everything that picks one: its spaces and
// the tags holding it (as tag:<name>), in the person's [display] priority. The first
// decides which name rule applies, a download's {space}, a notification's {space},
// and which place rule ranks as the room's space. What priority does not name
// follows: spaces first, then tags in config order — so a broad tag (All, every room)
// written last is a room's home only when nothing else is.

// Homes is a room's spaces and tags in priority order; a tag is TagEntry(name), so a
// tag and a space of the same name stay apart. tags is in config order.
func Homes(spaces, tags, priority []string) []string {
	homes := make([]string, 0, len(spaces)+len(tags))
	homes = append(homes, spaces...)
	for _, tag := range tags {
		homes = append(homes, TagEntry(tag))
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
