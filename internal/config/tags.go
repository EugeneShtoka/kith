package config

import (
	"slices"
	"strings"
)

// Tag is one [[tag]]: a user-defined room group shown in the rail; see domain.Tag.
type Tag struct {
	Name     string   `toml:"name"`
	Rule     []string `toml:"rule"`
	Picked   []string `toml:"picked"`
	Excluded []string `toml:"excluded"`
	Hidden   bool     `toml:"hidden"`

	// CountsUnread and Notifies nil are true.
	CountsUnread *bool `toml:"counts_unread"`
	Notifies     *bool `toml:"notifies"`
	// Exclusive is where its rooms leave: ExclusiveTags, ExclusiveSpaces, ExclusiveOrigin.
	Exclusive     []string `toml:"exclusive"`
	Sticky        bool     `toml:"sticky"`
	HideWhenEmpty bool     `toml:"hide_when_empty"`
	First         bool     `toml:"first"`
	CountInLabel  bool     `toml:"count_in_label"`
}

// Where a tag's rooms leave.
const (
	ExclusiveTags   = "tags"   // other tags' rows
	ExclusiveSpaces = "spaces" // spaces they do not belong to
	ExclusiveOrigin = "origin" // the spaces they belong to
)

// ExclusiveFrom reports whether exclusive lists what (any case).
func (t Tag) ExclusiveFrom(what string) bool {
	return slices.ContainsFunc(t.Exclusive, func(e string) bool { return strings.EqualFold(strings.TrimSpace(e), what) })
}

// WithExclusive is exclusive with what added or removed, kept in tags, spaces, origin order.
func (t Tag) WithExclusive(what string, on bool) []string {
	var out []string
	for _, w := range []string{ExclusiveTags, ExclusiveSpaces, ExclusiveOrigin} {
		if (w == what && on) || (w != what && t.ExclusiveFrom(w)) {
			out = append(out, w)
		}
	}
	return out
}

// Counts reports counts_unread, which is true unless set false.
func (t Tag) Counts() bool { return enabled(t.CountsUnread) }

// Notifying reports notifies, which is true unless set false.
func (t Tag) Notifying() bool { return enabled(t.Notifies) }
