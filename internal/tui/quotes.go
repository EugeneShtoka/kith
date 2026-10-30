package tui

import (
	"log/slog"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Reply targets not among the loaded messages are fetched by ID (FetchEvent). Unlike thread
// roots they are kept aside, not merged into the timeline, where an old one would land in
// the middle of today.

// maxQuoteFetches bounds the fetches issued per pass.
const maxQuoteFetches = 6

type quoteState struct {
	known map[domain.EventID]domain.Message
	// asked holds IDs already requested, answered or not, so an unobtainable target is not
	// re-requested every frame.
	asked map[domain.EventID]bool
	// rev moves with every change to known, keying the rows that draw a quote (rowInputs).
	rev uint64
}

// quotedTarget is the message a reply answers, if known: a loaded message (shown or folded
// into a thread, read in place so edits show), else a fetched one.
func (m Model) quotedTarget(derived *derivedCache, id domain.EventID) (domain.Message, bool) {
	if msg, ok := derived.at(id); ok {
		return msg, true
	}
	if i, ok := derived.loaded[id]; ok && i < len(m.timeline.messages) {
		return m.timeline.messages[i], true
	}
	msg, ok := m.quotes.known[id]
	return msg, ok
}

// loadQuotes asks for the unknown reply targets of the drawn rows.
func (m Model) loadQuotes() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok {
		return m, nil
	}
	derived := m.derivedFor()
	shown := m.shownMessages()
	var wanted []domain.EventID
	for i := range shown {
		target := shown[i].ReplyTo
		if target == "" || m.quotes.asked[target] {
			continue
		}
		if _, known := m.quotedTarget(derived, target); known {
			continue
		}
		wanted = append(wanted, target)
	}
	if len(wanted) == 0 {
		return m, nil
	}
	// quotedTarget already found every loaded target; what is left is fetched.
	var cmds []tea.Cmd
	for _, target := range wanted {
		if len(cmds) >= maxQuoteFetches {
			break
		}
		m = m.markQuoteAsked(target)
		cmds = append(cmds, m.fetchQuoteCmd(room.ID, target))
	}
	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

// markQuoteAsked records a request, copy-on-write since earlier Model copies must not see it.
func (m Model) markQuoteAsked(id domain.EventID) Model {
	m.quotes.asked = withEntry(m.quotes.asked, id, true)
	return m
}

// rememberQuote files a reply target, fetched or from a frame, copy-on-write.
func (m Model) rememberQuote(msg domain.Message) Model {
	m.quotes.known = withEntry(m.quotes.known, msg.ID, msg)
	m.quotes.rev++
	return m
}

// handleFetchedQuote files a fetched reply target. Failure is silent; the row keeps its
// placeholder.
func (m Model) handleFetchedQuote(msg fetchedQuoteMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelDebug, "fetch quoted message", msg.err)
	if msg.err != nil || msg.message.ID == "" {
		return m, nil
	}
	if _, framed := m.quotes.known[msg.message.ID]; framed {
		return m, nil // a frame since the ask brought it as it is now; the fetch may have read it before
	}
	return m.rememberQuote(msg.message), nil
}

// followQuote updates a quote from a frame for its message, loaded or not, and also one
// whose fetch is still in flight (handleFetchedQuote then keeps it).
func (m Model) followQuote(frame domain.Message) Model {
	if !m.quotes.asked[frame.ID] {
		return m
	}
	return m.rememberQuote(frame)
}

// goToReplied moves the cursor to the message the selected one answers: revealed if
// loaded (opening its thread), else an armed jump that pages history (resolveJump).
func (m Model) goToReplied() (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	if !ok {
		return m, nil
	}
	if msg.ReplyTo == "" {
		return m.say("this message is not a reply"), nil
	}
	if indexOfMessage(m.timeline.messages, msg.ReplyTo) >= 0 {
		next, cmd := m.revealMessage(msg.ReplyTo)
		return next.clearStatus(), cmd
	}
	if m.timeline.hist.atStart {
		return m.say("the message it answers is not in this room's history"), nil
	}
	// Armed, not fetched: a fetched quote stays out of the timeline, so page back.
	m.jump.to, m.jump.why = msg.ReplyTo, "message it answers"
	m = m.say("looking back for the message it answers…")
	return m.loadOlder()
}
