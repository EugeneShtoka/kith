package tui

import (
	"fmt"
	"image/color"
	"log/slog"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Everything one person said, in every room you share, by every ID kith links to them:
// drawn as the timeline draws a message, newest at the bottom, with the room above each
// run of messages in it. It takes the timeline pane, as the history view does, and pages
// back from the cache as you go up. Hidden people are shown: you opened them on purpose.

// saidPage is how many messages one read brings.
const saidPage = 100

// saidNarrow is how wide the view reaches: every room, one room, or one network.
type saidNarrow uint8

const (
	saidEverywhere saidNarrow = iota
	saidInRoom
	saidOnNetwork
)

type saidState struct {
	open bool
	// id is the ID it was opened on, name what they are called; ids every ID they go by.
	id, name string
	ids      []string
	narrow   saidNarrow
	room     domain.RoomID   // saidInRoom's
	network  domain.Protocol // saidOnNetwork's
	// msgs are oldest first; cursor is the selected one, bottom the one drawn last.
	msgs           []domain.Message
	cursor, bottom int
	loading        bool
	// atStart: the oldest cached is in msgs.
	atStart bool
	err     error
	// gen drops a page asked for before the view was reopened or narrowed.
	gen int
}

// saidPageMsg is one page read, oldest first; older reports it continues msgs.
type saidPageMsg struct {
	gen   int
	older bool
	page  []domain.Message
	err   error
}

// openSaid opens everything id's person said, everywhere.
func (m Model) openSaid(id, name string) (Model, tea.Cmd) {
	if id == "" {
		return m, nil
	}
	m = m.closePrompt()
	m.search, m.history = searchState{}, historyState{}
	m.said = saidState{open: true, id: id, name: name, ids: m.personIDs(id), loading: true, gen: m.said.gen + 1}
	m.focus = paneTimeline
	return m.clearStatus(), m.saidPageCmd(false)
}

// saidOfSender opens it on the selected message's sender.
func (m Model) saidOfSender() (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	if !ok || msg.Sender == "" {
		return m, nil
	}
	return m.openSaid(msg.Sender, stripIsolates(m.processedName(msg)))
}

func (m Model) closeSaid() (Model, tea.Cmd) {
	m.said = saidState{gen: m.said.gen}
	return m.clearStatus(), nil
}

// saidRequest is the next page: before the oldest held when older.
func (m Model) saidRequest(older bool) domain.SaidRequest {
	s := m.said
	req := domain.SaidRequest{Senders: s.ids, Rooms: domain.EveryRoom(), Limit: saidPage}
	switch s.narrow {
	case saidInRoom:
		req.Rooms = domain.TheseRooms([]domain.RoomID{s.room})
	case saidOnNetwork:
		req.Senders = slices.DeleteFunc(slices.Clone(s.ids), func(id string) bool { return domain.ProtocolOf(id) != s.network })
	case saidEverywhere:
	}
	if older && len(s.msgs) > 0 {
		req.Before = s.msgs[0].Place()
	}
	return req
}

func (m Model) saidPageCmd(older bool) tea.Cmd {
	ctx, backend, gen, req := m.ctx, m.backend, m.said.gen, m.saidRequest(older)
	return func() tea.Msg {
		page, err := backend.MessagesFrom(ctx, req)
		return saidPageMsg{gen: gen, older: older, page: page, err: err}
	}
}

// handlePeopleViewsMsg routes the answers of the views of people (this one and a tag's
// people); handled is false for any other message.
func (m Model) handlePeopleViewsMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case saidPageMsg:
		return answered(m.handleSaidPage(msg))
	case saidPeopleMsg:
		return answered(m.handleSaidPeople(msg))
	case tagPeopleCountsMsg:
		return answered(m.handleTagPeopleCounts(msg))
	}
	return m, nil, false
}

// handleSaidPage files a page if it is still for the view as it is. An empty page means
// the cache has nothing older.
func (m Model) handleSaidPage(msg saidPageMsg) (Model, tea.Cmd) {
	if !m.said.open || msg.gen != m.said.gen {
		return m, nil
	}
	s := &m.said
	s.loading, s.err = false, msg.err
	if msg.err != nil {
		return m, nil
	}
	if len(msg.page) == 0 {
		s.atStart = true
		return m, nil
	}
	if msg.older {
		s.msgs = append(slices.Clone(msg.page), s.msgs...)
		s.cursor += len(msg.page)
		s.bottom += len(msg.page)
	} else {
		s.msgs = msg.page
		s.cursor, s.bottom = len(msg.page)-1, len(msg.page)-1
	}
	return m.loadSaidQuotes(msg.page)
}

// loadSaidQuotes fetches the messages the page answers that nothing holds yet.
func (m Model) loadSaidQuotes(page []domain.Message) (Model, tea.Cmd) {
	var cmds []tea.Cmd
	for i := range page {
		target := page[i].ReplyTo
		if target == "" || m.quotes.asked[target] {
			continue
		}
		if _, ok := m.saidQuoted(target); ok {
			continue
		}
		m = m.markQuoteAsked(target)
		cmds = append(cmds, m.fetchQuoteCmd(page[i].RoomID, target))
	}
	return m, tea.Batch(cmds...)
}

// saidQuoted is a replied-to message, if the view or a fetch holds it.
func (m Model) saidQuoted(id domain.EventID) (domain.Message, bool) {
	if i := indexOfMessage(m.said.msgs, id); i >= 0 {
		return m.said.msgs[i], true
	}
	msg, ok := m.quotes.known[id]
	return msg, ok
}

// narrowSaid cycles everywhere → the selected message's room → its sender's network →
// everywhere. The network step is skipped for someone on one network only.
func (m Model) narrowSaid() (Model, tea.Cmd) {
	s := m.said
	at, ok := s.selected()
	switch {
	case s.narrow == saidEverywhere && ok:
		s.narrow, s.room = saidInRoom, at.RoomID
	case s.narrow == saidInRoom && ok && m.saidNetworks() > 1:
		s.narrow, s.network = saidOnNetwork, domain.ProtocolOf(at.Sender)
	default:
		s.narrow = saidEverywhere
	}
	m.said = saidState{
		open: true, id: s.id, name: s.name, ids: s.ids, narrow: s.narrow, room: s.room, network: s.network,
		loading: true, gen: s.gen + 1,
	}
	return m, m.saidPageCmd(false)
}

// saidNetworks is how many networks their IDs are on.
func (m Model) saidNetworks() int {
	seen := map[domain.Protocol]bool{}
	for _, id := range m.said.ids {
		seen[domain.ProtocolOf(id)] = true
	}
	return len(seen)
}

func (s saidState) selected() (domain.Message, bool) {
	if s.cursor < 0 || s.cursor >= len(s.msgs) {
		return domain.Message{}, false
	}
	return s.msgs[s.cursor], true
}

// jumpToSaid opens the selected message in its room, as a search hit does.
func (m Model) jumpToSaid() (Model, tea.Cmd) {
	msg, ok := m.said.selected()
	if !ok {
		return m, nil
	}
	room, found := m.roomByID(msg.RoomID)
	if !found {
		return m.say("that room is no longer in your list"), nil
	}
	m.said = saidState{gen: m.said.gen}
	m.jump.to, m.jump.why = msg.ID, "message"
	next, cmd := m.selectRoom(room)
	next.focus = paneTimeline
	return next, cmd
}

// moveSaid moves the cursor, keeping it drawn, and reads further back at the top.
func (m Model) moveSaid(delta int) (Model, tea.Cmd) {
	s := &m.said
	if len(s.msgs) == 0 {
		return m, nil
	}
	s.cursor = clampIndex(s.cursor+delta, len(s.msgs))
	width, rows := m.saidWidth(), m.saidRows()
	switch {
	case s.cursor > s.bottom:
		s.bottom = s.cursor
	case s.cursor < m.saidTop(s.bottom, width, rows):
		// Up past the top: the cursor becomes the top row.
		bottom, used := s.cursor, 0
		for i := s.cursor; i < len(s.msgs); i++ {
			used += len(m.saidMessageRows(i, width))
			if used > rows {
				break
			}
			bottom = i
		}
		s.bottom = bottom
	}
	if s.cursor == 0 && !s.atStart && !s.loading {
		s.loading = true
		return m, m.saidPageCmd(true)
	}
	return m, nil
}

// saidWidth and saidRows are the view's size inside the pane, below its summary line.
func (m Model) saidWidth() int { return max(m.width-railWidth-roomsWidth-paneBorder, 1) }
func (m Model) saidRows() int  { return max(m.height-5, 1) }

// saidTop is the first message wholly drawn when bottom is drawn last.
func (m Model) saidTop(bottom, width, rows int) int {
	used, top := 0, bottom
	for i := bottom; i >= 0; i-- {
		used += len(m.saidMessageRows(i, width))
		if used > rows {
			break
		}
		top = i
	}
	return top
}

// saidMessageRows is one message as drawn here: the room's rule above the first of a run
// and the first of a day, then the message as the timeline draws it.
func (m Model) saidMessageRows(i, width int) []string {
	msgs := m.said.msgs
	msg := msgs[i]
	var rows []string
	if i == 0 || msgs[i-1].RoomID != msg.RoomID || !sameDay(msgs[i-1].Timestamp, msg.Timestamp) {
		rows = append(rows, m.saidRoomRow(msg, width))
	}
	colors := m.saidColors()
	nameW := m.saidNameWidth()
	preview := ""
	if msg.ReplyTo != "" && (i == 0 || msgs[i-1].ID != msg.ReplyTo) {
		tgt, ok := m.saidQuoted(msg.ReplyTo)
		preview = m.quoteRow(msg, tgt, ok, nameW, width, colors)
	}
	return append(rows, m.messageRows(msg, width, nameW, colors, i == m.said.cursor, preview)...)
}

// saidRoomRow names the room a run of messages is in, its network and the day. A room
// no longer listed goes by its ID and the network of the ID that wrote there.
func (m Model) saidRoomRow(msg domain.Message, width int) string {
	label, network := string(msg.RoomID), domain.ProtocolOf(msg.Sender)
	if room, ok := m.roomByID(msg.RoomID); ok {
		label, network = m.roomName(room), m.factsFor(room).Protocol
	}
	return m.ruleRow(isolate(label)+" · "+network.String()+" · "+dayLabel(msg.Timestamp, m.prefs.clock), width, m.theme.Muted)
}

// saidColors paints each of their IDs in its network's color: a person on three networks
// shows which one each message came by.
func (m Model) saidColors() map[string]color.Color {
	out := make(map[string]color.Color, len(m.said.ids))
	for _, id := range m.said.ids {
		out[id] = m.networkColor(domain.ProtocolOf(id))
	}
	return out
}

// saidNameWidth is the widest name they are drawn with, capped as a room's column is.
func (m Model) saidNameWidth() int {
	w := 1
	for i := range m.said.msgs {
		w = max(w, ansi.StringWidth(displayName(m.processedName(m.said.msgs[i]))))
	}
	return min(w, saidNameCap)
}

const saidNameCap = 20

// saidTitle names the pane: who, and how wide.
func (m Model) saidTitle() string {
	title := "Everything " + isolate(m.said.name) + " said"
	switch m.said.narrow {
	case saidInRoom:
		if room, ok := m.roomByID(m.said.room); ok {
			return title + " · in " + isolate(m.roomName(room))
		}
		return title + " · in one room"
	case saidOnNetwork:
		return title + " · on " + m.said.network.String()
	case saidEverywhere:
	}
	return title
}

// saidSummary is the line under the title: how much is here, and whether they are hidden.
func (m Model) saidSummary() string {
	s := m.said
	var line string
	switch n := len(s.msgs); {
	case s.err != nil:
		line = "could not read what they said: " + s.err.Error()
	case n == 0 && s.loading:
		line = "reading what they said…"
	case n == 0:
		line = "nothing they said is cached"
	case s.atStart:
		line = fmt.Sprintf("%d messages — from the oldest kith has cached", n)
	default:
		line = fmt.Sprintf("%d messages so far — older ones load as you go up", n)
	}
	if m.markedPeople().Has(s.id) {
		line = "hidden in every room, shown here because you asked · " + line
	}
	return line
}

// saidLines draws the view: the summary, then the messages ending at bottom, the oldest
// cached marked as where the cache starts.
func (m Model) saidLines(width, rows int) []string {
	lines := []string{m.theme.Muted.Render(m.saidSummary())}
	if len(m.said.msgs) == 0 {
		return lines
	}
	rows = max(rows-1, 1)
	var drawn []string
	for i := m.said.bottom; i >= 0 && len(drawn) < rows; i-- {
		drawn = append(m.saidMessageRows(i, width), drawn...)
		if i == 0 && m.said.atStart && len(drawn) < rows {
			drawn = append([]string{m.ruleRow("the start of what kith has cached", width, m.theme.Faint)}, drawn...)
		}
	}
	if len(drawn) > rows {
		drawn = drawn[len(drawn)-rows:]
	}
	return append(lines, drawn...)
}

// handleSaidKey owns every key while the view is open, through scopeSearch and scopeNav
// so it moves and acts as a results list does; press is the whole sequence ("g g").
func (m Model) handleSaidKey(press string) (Model, tea.Cmd) {
	act := m.keys.lookup(press, scopeSearch, scopeNav, scopeCommand)
	switch act {
	case actJump:
		return m.jumpToSaid()
	case actSearchScope:
		return m.narrowSaid()
	case actClose, actBack:
		return m.closeSaid()
	case actHelp:
		m.reader = m.reader.opening(readerHelp)
		return m, nil
	}
	if delta, ok := navDelta(act, m.take(), m.saidRows(), len(m.said.msgs)); ok {
		return m.moveSaid(delta)
	}
	return m, nil
}

// saidOf is `:from`: the person named, by an ID or a name, else the person in view (the
// selected message's sender, else the open room's one person).
func (m Model) saidOf(arg string) (Model, tea.Cmd) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		if _, ok := m.selectedMessage(); ok && m.focus == paneTimeline {
			return m.saidOfSender()
		}
		if peer, many := m.onePerson(m.timeline.members); !many && peer.UserID != "" {
			return m.openSaid(peer.UserID, stripIsolates(m.personName(peer.UserID)))
		}
		return m.say(":from <person> — or select a message, and it is its sender"), nil
	}
	if strings.Contains(arg, ":") {
		return m.openSaid(arg, stripIsolates(m.personName(arg)))
	}
	if rows := m.matchPeople(arg, m.fromPeople()); len(rows) > 0 {
		return m.openSaid(rows[0].userID, rows[0].text)
	}
	return m.say("no one called " + isolate(arg) + " has written where kith can see"), nil
}

// fromPeople is who `:from` completes and resolves against: everyone who has written,
// most active first, once read, then the open room's members.
func (m Model) fromPeople() []domain.Member {
	seen := map[string]bool{}
	var out []domain.Member
	for _, list := range [][]domain.Member{m.saidPeople, m.timeline.members} {
		for _, p := range list {
			if !seen[p.UserID] {
				seen[p.UserID] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// saidPeopleMsg is everyone who has written anywhere, for `:from`.
type saidPeopleMsg struct {
	people []domain.Member
	err    error
}

func (m Model) saidPeopleCmd() tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		people, err := backend.SearchSenders(ctx, domain.EveryRoom(), searchSenderLimit)
		return saidPeopleMsg{people: people, err: err}
	}
}

// handleSaidPeople keeps the list and refreshes an open `:from` popup.
func (m Model) handleSaidPeople(msg saidPeopleMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelDebug, "load people for :from", msg.err)
	if msg.err != nil {
		return m, nil
	}
	m.saidPeople = msg.people
	if m.completion.active && m.completion.trigger == fromTrigger {
		m.completion.scanning = false
		return m.refreshCandidates(), nil
	}
	return m, nil
}

// fromTrigger opens the person popup on the command line: `:from ` and a name.
const fromTrigger = "from "

// saidCandidates are the people `:from` offers; accepting writes the command whole, by
// ID, and runs it.
func (m Model) saidCandidates(query string) []candidate {
	rows := m.matchPeople(query, m.fromPeople())
	out := make([]candidate, 0, len(rows))
	for _, row := range rows {
		if row.userID != "" {
			out = append(out, candidate{text: fromTrigger + row.userID, label: row.label, detail: row.detail})
		}
	}
	return out
}

func (m Model) saidHint() string {
	return m.hintLine(
		keyed(m.keys.keyHint(scopeNav, actUp)+"/"+m.keys.keyHint(scopeNav, actDown), "messages"),
		keyed(m.keys.keyHint(scopeSearch, actJump), "go to message"),
		keyed(m.keys.keyHint(scopeSearch, actSearchScope), "this room, this network, everywhere"),
		keyed(m.keys.keyHint(scopeSearch, actClose), "close"),
	)
}
