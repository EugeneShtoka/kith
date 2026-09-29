package tui

import (
	"log/slog"
	"maps"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// emojiOrder is one room's emoji scores (db.EmojiScores: 10 per use here, 3 in its
// spaces, 1 anywhere), per kind, since reacting and composing are different
// vocabularies. Fetched once per room per session so the order does not shift.
type emojiOrder struct {
	reaction map[string]int
	composed map[string]int
}

// scoresFor is the ranking to sort by for a kind, or nil when none has arrived yet.
func (o emojiOrder) scoresFor(kind domain.EmojiKind) map[string]int {
	if kind == domain.EmojiReaction {
		return o.reaction
	}
	return o.composed
}

// foldScores keys a ranking on the bare emoji, summing spellings that differ only in
// presentation selector or tone (usage records what was sent).
func foldScores(scores map[string]int) map[string]int {
	if len(scores) == 0 {
		return nil
	}
	folded := make(map[string]int, len(scores))
	for emoji, score := range scores {
		folded[bareEmoji(emoji)] += score
	}
	return folded
}

// emojiRanker is the one order emoji are offered in: quick palette, browser grid and
// :shortcode: popup. Compared lexicographically: this kind's score, the other kind's
// score, position in the static defaults, then input order (alphabetical).
type emojiRanker struct {
	primary, secondary, defaults map[string]int
}

// ranker builds the ranking for one kind in the open room. Scope "static" ignores
// history; the other scopes are applied by the query (emojiScoresCmd).
func (m Model) ranker(kind domain.EmojiKind) emojiRanker {
	order := m.glyphs.orders[m.openRoom]
	if m.glyphs.scope == "static" {
		order = emojiOrder{}
	}
	defaults := make(map[string]int, len(m.glyphs.static))
	for i, e := range m.glyphs.static {
		defaults[bareEmoji(e)] = len(m.glyphs.static) - i
	}
	return emojiRanker{
		primary:   order.scoresFor(kind),
		secondary: order.scoresFor(otherKind(kind)),
		defaults:  defaults,
	}
}

// otherKind is the vocabulary that breaks this one's ties.
func otherKind(kind domain.EmojiKind) domain.EmojiKind {
	if kind == domain.EmojiReaction {
		return domain.EmojiComposed
	}
	return domain.EmojiReaction
}

// key is the sort key, most significant first.
func (r emojiRanker) key(emoji string) [3]int {
	bare := bareEmoji(emoji)
	return [3]int{r.primary[bare], r.secondary[bare], r.defaults[bare]}
}

// sorted orders a list by the ranking, descending, keeping the incoming order for ties.
func (r emojiRanker) sorted(emojis []string) []string {
	ranked := slices.Clone(emojis)
	slices.SortStableFunc(ranked, func(a, b string) int {
		ka, kb := r.key(a), r.key(b)
		return slices.Compare(kb[:], ka[:])
	})
	return ranked
}

// rankedEmoji is the vocabulary in the order this room's history suggests.
func (m Model) rankedEmoji(kind domain.EmojiKind) []string {
	return m.ranker(kind).sorted(m.glyphs.set.all)
}

// quickPalette is the head of the same order, filtered (before truncating) to real
// emoji this room's network accepts.
func (m Model) quickPalette() []string {
	usable := m.allowedReactions(onlyEmoji(m.rankedEmoji(domain.EmojiReaction)))
	if len(usable) > paletteSize {
		usable = usable[:paletteSize]
	}
	return m.toneAll(usable)
}

// handleEmojiScores files a room's ranking, kept even if the room is no longer open.
func (m Model) handleEmojiScores(msg emojiScoresMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelDebug, "load emoji ranking", msg.err, "room", msg.roomID)
	if msg.err != nil || msg.roomID == "" {
		return m, nil // an unranked panel is alphabetical
	}
	orders := make(map[domain.RoomID]emojiOrder, len(m.glyphs.orders)+1)
	maps.Copy(orders, m.glyphs.orders)
	order := orders[msg.roomID]
	if msg.kind == domain.EmojiReaction {
		order.reaction = foldScores(msg.scores)
	} else {
		order.composed = foldScores(msg.scores)
	}
	orders[msg.roomID] = order
	m.glyphs.orders = orders
	if msg.roomID == m.openRoom {
		m.glyphs.palette = m.quickPalette()
	}
	return m, nil
}
