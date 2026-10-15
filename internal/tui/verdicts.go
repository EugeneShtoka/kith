package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// What the daemon's filters moved rooms into. A tag holds the rooms a filter moved there
// (domain.RoomFacts.Caught) beside its rule and picked rooms; taking one out writes the
// tag's excluded, and a release so no filter on any machine moves it back.

type verdictsLoadedMsg struct{ verdicts []domain.TagVerdict }

// loadVerdictsCmd reads the moves and releases, at startup and on the poll. A read
// failure moves nothing.
func (m Model) loadVerdictsCmd() tea.Cmd {
	ctx, backend, log := m.ctx, m.backend, m.log
	return func() tea.Msg {
		verdicts, err := backend.TagVerdicts(ctx)
		if err != nil {
			log.Warn("load tag verdicts failed", "err", err)
			return nil
		}
		return verdictsLoadedMsg{verdicts: verdicts}
	}
}

// handleVerdictsLoaded replaces the verdicts wholesale (a release from another machine
// arrives as one) and rebuilds the rail.
func (m Model) handleVerdictsLoaded(msg verdictsLoadedMsg) (Model, tea.Cmd) {
	verdicts := make(map[domain.RoomID][]domain.TagVerdict, len(msg.verdicts))
	caught := map[domain.RoomID][]string{}
	for _, v := range msg.verdicts {
		verdicts[v.Room] = append(verdicts[v.Room], v)
		if v.In() {
			caught[v.Room] = append(caught[v.Room], v.Tag)
		}
	}
	m.rail.verdicts, m.rail.caught = verdicts, caught
	return m.refreshPlaces().rebuiltRail(), nil
}

// moveOf is the filter's move of room into tag, if one holds it there.
func (m Model) moveOf(room domain.RoomID, tag string) (domain.TagVerdict, bool) {
	for _, v := range m.rail.verdicts[room] {
		if v.In() && domain.SameTag(v.Tag, tag) {
			return v, true
		}
	}
	return domain.TagVerdict{}, false
}

// tagKey acts on a sequence bound to a tag: in the timeline the selected message's
// sender goes in, or out (markedpeople.go); elsewhere the room under the cursor (or the
// open one). Never while typing.
func (m Model) tagKey(press string) (Model, tea.Cmd, bool) {
	if m.typing() {
		return m, nil, false
	}
	tag, ok := m.keys.tagFor(press)
	if !ok {
		return m, nil, false
	}
	view := m.unreadView()
	i, ok := view.tags.Index(tag)
	if !ok {
		return m.say("no tag is called " + quoted(tag) + " — see [keys.tag]"), nil, true
	}
	if m.focus == paneTimeline {
		next, cmd := m.toggleSenderIn(view.tags.At(i).Name)
		return next, cmd, true
	}
	room, ok := m.currentRoom()
	if !ok || room.IsInvite() {
		return m, nil, true
	}
	next, cmd := m.fileTags(room, []tagFiling{{tag: i, label: view.tags.At(i).Name, in: !view.tagsOf(room).in[i]}})
	return next, cmd, true
}

// releaseCmds release room from each tag it leaves that a filter moved it into, so no
// filter moves it back; the excluded entry holds it out here meanwhile.
func (m Model) releaseCmds(room domain.RoomID, outOf []string) tea.Cmd {
	var cmds []tea.Cmd
	for _, tag := range outOf {
		if _, moved := m.moveOf(room, tag); moved {
			cmds = append(cmds, m.releaseCmd(room, tag))
		}
	}
	return tea.Batch(cmds...)
}

// verdictsWrittenMsg is a release or a retag written; failed says what a failure costs
// (the config already holds the change here).
type verdictsWrittenMsg struct {
	failed string
	err    error
}

func (m Model) releaseCmd(room domain.RoomID, tag string) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		release := domain.TagVerdict{Room: room, Tag: tag, Released: true, At: time.Now()}
		return verdictsWrittenMsg{failed: "out of " + tag + " here, but your other machines were not told", err: backend.MarkTag(ctx, release)}
	}
}

func (m Model) handleVerdictsWritten(msg verdictsWrittenMsg) (Model, tea.Cmd) {
	if msg.err == nil {
		return m, m.loadVerdictsCmd()
	}
	return m.sayErr(msg.failed, msg.err), m.loadVerdictsCmd()
}

// moveReasons say which filters moved room into which tags, for `/why`.
func (m Model) moveReasons(room domain.Room) []string {
	var out []string
	for _, v := range m.rail.verdicts[room.ID] {
		if v.In() {
			out = append(out, "in "+isolate(m.tagLabel(v.Tag))+" — "+v.Rule.Reason(v.Filter))
		}
	}
	return out
}

// tagLabel is a tag's configured spelling, for a name verdicts keep lower-cased.
func (m Model) tagLabel(name string) string {
	if i, ok := m.rail.tags.Index(name); ok {
		return m.rail.tags.At(i).Name
	}
	return name
}

// mutingTags are the tags holding room that do not notify.
func (m Model) mutingTags(room domain.Room) []string {
	view := m.unreadView()
	held := view.tagsOf(room).in
	var out []string
	for i := range view.tags.Len() {
		if i < len(held) && held[i] && view.tags.At(i).Mute {
			out = append(out, view.tags.At(i).Name)
		}
	}
	return out
}

// openCaught previews what the filters catch in the local index, with the usual scope
// ladder (`/caught` = this room, tab widens, `:caught` = everywhere).
func (m Model) openCaught() (Model, tea.Cmd) {
	filters, err := setup.Filters(m.conf.base)
	if err != nil {
		return m.sayErr("the filters do not parse", err), nil
	}
	kind := caughtList{filters: map[string]string{}}
	for i := range filters {
		filter := &filters[i]
		if len(filter.Words.Words) == 0 {
			// Filters with no words (a sender or a language alone) cannot be previewed;
			// counted so the summary says so.
			kind.wordless++
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

// joinedTags is a list of tag names in words.
func joinedTags(names []string) string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, isolate(n))
	}
	return strings.Join(out, ", ")
}
