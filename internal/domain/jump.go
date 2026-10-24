package domain

import "strings"

// JumpKind is what sort of place a jump target names.
type JumpKind int

const (
	// JumpNone is a target that could not be read.
	JumpNone JumpKind = iota
	// JumpRoom is a room, named by its ID: "matrix:<account>/!abc:example.org".
	JumpRoom
	// JumpHome is a rail row, named by a home entry: space:, tag: or place:<name>.
	JumpHome
)

// JumpTarget is a configured destination: what kind of place, and which one.
type JumpTarget struct {
	Kind JumpKind
	// Name is a room ID for a room and the home entry for a rail row.
	Name string
}

// String spells a target as config writes it.
func (t JumpTarget) String() string {
	switch t.Kind {
	case JumpRoom:
		return jumpRoom + t.Name
	case JumpHome:
		return t.Name
	case JumpNone:
		return ""
	}
	return ""
}

// jumpRoom is the room target's prefix.
const jumpRoom = "room:"

// ParseJump reads a configured target. ok is false for anything unreadable (no or
// unknown kind, empty name, a room not named by ID).
func ParseJump(value string) (JumpTarget, bool) {
	value = strings.TrimSpace(value)
	if _, _, ok := ParseHomeEntry(value); ok {
		return JumpTarget{Kind: JumpHome, Name: value}, true
	}
	if !hasPrefixFold(value, jumpRoom) {
		return JumpTarget{}, false
	}
	name := strings.TrimSpace(value[len(jumpRoom):])
	if !IsRoomID(name) {
		return JumpTarget{}, false
	}
	return JumpTarget{Kind: JumpRoom, Name: name}, true
}

// HasJumpPrefix reports whether a target carries a prefix a jump may have, readable or
// not.
func HasJumpPrefix(value string) bool {
	value = strings.TrimSpace(value)
	for _, p := range []string{jumpRoom, entrySpace, termTag, entryPlace} {
		if hasPrefixFold(value, p) {
			return true
		}
	}
	return false
}

// JumpKinds lists the allowed target forms, for error messages.
func JumpKinds() []string {
	return []string{"room:<!id:server>", "space:<name>", "tag:<name>", "place:<name>"}
}
