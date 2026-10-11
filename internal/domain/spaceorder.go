package domain

import "strings"

// OrderSpaces orders a room's spaces by the user's priority list; unranked ones follow.
func OrderSpaces(spaces, priority []string) []string {
	if len(spaces) < 2 || len(priority) == 0 {
		return spaces
	}
	rank := make(map[string]int, len(priority))
	for i, name := range priority {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		if _, seen := rank[key]; !seen {
			rank[key] = i
		}
	}

	ordered := make([]string, 0, len(spaces))
	taken := make([]bool, len(spaces))
	// Walk the priority list so ranked spaces come out in the user's order.
	for i := range priority {
		for j, space := range spaces {
			if taken[j] {
				continue
			}
			if at, ok := rank[strings.ToLower(strings.TrimSpace(space))]; ok && at == i {
				ordered = append(ordered, space)
				taken[j] = true
			}
		}
	}
	for j, space := range spaces {
		if !taken[j] {
			ordered = append(ordered, space)
		}
	}
	return ordered
}
