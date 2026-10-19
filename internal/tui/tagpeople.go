package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A tag's people. On a tag, the room list switches between the tag's rooms and the people
// it holds — put there by a tag key on their message, or by a filter — one row per person
// across every ID kith links to them. A muted tag hides its people in every room
// (markedpeople.go), so this is where a hidden person is found again, taken out, or read.

type tagPeopleState struct {
	// tag is the rail key whose people the room list shows; on any other group it shows rooms.
	tag    string
	cursor int
	// counts is how many messages each row's person wrote, by tagPerson.id.
	counts map[string]int
}

// tagPerson is one person a tag holds.
type tagPerson struct {
	// id is the ID they were found by; the card, the view and the key take it.
	id string
	// ids is every ID they go by, less yours: the ones the tag hides.
	ids   []string
	byYou bool
	// marks are the filters' marks of them into the tag.
	marks []domain.PersonVerdict
}

// showingTagPeople reports whether the room list shows the people of the tag it is on.
func (m Model) showingTagPeople() bool {
	return m.tagPeople.tag != "" && m.tagPeople.tag == m.rail.key()
}

// tagPeopleRows is the people tag holds, by name: listed in its people, or marked into
// it by a filter and not released. Linked IDs are one row.
func (m Model) tagPeopleRows(tag string) []tagPerson {
	var rows []tagPerson
	add := func(entry string, byYou bool, mark *domain.PersonVerdict) {
		ids := slices.DeleteFunc(m.personIDs(entry), m.fromMe)
		if len(ids) == 0 {
			return
		}
		at := slices.IndexFunc(rows, func(r tagPerson) bool {
			return slices.ContainsFunc(r.ids, func(id string) bool { return slices.Contains(ids, id) })
		})
		if at < 0 {
			id := entry
			if m.fromMe(id) {
				id = ids[0]
			}
			rows = append(rows, tagPerson{id: id, ids: ids})
			at = len(rows) - 1
		}
		rows[at].byYou = rows[at].byYou || byYou
		if mark != nil {
			rows[at].marks = append(rows[at].marks, *mark)
		}
	}
	if j := m.configTag(tag); j >= 0 {
		for _, entry := range m.conf.base.Tags[j].People {
			add(entry, true, nil)
		}
	}
	for i := range m.rail.people {
		if v := m.rail.people[i]; v.In() && domain.SameTag(v.Tag, tag) {
			add(v.Person, false, &v)
		}
	}
	slices.SortStableFunc(rows, func(a, b tagPerson) int {
		return strings.Compare(strings.ToLower(m.tagPersonName(a)), strings.ToLower(m.tagPersonName(b)))
	})
	return rows
}

func (m Model) tagPersonName(p tagPerson) string { return stripIsolates(m.personName(p.id)) }

// shownTagPeople is the people of the tag the room list is on.
func (m Model) shownTagPeople() []tagPerson {
	name, ok := domain.TagOf(m.tagPeople.tag)
	if !ok {
		return nil
	}
	return m.tagPeopleRows(name)
}

// selectedTagPerson is the person under the cursor.
func (m Model) selectedTagPerson() (tagPerson, bool) {
	rows := m.shownTagPeople()
	if len(rows) == 0 {
		return tagPerson{}, false
	}
	return rows[clampIndex(m.tagPeople.cursor, len(rows))], true
}

// toggleTagPeople switches the room list on a tag between its rooms and its people.
func (m Model) toggleTagPeople() (Model, tea.Cmd) {
	if m.showingTagPeople() {
		m.tagPeople = tagPeopleState{}
		return m, nil
	}
	key := m.rail.key()
	if !isTagGroup(key) {
		return m.say("only a tag holds people — this is " + isolate(m.rail.label())), nil
	}
	m.tagPeople = tagPeopleState{tag: key}
	return m, m.tagPeopleCountsCmd()
}

// tagPeopleCountsMsg is how many messages each person of a tag wrote.
type tagPeopleCountsMsg struct {
	tag    string
	counts map[string]int
	err    error
}

func (m Model) tagPeopleCountsCmd() tea.Cmd {
	rows := m.shownTagPeople()
	if len(rows) == 0 {
		return nil
	}
	ctx, backend, tag := m.ctx, m.backend, m.tagPeople.tag
	people := make([][]string, len(rows))
	for i := range rows {
		people[i] = rows[i].ids
	}
	return func() tea.Msg {
		counts, err := backend.MessageCounts(ctx, people)
		out := make(map[string]int, len(rows))
		for i := range rows {
			if i < len(counts) {
				out[rows[i].id] = counts[i]
			}
		}
		return tagPeopleCountsMsg{tag: tag, counts: out, err: err}
	}
}

func (m Model) handleTagPeopleCounts(msg tagPeopleCountsMsg) (Model, tea.Cmd) {
	if msg.err != nil || msg.tag != m.tagPeople.tag {
		return m.sayFailure("could not count their messages: ", msg.err), nil
	}
	m.tagPeople.counts = msg.counts
	return m, nil
}

// handleTagPeopleKey drives the room list while it shows a tag's people.
func (m Model) handleTagPeopleKey(press string) (Model, tea.Cmd) {
	act := m.keys.lookup(press, m.withPlayerScope(scopeRooms, scopeNav, scopeCommand)...)
	if mdl, cmd, handled := m.commandAction(act); handled {
		return mdl, cmd
	}
	rows := m.shownTagPeople()
	switch act {
	case actTagPeople:
		return m.toggleTagPeople()
	case actBack:
		m.focus = paneRail
		return m, nil
	case actOpen, actPerson, actSaid:
		row, ok := m.selectedTagPerson()
		if !ok {
			return m, nil
		}
		if act == actSaid {
			return m.openSaid(row.id, m.tagPersonName(row))
		}
		return m.openPerson(row.id, m.tagPersonName(row))
	}
	if delta, ok := navDelta(act, m.take(), max(paneBodyRows(m.height-1)/tagPersonRows, 1), len(rows)); ok {
		m.tagPeople.cursor = clampIndex(m.tagPeople.cursor+delta, len(rows))
	}
	return m, nil
}

// takeTagPersonOut is the shown tag's key on a person: every entry and mark of theirs
// comes out of it.
func (m Model) takeTagPersonOut(tag string) (Model, tea.Cmd) {
	shown, _ := domain.TagOf(m.tagPeople.tag)
	if !domain.SameTag(tag, shown) {
		return m.say("here " + isolate(m.tagLabel(shown)) + "'s key takes a person out; " + isolate(m.tagLabel(tag)) + "'s files rooms"), nil
	}
	row, ok := m.selectedTagPerson()
	if !ok {
		return m, nil
	}
	next, cmd, out := m.takePersonOut(shown, row.id)
	if !out {
		return m, nil
	}
	next.tagPeople.cursor = clampIndex(next.tagPeople.cursor, len(next.shownTagPeople()))
	return next, cmd
}

// tagPersonRows is how many rows a person takes: their name, then how they got there.
const tagPersonRows = 2

// tagPeopleTitle is the room list's title over a tag's people.
func (m Model) tagPeopleTitle(n int) string {
	label := isolate(m.rail.label())
	if n == 1 {
		return label + " · 1 person"
	}
	return fmt.Sprintf("%s · %d people", label, n)
}

// tagPeopleLines draws the people, two rows each, windowed on the cursor.
func (m Model) tagPeopleLines(rows []tagPerson, inner, body int, active bool) []string {
	if len(rows) == 0 {
		return []string{m.theme.Muted.Render("(nobody — a tag key on a message puts its sender here)")}
	}
	cursor := clampIndex(m.tagPeople.cursor, len(rows))
	fit := max(body/tagPersonRows, 1)
	first := windowStart(cursor, fit, len(rows))
	lines := make([]string, 0, body)
	for i := first; i < min(first+fit, len(rows)); i++ {
		row := rows[i]
		badge := ""
		if n, ok := m.tagPeople.counts[row.id]; ok {
			badge = fmt.Sprint(n)
		}
		label := rowLabel{name: isolate(m.tagPersonName(row)), color: m.networkColor(domain.ProtocolOf(row.id))}
		lines = append(lines,
			m.listRow(label, badge, false, inner, i == cursor, active),
			m.theme.Muted.Render(clamp("  "+m.tagPersonDetail(row), inner)))
	}
	return lines
}

// tagPersonDetail is how a person got into the tag, and on which networks: "WhatsApp,
// Matrix · you, filter crypto · 12 Oct".
func (m Model) tagPersonDetail(p tagPerson) string {
	var networks []string
	for _, id := range p.ids {
		if strings.HasPrefix(id, domain.PhoneID("")) {
			continue
		}
		if n := domain.ProtocolOf(id).String(); n != "" && !slices.Contains(networks, n) {
			networks = append(networks, n)
		}
	}
	var who []string
	if p.byYou {
		who = append(who, "you")
	}
	var last time.Time
	for _, v := range p.marks {
		name := "a filter"
		if v.Filter != "" {
			name = "filter " + v.Filter
		}
		if !slices.Contains(who, name) {
			who = append(who, name)
		}
		if v.At.After(last) {
			last = v.At
		}
	}
	parts := []string{strings.Join(networks, ", "), strings.Join(who, ", ")}
	if !last.IsZero() {
		parts = append(parts, m.prefs.clock.ShortDate(last))
	}
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), " · ")
}

func (m Model) tagPeopleHint() string {
	return m.hintLine(
		keyed(m.keys.keyHint(scopeNav, actUp)+"/"+m.keys.keyHint(scopeNav, actDown), "people"),
		keyed(m.keys.keyHint(scopeNav, actOpen), "card"),
		keyed(m.keys.keyHint(scopeRooms, actSaid), "what they said"),
		note("the tag's key takes them out"),
		keyed(m.keys.keyHint(scopeRooms, actTagPeople), "rooms"),
	)
}
