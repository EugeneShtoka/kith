package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Every surface that draws somebody's words, fed the same words.

// textFixtures are the words every surface is given. The English one is the control:
// it must come out untouched.
var textFixtures = map[string]string{
	"hebrew": "שלום עולם",
	"mixed":  "שלום world 42",
	"arabic": "مرحبا بالعالم",
	"latin":  "hello world",
}

// textSurface is one place somebody's words are drawn.
type textSurface struct {
	name string
	// draw renders text the way the surface does, from a model holding it as a room's
	// name and topic, a person's name and a message's body.
	draw func(t *testing.T, m Model, text string) string
	// flush is the width a right-to-left row of this surface is flushed to, or zero for
	// a surface that is a line of ours rather than a column of paragraphs.
	flush int
}

// surfaceRoom is where the fixture lives: a room named and topped with it, one message
// from a person named with it and saying it, and a threaded reply under that.
func surfaceRoom(t *testing.T, text string) Model {
	t.Helper()
	m := sized(t, update(t, newModel(), roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: text, Topic: text}}}))
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{
		Messages: []domain.Message{
			{ID: "$1", RoomID: "!a:x", Sender: "@d:x", SenderName: text, Body: text, Timestamp: at(1)},
			{ID: "$2", RoomID: "!a:x", Sender: "@d:x", SenderName: text, Body: "reply", Timestamp: at(2), ThreadRoot: "$1"},
		},
	}})
	m.focus, m.compose.insertMode = paneTimeline, false
	return m.clearStatus()
}

func (m Model) fixtureRoom(t *testing.T) domain.Room {
	t.Helper()
	room, ok := m.currentRoom()
	if !ok {
		t.Fatal("no room open")
	}
	return room
}

func (m Model) fixtureMessage(t *testing.T) domain.Message {
	t.Helper()
	if len(m.timeline.messages) == 0 {
		t.Fatal("no message loaded")
	}
	return m.timeline.messages[0]
}

// messageWidth is the timeline width the body surfaces are drawn at.
const messageWidth = 60

func textSurfaces() []textSurface {
	return []textSurface{
		{name: "timeline body", flush: messageWidth, draw: func(t *testing.T, m Model, text string) string {
			return strings.Join(m.messageRows(m.fixtureMessage(t), messageWidth, m.nameColWidth(), m.senderColorMap(), false, ""), "\n")
		}},
		{name: "timeline sender column", draw: func(t *testing.T, m Model, text string) string {
			msg := m.fixtureMessage(t)
			return m.messagePrefix(msg, 20, m.senderColorMap()[msg.Sender], false)
		}},
		// A chip is a line of ours with the file name in it — "📎 <name> · 240 KB" — so it
		// reads left to right and is not flushed; the name in it is in visual order.
		{name: "attachment file name", draw: func(t *testing.T, m Model, text string) string {
			msg := domain.Message{ID: "$f", RoomID: "!a:x", Sender: "@d:x", Timestamp: at(3),
				Media: &domain.Media{Type: domain.MediaFile, Name: text}}
			return strings.Join(m.messageRows(msg, messageWidth, m.nameColWidth(), m.senderColorMap(), false, ""), "\n")
		}},
		{name: "captioned attachment chip", draw: func(t *testing.T, m Model, text string) string {
			msg := domain.Message{ID: "$f", RoomID: "!a:x", Sender: "@d:x", Timestamp: at(3), Body: "caption",
				Media: &domain.Media{Type: domain.MediaFile, Name: text}}
			return strings.Join(m.hangingRows(msg, nil, 8, 80), "\n")
		}},
		{name: "revealed deleted message", draw: func(t *testing.T, m Model, text string) string {
			msg := domain.Message{ID: "$r", RoomID: "!a:x", Sender: "@d:x", Timestamp: at(3), Body: text, Redacted: true}
			m.timeline.revealed = map[domain.EventID]bool{msg.ID: true}
			return strings.Join(m.messageRows(msg, messageWidth, m.nameColWidth(), m.senderColorMap(), false, ""), "\n")
		}},
		{name: "deleted message reason", draw: func(t *testing.T, m Model, text string) string {
			msg := domain.Message{ID: "$r", RoomID: "!a:x", Sender: "@d:x", Timestamp: at(3),
				Redacted: true, RedactedBy: "@mod:x", RedactedReason: text}
			return strings.Join(m.messageRows(msg, messageWidth, m.nameColWidth(), m.senderColorMap(), false, ""), "\n")
		}},
		{name: "reply quote", draw: func(t *testing.T, m Model, text string) string {
			reply := domain.Message{ID: "$q", RoomID: "!a:x", Sender: "@e:x", ReplyTo: "$1", Timestamp: at(3)}
			return m.replyPreview(reply, m.derivedFor(), 8, 80, m.senderColorMap())
		}},
		{name: "thread summary row", draw: func(t *testing.T, m Model, text string) string {
			return m.threadRow(domain.Thread{RoomID: "!a:x", Count: 2, RootLoaded: true,
				LatestSender: "@d:x", LatestSenderName: text, LatestAt: at(2)}, 8, 80)
		}},
		{name: "topic reader", flush: 60, draw: func(t *testing.T, m Model, text string) string {
			return strings.Join(m.readerLines(m.topicLines(), 60, nil), "\n")
		}},
		{name: "reader title", draw: func(t *testing.T, m Model, text string) string {
			return m.readerBox(m.topicTitle(), []string{"x"}, nil)
		}},
		{name: "history version", flush: 59, draw: func(t *testing.T, m Model, text string) string {
			m.history.msg = m.fixtureMessage(t)
			m.history.revs = []domain.Revision{{ID: "$1", Body: text, At: at(1)}}
			return strings.Join(m.versionLines(60), "\n")
		}},
		{name: "history title", draw: func(t *testing.T, m Model, text string) string {
			m.history.msg = m.fixtureMessage(t)
			return m.framePane(m.historyTitle(), nil, 60, 4, true)
		}},
		{name: "history deletion line", draw: func(t *testing.T, m Model, text string) string {
			msg := m.fixtureMessage(t)
			msg.Redacted, msg.RedactedBy, msg.RedactedReason = true, "@mod:x", text
			m.history.msg = msg
			return m.deletionLine()
		}},
		{name: "search excerpt", draw: func(t *testing.T, m Model, text string) string {
			return m.excerptCell(domain.HighlightStart+text+domain.HighlightEnd, 40)
		}},
		{name: "search row sender", draw: func(t *testing.T, m Model, text string) string {
			return m.searchRow(domain.SearchHit{RoomID: "!a:x", SenderName: text, Snippet: "x", Timestamp: at(1)}, false, 80)
		}},
		{name: "room list row", draw: func(t *testing.T, m Model, text string) string {
			return m.listRow(rowLabel{name: m.roomLabel(m.fixtureRoom(t))}, "", false, 40, false, true)
		}},
		{name: "pinned thread row", draw: func(t *testing.T, m Model, text string) string {
			return m.listRow(rowLabel{lead: m.prefs.display.Threads.RowMark(), name: m.threadRowLabel(domain.ThreadUnread{Title: text})},
				"", false, 40, false, true)
		}},
		{name: "timeline pane title", draw: func(t *testing.T, m Model, text string) string {
			return strings.SplitN(m.renderTimeline(100, 12), "\n", 3)[1]
		}},
		{name: "topic in the title", draw: func(t *testing.T, m Model, text string) string {
			return strings.SplitN(m.renderTimeline(100, 12), "\n", 3)[1]
		}},
		{name: "thread breadcrumb", draw: func(t *testing.T, m Model, text string) string {
			m.thread.root = "$1"
			return m.framePane(threadMark+" "+m.threadTitle(), nil, 100, 4, true)
		}},
		{name: "thread picker", draw: func(t *testing.T, m Model, text string) string {
			m = listedThreads(t, m)
			return strings.Join(m.pickerLines(60, 4), "\n")
		}},
		// The daemon-backed thread lists: one room (the room left out of the label) and
		// several (each row names its room), plus the empty answer. The latest sender is
		// someone else, so the thread's name is the only copy of the words on the row.
		{name: "room thread picker, one room", draw: func(t *testing.T, m Model, text string) string {
			room := m.fixtureRoom(t)
			next, _ := m.handleScopeThreads(scopeThreadsMsg{only: room, threads: []scopeThread{{room: room,
				thread: domain.Thread{Root: "$1", RoomID: room.ID, Count: 1, LatestSender: "@e:x", LatestSenderName: "bob"}}}})
			return strings.Join(next.pickerLines(60, 4), "\n")
		}},
		{name: "scoped thread picker, several rooms", draw: func(t *testing.T, m Model, text string) string {
			room := m.fixtureRoom(t)
			other := domain.Room{ID: "!b:x", Name: "other"}
			thread := domain.Thread{Root: "$1", RoomID: room.ID, Count: 1, LatestSender: "@e:x", LatestSenderName: "bob", LatestAt: at(2)}
			next, _ := m.handleScopeThreads(scopeThreadsMsg{threads: []scopeThread{
				{room: room, thread: thread}, {room: other, thread: domain.Thread{Root: "$9", RoomID: other.ID, Count: 1}},
			}})
			return strings.Join(next.pickerLines(80, 4), "\n")
		}},
		{name: "no threads in the room", draw: func(t *testing.T, m Model, text string) string {
			// The fixture's timeline has a thread, which the open room's list takes in.
			next, _ := m.setMessages(nil).handleScopeThreads(scopeThreadsMsg{only: m.fixtureRoom(t)})
			return next.renderStatus()
		}},
		// A rule's scope is a sentence of ours around names: a person's alias, a space.
		{name: "rule list, person", draw: func(t *testing.T, m Model, text string) string {
			m.prefs.identities = map[string]resolvedIdentity{"@d:x": {alias: text}}
			m.conf.base.Notifications.Rules = []config.Rule{{Sender: "@d:x"}}
			next, _ := m.openRuleList()
			return strings.Join(next.pickerLines(60, 4), "\n")
		}},
		{name: "rule list, space", draw: func(t *testing.T, m Model, text string) string {
			m.conf.base.Notifications.Rules = []config.Rule{{Sender: "@e:x", Match: "space:" + text}}
			next, _ := m.openRuleList()
			return strings.Join(next.pickerLines(60, 4), "\n")
		}},
		{name: "scheduled message picker", draw: func(t *testing.T, m Model, text string) string {
			now := time.Now()
			entry := domain.ScheduledMessage{ID: "s", RoomID: "!a:x", Body: text, At: now.Add(time.Hour), Written: now}
			return m.labeledRow(cursor(false), m.scheduledRowLabel(entry, now), "", false, 80)
		}},
		{name: "people picker", draw: func(t *testing.T, m Model, text string) string {
			m.timeline.members = []domain.Member{{UserID: "@d:x", DisplayName: text}}
			m.picker = newPicker(pickerPeople, m.peopleItems())
			return strings.Join(m.pickerLines(60, 4), "\n")
		}},
		{name: "picker filter header", draw: func(t *testing.T, m Model, text string) string {
			m.timeline.members = []domain.Member{{UserID: "@d:x", DisplayName: text}}
			m.picker = newPicker(pickerPeople, m.peopleItems())
			m.picker.filter = text
			m.picker = m.picker.refilter()
			return strings.Join(m.pickerLines(60, 4), "\n")
		}},
		{name: "jump picker", draw: func(t *testing.T, m Model, text string) string {
			m.picker = newPicker(pickerJump, m.jumpItems())
			return strings.Join(m.pickerLines(60, 8), "\n")
		}},
		{name: "mute picker", draw: func(t *testing.T, m Model, text string) string {
			targets := m.muteTargets()
			return m.labeledRow(cursor(false), targets[0].label, "", false, 60)
		}},
		{name: "rule scope picker", draw: func(t *testing.T, m Model, text string) string {
			ruled, _ := m.openRuleForSender()
			return strings.Join(ruled.pickerLines(60, 6), "\n")
		}},
		{name: "mention completion", draw: func(t *testing.T, m Model, text string) string {
			m.timeline.members = []domain.Member{{UserID: "@d:x", DisplayName: text}}
			rows := m.mentionCandidates("")
			if len(rows) == 0 {
				t.Fatal("no candidate")
			}
			return m.candidateRow(rows[0], false, 60)
		}},
		{name: "status line", draw: func(t *testing.T, m Model, text string) string {
			return m.say("marked " + m.roomName(m.fixtureRoom(t)) + " read").renderStatus()
		}},
		{name: "go to person status", draw: func(t *testing.T, m Model, text string) string {
			m.prefs.identities = map[string]resolvedIdentity{"@d:x": {alias: text}}
			next, _ := m.followLink("matrix:u/d:x")
			return next.renderStatus()
		}},
		{name: "space with nothing unread", draw: func(t *testing.T, m Model, text string) string {
			m.rail.groups = []group{{key: "s", label: text, admits: func(unreadView, domain.Room) bool { return false }}}
			m.rail.cursor = 0
			next, _ := m.askMarkGroupRead()
			return next.renderStatus()
		}},
		{name: "mark space read prompt", draw: func(t *testing.T, m Model, text string) string {
			m.rail.groups = []group{{key: "s", label: text, admits: func(unreadView, domain.Room) bool { return true }}}
			m.rail.cursor = 0
			m.unread["!a:x"] = domain.Unread{RoomID: "!a:x", Messages: 1, Counted: true}
			next, _ := m.askMarkGroupRead()
			if next.confirm.action != pendingMarkGroupRead {
				t.Fatal("no confirmation asked")
			}
			return next.renderStatus()
		}},
		{name: "sent attachment status", draw: func(t *testing.T, m Model, text string) string {
			next, _ := m.handleAttachSent(attachSentMsg{name: text})
			return next.renderStatus()
		}},
		{name: "saving attachment status", draw: func(t *testing.T, m Model, text string) string {
			msg := domain.Message{ID: "$f", RoomID: "!a:x", Sender: "@d:x", Timestamp: at(3),
				Media: &domain.Media{Type: domain.MediaFile, Name: text}}
			m = m.setMessages(append(m.timeline.messages, msg))
			m.timeline.selected = msg.ID
			next, _ := m.download()
			return next.renderStatus()
		}},
		{name: "copy status", draw: func(t *testing.T, m Model, text string) string {
			return m.say(describeCopy("message", text)).renderStatus()
		}},
		{name: "confirm prompt", draw: func(t *testing.T, m Model, text string) string {
			m.confirm = confirmState{action: pendingLeave, room: m.fixtureRoom(t)}
			return m.renderStatus()
		}},
		{name: "why overlay", draw: func(t *testing.T, m Model, text string) string {
			return m.whyView()
		}},
		{name: "typing note", draw: func(t *testing.T, m Model, text string) string {
			m.live.typists = map[domain.RoomID][]string{"!a:x": {"@d:x"}}
			return m.composerDivider(80)
		}},
		{name: "invitation", draw: func(t *testing.T, m Model, text string) string {
			m.prefs.identities = map[string]resolvedIdentity{"@d:x": {alias: text}}
			return strings.Join(m.inviteLines(domain.Room{ID: "!i:x", Name: text, InvitedBy: "@d:x"}, 60, 20), "\n")
		}},
		// The whole frame, last: whatever a row above missed, a name that reaches the
		// screen anywhere in the default layout is caught here.
		{name: "whole frame", draw: func(t *testing.T, m Model, text string) string {
			return m.View().Content
		}},
		{name: "whole frame, thread open", draw: func(t *testing.T, m Model, text string) string {
			m.thread.root = "$1"
			return m.View().Content
		}},
		{name: "player title", draw: func(t *testing.T, m Model, text string) string {
			m.player.title, m.player.loading = text, true
			return m.renderPlayer(80)
		}},
	}
}

func TestEverySurfaceDrawsUserTextInVisualOrder(t *testing.T) {
	t.Parallel()

	for _, surface := range textSurfaces() {
		for fixture, text := range textFixtures {
			t.Run(surface.name+"/"+fixture, func(t *testing.T) {
				t.Parallel()
				m := surfaceRoom(t, text)
				raw := surface.draw(t, m, text)
				if strings.ContainsAny(raw, isolateOpen+isolateClose) ||
					strings.Contains(raw, escapedOpen) || strings.Contains(raw, escapedClose) {
					t.Fatalf("isolate marks reached the screen: %q", raw)
				}
				drawn := stripStyles(raw)
				want := displayName(text)
				if !strings.Contains(drawn, want) {
					t.Fatalf("want %q (visual order) in:\n%s", want, drawn)
				}
				if want != text && strings.Contains(drawn, text) {
					t.Fatalf("%q is drawn in logical order — backwards on a terminal:\n%s", text, drawn)
				}
				if surface.flush == 0 || !containsRTL(text) {
					return
				}
				for row := range strings.SplitSeq(drawn, "\n") {
					if !strings.Contains(row, want) {
						continue
					}
					if got := ansi.StringWidth(strings.TrimRight(row, " ")); got != surface.flush {
						t.Errorf("right-to-left row is not flushed right (ends at column %d, want %d): %q",
							got, surface.flush, row)
					}
				}
			})
		}
	}
}
