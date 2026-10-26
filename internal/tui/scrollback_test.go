package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// cacheOnlyRoom is room !a:x with n cached messages on a network that serves no
// history (apitest.Nop's Timeline is one empty page), opened: the cached preview and
// the network's page have landed.
func cacheOnlyRoom(t *testing.T, n int) (Model, []domain.Message) {
	t.Helper()
	all := manyMessages(n)
	around := 0
	b := cachedRoomBackend{all: all, newest: 2000, around: &around}
	m := sized(t, update(t, starterNew(b, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: mx + "!a:x", Name: "Alpha"},
	}}))
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m = update(t, m, cachedTimelineMsg{roomID: mx + "!a:x", messages: all[n-2000:]})
	m = update(t, m, timelineMsg{roomID: mx + "!a:x"})
	m.focus, m.compose.insertMode = paneTimeline, false
	return m, all
}

// followAll runs cmd and every command its messages lead to.
func followAll(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	pending := []tea.Cmd{cmd}
	for steps := 0; len(pending) > 0; steps++ {
		if steps > 1000 {
			t.Fatal("history never stopped loading")
		}
		c := pending[0]
		pending = pending[1:]
		for _, msg := range msgsOf(t, c) {
			next, more := asModel(m.Update(msg))
			m = next
			pending = append(pending, more)
		}
	}
	return m
}

// Going to the start of a room whose network serves no history reads the cache to its
// first message, past the preview and past the network backfill's cap.
func TestTheStartOfARoomIsTheCachesFirstMessage(t *testing.T) {
	t.Parallel()
	const n = maxTimelineMessages + 5000
	m, all := cacheOnlyRoom(t, n)

	next, cmd := m.jumpToStart()
	m = followAll(t, next, cmd)

	if got := len(m.timeline.messages); got != n {
		t.Fatalf("loaded %d messages, want all %d cached", got, n)
	}
	if m.timeline.messages[0].ID != all[0].ID {
		t.Errorf("oldest loaded = %s, want %s", m.timeline.messages[0].ID, all[0].ID)
	}
	if !m.timeline.hist.atStart {
		t.Error("the cache's first message is not taken for the room's start")
	}
}

// Paging up past the cached preview reads the cache's next page; the network's empty
// page does not make the preview the start.
func TestPagingUpReadsOlderCachedMessages(t *testing.T) {
	t.Parallel()
	m, all := cacheOnlyRoom(t, 5000)
	if m.timeline.hist.atStart {
		t.Fatal("the cached preview is taken for the room's start")
	}

	next, cmd := m.loadOlder()
	m = followAll(t, next, cmd)

	if got := len(m.timeline.messages); got != 2000+cachedPageSize {
		t.Fatalf("loaded %d messages, want the preview and one cached page (%d)", got, 2000+cachedPageSize)
	}
	if m.timeline.messages[0].ID != all[5000-2000-cachedPageSize].ID {
		t.Errorf("oldest loaded = %s, want %s", m.timeline.messages[0].ID, all[5000-2000-cachedPageSize].ID)
	}
	if m.timeline.hist.atStart {
		t.Error("a page short of the cache's start is taken for the room's start")
	}
}
