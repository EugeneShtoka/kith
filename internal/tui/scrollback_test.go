package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// noNetworkPage is every way a room's network has no older page to give, by its
// Timeline's error: it serves no history (one empty page), or it is off or not
// connected (as the daemon reports either).
var noNetworkPage = map[string]error{
	"serves no history":          nil,
	"is off or is not connected": fmt.Errorf("%w: Slack (not logged in) (slack:T/C)", api.ErrNetworkOff),
}

// networkAnswering is cachedRoomBackend whose network answers Timeline with err (an
// empty page when nil).
type networkAnswering struct {
	cachedRoomBackend
	err error
}

func (b networkAnswering) Timeline(context.Context, domain.RoomID, string, int) (domain.TimelinePage, error) {
	return domain.TimelinePage{}, b.err
}

// cacheOnlyRoom is room !a:x with n cached messages on a network whose Timeline answers
// with networkErr, opened: the cached preview and the network's answer have landed.
func cacheOnlyRoom(t *testing.T, n int, networkErr error) (Model, []domain.Message) {
	t.Helper()
	all := manyMessages(n)
	around := 0
	b := networkAnswering{cachedRoomBackend{all: all, newest: 2000, around: &around}, networkErr}
	m := sized(t, update(t, starterNew(b, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: mx + "!a:x", Name: "Alpha"},
	}}))
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m = update(t, m, cachedTimelineMsg{roomID: mx + "!a:x", messages: all[n-2000:]})
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", err: networkErr})
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

// Going to the start of a room whose network has no page to give reads the cache to its
// first message, past the preview and past the network backfill's cap.
func TestTheStartOfARoomIsTheCachesFirstMessage(t *testing.T) {
	t.Parallel()
	for name, networkErr := range noNetworkPage {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			const n = maxTimelineMessages + 5000
			m, all := cacheOnlyRoom(t, n, networkErr)

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
		})
	}
}

// Paging up past the cached preview reads the cache's next page; the network having no
// page does not make the preview the start, nor is it an error.
func TestPagingUpReadsOlderCachedMessages(t *testing.T) {
	t.Parallel()
	for name, networkErr := range noNetworkPage {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, all := cacheOnlyRoom(t, 5000, networkErr)
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
			if got := m.status(); strings.Contains(got, "error") {
				t.Errorf("status = %q, want no error", got)
			}
		})
	}
}
