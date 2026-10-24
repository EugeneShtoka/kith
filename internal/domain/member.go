package domain

import (
	"sort"
	"strings"
	"unicode"
)

// Member is one participant in a room, with their per-room display name.
type Member struct {
	UserID      string
	DisplayName string
}

// Name is the display name, else the ID's short name, else the ID; never blank.
func (m Member) Name() string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	if local := ShortName(m.UserID); local != "" {
		return local
	}
	return m.UserID
}

// Localpart returns "alice" for "@alice:example.org" (or "matrix:@alice:example.org").
func Localpart(mxid string) string {
	local := strings.TrimPrefix(MatrixNative(mxid), "@")
	if colon := strings.IndexByte(local, ':'); colon >= 0 {
		local = local[:colon]
	}
	return local
}

// Matches ranks how well the member matches query: 1 best, 0 no match.
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

// SortMembers orders members by name, case-insensitively, then by user ID.
func SortMembers(members []Member) {
	sort.SliceStable(members, func(i, j int) bool {
		a, b := strings.ToLower(members[i].Name()), strings.ToLower(members[j].Name())
		if a != b {
			return a < b
		}
		return members[i].UserID < members[j].UserID
	})
}
