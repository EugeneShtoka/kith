package domain

import "fmt"

// MediaType is the kind of attachment a media message carries.
type MediaType string

const (
	MediaImage MediaType = "image" // m.image
	MediaVideo MediaType = "video" // m.video
	MediaAudio MediaType = "audio" // m.audio
	MediaFile  MediaType = "file"  // m.file (and anything else with an attachment)
)

// VoiceMessage names a voice note, which has no file name; bridges use the same body.
const VoiceMessage = "voice message"

// Media is the display metadata for a message's attachment.
type Media struct {
	Type MediaType
	Name string // the file name (from the message body)
	// Mime is the sender's unverified content type.
	Mime   string
	Width  int // pixels, 0 if unknown
	Height int // pixels, 0 if unknown
	Size   int // bytes, 0 if unknown
}

// IsImage reports whether the attachment is a still image, the only kind rendered
// inline.
func (m *Media) IsImage() bool { return m != nil && m.Type == MediaImage }

// IsAudio reports whether the attachment is m.audio (voice notes included).
func (m *Media) IsAudio() bool { return m != nil && m.Type == MediaAudio }

// IsVideo reports whether the attachment is a video.
func (m *Media) IsVideo() bool { return m != nil && m.Type == MediaVideo }

// MediaRule is one place's attachment policy; a nil field inherits.
type MediaRule struct {
	// Match is a room ID or a home entry (NamesHome); Sender is an MXID.
	Match  string
	Sender string
	Auto   *bool
	Cache  *bool
	// Speed is the voice note playback multiplier.
	Speed *float64
}

// MediaPolicy is the effective policy for a place: auto-fetch images, cache them on
// disk, and voice note speed.
type MediaPolicy struct {
	Auto  bool
	Cache bool
	Speed float64
}

// ResolveMedia applies the narrowest matching rule over the defaults.
func ResolveMedia(auto, cache bool, speed float64, rules []MediaRule, place DownloadPlace) MediaPolicy {
	policy := MediaPolicy{Auto: auto, Cache: cache, Speed: speed}
	if rule, ok := narrowestMediaRule(rules, place); ok {
		if rule.Auto != nil {
			policy.Auto = *rule.Auto
		}
		if rule.Cache != nil {
			policy.Cache = *rule.Cache
		}
		if rule.Speed != nil {
			policy.Speed = *rule.Speed
		}
	}
	return policy
}

// narrowestMediaRule finds the most specific rule for a place, later rules winning ties.
func narrowestMediaRule(rules []MediaRule, place DownloadPlace) (MediaRule, bool) {
	at, ok := Narrowest(Matches(rules, func(r MediaRule) Match {
		return Match{Place: r.Match, Sender: r.Sender}
	}), place.scope())
	if !ok {
		return MediaRule{}, false
	}
	return rules[at], true
}

// HumanSize formats a byte count: "1.2 MB", "340 KB", "12 B".
func HumanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
