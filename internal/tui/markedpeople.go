package tui

import (
	"maps"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A person put in a muted tag (notifies = false, as Spam) by a tag key in the timeline
// is hidden in every room: their messages and the direct replies to them. They are on the tag's people
// list in the config, as a room filed by hand is on its picked list. Hidden, not
// deleted: a key shows the open room's until you leave it, and taking the mark off
// brings everything back.

// markedPeople is who the muted tags hide, listed or put there by a filter, matched
// across every ID kith links to them.
func (m Model) markedPeople() domain.MarkedPeople {
	marked := slices.Concat(m.rail.tags.MutedPeople(), domain.HiddenByVerdict(m.rail.people, m.rail.tags.MutedTags()))
	return domain.NewMarkedPeople(marked, m.identityIDs(), m.linkedIDs())
}

// linkedIDs answers which identifiers are directly the same person as one: the
// directory's (accounts, bridges, numbers) and the [[display.identity]] group's.
func (m Model) linkedIDs() func(id string) []string {
	groups := make(map[string][]string, len(m.prefs.identities))
	for id, ident := range m.prefs.identities {
		if ident.key != "" {
			groups[ident.key] = append(groups[ident.key], id)
		}
	}
	dir := m.dir.dir
	return func(id string) []string {
		out := dir.Identifiers(id)
		if ident, ok := m.prefs.identities[id]; ok && ident.key != "" {
			out = append(append([]string(nil), out...), groups[ident.key]...)
		}
		return out
	}
}

// identityIDs is every ID a [[display.identity]] names.
func (m Model) identityIDs() []string {
	return slices.Collect(maps.Keys(m.prefs.identities))
}

// markedShownHere reports whether the open room shows its hidden people's messages.
func (m Model) markedShownHere() bool {
	return m.openRoom != "" && m.markedShownIn == m.openRoom
}

// withMarkedPeopleHidden drops the messages of people a muted tag holds, and direct
// replies to them, unless this room shows them. Your own messages are never hidden. It
// filters `shown`, so all pane indices agree.
func (m Model) withMarkedPeopleHidden(msgs []domain.Message) []domain.Message {
	if m.markedShownHere() {
		return msgs
	}
	marked := m.markedPeople()
	if marked.Empty() {
		return msgs
	}
	known := map[string]bool{}
	isMarked := func(sender string) bool {
		hidden, ok := known[sender]
		if !ok {
			hidden = marked.Has(sender) && !m.fromMe(sender)
			known[sender] = hidden
		}
		return hidden
	}
	senders := make(map[domain.EventID]string, len(m.timeline.messages))
	for i := range m.timeline.messages {
		senders[m.timeline.messages[i].ID] = m.timeline.messages[i].Sender
	}
	out := make([]domain.Message, 0, len(msgs))
	for i := range msgs {
		if isMarked(msgs[i].Sender) || (msgs[i].ReplyTo != "" && isMarked(senders[msgs[i].ReplyTo])) {
			continue
		}
		out = append(out, msgs[i])
	}
	return out
}

// toggleSenderIn is a tag key in the timeline: the selected message's sender goes into
// the tag, or, when the tag already holds them by any of their IDs, every entry that is
// them comes out, listed or put there by a filter; a filter's mark is released, so no
// filter puts them back. A muted tag hides whom it holds.
func (m Model) toggleSenderIn(tag string) (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	if !ok {
		return m.say("no message here — the room list's " + tag + " key files the room"), nil
	}
	if m.fromMe(msg.Sender) {
		return m.say("that is you — you cannot put yourself in a tag"), nil
	}
	cfg := m.conf.base.Clone()
	j := slices.IndexFunc(cfg.Tags, func(t config.Tag) bool { return domain.SameTag(t.Name, tag) })
	if j < 0 {
		return m, nil
	}
	name, label := m.personName(msg.Sender), isolate(cfg.Tags[j].Name)
	known, same := m.identityIDs(), m.linkedIDs()
	isThem := func(entry string) bool { return domain.NewMarkedPeople([]string{entry}, known, same).Has(msg.Sender) }
	kept := cfg.Tags[j].People[:0:0]
	for _, entry := range cfg.Tags[j].People {
		if !isThem(entry) {
			kept = append(kept, entry)
		}
	}
	marks := slices.DeleteFunc(slices.Clone(m.rail.people), func(v domain.PersonVerdict) bool {
		return !v.In() || !domain.SameTag(v.Tag, tag) || !isThem(v.Person)
	})
	if len(kept) < len(cfg.Tags[j].People) || len(marks) > 0 {
		done := name + " is out of " + label
		released, release := m.releasePeople(marks)
		if len(kept) == len(cfg.Tags[j].People) {
			return released.say(done), release
		}
		cfg.Tags[j].People = kept
		next, cmd := released.applyKeepingSelection(cfg, done)
		return next, tea.Batch(cmd, release)
	}
	cfg.Tags[j].People = append(cfg.Tags[j].People, msg.Sender)
	done := name + " is in " + label
	if !cfg.Tags[j].Notifying() {
		done += ", hidden in every room"
		if key := m.keys.keyHint(scopeTimeline, actShowMarked); key != "" {
			done += " — " + key + " shows them here"
		}
	}
	return m.applyKeepingSelection(cfg, done)
}

// releasePeople takes filters' marks out at once here and writes their releases, so
// no filter on any machine puts the person back.
func (m Model) releasePeople(marks []domain.PersonVerdict) (Model, tea.Cmd) {
	if len(marks) == 0 {
		return m, nil
	}
	before := m.shownMessages()
	people := slices.Clone(m.rail.people)
	cmds := make([]tea.Cmd, 0, len(marks))
	for _, mark := range marks {
		release := domain.PersonVerdict{Person: mark.Person, Tag: mark.Tag, Released: true, At: time.Now()}
		for i := range people {
			if people[i].Person == mark.Person && domain.SameTag(people[i].Tag, mark.Tag) {
				people[i] = release
			}
		}
		cmds = append(cmds, m.releasePersonCmd(release))
	}
	m.rail.people = people
	m.rail.peopleRev++
	return m.keptSelection(before), tea.Batch(cmds...)
}

func (m Model) releasePersonCmd(release domain.PersonVerdict) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		return verdictsWrittenMsg{
			failed: "out of " + release.Tag + " here, but your other machines were not told",
			err:    backend.MarkPerson(ctx, release),
		}
	}
}

// toggleMarkedShown shows the open room's hidden people's messages, or hides them again.
func (m Model) toggleMarkedShown() (Model, tea.Cmd) {
	if m.openRoom == "" {
		return m, nil
	}
	if m.markedShownHere() {
		before := m.shownMessages()
		m.markedShownIn = ""
		return m.keptSelection(before).say("hidden people are hidden again"), nil
	}
	m.markedShownIn = m.openRoom
	return m.say("showing hidden people here until you leave"), nil
}

// applyKeepingSelection applies cfg, moving the message cursor off a message it hides.
func (m Model) applyKeepingSelection(cfg config.Config, done string) (Model, tea.Cmd) {
	before := m.shownMessages()
	next, cmd := m.applyConfig(cfg, done)
	return next.keptSelection(before), cmd
}

// keptSelection moves the selection, if the shown messages no longer hold it, to the
// nearest one of before that they still do: newer first, then older.
func (m Model) keptSelection(before []domain.Message) Model {
	shown := m.shownMessages()
	drawn := func(id domain.EventID) bool {
		return slices.ContainsFunc(shown, func(msg domain.Message) bool { return msg.ID == id })
	}
	if m.timeline.selected == "" || drawn(m.timeline.selected) {
		return m
	}
	at := slices.IndexFunc(before, func(msg domain.Message) bool { return msg.ID == m.timeline.selected })
	for _, step := range []int{1, -1} {
		for i := at + step; at >= 0 && i >= 0 && i < len(before); i += step {
			if drawn(before[i].ID) {
				m.timeline.selected = before[i].ID
				return m.scrollToSelection()
			}
		}
	}
	m.timeline.selected = ""
	return m
}
