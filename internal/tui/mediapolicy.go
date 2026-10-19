package tui

import (
	"path/filepath"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// showsPictures reports whether pictures should be fetched and drawn now: only with the
// timeline focused, so browsing the room list does not fetch every room's photos.
func (m Model) showsPictures() bool {
	return m.pics.graphics != graphicsNone && m.focus == paneTimeline
}

// mediaPolicy is whether one message's attachment is auto-fetched and may be cached,
// resolved per place with narrowest-wins precedence.
func (m Model) mediaPolicy(msg domain.Message) domain.MediaPolicy {
	return m.mediaPolicyAt(m.placeOf(msg))
}

// mediaPolicyAt is the attachment policy at place.
func (m Model) mediaPolicyAt(place domain.DownloadPlace) domain.MediaPolicy {
	defaults := domain.MediaPolicy{Auto: m.pics.auto, Cache: m.pics.caching, Anonymize: m.pics.anonymize, Speed: m.pics.speed}
	return domain.ResolveMedia(defaults, m.pics.rules, place)
}

// sentName is the name a file goes out under in a room: a neutral one (domain.SentName)
// where [display.media] anonymize holds there, its own elsewhere.
func (m Model) sentName(roomID domain.RoomID, path string) string {
	name := filepath.Base(path)
	if m.anonymizes(roomID, path) {
		return domain.SentName(name)
	}
	return name
}

// anonymizes reports whether [display.media] anonymize holds for a file sent to a room:
// a neutral name, and a picture without its metadata.
func (m Model) anonymizes(roomID domain.RoomID, path string) bool {
	return m.mediaPolicyAt(m.placeOf(domain.Message{RoomID: roomID, Media: &domain.Media{Name: filepath.Base(path)}})).Anonymize
}

// mediaRules converts the configured per-place rules into the pure layer's shape.
func mediaRules(cfg config.Media) []domain.MediaRule {
	configured := cfg.Rules
	if len(configured) == 0 {
		return nil
	}
	rules := make([]domain.MediaRule, 0, len(configured))
	for _, rule := range configured {
		rules = append(rules, domain.MediaRule{
			Match:     rule.Match,
			Sender:    rule.Sender,
			Auto:      rule.Auto,
			Cache:     rule.Cache,
			Anonymize: rule.Anonymize,
			Speed:     rule.Speed,
		})
	}
	return rules
}
