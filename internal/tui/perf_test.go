package tui

import (
	"fmt"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// settled is m after one Update, as a keypress finds it: Update's caches (phrase index,
// derived rows) are built. Otherwise a benchmark repeating a key on its starting model would
// rebuild them every iteration, a cost a session pays once per timeline change.
func settled(m Model) Model {
	next, _ := benchModel(m.Update(settleMsg{}))
	return next
}

// benchModel is asModel without the staleRow check, which re-renders every cached row and
// would dominate a benchmark.
func benchModel(mdl tea.Model, cmd tea.Cmd) (Model, tea.Cmd) {
	return mdl.(Model), cmd
}

// settleMsg is a message nothing handles; Update still runs its settling steps.
type settleMsg struct{}

// Benchmarks for the unbounded lists (timeline, room list) at cache-sized inputs.
// Run: go test ./internal/tui -run xxx -bench 'Layout|RoomRows|Scroll' -benchmem
var benchSenders = []string{
	"matrix:@alice:example.org", "matrix:@bob:example.org", "matrix:@carol:example.org",
	"matrix:@dave:example.org", "matrix:@erin:example.org",
}

// benchBodies vary in length and include RTL and emoji, which go through reorder().
var benchBodies = []string{
	"ok",
	"sounds good to me, let's do that tomorrow",
	"I pushed the branch — can you take a look when you get a chance? " +
		"There are two commits, the second one is the interesting one and it " +
		"touches the layout code we talked about on Tuesday.",
	"שלום, מה נשמע? אני אבדוק את זה מחר בבוקר",
	"🙋‍♀️ me too",
}

// benchMessages builds n messages over several days with some replies and thread replies.
func benchMessages(n int) []domain.Message {
	msgs := make([]domain.Message, n)
	start := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	for i := range msgs {
		msgs[i] = domain.Message{
			ID:        domain.EventID(fmt.Sprintf(mx+"$ev%d", i)),
			RoomID:    mx + "!bench:example.org",
			Sender:    benchSenders[i%len(benchSenders)],
			Body:      benchBodies[i%len(benchBodies)],
			Timestamp: start.Add(time.Duration(i) * 7 * time.Minute),
		}
		// Every eleventh message replies to the previous one (quote-reply preview cost).
		if i > 0 && i%11 == 0 {
			msgs[i].ReplyTo = msgs[i-1].ID
		}
		// Every seventeenth is in a thread rooted a little way back, so CollapseThreads has work.
		if i > 40 && i%17 == 0 {
			msgs[i].ThreadRoot = msgs[i-40].ID
		}
	}
	return msgs
}

// benchTimelineModel is a sized model with a room open and n messages loaded.
func benchTimelineModel(n int) Model {
	m := newModel()
	m.width, m.height, m.ready = 200, 50, true
	m.selves = []string{"matrix:@alice:example.org"}
	m.openRoom = mx + "!bench:example.org"
	m.rooms = m.rooms.withJoined([]domain.Room{{ID: mx + "!bench:example.org", Name: "Bench"}})
	m = m.setMessages(benchMessages(n))
	m.focus = paneTimeline
	return m
}

// benchRooms builds n rooms with names of realistic length.
func benchRooms(n int) []domain.Room {
	rooms := make([]domain.Room, n)
	for i := range rooms {
		rooms[i] = domain.Room{
			ID:   domain.RoomID(fmt.Sprintf(mx+"!room%d:example.org", i)),
			Name: fmt.Sprintf("Project %d — planning and follow-up", i),
		}
	}
	return rooms
}

func benchRoomListModel(n int) Model {
	m := newModel()
	m.width, m.height, m.ready = 200, 50, true
	m.rooms = m.rooms.withJoined(benchRooms(n))
	m.focus = paneRooms
	return m
}

func BenchmarkLayoutRows(b *testing.B) {
	for _, n := range []int{100, 300, 500, 2000} {
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			m := benchTimelineModel(n)
			b.ReportAllocs()
			for b.Loop() {
				_ = m.layoutRows()
			}
		})
	}
}

// BenchmarkTimelineView is the whole frame.
func BenchmarkTimelineView(b *testing.B) {
	for _, n := range []int{100, 300, 500, 2000} {
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			m := benchTimelineModel(n)
			b.ReportAllocs()
			for b.Loop() {
				_ = m.View()
			}
		})
	}
}

// BenchmarkScrollKeypress is Update on the key plus the render.
func BenchmarkScrollKeypress(b *testing.B) {
	for _, n := range []int{300, 500, 2000} {
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			m := settled(benchTimelineModel(n))
			key := tea.KeyPressMsg{Code: tea.KeyPgUp}
			b.ReportAllocs()
			for b.Loop() {
				next, _ := benchModel(m.Update(key))
				mdl := next
				_ = mdl.View()
			}
		})
	}
}

func BenchmarkRoomRows(b *testing.B) {
	for _, n := range []int{100, 300, 500, 2000} {
		b.Run(fmt.Sprintf("rooms=%d", n), func(b *testing.B) {
			m := benchRoomListModel(n)
			b.ReportAllocs()
			for b.Loop() {
				_ = m.roomRows()
			}
		})
	}
}

// BenchmarkRoomListKeypress is one press of j in the room list, frame included.
func BenchmarkRoomListKeypress(b *testing.B) {
	for _, n := range []int{300, 500, 2000} {
		b.Run(fmt.Sprintf("rooms=%d", n), func(b *testing.B) {
			m := settled(benchRoomListModel(n))
			key := tea.KeyPressMsg{Code: 'j', Text: "j"}
			b.ReportAllocs()
			for b.Loop() {
				next, _ := benchModel(m.Update(key))
				mdl := next
				_ = mdl.View()
			}
		})
	}
}

// BenchmarkScrollAtDepth is a keypress deep in a long room, row cache warm.
func BenchmarkScrollAtDepth(b *testing.B) {
	for _, depth := range []int{0, 500, 1500, 3000} {
		b.Run(fmt.Sprintf("scroll=%d", depth), func(b *testing.B) {
			m := benchTimelineModel(2000)
			m.timeline.scroll = depth
			m = settled(m)
			// Warm the cache as scrolling there would.
			_ = m.layoutRows()
			key := tea.KeyPressMsg{Code: tea.KeyPgUp}
			b.ReportAllocs()
			for b.Loop() {
				next, _ := benchModel(m.Update(key))
				mdl := next
				_ = mdl.View()
			}
		})
	}
}

// benchRealisticRoomList mirrors a real account: 337 rooms, a third unread, eleven
// spaces, seventeen archived, name rules on. The archive is the expensive part.
func benchRealisticRoomList() Model {
	rooms := make([]domain.Room, 337)
	children := make([]domain.RoomID, 0, len(rooms))
	unread := make([]domain.Unread, 0, len(rooms)/3+1)
	archived := make([]string, 0, 17)
	for i := range rooms {
		id := domain.RoomID(fmt.Sprintf(mx+"!room%03d:example.org", i))
		rooms[i] = domain.Room{
			ID: id, Name: fmt.Sprintf("Room %03d with a fairly long name", i),
			Members: []string{"Dana Levi", "Sam Cohen", "Alex Toms"},
		}
		children = append(children, id)
		if i%3 == 0 {
			unread = append(unread, domain.Unread{
				RoomID: id, Messages: i%7 + 1, Mentions: i % 2, Counted: true,
			})
		}
		if i%20 == 0 && len(archived) < 17 {
			archived = append(archived, string(id))
		}
	}
	spaces := make([]domain.Space, 11)
	for i := range spaces {
		spaces[i] = domain.Space{
			ID:   domain.SpaceID(fmt.Sprintf(mx+"!space%d:example.org", i)),
			Name: fmt.Sprintf("Space %d", i),
			// Every space holds a slice of the rooms, so spacesOf has real work.
			Children: children[i*30 : min((i+1)*30, len(children))],
		}
	}
	display := config.Display{
		// Name rules on: every room label goes through shortening as well as bidi.
		SpaceRules: []config.SpaceRule{{Space: "space:Space 0", FirstNameOnly: true}},
	}
	m := starterNew(apitest.Nop{}, display)
	cfg := m.conf.base.Clone()
	for i := range cfg.Tags {
		if cfg.Tags[i].Name == "Archived" {
			cfg.Tags[i].Picked = archived
		}
	}
	m = m.WithConfigFile("", cfg)
	m.width, m.height, m.ready = 200, 50, true
	m.focus = paneRooms
	m.rooms = m.rooms.withJoined(rooms)
	m = m.refreshPlaces()
	m.rooms = m.rooms.withSpaces(spaces) // as the spaces load does
	m = m.refreshPlaces()
	m.rail.groups = railGroups(spaces, m.prefs.display.Rail, nil, m.unreadView(), rooms)
	for _, u := range unread {
		m.unread[u.RoomID] = u
	}
	return m
}

// BenchmarkRealRoomListKeypress is one press of j in that list, frame included.
func BenchmarkRealRoomListKeypress(b *testing.B) {
	m := settled(benchRealisticRoomList())
	key := tea.KeyPressMsg{Code: 'j', Text: "j"}
	b.ReportAllocs()
	for b.Loop() {
		next, _ := benchModel(m.Update(key))
		mdl := next
		_ = mdl.View()
	}
}

// BenchmarkRealFilteredRooms is the call the room pane is built on.
func BenchmarkRealFilteredRooms(b *testing.B) {
	m := benchRealisticRoomList()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.filteredRooms()
	}
}

// BenchmarkRealRoomListKeypressWithActivity is that list with nearly every room having a
// last-message time, so recent orders them and name only breaks ties.
func BenchmarkRealRoomListKeypressWithActivity(b *testing.B) {
	m := benchRealisticRoomList()
	m.lastMessage = make(map[domain.RoomID]time.Time, len(m.rooms.all))
	start := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	for i := range m.rooms.all {
		if i%25 != 0 { // a few rooms never had cached history
			m.lastMessage[m.rooms.all[i].ID] = start.Add(time.Duration(i*37%337) * time.Hour)
		}
	}
	m = settled(m)
	key := tea.KeyPressMsg{Code: 'j', Text: "j"}
	b.ReportAllocs()
	for b.Loop() {
		next, _ := benchModel(m.Update(key))
		mdl := next
		_ = mdl.View()
	}
}

// BenchmarkMessageArrival is a live message landing in the open room at the bottom, frame
// included: derived answers and window rows are rebuilt. The model is reused, so each
// iteration is the same arrival.
func BenchmarkMessageArrival(b *testing.B) {
	for _, n := range []int{300, 2000} {
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			m := settled(benchTimelineModel(n))
			last := m.timeline.messages[len(m.timeline.messages)-1]
			msg := incomingMsg{message: domain.Message{
				ID: mx + "$live", RoomID: last.RoomID, Sender: benchSenders[1],
				Body: benchBodies[2], Timestamp: last.Timestamp.Add(time.Minute),
			}}
			b.ReportAllocs()
			for b.Loop() {
				next, _ := benchModel(m.Update(msg))
				_ = next.View()
			}
		})
	}
}

// BenchmarkScrolledMessageArrival is BenchmarkMessageArrival scrolled up, with the view held
// around the new message (keepAnchored).
func BenchmarkScrolledMessageArrival(b *testing.B) {
	for _, n := range []int{300, 2000} {
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			m := benchTimelineModel(n)
			m.timeline.scroll = 40
			m = settled(m)
			last := m.timeline.messages[len(m.timeline.messages)-1]
			msg := incomingMsg{message: domain.Message{
				ID: mx + "$live", RoomID: last.RoomID, Sender: benchSenders[1],
				Body: benchBodies[2], Timestamp: last.Timestamp.Add(time.Minute),
			}}
			b.ReportAllocs()
			for b.Loop() {
				next, _ := benchModel(m.Update(msg))
				_ = next.View()
			}
		})
	}
}

// BenchmarkResize alternates two widths so every frame re-wraps the window.
func BenchmarkResize(b *testing.B) {
	m := settled(benchTimelineModel(2000))
	sizes := []tea.WindowSizeMsg{{Width: 180, Height: 50}, {Width: 200, Height: 50}}
	i := 0
	b.ReportAllocs()
	for b.Loop() {
		next, _ := benchModel(m.Update(sizes[i%2]))
		_ = next.View()
		i++
	}
}
