package domain

// MentionRankRung bounds each ranking rung (recent speakers, mention history).
const MentionRankRung = 32

// RankMembers orders a room's members for the mention dropdown: the recent speakers,
// then the most mentioned, then everyone else in the order given (alphabetical, as
// the cache lists them); at most limit (0 or less: all). A rung's user who is no
// longer a member is skipped, and nobody is listed twice.
func RankMembers(all []Member, speakers, mentioned []string, limit int) []Member {
	byID := make(map[string]Member, len(all))
	for _, m := range all {
		byID[m.UserID] = m
	}
	placed := make(map[string]bool, len(all))
	out := make([]Member, 0, len(all))
	for _, rung := range [][]string{speakers, mentioned} {
		for _, userID := range rung {
			member, present := byID[userID]
			if !present || placed[userID] {
				continue
			}
			placed[userID] = true
			out = append(out, member)
		}
	}
	for _, member := range all {
		if !placed[member.UserID] {
			out = append(out, member)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
