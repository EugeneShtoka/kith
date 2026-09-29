package config

// Clipboard is [clipboard]: copying out of the timeline and opening links.
type Clipboard struct {
	Command       string `toml:"command"` // gets copied text on stdin, never argv
	AutoCopy      bool   `toml:"auto_copy"`
	OpenCommand   string `toml:"open_command"`  // gets one URL argument, no shell; empty xdg-open
	FocusCommand  string `toml:"focus_command"` // run through a shell; empty detects the WM
	CopyDownloads bool   `toml:"copy_downloads"`
}

// defaultOpenCommand is the desktop's URL handler on the platforms kith targets.
const defaultOpenCommand = "xdg-open"

// OpenCommandOrDefault is the program handed a link, defaulting to the freedesktop
// opener.
func (c Clipboard) OpenCommandOrDefault() string {
	if c.OpenCommand == "" {
		return defaultOpenCommand
	}
	return c.OpenCommand
}

// Codes is [codes]: where verification codes are looked for and what one looks like.
type Codes struct {
	Include      []string `toml:"include"`    // place vocabulary; empty everywhere
	Exclude      []string `toml:"exclude"`    // place vocabulary; wins over Include
	MinLength    int      `toml:"min_length"` // 0 is 4
	MaxLength    int      `toml:"max_length"` // 0 is 8
	Letters      *bool    `toml:"letters"`
	Digits       *bool    `toml:"digits"`
	Symbols      string   `toml:"symbols"` // extra allowed characters
	RequireDigit *bool    `toml:"require_digit"`
}

// Default code shape, applied when the file says nothing.
const (
	defaultCodeMinLength = 4
	defaultCodeMaxLength = 8
)

// Min is the shortest a code may be, defaulting when unset.
func (c Codes) Min() int {
	if c.MinLength <= 0 {
		return defaultCodeMinLength
	}
	return c.MinLength
}

// Max is the longest a code may be, defaulting when unset.
func (c Codes) Max() int {
	if c.MaxLength <= 0 {
		return defaultCodeMaxLength
	}
	return c.MaxLength
}

// LettersAllowed reports whether a code may contain letters (default true).
func (c Codes) LettersAllowed() bool { return enabled(c.Letters) }

// DigitsAllowed reports whether a code may contain digits (default true).
func (c Codes) DigitsAllowed() bool { return enabled(c.Digits) }

// DigitRequired reports whether a code must contain a digit (default true).
func (c Codes) DigitRequired() bool { return enabled(c.RequireDigit) }
