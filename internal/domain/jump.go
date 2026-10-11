package domain

import "strings"

// JumpKind is what sort of place a jump target names.
type JumpKind int

const (
	// JumpNone is a target that could not be read.
	JumpNone JumpKind = iota
	// JumpRoom is a room, named by its ID: "!abc:example.org".
	JumpRoom
	// JumpSpace is a rail group: a Matrix space or a synthetic one (All, DMs, Unread).
	JumpSpace
)

// JumpTarget is a configured destination: what kind of place, and which one.
type JumpTarget struct {
	Kind JumpKind
	// Name is a room ID for a room and a space's name for a space.
	Name string
}

// String spells a target as config writes it.
func (t JumpTarget) String() string {
	if t.Kind == JumpNone {
		return ""
	}
	return t.Kind.String() + ":" + t.Name
}

// String names a kind as config spells it.
func (k JumpKind) String() string {
	switch k {
	case JumpRoom:
		return "room"
	case JumpSpace:
		return "space"
	case JumpNone:
		return ""
	}
	return ""
}

// ParseJump reads a configured target. ok is false for anything unreadable (no or
// unknown kind, empty name, a room not named by ID).
func ParseJump(value string) (JumpTarget, bool) {
	kind, name, found := strings.Cut(strings.TrimSpace(value), ":")
	name = strings.TrimSpace(name)
	if !found || name == "" {
		return JumpTarget{}, false
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "room":
		if !IsRoomID(name) {
			return JumpTarget{}, false
		}
		return JumpTarget{Kind: JumpRoom, Name: name}, true
	case "space":
		return JumpTarget{Kind: JumpSpace, Name: name}, true
	}
	return JumpTarget{}, false
}

// JumpKinds lists the allowed target forms, for error messages.
func JumpKinds() []string { return []string{"room:<!id:server>", "space:<name>"} }
