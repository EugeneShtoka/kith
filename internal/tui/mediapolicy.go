package tui

import (
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
	return domain.ResolveMedia(m.pics.auto, m.pics.caching, m.pics.speed, m.pics.rules, m.placeOf(msg))
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
			Match:  rule.Match,
			Sender: rule.Sender,
			Auto:   rule.Auto,
			Cache:  rule.Cache,
			Speed:  rule.Speed,
		})
	}
	return rules
}
