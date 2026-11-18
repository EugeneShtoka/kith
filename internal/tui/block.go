package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// blockableKeys names the networks whose adapters implement route.Blocker, by their
// phonebook identifier key. Keep in sync with the adapters.
var blockableKeys = []string{"whatsapp", "telegram", "messenger", "instagram"}

// blockTargets is every kith-shaped ID of the person on a blockable network: the
// card's, and the one their card was opened on, which names them before their card
// has that network's key.
func blockTargets(p personState) []string {
	out := collectBlockableIDs(p.card.IDs)
	for _, key := range blockableKeys {
		if protocolOfKey(key) == domain.NetworkOf(p.id) && !slices.Contains(out, p.id) {
			out = append(out, p.id)
		}
	}
	return out
}

// blockedChangedMsg is the outcome of a block/unblock round-trip; one message
// per network ID the person has.
type blockedChangedMsg struct {
	userID  string
	blocked bool
	err     error
}

// blockUnblockCmd calls BlockPerson or UnblockPerson for every ID the person has
// on a network with a server-side block list.
func (m Model) blockUnblockCmd(p personState, block bool) tea.Cmd {
	backend, ctx := m.backend, m.ctx
	targets := blockTargets(p)
	if len(targets) == 0 {
		return func() tea.Msg {
			return blockedChangedMsg{err: errNoBlockableID{}}
		}
	}
	cmds := make([]tea.Cmd, 0, len(targets))
	for _, target := range targets {
		userID := target
		cmds = append(cmds, func() tea.Msg {
			var err error
			if block {
				err = backend.BlockPerson(ctx, userID)
			} else {
				err = backend.UnblockPerson(ctx, userID)
			}
			return blockedChangedMsg{userID: userID, blocked: block, err: err}
		})
	}
	return tea.Batch(cmds...)
}

// collectBlockableIDs is every kith-shaped ID of the card on a blockable network.
// The phonebook stores IDs as the network's own native form; wrap each in the
// kith "<network>:<native>" shape the adapter expects.
func collectBlockableIDs(ids map[string][]string) []string {
	var out []string
	for _, key := range blockableKeys {
		network := protocolOfKey(key)
		for _, native := range ids[key] {
			out = append(out, domain.NativePerson(network, native))
		}
	}
	return out
}

// protocolOfKey maps a phonebook key ("whatsapp", "telegram", ...) to its kith
// protocol.
func protocolOfKey(key string) domain.Protocol {
	switch key {
	case "whatsapp":
		return domain.ProtocolWhatsApp
	case "telegram":
		return domain.ProtocolTelegram
	case "messenger":
		return domain.ProtocolMessenger
	case "instagram":
		return domain.ProtocolInstagram
	}
	return domain.ProtocolMatrix
}

// errNoBlockableID is used when the row is chosen on a card without a WhatsApp /
// Telegram / Meta identifier.
type errNoBlockableID struct{}

func (errNoBlockableID) Error() string {
	return "this person has no ID on a network with a server-side block list"
}

var _ error = errNoBlockableID{}

// handleBlockedChanged says what the server said. One message per ID the person
// has; a success per ID says the server took it, a failure per ID says what the
// server refused.
func (m Model) handleBlockedChanged(msg blockedChangedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("change the block list", msg.err), nil
	}
	what := "blocked"
	if !msg.blocked {
		what = "unblocked"
	}
	return m.say(what + " " + msg.userID), nil
}
