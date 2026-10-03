package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// When the open room counts as read, and whether the room is told. The delay is per
// room opening, not per message, so a busy room still gets marked read.

// receiptState is the read-receipt policy and the receipts sent under it.
type receiptState struct {
	// policy is the read-receipt policy and its overrides.
	policy readSettings
	// armed counts room openings, so a stale read-delay timer does nothing.
	armed int
	// marked is the newest event a receipt landed for in this room, so each is marked
	// once; sending is the one in flight. A failed receipt leaves marked alone, so the
	// next trigger sends it again.
	marked  domain.EventID
	sending domain.EventID
}

// readSettings is the outer read policy that rules refine.
type readSettings struct {
	send  bool          // announce receipts rather than send them privately
	delay time.Duration // how long a room must be open to count as read
	rules []domain.ReadRule
}

// readSettingsFrom reads the policy out of a display config.
func readSettingsFrom(cfg config.Display) readSettings {
	return readSettings{send: cfg.SendsReceipts(), delay: cfg.ReadAfter(), rules: readRules(cfg)}
}

// readTimerMsg fires when a room has been open long enough to count as read. It
// carries what it was armed for so a stale timer does nothing.
type readTimerMsg struct {
	roomID domain.RoomID
	thread domain.EventID
	armed  int
}

// readPolicy is what applies in the open room, resolved against the newest message's
// sender — the person a receipt would report to.
func (m Model) readPolicy() domain.ReadPolicy {
	place := m.readPlace(m.openRoom)
	if msgs := m.shownMessages(); len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		place.Sender = last.Sender
		place.Person = m.processedName(last)
	}
	return domain.ResolveRead(m.receipts.policy.send, m.receipts.policy.delay, m.receipts.policy.rules, place)
}

// readPlace names a room for the rule ladder.
func (m Model) readPlace(roomID domain.RoomID) domain.DownloadPlace {
	place := domain.DownloadPlace{RoomID: string(roomID)}
	if room, ok := m.roomByID(roomID); ok {
		place.Room = m.roomLabel(room)
		if homes := m.homesOf(room.ID); len(homes) > 0 {
			place.Space = homes[0]
		}
	}
	return place
}

// splitByReceipt divides rooms by whether their receipts are announced. No sender is
// named: a bulk mark-read is about rooms, not whoever spoke last.
func (m Model) splitByReceipt(roomIDs []domain.RoomID) (announced, quiet []domain.RoomID) {
	for _, roomID := range roomIDs {
		policy := domain.ResolveRead(m.receipts.policy.send, m.receipts.policy.delay, m.receipts.policy.rules, m.readPlace(roomID))
		if policy.Send {
			announced = append(announced, roomID)
		} else {
			quiet = append(quiet, roomID)
		}
	}
	return announced, quiet
}

// armFocusRead starts the clock on a room the cursor has just landed on (a weaker
// gesture than opening it; off by default). Bumping receipts.armed disarms older timers.
func (m Model) armFocusRead() (Model, tea.Cmd) {
	m.receipts.armed++
	policy := m.readPolicy()
	switch {
	case !policy.OnFocus:
		return m, nil
	case policy.FocusIsImmediate():
		return m.markRead()
	}
	armed, roomID, thread := m.receipts.armed, m.openRoom, m.thread.root
	return m, tea.Tick(policy.FocusAfter, func(time.Time) tea.Msg {
		return readTimerMsg{roomID: roomID, thread: thread, armed: armed}
	})
}

// handleReadTimer marks a room read once the cursor has rested on it long enough.
func (m Model) handleReadTimer(msg readTimerMsg) (Model, tea.Cmd) {
	if msg.armed != m.receipts.armed || msg.roomID != m.openRoom || msg.thread != m.thread.root {
		return m, nil
	}
	return m.markRead()
}

// readRules converts the configured per-place rules into the pure layer's shape.
func readRules(cfg config.Display) []domain.ReadRule {
	if len(cfg.ReadRules) == 0 {
		return nil
	}
	rules := make([]domain.ReadRule, 0, len(cfg.ReadRules))
	for _, rule := range cfg.ReadRules {
		converted := domain.ReadRule{Match: rule.Match, Sender: rule.Sender, Send: rule.Send}
		if rule.Delay != nil {
			delay := time.Duration(max(*rule.Delay, 0)) * time.Second
			converted.Delay = &delay
		}
		rules = append(rules, converted)
	}
	return rules
}
