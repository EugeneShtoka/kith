package config

import "time"

// Notifications is [notifications]: sinks, templates, rate limits and rules (see
// docs/notifications.md).
type Notifications struct {
	Enabled      bool   `toml:"enabled"`
	Desktop      bool   `toml:"desktop"` // freedesktop D-Bus popup
	Command      string `toml:"command"`
	Title        string `toml:"title"` // template; empty DefaultTitleTemplate, "-" none
	Body         string `toml:"body"`  // template; empty DefaultBodyTemplate, "-" none
	Sound        string `toml:"sound"` // a file; empty silent
	SoundCommand string `toml:"sound_command"`
	Timeout      *int   `toml:"timeout"`      // seconds; nil default, -1 until dismissed
	MaxPerRoom   int    `toml:"max_per_room"` // per RateWindow; 0 no limit
	RateWindow   string `toml:"rate_window"`  // a duration
	Rules        []Rule `toml:"rule"`
}

// Rule is one [[notifications.rule]]: part of the policy for one place or person.
type Rule struct {
	Name   string `toml:"name"`
	Match  string `toml:"match"`  // one place-vocabulary entry (setup.PlaceEntries); empty is account-wide
	Sender string `toml:"sender"` // MXID
	Sound  string `toml:"sound"`
	Show   string `toml:"show"` // none, mention, dm or all
	Ring   string `toml:"ring"` // as Show; unset follows it
	When   string `toml:"when"` // a daily window, "22:00-08:00"; empty always
	Thread string `toml:"thread"`
}

// DefaultTimeoutSeconds is how long a desktop popup stays up when nothing says
// otherwise.
const DefaultTimeoutSeconds = 15

// PopupTimeout is how long a popup stays up: the configured seconds, the default when
// unset, and a negative duration for "until dismissed" (the config's -1).
func (n Notifications) PopupTimeout() time.Duration {
	seconds := DefaultTimeoutSeconds
	if n.Timeout != nil {
		seconds = *n.Timeout
	}
	if seconds < 0 {
		return -1 // stays until dismissed
	}
	return time.Duration(seconds) * time.Second
}

// The built-in notification format, used for any template the user leaves unset.
const (
	DefaultTitleTemplate = "{space} · {room} · {sender}"
	DefaultBodyTemplate  = "{body}"
)

// TitleTemplate is the effective summary template, defaulting to DefaultTitleTemplate.
func (n Notifications) TitleTemplate() string { return template(n.Title, DefaultTitleTemplate) }

// BodyTemplate is the effective body template, defaulting to DefaultBodyTemplate.
// As with TitleTemplate, "-" means deliberately empty.
func (n Notifications) BodyTemplate() string { return template(n.Body, DefaultBodyTemplate) }

// template resolves one configured template against its default: unset falls back
// to the default, and "-" is the explicit way to ask for nothing at all.
func template(configured, fallback string) string {
	switch configured {
	case "":
		return fallback
	case "-":
		return ""
	default:
		return configured
	}
}

// Schedule is [schedule]: messages written now and sent later.
type Schedule struct {
	OverdueCutoff *int `toml:"overdue_cutoff_hours"` // nil default, <= 0 holds nothing
}

// DefaultOverdueCutoffHours is how late a missed message may be and still go out on its
// own.
const DefaultOverdueCutoffHours = 24

// Cutoff is how far past its time a message may be and still be sent, resolving the
// default.
func (s Schedule) Cutoff() time.Duration {
	hours := DefaultOverdueCutoffHours
	if s.OverdueCutoff != nil {
		hours = *s.OverdueCutoff
	}
	if hours <= 0 {
		return 0 // domain.PlanSchedule reads this as "hold nothing"
	}
	return time.Duration(hours) * time.Hour
}
