package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A WhatsApp "@<number>" mention is drawn with the person's known name: the number until a
// name is known, then the member list's name, even if that arrives after the first draw.
func TestANumberMentionIsDrawnWithTheName(t *testing.T) {
	t.Parallel()
	const eli = "whatsapp:1500000005@s.whatsapp.net"
	m := withRooms(t, newModel())
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 20})
	m.openRoom = mx + "!a:x"
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{{
		ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@maya:x", SenderName: "Maya", Body: "@100000000000005 can you look?",
		Timestamp: at(1), Mentions: []domain.Mention{{UserID: eli, Name: "@100000000000005"}},
	}}}})
	m = settled(m) // drawn from the shared row cache from here on
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "100000000000005") || !strings.Contains(view, "@+1500000005 can you look?") {
		t.Errorf("before a name is known, the mention is not the number:\n%s", view)
	}

	m = settled(update(t, m, membersMsg{roomID: mx + "!a:x", members: []domain.Member{{UserID: eli, DisplayName: "Eli Cohen"}}}))
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "@Eli Cohen can you look?") {
		t.Errorf("the member's name does not reach the drawn row:\n%s", view)
	}
}

// A mention is drawn with the name known now, not as sent: a Telegram mention by the alias, a
// Matrix one by the member's current name, a first name where the space keeps first names.
func TestEveryMentionIsDrawnWithTheNameKnownNow(t *testing.T) {
	t.Parallel()
	m := withRooms(t, newModel())
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 20})
	m.openRoom = mx + "!a:x"
	m.prefs.identities = map[string]resolvedIdentity{"telegram:7": {alias: "Vanya Sokolov"}}
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{
			ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@maya:x", SenderName: "Maya", Body: "Ivan Petrov, thoughts?", Timestamp: at(1),
			Mentions: []domain.Mention{{UserID: "telegram:7", Name: "Ivan Petrov"}},
		},
		{
			ID: mx + "$2", RoomID: mx + "!a:x", Sender: "matrix:@maya:x", SenderName: "Maya", Body: "ask Robert", Timestamp: at(2),
			Mentions: []domain.Mention{{UserID: "matrix:@rob:x", Name: "Robert"}},
		},
	}}})
	m = settled(update(t, m, membersMsg{roomID: mx + "!a:x", members: []domain.Member{{UserID: "matrix:@rob:x", DisplayName: "Rob Stone"}}}))
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Vanya Sokolov, thoughts?") || !strings.Contains(view, "ask Rob Stone") {
		t.Errorf("the mentions are not drawn with the names known now:\n%s", view)
	}

	if got := m.mentionedName("telegram:7", mx+"!a:x", "Ivan Petrov"); got != "Vanya Sokolov" {
		t.Fatalf("mentionedName = %q", got)
	}
	m.prefs.display.SpaceRules = []config.SpaceRule{{Space: m.ownSpace(mx + "!a:x"), FirstNameOnly: true}}
	if got := m.mentionedName("telegram:7", mx+"!a:x", "Ivan Petrov"); got != "Vanya" {
		t.Errorf("in a first-names space the mention = %q, want Vanya", got)
	}
}

// A search result names its sender and mentioned people as the timeline would.
func TestASearchResultNamesPeopleAsTheTimelineDoes(t *testing.T) {
	t.Parallel()
	m := withRooms(t, newModel())
	m = update(t, m, tea.WindowSizeMsg{Width: 140, Height: 20})
	m.prefs.identities = map[string]resolvedIdentity{"slack:T1/U1": {alias: "Annie"}}
	row := ansi.Strip(m.searchRow(domain.SearchHit{
		RoomID: mx + "!a:x", EventID: mx + "$1", Sender: "slack:T1/U1", SenderName: "Ann", Timestamp: at(1),
		Snippet:  "@Bo Old, standup moved",
		Mentions: []domain.Mention{{UserID: "slack:T1/U2", Name: "@Bo Old", Known: "Bo New"}},
	}, false, 140))
	if !strings.Contains(row, "Annie") || !strings.Contains(row, "@Bo New, standup moved") {
		t.Errorf("search row = %q, want Annie and \"@Bo New, standup moved\"", row)
	}
}

// A cached message carries a mentioned person's current name (Mention.Known), drawn even
// before the member list loads.
func TestAMentionIsDrawnWithTheNameTheCacheKnowsNow(t *testing.T) {
	t.Parallel()
	m := withRooms(t, newModel())
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 20})
	m.openRoom = mx + "!a:x"
	m = settled(update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{{
		ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@maya:x", SenderName: "Maya", Body: "@Bo Old, standup moved", Timestamp: at(1),
		Mentions: []domain.Mention{{UserID: "slack:T1/U2", Name: "@Bo Old", Known: "Bo New"}},
	}}}}))
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "@Bo New, standup moved") {
		t.Errorf("the mention is not drawn with the name the cache knows now:\n%s", view)
	}
}

// `@` offers the room's broadcasts the query begins before its people, and inserts
// them as typed, recording no mention.
func TestMentionCandidatesOfferTheRoomsBroadcastsFirst(t *testing.T) {
	t.Parallel()
	m := update(t, newModel(), roomsMsg{rooms: []domain.Room{
		{ID: "slack:T1/C1", Name: "general", Broadcasts: []string{"@here", "@channel", "@everyone"}},
		{ID: "slack:T1/D2", Name: "Hela", IsDirect: true},
	}})
	m.timeline.members = []domain.Member{{UserID: "slack:T1.U2", DisplayName: "Hela"}}
	for _, tc := range []struct {
		room  domain.RoomID
		query string
		want  []string
	}{
		{"slack:T1/C1", "", []string{"@here", "@channel", "@everyone", "Hela"}},
		{"slack:T1/C1", "He", []string{"@here", "Hela"}},
		{"slack:T1/C1", "ch", []string{"@channel"}},
		{"slack:T1/D2", "he", []string{"Hela"}},
	} {
		m.openRoom = tc.room
		var got []string
		for _, c := range m.mentionCandidates(tc.query) {
			got = append(got, c.text)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("in %s, @%s offers %q, want %q", tc.room, tc.query, got, tc.want)
		}
	}

	m.openRoom = "slack:T1/C1"
	m.focus, m.compose.insertMode = paneTimeline, true
	m = typeInto(t, m, "hi @he")
	if !m.completion.active || m.completion.trigger != "@" {
		t.Fatalf("typing @he opened %+v, want the mention popup", m.completion)
	}
	m, _ = m.acceptCompletion()
	if got := m.editorFor(fieldComposer).text; got != "hi @here " {
		t.Errorf("composer = %q, want %q", got, "hi @here ")
	}
	if len(m.compose.drafted) != 0 {
		t.Errorf("a broadcast recorded mentions %+v", m.compose.drafted)
	}
}

// `@` offers people by the name the timeline gives them: in a space that keeps first
// names, "Sagi", not "Sagi Chizik"; an identity's or a DM's alias as the timeline has
// it. Never truncated to the column width, since the name goes into the message.
func TestMentionCandidatesAreNamedAsTheTimelineNamesThem(t *testing.T) {
	t.Parallel()
	m := withRooms(t, newModel())
	m.openRoom = mx + "!a:x"
	m.timeline.members = []domain.Member{
		{UserID: "slack:T1.U1", DisplayName: "Sagi Chizik"},
		{UserID: "slack:T1.U2", DisplayName: "Evgeny"},
		{UserID: "matrix:@vanya:x", DisplayName: "ivan.p"},
		{UserID: "matrix:@dm:x", DisplayName: "dm-handle"},
	}
	m.prefs.identities = map[string]resolvedIdentity{"matrix:@vanya:x": {key: "vanya", alias: "Vanya Sokolov"}}
	m.peers.aliases = map[string]string{"matrix:@dm:x": "Dana Mor"}
	m.prefs.display.SpaceRules = []config.SpaceRule{{Space: m.ownSpace(mx + "!a:x"), FirstNameOnly: true}}
	m.prefs.device.MaxNameLength = 3

	var got []string
	for _, c := range m.mentionCandidates("") {
		got = append(got, c.text)
	}
	want := []string{"Sagi", "Evgeny", "Vanya", "Dana"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("@ offers %q, want %q", got, want)
	}
	for _, member := range m.timeline.members {
		_, name := m.personOf(member)
		m.prefs.device.MaxNameLength = 0
		timeline := m.processedName(domain.Message{RoomID: mx + "!a:x", Sender: member.UserID, SenderName: member.DisplayName})
		m.prefs.device.MaxNameLength = 3
		if name != timeline {
			t.Errorf("%s: @ offers %q, the timeline says %q", member.UserID, name, timeline)
		}
	}
}
