package config

import "time"

// Notifications is [notifications] in the bridge's config: what notifies, its sound,
// rate limits and rules (see docs/notifications.md).
type Notifications struct {
	Sound      string `toml:"sound"`        // a file; empty silent
	MaxPerRoom int    `toml:"max_per_room"` // per RateWindow; 0 no limit
	RateWindow string `toml:"rate_window"`  // a duration
	Rules      []Rule `toml:"rule"`
}

// DeviceNotifications is [notifications] in device.toml: how this device shows one.
type DeviceNotifications struct {
	Enabled      bool   `toml:"enabled"`
	Desktop      bool   `toml:"desktop"` // freedesktop D-Bus popup
	Command      string `toml:"command"`
	Title        string `toml:"title"` // template; empty DefaultTitleTemplate, "-" none
	Body         string `toml:"body"`  // template; empty DefaultBodyTemplate, "-" none
	SoundCommand string `toml:"sound_command"`
	Timeout      *int   `toml:"timeout"` // seconds; nil default, -1 until dismissed
}

// Rule is one [[notifications.rule]]: part of the policy for one place or person.
type Rule struct {
	Name   string `toml:"name"`
	Match  string `toml:"match"`  // one place-vocabulary entry (setup.PlaceEntries); empty is account-wide
	Sender string `toml:"sender"` // a person's ID
	Sound  string `toml:"sound"`
	Show   string `toml:"show"` // none, mention, dm or all
	Ring   string `toml:"ring"` // as Show; unset follows it
	When   string `toml:"when"` // a daily window, "22:00-08:00"; empty always
	Thread string `toml:"thread"`
}

// DefaultTimeoutSeconds is how long a desktop popup stays up by default.
const DefaultTimeoutSeconds = 15

// PopupTimeout is how long a popup stays up; negative means until dismissed (config -1).
func (n DeviceNotifications) PopupTimeout() time.Duration {
	seconds := DefaultTimeoutSeconds
	if n.Timeout != nil {
		seconds = *n.Timeout
	}
	if seconds < 0 {
		return -1
	}
	return time.Duration(seconds) * time.Second
}

// The built-in notification format, used for any template the user leaves unset.
const (
	DefaultTitleTemplate = "{space} · {room} · {sender}"
	DefaultBodyTemplate  = "{body}"
)

// TitleTemplate is the effective summary template, defaulting to DefaultTitleTemplate.
func (n DeviceNotifications) TitleTemplate() string { return template(n.Title, DefaultTitleTemplate) }

// BodyTemplate is the effective body template, defaulting to DefaultBodyTemplate.
func (n DeviceNotifications) BodyTemplate() string { return template(n.Body, DefaultBodyTemplate) }

// template resolves a configured template: unset is fallback, "-" is empty.
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

// DefaultOverdueCutoffHours is how late a missed scheduled message may still be sent.
const DefaultOverdueCutoffHours = 24

// Cutoff is how far past its time a scheduled message may still be sent.
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
