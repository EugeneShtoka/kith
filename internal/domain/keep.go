package domain

// KeepRule is how many messages one place keeps, overriding the setting everywhere.
type KeepRule struct {
	// Match is a place entry (ParseEntry): a room, a space, a tag, a network.
	Match    string
	Messages int // negative keeps every one
}

// MessagesKept is how many messages room keeps: the narrowest rule naming it (one
// room over a set; among equals, the last), else base. Negative keeps every one.
func MessagesKept(base int, rules []KeepRule, room RoomFacts) int {
	kept, best := base, EntryInvalid
	for _, r := range rules {
		if kind, ok := room.Match(r.Match); ok && kind >= best {
			kept, best = r.Messages, kind
		}
	}
	return kept
}

// ArchiveRule is whether one place's attachments are archived, overriding the setting
// everywhere.
type ArchiveRule struct {
	Match   string // a place entry (ParseEntry)
	Archive bool
}

// MediaArchived is whether room's attachments are archived: the narrowest rule naming
// it (one room over a set; among equals, the last), else base.
func MediaArchived(base bool, rules []ArchiveRule, room RoomFacts) bool {
	archived, best := base, EntryInvalid
	for _, r := range rules {
		if kind, ok := room.Match(r.Match); ok && kind >= best {
			archived, best = r.Archive, kind
		}
	}
	return archived
}

// EncryptRule is whether one place's archived attachments are kept encrypted,
// overriding the setting everywhere.
type EncryptRule struct {
	Match   string // a place entry (ParseEntry)
	Encrypt bool
}

// MediaEncrypted is whether room's archived attachments are kept encrypted: the
// narrowest rule naming it (one room over a set; among equals, the last), else base.
func MediaEncrypted(base bool, rules []EncryptRule, room RoomFacts) bool {
	encrypted, best := base, EntryInvalid
	for _, r := range rules {
		if kind, ok := room.Match(r.Match); ok && kind >= best {
			encrypted, best = r.Encrypt, kind
		}
	}
	return encrypted
}
