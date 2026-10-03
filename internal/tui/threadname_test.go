package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// naming builds a client with the thread namer on and a model that answers with reply.
func naming(t *testing.T, reply string) (Model, *modelBackend) {
	t.Helper()
	b := &modelBackend{result: domain.ModelResult{Text: reply}}
	m := update(t, starterNew(b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	m.conf.base.Assist.Endpoint = "https://example.invalid/v1/chat/completions"
	m.conf.base.Assist.NameThreads = true
	return m, b
}

// A thread with enough replies and no name gets one, and it is written to the config.
func TestAThreadWithNoNameIsNamed(t *testing.T) {
	t.Parallel()

	m, b := naming(t, "Rolling back the migration")
	threads := []domain.Thread{{Root: "$root", RoomID: "!a:x", Count: 4}}

	// Through the real trigger: handleRoomThreads is where a room's thread list arrives
	// and is what arms this.
	next, cmd := m.handleRoomThreads(roomThreadsMsg{roomID: "!a:x", threads: threads})
	if cmd == nil {
		t.Fatal("no request was made for an unnamed thread")
	}
	msg, ok := drainFor[threadNamedMsg](cmd)
	if !ok {
		t.Fatal("the model's answer did not come back as a name")
	}
	if msg.name != "Rolling back the migration" {
		t.Errorf("name = %q", msg.name)
	}

	// It reads the thread, not the room: ReplyTo is how every task here says "the root
	// of the thread this is about".
	if len(b.asked) != 1 || b.asked[0].ReplyTo != "$root" {
		t.Fatalf("asked = %+v, want one request naming the thread root", b.asked)
	}
	if b.asked[0].Task != domain.ModelThreadName {
		t.Errorf("task = %q, want %q", b.asked[0].Task, domain.ModelThreadName)
	}

	// And the answer lands in the same list a typed name goes to.
	named, _ := next.handleThreadNamed(msg)
	if got := named.prefs.threadAliases["$root"]; got != "Rolling back the migration" {
		t.Errorf("threadAliases = %q, want the generated name", got)
	}
	if got := named.prefs.display.NameFor(config.NameTargetThread + "$root"); got == "" {
		t.Error("the name was not written to the config, so it would not survive a restart")
	}
}

// The gates, each on its own: every one of them means "ask nothing".
func TestTheThreadNamerAsksNothingUnlessItShould(t *testing.T) {
	t.Parallel()

	thread := domain.Thread{Root: "$root", RoomID: "!a:x", Count: 4}
	for _, tc := range []struct {
		name string
		with func(Model) Model
	}{
		{"off by default", func(m Model) Model { m.conf.base.Assist.NameThreads = false; return m }},
		{"no endpoint", func(m Model) Model { m.conf.base.Assist.Endpoint = ""; return m }},
		{"already named by hand", func(m Model) Model {
			m.prefs.threadAliases = map[domain.EventID]string{"$root": "What I called it"}
			return m
		}},
		{"already asked this session", func(m Model) Model {
			m.namedThreads["$root"] = true
			return m
		}},
		{"too few replies to be about anything yet", func(m Model) Model {
			m.conf.base.Assist.NameThreadsAfter = 5
			return m
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, b := naming(t, "A name")
			if _, cmd := tc.with(m).nameThreads("!a:x", []domain.Thread{thread}); cmd != nil {
				cmd()
			}
			if len(b.asked) != 0 {
				t.Errorf("asked the model %d time(s) when it should not have", len(b.asked))
			}
		})
	}
}

// A name typed while the model was thinking wins.
func TestATypedNameBeatsAGeneratedOne(t *testing.T) {
	t.Parallel()

	m, _ := naming(t, "What the model thought")
	m.prefs.threadAliases = map[domain.EventID]string{"$root": "What I typed"}

	next, _ := m.handleThreadNamed(threadNamedMsg{room: "!a:x", root: "$root", name: "What the model thought"})
	if got := next.prefs.threadAliases["$root"]; got != "What I typed" {
		t.Errorf("threadAliases = %q, want the typed name kept", got)
	}
}

// An unusable answer is not written, and does not become a name.
func TestAnEmptyAnswerNamesNothing(t *testing.T) {
	t.Parallel()

	m, _ := naming(t, "   \n  ")
	_, cmd := m.nameThreads("!a:x", []domain.Thread{{Root: "$root", RoomID: "!a:x", Count: 4}})
	if cmd == nil {
		t.Fatal("no request was made")
	}
	if _, ok := drainFor[threadNamedMsg](cmd); ok {
		t.Error("an answer with no usable name still produced one")
	}
}

// Threads are named when a room's messages land, not when its thread *list* does.
func TestThreadsAreNamedWhenTheTimelineLands(t *testing.T) {
	t.Parallel()

	b := &modelBackend{result: domain.ModelResult{Text: "Rolling back the migration"}}
	m := update(t, starterNew(b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m.conf.base.Assist.Endpoint = "https://example.invalid/v1/chat/completions"
	m.conf.base.Assist.NameThreads = true
	// The room list's thread rows stay off, which is the default and was the gate.
	if m.prefs.display.Threads.Mode() == config.ThreadsAll {
		t.Fatal("the room list is showing all threads; the test is not about the thing it says")
	}

	at := time.Now()
	root := domain.Message{ID: "$root", RoomID: "!a:x", Sender: "@a:x", Body: "start", Timestamp: at}
	reply := func(n int) domain.Message {
		return domain.Message{
			ID: domain.EventID(fmt.Sprintf("$r%d", n)), RoomID: "!a:x", Sender: "@b:x",
			Body: "more", ThreadRoot: "$root", Timestamp: at.Add(time.Duration(n) * time.Minute),
		}
	}
	landed, cmd := m.handleCachedTimeline(cachedTimelineMsg{
		roomID: "!a:x", messages: []domain.Message{root, reply(1), reply(2)},
	})
	if cmd == nil {
		t.Fatal("the timeline landing produced no command at all")
	}
	if _, ok := drainFor[threadNamedMsg](cmd); !ok {
		t.Fatal("no thread was named when the messages landed")
	}
	if !landed.namedThreads["$root"] {
		t.Error("the thread was not marked as asked about, so it would be asked again")
	}
	if len(b.asked) != 1 || b.asked[0].ReplyTo != "$root" {
		t.Errorf("asked = %+v, want one request for the thread root", b.asked)
	}
}
