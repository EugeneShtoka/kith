package config

import "cmp"

// GUINotices is [gui.notices]: how many seconds a note of each level stays on a graphical
// client's bar before it goes by itself. 0 or -1 keeps it until dismissed; unset is the
// default.
type GUINotices struct {
	Message *int `toml:"message"`
	Warning *int `toml:"warning"`
	Error   *int `toml:"error"`
}

// The default lifetimes: a message and a warning go by themselves, an error stays.
const (
	DefaultMessageSeconds = 5
	DefaultWarningSeconds = 10
)

// Over is n with each level it leaves unset taken from shared: a device's over the bridge's.
func (n GUINotices) Over(shared GUINotices) GUINotices {
	return GUINotices{
		Message: cmp.Or(n.Message, shared.Message),
		Warning: cmp.Or(n.Warning, shared.Warning),
		Error:   cmp.Or(n.Error, shared.Error),
	}
}

// Lifetimes are each level's seconds, defaults filled in; -1 is until dismissed.
func (n GUINotices) Lifetimes() (message, warning, err int) {
	return lifetime(n.Message, DefaultMessageSeconds), lifetime(n.Warning, DefaultWarningSeconds), lifetime(n.Error, -1)
}

func lifetime(set *int, def int) int {
	switch {
	case set == nil:
		return def
	case *set <= 0:
		return -1
	default:
		return *set
	}
}
