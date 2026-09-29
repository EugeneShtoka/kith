package tui

import (
	"log/slog"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Reactions a bridged network refuses are learned, not hardcoded: the daemon sees a
// bridge redact one of ours, the client sees its own send fail. Both write one table
// keyed on protocol, since the restriction belongs to the network.

// refusedIn reports whether emoji is known to be refused by protocol.
func (m Model) refusedIn(protocol domain.Protocol, emoji string) bool {
	if !protocol.IsBridged() {
		return false
	}
	return m.glyphs.refused[protocol.String()][bareEmoji(emoji)]
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
		byProtocol[r.Protocol][bareEmoji(r.Emoji)] = true
	}
	m.glyphs.refused = byProtocol
	return m, nil
}

// noteReactionFailure records a refusal only the client saw (its send failed) and
// says so, or the emoji vanishing from the palette would be inexplicable.
func (m Model) noteReactionFailure(emoji string) (Model, tea.Cmd) {
	protocol := m.roomProtocol()
	if !protocol.IsBridged() || emoji == "" {
		return m, nil
	}
	refused := cloneRefusals(m.glyphs.refused)
	if refused[protocol.String()] == nil {
		refused[protocol.String()] = map[string]bool{}
	}
	refused[protocol.String()][bareEmoji(emoji)] = true
	m.glyphs.refused = refused
	m = m.say(protocol.String() + " would not take " + emoji + " — it will not be offered here again")
	return m, m.recordRefusalCmd(protocol.String(), emoji)
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
