package domain

import (
	"sort"
	"strings"
	"unicode"
)

// Member is one participant in a room: their user ID and the display name they use
// there.
type Member struct {
	UserID      string
	DisplayName string
}

// Name is the member's human-facing label, falling back to the ID's short name (a
// localpart, a phone number) and then the ID itself, so a member never renders blank.
func (m Member) Name() string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	if local := ShortName(m.UserID); local != "" {
		return local
	}
	return m.UserID
}

// Localpart is the part of a Matrix ID before the server: "@alice:example.org" →
// "alice".
func Localpart(mxid string) string {
	local := strings.TrimPrefix(mxid, "@")
	if colon := strings.IndexByte(local, ':'); colon >= 0 {
		local = local[:colon]
	}
	return local
}

// Matches reports whether the member answers to query, and how well.
func (m Member) Matches(query string) int {
	if query == "" {
		return 1 // everything matches an empty query, all equally
	}
	q := strings.ToLower(query)
	name := strings.ToLower(m.Name())
	local := strings.ToLower(ShortName(m.UserID))

	for _, token := range splitName(name) {
		if strings.HasPrefix(token, q) {
			return 1
		}
	}
	if strings.HasPrefix(local, q) {
		return 2
	}
	if strings.Contains(name, q) {
		return 3
	}
	if strings.Contains(local, q) {
		return 4
	}
	return 0
}

// splitName breaks a display name into the tokens a person might type the start of.
func splitName(name string) []string {
	return strings.FieldsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// SortMembers orders members by display name, case-insensitively, with the MXID as
// a stable tiebreaker so two people sharing a name keep a fixed order.
func SortMembers(members []Member) {
	sort.SliceStable(members, func(i, j int) bool {
		a, b := strings.ToLower(members[i].Name()), strings.ToLower(members[j].Name())
		if a != b {
			return a < b
		}
		return members[i].UserID < members[j].UserID
	})
}
