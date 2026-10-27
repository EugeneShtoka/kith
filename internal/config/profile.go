package config

// Profile is one [[profile]]: a named setup a client runs; see domain.Profile. Include
// empty is every room.
type Profile struct {
	Name    string        `toml:"name"`
	Include []string      `toml:"include"`
	Exclude []string      `toml:"exclude"`
	People  ProfilePeople `toml:"people"`
}

// ProfilePeople is a profile's people: Exclude takes person IDs and phonebook card
// field terms (key:value), whose people the profile hides.
type ProfilePeople struct {
	Exclude []string `toml:"exclude"`
}
