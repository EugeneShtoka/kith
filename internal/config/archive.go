package config

import "strings"

// DefaultArchiveTag is the tag a network's archive is when [<network>.archive] names
// none.
const DefaultArchiveTag = "Archived"

// NetworkArchive is [<network>.archive]: how a network's own archive (Telegram's
// Archived folder, WhatsApp's archived chats) and kith's archive tag go together.
type NetworkArchive struct {
	// Follow nil is true: a chat archived on the network is in the tag.
	Follow *bool `toml:"follow"`
	// Mirror files a chat into the tag, or out, on the network too.
	Mirror bool `toml:"mirror"`
	// Tag is which tag is the archive; empty is DefaultArchiveTag.
	Tag string `toml:"tag"`
}

// Follows reports follow, which is true unless set false.
func (a NetworkArchive) Follows() bool { return enabled(a.Follow) }

// TagName is the archive's tag: tag, else DefaultArchiveTag.
func (a NetworkArchive) TagName() string {
	if name := strings.TrimSpace(a.Tag); name != "" {
		return name
	}
	return DefaultArchiveTag
}

// Archives maps each network with its own archive, by section name, to its
// [<network>.archive], editable in place.
func (c *Config) Archives() map[string]*NetworkArchive {
	return map[string]*NetworkArchive{"telegram": &c.Telegram.Archive, "whatsapp": &c.WhatsApp.Archive}
}
