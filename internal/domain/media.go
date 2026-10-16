package domain

import (
	"fmt"
	"path"
	"strings"
)

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
	// Anonymize sends attachments under a neutral name (SentName).
	Anonymize *bool
	// Speed is the voice note playback multiplier.
	Speed *float64
}

// MediaPolicy is the effective policy for a place: auto-fetch images, cache them on
// disk, send attachments under a neutral name, and voice note speed.
type MediaPolicy struct {
	Auto      bool
	Cache     bool
	Anonymize bool
	Speed     float64
}

// ResolveMedia applies the narrowest matching rule over defaults, the policy where no
// rule says otherwise.
func ResolveMedia(defaults MediaPolicy, rules []MediaRule, place DownloadPlace) MediaPolicy {
	policy := defaults
	if rule, ok := narrowestMediaRule(rules, place); ok {
		if rule.Auto != nil {
			policy.Auto = *rule.Auto
		}
		if rule.Cache != nil {
			policy.Cache = *rule.Cache
		}
		if rule.Anonymize != nil {
			policy.Anonymize = *rule.Anonymize
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

// sentKinds is what a file's extension says it is, as an attachment sent under a
// neutral name names it.
var sentKinds = map[string]string{}

func init() {
	for kind, exts := range map[string][]string{
		"photo":    {"jpg", "jpeg", "png", "gif", "webp", "heic", "heif", "avif", "bmp", "tif", "tiff", "svg", "raw", "dng", "cr2", "nef", "arw"},
		"video":    {"mp4", "m4v", "mkv", "mov", "avi", "webm", "mpeg", "mpg", "wmv", "flv", "3gp", "ts", "mts"},
		"music":    {"mp3", "flac", "wav", "ogg", "oga", "opus", "m4a", "aac", "wma", "alac", "aiff", "aif", "mid", "midi"},
		"document": {"pdf", "doc", "docx", "odt", "rtf", "txt", "md", "xls", "xlsx", "ods", "csv", "ppt", "pptx", "odp", "epub", "pages", "numbers", "key", "tex", "djvu"},
		"archive":  {"zip", "7z", "rar", "tar", "gz", "tgz", "bz2", "xz", "zst", "lz", "lzma", "cab", "iso"},
	} {
		for _, ext := range exts {
			sentKinds[ext] = kind
		}
	}
}

// SentName is the name an attachment called name is sent under when names are
// anonymized: what it is (photo, video, music, document, archive; file for anything
// else) and its extension, lower-cased, so it still opens. "IMG_2041 Haifa.JPG" is
// photo.jpg, "contract v3.docx" document.docx, "backup.tar.gz" archive.tar.gz.
func SentName(name string) string {
	base := path.Base(strings.ReplaceAll(name, "\\", "/"))
	lower := strings.ToLower(base)
	ext := strings.TrimPrefix(path.Ext(lower), ".")
	if ext == "" || ext == lower[1:] && strings.HasPrefix(lower, ".") {
		return "file"
	}
	// A compound archive keeps both parts: .tar.gz, .tar.xz.
	full := "." + ext
	if stem := strings.TrimSuffix(lower, full); strings.HasSuffix(stem, ".tar") && sentKinds[ext] == "archive" {
		full = ".tar" + full
	}
	kind, ok := sentKinds[ext]
	if !ok {
		kind = "file"
	}
	return kind + full
}
