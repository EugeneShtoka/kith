package config

// Profile is one [[profile]]: a named setup a client runs; see domain.Profile. Include
// empty is every room.
type Profile struct {
	Name    string   `toml:"name"`
	Include []string `toml:"include"`
	Exclude []string `toml:"exclude"`
}
