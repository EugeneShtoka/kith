package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// blockableKeys names the phonebook identifier keys of networks whose adapters
// implement route.Blocker. Keep in sync with the adapters.
var blockableKeys = []string{"whatsapp", "telegram", "messenger", "instagram"}

// hasBlockableID reports whether a card has any ID on a network with a server-side
// block list.
func hasBlockableID(ids map[string][]string) bool {
	for _, key := range blockableKeys {
		if len(ids[key]) > 0 {
			return true
		}
	}
	return false
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
	targets := collectBlockableIDs(p.card.IDs)
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
