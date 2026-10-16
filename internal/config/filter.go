package config

// Filter is one [[filter]]: words, a sender or a language that move a room into a tag.
type Filter struct {
	Name  string   `toml:"name"`
	Tag   string   `toml:"tag"`   // the tag it moves rooms into
	Words []string `toml:"words"` // tracked-word globs
	From  string   `toml:"from"`  // a sender's ID
	In    []string `toml:"in"`    // place vocabulary; empty is everywhere
	// Lang and NotLang are language codes, family:… or region:…; Script is writing
	// systems. Empty is any.
	Lang    []string `toml:"lang"`
	NotLang []string `toml:"not_lang"`
	Script  []string `toml:"script"`
	// Moves is the rules its catch moves a room, or marks its sender, by; empty is
	// FilterMovesDefault.
	Moves []string `toml:"moves"`
	// Ratio, Floor and Window are the ratio moves' share, least message count and
	// period; zero or empty is the default.
	Ratio  float64 `toml:"ratio"`
	Floor  int     `toml:"floor"`
	Window string  `toml:"window"`
	// Spread is how many rooms one caught message must reach for sender_spread; zero
	// is the default.
	Spread int `toml:"spread"`
}

// The rules a filter's catch can move a room by.
const (
	FilterMoveAlways       = "always"
	FilterMoveFirstMessage = "first_message"
	FilterMoveDirect       = "direct"
	FilterMoveRatio        = "ratio"
	// The sender moves mark the person who sent a caught message, in every room.
	FilterMoveSenderFirst  = "sender_first"
	FilterMoveSenderRatio  = "sender_ratio"
	FilterMoveSenderSpread = "sender_spread"
)

// FilterMovesDefault is a filter's moves when it lists none.
var FilterMovesDefault = []string{FilterMoveFirstMessage, FilterMoveDirect}
