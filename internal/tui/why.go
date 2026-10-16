package tui

import (
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// The "why is it quiet?" overlay: the rule chain resolved for the open room (no
// sender), least specific first. Sender-dependent rules are counted, not guessed at.

func (m Model) openWhy() (Model, tea.Cmd) {
	m.reader = m.reader.opening(readerWhy)
	return m, nil
}

func (m Model) handleWhyKey(tea.KeyPressMsg) (Model, tea.Cmd) {
	m.reader = m.reader.closed()
	return m, nil
}

func (m Model) whyView() string {
	var b strings.Builder
	b.WriteString(m.theme.TitleActive.Render("Why is it quiet?") + "\n\n")
	// whyLines stays logical for the tests; each line is drawn here.
	lines := m.whyLines(time.Now())
	for i := range lines {
		lines[i] = drawSentence(lines[i])
	}
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n\n" + m.theme.Muted.Render("any key closes"))
	return m.centeredBox(b.String())
}

func (m Model) whyLines(now time.Time) []string {
	room, ok := m.currentRoom()
	if !ok {
		return []string{m.theme.Muted.Render("no room open — an explanation needs something to explain")}
	}
	scope := notify.Scope{Room: setup.Place{Room: m.factsFor(room)}}
	// With a thread open, the question is about the thread.
	if m.thread.open() {
		scope.Thread = string(m.thread.root)
		scope.Participating = m.spokeInThread(m.thread.root)
	}
	rules := notify.Rules(m.notifications.rules, m.notifications.temps)
	resolved := notify.Resolve(rules, scope, now)

	// A tag that does not notify is not a rule: a room it holds never notifies, so that is
	// answered first, with the chain shown as what applies once the room leaves it. A
	// hidden person's message under the cursor comes before both.
	moves := append(m.hiddenReasons(), m.moveReasons(room)...)
	if muting := m.mutingTags(room); len(muting) > 0 {
		head := []string{m.theme.Muted.Render(drawSentence("for " + m.roomName(room) + ", right now:")), ""}
		head = append(head, moves...)
		head = append(head,
			"it is in "+joinedTags(muting)+", which does not notify (notifies = false)",
			m.theme.Muted.Render("taking it out of that tag lets it notify again"),
			"",
			m.theme.Muted.Render("the rules below would otherwise apply:"), "")
		return append(head, m.whyChain(resolved, scope, now)...)
	}

	// The master switch comes before any rule; the chain is still shown.
	var lines []string
	if len(moves) > 0 {
		lines = append(append(lines, moves...), "")
	}
	if !m.notifications.on {
		lines = append(lines,
			"notifications are off — nothing is delivered, whatever the rules say",
			m.theme.Muted.Render("turn them on in settings (notifications · enabled)"),
			"",
			m.theme.Muted.Render(drawSentence("for "+m.roomName(room)+", they would be:")), "")
	} else {
		lines = append(lines, m.theme.Muted.Render(drawSentence("for "+m.roomName(room)+", right now:")), "")
	}
	return append(lines, m.whyChain(resolved, scope, now)...)
}

// whyChain is the applied rules, the result, and the count of sender-dependent rules.
func (m Model) whyChain(resolved notify.Resolved, scope notify.Scope, now time.Time) []string {
	var lines []string
	if len(resolved.Applied) == 0 {
		lines = append(lines, "nothing applies here, so nothing notifies")
	}
	for i := range resolved.Applied {
		lines = append(lines, "  "+whyLine(resolved.Applied[i], resolved, now))
	}

	lines = append(lines, "", m.theme.Muted.Render("result: ")+whyResult(resolved))
	if pending := m.senderRules(scope, now); pending > 0 {
		lines = append(lines, "", m.theme.Muted.Render(senderNote(pending)))
	}
	return lines
}

// whyLine is one rule in the chain: its name, when it is active, what it says, and
// whether it had the last word.
func whyLine(rule notify.Rule, resolved notify.Resolved, now time.Time) string {
	parts := []string{ruleName(rule)}
	if rule.Temp {
		parts = append(parts, "muted"+remainingNote(rule, now))
	} else if rule.When != nil {
		parts = append(parts, rule.When.String())
	}
	if rule.Show != nil {
		parts = append(parts, "show = "+rule.Show.String())
	}
	if rule.Ring != nil {
		parts = append(parts, "ring = "+rule.Ring.String())
	}
	if rule.Sound != "" {
		parts = append(parts, "sound "+rule.Sound)
	}
	if rule.Thread != notify.ThreadAny {
		parts = append(parts, "thread = "+rule.Thread.String())
	}
	line := strings.Join(parts, " · ")
	if sameRule(rule, resolved.ShowBy) {
		line += "   ← decides"
	}
	return line
}

// sameRule reports whether two rules are the same statement (rules have no ID).
func sameRule(a, b notify.Rule) bool {
	return a.Name == b.Name && a.Match == b.Match && a.Sender == b.Sender &&
		a.Thread == b.Thread && a.Temp == b.Temp
}

// spokeInThread reports whether we sent the thread's root or any reply, which is what
// `thread = "participating"` asks. The daemon answers the same from its cache.
func (m Model) spokeInThread(root domain.EventID) bool {
	if root == "" {
		return false
	}
	for i := range m.timeline.messages {
		if !m.isMe(m.timeline.messages[i].Sender) {
			continue
		}
		if m.timeline.messages[i].ThreadRoot == root || m.timeline.messages[i].ID == root {
			return true
		}
	}
	return false
}

// whyResult states the outcome in the terms the rest of the UI uses.
func whyResult(r notify.Resolved) string {
	switch {
	case r.Show == notify.LevelNone:
		return "nothing notifies here"
	case r.Ring == notify.LevelNone:
		return r.Show.String() + " notifies, silently"
	default:
		return r.Show.String() + " notifies, with sound"
	}
}

// senderRules counts the rules that apply here only for a particular sender, which
// cannot be resolved without a message from them.
func (m Model) senderRules(scope notify.Scope, now time.Time) int {
	count := 0
	rules := notify.Rules(m.notifications.rules, m.notifications.temps)
	for i := range rules {
		rule := &rules[i]
		if rule.Sender == "" || !rule.Live(now) {
			continue
		}
		// Ask the engine with the sender filled in, so the count agrees with resolution.
		probe := scope
		probe.Sender = rule.Sender
		if len(notify.Resolve(rules[i:i+1], probe, now).Applied) > 0 {
			count++
		}
	}
	return count
}

func senderNote(n int) string {
	if n == 1 {
		return "1 more rule here depends on who is writing"
	}
	return strconv.Itoa(n) + " more rules here depend on who is writing"
}
