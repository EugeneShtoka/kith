package domain

// MediaType is the kind of attachment a media message carries.
type MediaType string

const (
	MediaImage MediaType = "image" // m.image
	MediaVideo MediaType = "video" // m.video
	MediaAudio MediaType = "audio" // m.audio
	MediaFile  MediaType = "file"  // m.file (and anything else with an attachment)
)

// VoiceMessage is the name of a recorded voice note, which carries no file name of its
// own; a Matrix bridge writes the same words into the body.
const VoiceMessage = "voice message"

// Media is the display metadata for a message's attachment: enough to render a
// placeholder chip and to size an inline/preview render.
type Media struct {
	// Type is what to draw: an image gets a preview, everything else a chip.
	Type MediaType
	Name string // the file name (from the message body)
	// Mime is the sender's claim about the content type.
	Mime   string
	Width  int // pixels, 0 if unknown
	Height int // pixels, 0 if unknown
	Size   int // bytes, 0 if unknown
}

// IsImage reports whether the attachment is a still image, the only kind the
// timeline can render inline (others show as a chip).
func (m *Media) IsImage() bool { return m != nil && m.Type == MediaImage }

// IsAudio reports whether the attachment is something to listen to — a voice note or
// any other m.audio — which is what the player can open.
func (m *Media) IsAudio() bool { return m != nil && m.Type == MediaAudio }

// IsVideo reports whether the attachment is something to watch.
func (m *Media) IsVideo() bool { return m != nil && m.Type == MediaVideo }

// MediaRule is one place's attachment policy, in the pure layer's own terms. Each
// field is "unset means inherit", so a rule states only what it changes.
type MediaRule struct {
	// Match is a room ID or a space's name; Sender is an MXID.
	Match  string
	Sender string
	Auto   *bool
	Cache  *bool
	// Speed is how fast this place's voice notes play, as a multiplier.
	Speed *float64
}

// MediaPolicy is what actually applies somewhere: whether pictures are fetched without
// being asked, whether their bytes are kept on disk, and how fast a voice note plays.
type MediaPolicy struct {
	Auto  bool
	Cache bool
	Speed float64
}

// ResolveMedia works out what applies in one place.
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

// narrowestMediaRule finds the most specific rule for a place, later rules winning
// among equals — see narrowestDownloadRule, which resolves the same question the same
// way over the same list.
func narrowestMediaRule(rules []MediaRule, place DownloadPlace) (MediaRule, bool) {
	at, ok := Narrowest(Matches(rules, func(r MediaRule) Match {
		return Match{Place: r.Match, Sender: r.Sender}
	}), place.scope())
	if !ok {
		return MediaRule{}, false
	}
	return rules[at], true
}
