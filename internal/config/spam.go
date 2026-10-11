package config

// Spam is [spam]: the place a filtered conversation goes, and the rules that put it
// there.
type Spam struct {
	Rooms   []string     `toml:"rooms"`  // place vocabulary
	Except  []string     `toml:"except"` // outranks Rooms and every rule
	Ratio   float64      `toml:"ratio"`  // for filters that move by ratio; 0 is the default
	Floor   int          `toml:"floor"`
	Window  string       `toml:"window"` // a duration; empty is a week
	Filters []SpamFilter `toml:"filter"`
}

// SpamFilter is one [[spam.filter]]: a named word list that marks a message as spam.
type SpamFilter struct {
	Name  string   `toml:"name"`
	Words []string `toml:"words"` // tracked-word globs
	From  string   `toml:"from"`  // MXID
	In    []string `toml:"in"`    // place vocabulary; empty is everywhere
	// Moves is the rules its catch moves a room by; empty is SpamMovesDefault.
	Moves []string `toml:"moves"`
}

// The rules a filter's catch can move a room into Spam by.
const (
	SpamMoveAlways       = "always"
	SpamMoveFirstMessage = "first_message"
	SpamMoveDirect       = "direct"
	SpamMoveRatio        = "ratio"
)

// SpamMovesDefault is a filter's moves when it lists none.
var SpamMovesDefault = []string{SpamMoveFirstMessage, SpamMoveDirect}
