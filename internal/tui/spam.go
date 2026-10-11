package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// Spam is a move, not a tag: no space lists a spam room, and a Spam tag (rule `spam`,
// exclusive) gives them a row. Spam rooms are out of unread totals, badges and
// group-wide mark-read.

func (v unreadView) isSpam(room domain.Room) bool { return v.spamRooms[room.ID] }

// verdict is why a room is in Spam (which rule, which filter); empty if it isn't.
func (v unreadView) verdict(room domain.Room) domain.SpamVerdict {
	if !v.isSpam(room) {
		return domain.SpamVerdict{}
	}
	if caught, ok := v.caught[room.ID]; ok && caught.Spam() {
		return caught
	}
	return domain.SpamVerdict{Room: room.ID, Rule: domain.SpamByHand}
}

// resolveSpam is the slow per-room answer, computed when inputs change. The exemption
// wins: a carve-out a rule could outrank would be only a suggestion.
func (v unreadView) resolveSpam(room domain.Room) bool {
	if v.facts == nil {
		return false
	}
	facts := v.facts(room)
	if v.spam.Excused(facts) {
		return false
	}
	if v.spam.Names(facts) {
		return true
	}
	return v.caught[room.ID].Spam()
}

// refreshSpam recomputes the Spam set as a new map (as refreshArchived).
func (m Model) refreshSpam() Model {
	spam := map[domain.RoomID]bool{}
	if m.rail.spam.Has() || len(m.rail.caught) > 0 {
		view := m.unreadView()
		for i := range m.rooms.all {
			if view.resolveSpam(m.rooms.all[i]) {
				spam[m.rooms.all[i].ID] = true
			}
		}
	}
	m.rail.spamRooms = spam
	return m
}

// toggleSpam moves the selected room into Spam or out.
//
// Marking adds the room to `[spam] rooms` and drops any exemption. Releasing writes an
// exemption, since a rule-caught room cannot be un-listed but can be excused by ID. The
// release also goes to the room's account data so it applies on every machine.
func (m Model) toggleSpam() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok || room.IsInvite() {
		return m, nil
	}
	name, id := m.roomName(room), string(room.ID)
	if m.unreadView().isSpam(room) {
		spam := m.rail.spam.With(id, false).Excusing(id, true)
		m = m.releasedHere(room.ID)
		mdl, cmd := m.applySpam(spam, name+" is out of Spam — and exempt, so no rule puts it back")
		return mdl, tea.Batch(cmd, m.releaseSpamCmd(room.ID))
	}
	spam := m.rail.spam.Excusing(id, false).With(id, true)
	return m.applySpam(spam, name+" is in Spam — still readable, out of its space and out of the counts")
}

// applySpam writes both lists to the top-level `[spam]` section (the daemon reads it
// too) and keeps the cursor near the moved room.
func (m Model) applySpam(spam domain.Spam, done string) (Model, tea.Cmd) {
	was := m.roomCursor()
	cfg := m.conf.base.Clone()
	cfg.Spam.Rooms, cfg.Spam.Except = spam.Entries, spam.Except
	mdl, cmd := m.applyConfig(cfg, done)
	moved, move := mdl.keepCursorNearby(was)
	return moved, tea.Batch(cmd, move)
}

// releasedHere drops the rule's verdict before the next poll confirms it; copies the
// map, since an earlier Model still holds it.
func (m Model) releasedHere(roomID domain.RoomID) Model {
	caught := make(map[domain.RoomID]domain.SpamVerdict, len(m.rail.caught))
	for id, verdict := range m.rail.caught {
		if id != roomID {
			caught[id] = verdict
		}
	}
	m.rail.caught = caught
	return m
}

// spamReleasedMsg reports only failure; the local exemption already holds.
type spamReleasedMsg struct{ err error }

func (m Model) releaseSpamCmd(roomID domain.RoomID) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		release := domain.SpamVerdict{Room: roomID, Released: true, At: time.Now()}
		return spamReleasedMsg{err: backend.MarkSpam(ctx, release)}
	}
}

func (m Model) handleSpamReleased(msg spamReleasedMsg) (Model, tea.Cmd) {
	if msg.err == nil {
		return m, nil
	}
	return m.sayErr("released here, but your other machines were not told", msg.err), nil
}

// spamReason is why this room is in Spam, in explanation words; empty if it isn't.
func (m Model) spamReason(room domain.Room) string {
	verdict := m.unreadView().verdict(room)
	if !verdict.Spam() {
		return ""
	}
	return verdict.Rule.Reason(verdict.Filter)
}

type spamLoadedMsg struct{ caught []domain.SpamVerdict }

// loadSpamCmd reads which rooms the daemon's filters caught, at startup and on the
// poll. A read failure means nothing in Spam.
func (m Model) loadSpamCmd() tea.Cmd {
	ctx, backend, log := m.ctx, m.backend, m.log
	return func() tea.Msg {
		caught, err := backend.SpamRooms(ctx)
		if err != nil {
			log.Warn("load spam verdicts failed", "err", err)
			return nil
		}
		return spamLoadedMsg{caught: caught}
	}
}

// handleSpamLoaded replaces the verdicts wholesale (a release from another machine
// arrives as a non-spam verdict) and rebuilds the rail.
func (m Model) handleSpamLoaded(msg spamLoadedMsg) (Model, tea.Cmd) {
	caught := make(map[domain.RoomID]domain.SpamVerdict, len(msg.caught))
	for i := range msg.caught {
		if msg.caught[i].Spam() {
			caught[msg.caught[i].Room] = msg.caught[i]
		}
	}
	m.rail.caught = caught
	return m.refreshPlaces().rebuiltRail(), nil
}

// openCaught previews what the spam filters catch in the local index, with the usual
// scope ladder (`/caught` = this room, tab widens, `:caught` = everywhere).
func (m Model) openCaught() (Model, tea.Cmd) {
	rules, err := setup.SpamRules(m.conf.base.Spam)
	if err != nil {
		return m.sayErr("the spam filters do not parse", err), nil
	}
	kind := caughtList{filters: map[string]string{}}
	for _, filter := range rules.Filters {
		if len(filter.Words.Words) == 0 {
			// Sender-only filters cannot be previewed; counted so the summary says so.
			kind.senders++
			continue
		}
		for _, word := range filter.Words.Words {
			kind.words = append(kind.words, word)
			// First filter wins a shared word, as in the engine.
			if _, taken := kind.filters[word]; !taken {
				kind.filters[word] = filter.Name
			}
		}
	}
	return m.openList(promptCaught, m.defaultSearchScope(), kind)
}
