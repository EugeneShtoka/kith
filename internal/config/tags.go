package config

// Tag is one [[tag]]: the person's own grouping of rooms, shown in the rail with the
// spaces. What its fields mean is internal/domain's (domain.Tag).
type Tag struct {
	Name     string   `toml:"name"`
	Rule     []string `toml:"rule"`
	Picked   []string `toml:"picked"`
	Excluded []string `toml:"excluded"`
	Hidden   bool     `toml:"hidden"`

	// CountsUnread nil is true.
	CountsUnread   *bool `toml:"counts_unread"`
	Exclusive      bool  `toml:"exclusive"`
	SpaceExclusive bool  `toml:"space_exclusive"`
	Sticky         bool  `toml:"sticky"`
	HideWhenEmpty  bool  `toml:"hide_when_empty"`
	First          bool  `toml:"first"`
	CountInLabel   bool  `toml:"count_in_label"`
}

// Counts reports counts_unread, which is true unless set false.
func (t Tag) Counts() bool { return enabled(t.CountsUnread) }
