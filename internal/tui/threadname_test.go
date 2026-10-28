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
		roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	m.conf.base.Assist.Endpoint = "https://example.invalid/v1/chat/completions"
	m.conf.device.Assist.NameThreads = true
	return m, b
}

// A thread with enough replies and no name gets one, written to the config.
func TestAThreadWithNoNameIsNamed(t *testing.T) {
	t.Parallel()

	m, b := naming(t, "Rolling back the migration")
	threads := []domain.Thread{{Root: mx + "$root", RoomID: mx + "!a:x", Count: 4}}

	// The real trigger: handleRoomThreads receives a room's thread list.
	next, cmd := m.handleRoomThreads(roomThreadsMsg{roomID: mx + "!a:x", threads: threads})
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

	// It reads the thread, not the room: ReplyTo names the thread root.
	if len(b.asked) != 1 || b.asked[0].ReplyTo != mx+"$root" {
		t.Fatalf("asked = %+v, want one request naming the thread root", b.asked)
	}
	if b.asked[0].Task != domain.ModelThreadName {
		t.Errorf("task = %q, want %q", b.asked[0].Task, domain.ModelThreadName)
	}

	// The answer lands where a typed name goes.
	named, _ := next.handleThreadNamed(msg)
	if got := named.prefs.threadAliases[mx+"$root"]; got != "Rolling back the migration" {
		t.Errorf("threadAliases = %q, want the generated name", got)
	}
	if got := named.prefs.display.NameFor(config.NameTargetThread + mx + "$root"); got == "" {
		t.Error("the name was not written to the config, so it would not survive a restart")
	}
}

// Each gate alone means "ask nothing".
func TestTheThreadNamerAsksNothingUnlessItShould(t *testing.T) {
	t.Parallel()

	thread := domain.Thread{Root: mx + "$root", RoomID: mx + "!a:x", Count: 4}
	for _, tc := range []struct {
		name string
		with func(Model) Model
	}{
		{"off by default", func(m Model) Model { m.conf.device.Assist.NameThreads = false; return m }},
		{"no endpoint", func(m Model) Model { m.conf.base.Assist.Endpoint = ""; return m }},
		{"already named by hand", func(m Model) Model {
			m.prefs.threadAliases = map[domain.EventID]string{mx + "$root": "What I called it"}
			return m
		}},
		{"already asked this session", func(m Model) Model {
			m.namedThreads[mx+"$root"] = true
			return m
		}},
		{"too few replies to be about anything yet", func(m Model) Model {
			m.conf.device.Assist.NameThreadsAfter = 5
			return m
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, b := naming(t, "A name")
			if _, cmd := tc.with(m).nameThreads(mx+"!a:x", []domain.Thread{thread}); cmd != nil {
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
	m.prefs.threadAliases = map[domain.EventID]string{mx + "$root": "What I typed"}

	next, _ := m.handleThreadNamed(threadNamedMsg{room: mx + "!a:x", root: mx + "$root", name: "What the model thought"})
	if got := next.prefs.threadAliases[mx+"$root"]; got != "What I typed" {
		t.Errorf("threadAliases = %q, want the typed name kept", got)
	}
}

// An unusable answer is not written.
func TestAnEmptyAnswerNamesNothing(t *testing.T) {
	t.Parallel()

	m, _ := naming(t, "   \n  ")
	_, cmd := m.nameThreads(mx+"!a:x", []domain.Thread{{Root: mx + "$root", RoomID: mx + "!a:x", Count: 4}})
	if cmd == nil {
		t.Fatal("no request was made")
	}
	if _, ok := drainFor[threadNamedMsg](cmd); ok {
		t.Error("an answer with no usable name still produced one")
	}
}

// Threads are named when a room's messages land, not when its thread list does.
func TestThreadsAreNamedWhenTheTimelineLands(t *testing.T) {
	t.Parallel()

	b := &modelBackend{result: domain.ModelResult{Text: "Rolling back the migration"}}
	m := update(t, starterNew(b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m.conf.base.Assist.Endpoint = "https://example.invalid/v1/chat/completions"
	m.conf.device.Assist.NameThreads = true
	// The room list's thread rows stay off (the default).
	if m.prefs.device.Threads.Mode() == config.ThreadsAll {
		t.Fatal("the room list is showing all threads; the test is not about the thing it says")
	}

	at := time.Now()
	root := domain.Message{ID: mx + "$root", RoomID: mx + "!a:x", Sender: "matrix:@a:x", Body: "start", Timestamp: at}
	reply := func(n int) domain.Message {
		return domain.Message{
			ID: domain.EventID(fmt.Sprintf(mx+"$r%d", n)), RoomID: mx + "!a:x", Sender: "matrix:@b:x",
			Body: "more", ThreadRoot: mx + "$root", Timestamp: at.Add(time.Duration(n) * time.Minute),
		}
	}
	landed, cmd := m.handleCachedTimeline(cachedTimelineMsg{
		roomID: mx + "!a:x", messages: []domain.Message{root, reply(1), reply(2)},
	})
	if cmd == nil {
		t.Fatal("the timeline landing produced no command at all")
	}
	if _, ok := drainFor[threadNamedMsg](cmd); !ok {
		t.Fatal("no thread was named when the messages landed")
	}
	if !landed.namedThreads[mx+"$root"] {
		t.Error("the thread was not marked as asked about, so it would be asked again")
	}
	if len(b.asked) != 1 || b.asked[0].ReplyTo != mx+"$root" {
		t.Errorf("asked = %+v, want one request for the thread root", b.asked)
	}
}
