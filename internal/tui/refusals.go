package tui

import (
	"log/slog"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Reactions a bridged network refuses are learned, not hardcoded: the daemon sees a bridge
// redact one, the client sees its send fail. Both write one table keyed on protocol.

// tonedReaction is a toned reaction sent, kept to resend without its tone.
type tonedReaction struct {
	room   domain.RoomID
	target domain.EventID
	key    string
}

// refusalKey is how a refusal is kept: without a presentation selector, its tone kept.
// A refused tone is no refusal of the emoji: it is sent without one instead.
func refusalKey(emoji string) string { return strings.ReplaceAll(emoji, presentationSelector, "") }

// refusedIn reports whether emoji is known to be refused by protocol, whatever its
// tone: a network refusing 👍 refuses 👍🏻 too, but one refusing 👍🏻 takes 👍.
func (m Model) refusedIn(protocol domain.Protocol, emoji string) bool {
	if !protocol.IsBridged() {
		return false
	}
	return m.glyphs.refused[protocol.String()][bareEmoji(emoji)]
}

// refusesTones reports whether protocol takes no toned reaction: known of the network,
// or learned from one refused.
func (m Model) refusesTones(protocol domain.Protocol) bool {
	if !protocol.TakesTonedReactions() {
		return true
	}
	for emoji := range m.glyphs.refused[protocol.String()] {
		if withoutTone(emoji) != emoji {
			return true
		}
	}
	return false
}

// allowedReactions drops what the room's protocol refused, compared untoned (a
// network refusing 👍 refuses 👍🏻 too).
func (m Model) allowedReactions(emojis []string) []string {
	protocol := m.roomProtocol()
	if !protocol.IsBridged() || len(m.glyphs.refused[protocol.String()]) == 0 {
		return emojis
	}
	out := make([]string, 0, len(emojis))
	for _, emoji := range emojis {
		if !m.refusedIn(protocol, emoji) {
			out = append(out, emoji)
		}
	}
	return out
}

// roomProtocol is the open room's bridged network, inferred from ghost-user MXIDs
// in the loaded messages, then the member list; else ProtocolMatrix.
func (m Model) roomProtocol() domain.Protocol {
	for i := range m.timeline.messages {
		if p := domain.ProtocolOf(m.timeline.messages[i].Sender); p.IsBridged() {
			return p
		}
	}
	for i := range m.timeline.members {
		if p := domain.ProtocolOf(m.timeline.members[i].UserID); p.IsBridged() {
			return p
		}
	}
	return domain.ProtocolMatrix
}

// handleRefusals replaces the table with the daemon's copy.
func (m Model) handleRefusals(msg refusalsMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelWarn, "load reaction refusals", msg.err)
	if msg.err != nil {
		return m, nil // an unreadable record is not evidence that nothing is refused
	}
	byProtocol := make(map[string]map[string]bool, len(msg.refusals))
	for _, r := range msg.refusals {
		if byProtocol[r.Protocol] == nil {
			byProtocol[r.Protocol] = map[string]bool{}
		}
		byProtocol[r.Protocol][refusalKey(r.Emoji)] = true
	}
	m.glyphs.refused = byProtocol
	// A bridge that took the last toned reaction and then refused it: resend it plain.
	if t := m.glyphs.toned; t.key != "" && byProtocol[m.roomProtocol().String()][refusalKey(t.key)] {
		m.glyphs.toned = tonedReaction{}
		return m, m.sendReactionSaying(t.room, t.target, withoutTone(t.key),
			m.roomProtocol().String()+" would not take "+t.key+": sent "+withoutTone(t.key)+" instead")
	}
	return m, nil
}

// noteReactionFailure records a refusal only the client saw and says so, so the emoji does
// not vanish from the palette unexplained. A toned one refused is resent without its
// tone, and the network is taken to take no tones from then on.
func (m Model) noteReactionFailure(sent reactionSentMsg) (Model, tea.Cmd) {
	protocol, emoji := m.roomProtocol(), sent.key
	if !protocol.IsBridged() || emoji == "" {
		return m, nil
	}
	refused := cloneRefusals(m.glyphs.refused)
	if refused[protocol.String()] == nil {
		refused[protocol.String()] = map[string]bool{}
	}
	refused[protocol.String()][refusalKey(emoji)] = true
	m.glyphs.refused = refused
	record := m.recordRefusalCmd(protocol.String(), emoji)
	if plain := withoutTone(emoji); plain != emoji {
		m.glyphs.toned = tonedReaction{}
		return m, tea.Batch(record, m.sendReactionSaying(sent.room, sent.target, plain,
			protocol.String()+" would not take "+emoji+": sent "+plain+" instead"))
	}
	m = m.say(protocol.String() + " would not take " + emoji + " — it will not be offered here again")
	return m, record
}

// cloneRefusals deep-copies the table before a write, since Model is copied by value.
func cloneRefusals(in map[string]map[string]bool) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(in)+1)
	for protocol, emojis := range in {
		copied := make(map[string]bool, len(emojis))
		for emoji := range emojis {
			copied[emoji] = true
		}
		out[protocol] = copied
	}
	return out
}
