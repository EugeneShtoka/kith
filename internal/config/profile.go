package config

// Profile is one [[profile]]: a named setup a client runs; see domain.Profile. Include
// empty is every room.
type Profile struct {
	Name    string   `toml:"name"`
	Include []string `toml:"include"`
	Exclude []string `toml:"exclude"`
	// Archive is what the archive is in this profile; unset fields run the shared one.
	Archive ProfileArchive `toml:"archive"`
	// Rail replaces [display.rail] in this profile, whole, once it sets anything.
	Rail Rail `toml:"rail"`
	// People is whom this profile hides on top of everyone's hidden people.
	People ProfilePeople `toml:"people"`
}

// ProfilePeople is a profile's people: Exclude takes person IDs and phonebook card
// field terms (key:value), whose people the profile hides.
type ProfilePeople struct {
	Exclude []string `toml:"exclude"`
}

// ProfileArchive is a profile's archive = { rooms, push }: what each archive tag
// ([<network>.archive] tag, Archived by default) is in that profile. An empty Rooms
// and a nil Push keep the tag's own, so a save (which writes no empty list) reads
// back the same.
type ProfileArchive struct {
	// Rooms replaces the archive tag's rule (the tag-rule vocabulary, `not` entries
	// included); hand filing still writes the tag's shared picked and excluded.
	Rooms []string `toml:"rooms"`
	// Push replaces the archive tag's push_archive.
	Push *bool `toml:"push"`
}

// Overrides reports whether the profile sets anything of the archive's own.
func (a ProfileArchive) Overrides() bool { return len(a.Rooms) > 0 || a.Push != nil }

// Overrides reports whether a profile's rail sets anything, and so replaces
// [display.rail].
func (r Rail) Overrides() bool {
	return len(r.Order) > 0 || len(r.Hidden) > 0 || len(r.HideWhenEmpty) > 0 || r.NetworkColors
}
