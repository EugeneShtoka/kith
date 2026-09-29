package config

// Spam is [spam]: the place a filtered conversation goes, and the rules that put it
// there.
type Spam struct {
	Rooms        []string     `toml:"rooms"`  // place vocabulary
	Except       []string     `toml:"except"` // outranks Rooms and every rule
	FirstMessage *bool        `toml:"first_message"`
	Direct       *bool        `toml:"direct"`
	Ratio        float64      `toml:"ratio"` // 0 turns the ratio rule off
	Floor        int          `toml:"floor"`
	Window       string       `toml:"window"` // a duration; empty is a week
	Filters      []SpamFilter `toml:"filter"`
}

// SpamFilter is one [[spam.filter]]: a named word list that marks a message as spam.
type SpamFilter struct {
	Name  string   `toml:"name"`
	Words []string `toml:"words"` // tracked-word globs
	From  string   `toml:"from"`  // MXID
}

// FirstMessageRule and DirectRule are the two switches that default on.
func (s Spam) FirstMessageRule() bool { return enabled(s.FirstMessage) }
func (s Spam) DirectRule() bool       { return enabled(s.Direct) }
