package config

// Tag is one [[tag]]: the person's own grouping of rooms, shown in the rail with the
// spaces. What its fields mean is internal/domain's (domain.Tag).
type Tag struct {
	Name     string   `toml:"name"`
	Rule     []string `toml:"rule"`
	Picked   []string `toml:"picked"`
	Excluded []string `toml:"excluded"`
	Hidden   bool     `toml:"hidden"`
}
