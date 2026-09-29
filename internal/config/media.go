package config

import (
	"strings"
	"time"
)

// Media is [display.media]: how attachments are shown, saved and cached.
type Media struct {
	Mode             string      `toml:"mode"`       // MediaModes; empty is "placeholder"
	Detail           string      `toml:"detail"`     // MediaDetails; empty is "sextant"
	MaxHeight        int         `toml:"max_height"` // rows; 0 half the message area
	MaxWidth         int         `toml:"max_width"`  // columns; 0 the body column
	DownloadDir      string      `toml:"download_dir"`
	DownloadTemplate string      `toml:"download_template"`
	Rules            []MediaRule `toml:"rule"`
	Auto             *bool       `toml:"auto"`
	Cache            *bool       `toml:"cache"`
	CacheDir         string      `toml:"cache_dir"`    // empty is $XDG_CACHE_HOME/kith/media
	CacheMaxMB       int         `toml:"cache_max_mb"` // 0 is the media package's default
	Audio            Audio       `toml:"audio"`
	Viewer           string      `toml:"viewer"`
	Opener           string      `toml:"opener"`
	VideoPlayer      string      `toml:"video_player"`
}

// AutoLoad reports whether pictures are fetched without being asked (default true).
func (m Media) AutoLoad() bool { return enabled(m.Auto) }

// Caching reports whether attachments are kept on disk (default true).
func (m Media) Caching() bool { return enabled(m.Cache) }

// MediaRule is one [[display.media.rule]]; unset fields inherit.
type MediaRule struct {
	Match    string   `toml:"match"`  // a room ID or a space's name
	Sender   string   `toml:"sender"` // MXID, for per-person settings (speed)
	Auto     *bool    `toml:"auto"`
	Cache    *bool    `toml:"cache"`
	Dir      string   `toml:"dir"`
	Template string   `toml:"template"`
	Speed    *float64 `toml:"speed"`
}

// Audio is [display.media.audio]: the voice-note player.
type Audio struct {
	Speed      float64 `toml:"speed"`       // multiplier; 0 is DefaultAudioSpeed
	Skip       int     `toml:"skip"`        // seconds; 0 is DefaultAudioSkip
	Player     string  `toml:"player"`      // program and flags, no shell
	CloseAfter *int    `toml:"close_after"` // seconds; nil default, -1 never
}

// Audio defaults.
const (
	DefaultAudioSpeed = 1.0
	DefaultAudioSkip  = 10
)

// DefaultCloseAfterSeconds is how long a finished note's bar stays when nothing says
// otherwise.
const DefaultCloseAfterSeconds = 15

// CloseDelay is how long the bar stays after the note ends: the configured seconds, the
// default when unset, and a negative duration for "never" (the config's -1) — the same
// sign convention read_delay and the popup timeout use.
func (a Audio) CloseDelay() time.Duration {
	seconds := DefaultCloseAfterSeconds
	if a.CloseAfter != nil {
		seconds = *a.CloseAfter
	}
	if seconds < 0 {
		return -1 // stays until stopped by hand
	}
	return time.Duration(seconds) * time.Second
}

// MaxAudioSkip is the largest skip worth calling one.
const MaxAudioSkip = 600

// PlaySpeed is the multiplier voice notes start at.
func (a Audio) PlaySpeed() float64 {
	if a.Speed == 0 {
		return DefaultAudioSpeed
	}
	return a.Speed
}

// SkipStep is how far the back and forward keys move.
func (a Audio) SkipStep() time.Duration {
	if a.Skip == 0 {
		return DefaultAudioSkip * time.Second
	}
	return time.Duration(a.Skip) * time.Second
}

// PlayerCommand is the player as a program and its arguments, or nil for the default.
func (a Audio) PlayerCommand() []string { return strings.Fields(a.Player) }
