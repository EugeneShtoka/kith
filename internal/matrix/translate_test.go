package matrix

import (
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// evt builds a timeline event with parsed m.room.message content.
func evt(id, room, sender string, ts int64, content *event.MessageEventContent) *event.Event {
	return &event.Event{
		ID:        eventID(id),
		RoomID:    roomID(room),
		Sender:    userID(sender),
		Type:      event.EventMessage,
		Timestamp: ts,
		Content:   event.Content{Parsed: content},
	}
}

func eventID(s string) id.EventID { return id.EventID(s) }
func roomID(s string) id.RoomID   { return id.RoomID(s) }
func userID(s string) id.UserID   { return id.UserID(s) }

// buildMedia is what decides whether a message draws as a chip or as text.
func TestBuildMedia(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		evtType event.Type
		content *event.MessageEventContent
		want    domain.MediaType // "" means no attachment
	}{
		{"text is not media", event.EventMessage, &event.MessageEventContent{MsgType: event.MsgText}, ""},
		{"emote is not media", event.EventMessage, &event.MessageEventContent{MsgType: event.MsgEmote}, ""},
		{"notice is not media", event.EventMessage, &event.MessageEventContent{MsgType: event.MsgNotice}, ""},
		{"image", event.EventMessage, &event.MessageEventContent{MsgType: event.MsgImage}, domain.MediaImage},
		{"video", event.EventMessage, &event.MessageEventContent{MsgType: event.MsgVideo}, domain.MediaVideo},
		{"audio", event.EventMessage, &event.MessageEventContent{MsgType: event.MsgAudio}, domain.MediaAudio},
		{"file", event.EventMessage, &event.MessageEventContent{MsgType: event.MsgFile}, domain.MediaFile},
		// A sticker has no msgtype; the event type says it is a picture.
		{"sticker", event.EventSticker, &event.MessageEventContent{}, domain.MediaImage},
		{"message with no msgtype", event.EventMessage, &event.MessageEventContent{}, ""},
	} {
		got := buildMedia(tc.evtType, tc.content)
		if tc.want == "" {
			if got != nil {
				t.Errorf("%s: got %+v, want no attachment", tc.name, got)
			}
			continue
		}
		if got == nil {
			t.Errorf("%s: got no attachment, want %s", tc.name, tc.want)
		} else if got.Type != tc.want {
			t.Errorf("%s: type = %s, want %s", tc.name, got.Type, tc.want)
		}
	}
}

// Regression: stickers (own event type, no msgtype, often no body) used to vanish.
func TestToDomainMessageKeepsASticker(t *testing.T) {
	t.Parallel()

	sticker := evt("$s", "!r:x", "@alice:x", 1700000000000, &event.MessageEventContent{
		Body: "🎉",
		URL:  "mxc://x/sticker",
		Info: &event.FileInfo{MimeType: "image/webp", Width: 512, Height: 512},
	})
	sticker.Type = event.EventSticker

	got, ok := toDomainMessage(sticker)
	if !ok {
		t.Fatal("a sticker was dropped — it renders as no row at all")
	}
	if got.Media == nil {
		t.Fatal("a sticker came back with no attachment, so nothing would be drawn")
	}
	if got.Media.Type != domain.MediaImage {
		t.Errorf("media type = %s, want %s", got.Media.Type, domain.MediaImage)
	}
	if got.Media.Mime != "image/webp" || got.Media.Width != 512 {
		t.Errorf("media = %+v, want the info from the event", got.Media)
	}
	if got.Body != "🎉" {
		t.Errorf("body = %q, want the sticker's alt text kept", got.Body)
	}

	wordless := evt("$s2", "!r:x", "@alice:x", 1700000000001, &event.MessageEventContent{
		URL: "mxc://x/sticker2",
	})
	wordless.Type = event.EventSticker
	if _, ok := toDomainMessage(wordless); !ok {
		t.Error("a sticker with no alt text was dropped")
	}
}

// The name comes from filename (MSC2530), falling back to body only without one.
func TestBuildMediaNamesTheFileNotTheCaption(t *testing.T) {
	t.Parallel()

	withCaption := buildMedia(event.EventMessage, &event.MessageEventContent{
		MsgType: event.MsgImage, Body: "look at this", FileName: "cat.png",
	})
	if withCaption == nil || withCaption.Name != "cat.png" {
		t.Errorf("got %+v, want the file name rather than the caption", withCaption)
	}
	legacy := buildMedia(event.EventMessage, &event.MessageEventContent{MsgType: event.MsgImage, Body: "cat.png"})
	if legacy == nil || legacy.Name != "cat.png" {
		t.Errorf("got %+v, want the body used as the name when there is no filename", legacy)
	}
}

// m.typing is a complete set; its absence means unchanged.
func TestTypingIn(t *testing.T) {
	t.Parallel()

	self := userID("@me:x")
	typing := func(users ...string) *event.Event {
		ids := make([]id.UserID, len(users))
		for i, u := range users {
			ids[i] = userID(u)
		}
		return &event.Event{
			Type:    event.EphemeralEventTyping,
			Content: event.Content{Parsed: &event.TypingEventContent{UserIDs: ids}},
		}
	}

	if _, present := typingIn(nil, self); present {
		t.Error("no events reported as 'nobody is typing'; the indicator would flicker off")
	}
	other := &event.Event{Type: event.EphemeralEventReceipt}
	if _, present := typingIn([]*event.Event{other, nil}, self); present {
		t.Error("a batch with no m.typing was read as one")
	}

	users, present := typingIn([]*event.Event{typing("@alice:x", "@me:x")}, self)
	if !present {
		t.Fatal("an m.typing event was not seen")
	}
	if len(users) != 1 || users[0] != "@alice:x" {
		t.Errorf("typing = %v, want just Alice — we are not news to ourselves", users)
	}

	users, present = typingIn([]*event.Event{typing("@alice:x"), typing()}, self)
	if !present || len(users) != 0 {
		t.Errorf("typing = %v (present=%v), want the later empty set to win", users, present)
	}
}

func TestRankMembers(t *testing.T) {
	t.Parallel()

	all := []domain.Member{
		{UserID: "@alice:x"}, {UserID: "@bob:x"}, {UserID: "@carol:x"}, {UserID: "@dave:x"},
	}
	byID := map[string]domain.Member{}
	for _, m := range all {
		byID[m.UserID] = m
	}
	got := rankMembers(all, byID,
		[]string{"@carol:x", "@ghost:x"}, // a past speaker who has left the room
		[]string{"@bob:x", "@carol:x"})   // already placed on the rung above

	want := []string{"@carol:x", "@bob:x", "@alice:x", "@dave:x"}
	if len(got) != len(want) {
		t.Fatalf("got %d members, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].UserID != want[i] {
			t.Errorf("position %d = %s, want %s", i, got[i].UserID, want[i])
		}
	}
}

func TestRankMembersWithNoHistory(t *testing.T) {
	t.Parallel()

	all := []domain.Member{{UserID: "@alice:x"}, {UserID: "@bob:x"}}
	got := rankMembers(all, map[string]domain.Member{}, nil)
	if len(got) != 2 || got[0].UserID != "@alice:x" {
		t.Errorf("got %+v, want everyone in the order they arrived", got)
	}
}

func TestCapMembers(t *testing.T) {
	t.Parallel()

	members := []domain.Member{{UserID: "@a:x"}, {UserID: "@b:x"}, {UserID: "@c:x"}}
	for _, tc := range []struct {
		limit, want int
	}{{0, 3}, {-1, 3}, {2, 2}, {3, 3}, {10, 3}} {
		if got := capMembers(members, tc.limit); len(got) != tc.want {
			t.Errorf("capMembers(limit=%d) kept %d, want %d", tc.limit, len(got), tc.want)
		}
	}
}

// A room's totals include what is unread inside its threads.
func TestWithThreadsAddsTheirShare(t *testing.T) {
	t.Parallel()

	got := withThreads(domain.Unread{Messages: 2, Mentions: 1}, []domain.ThreadUnread{
		{Root: "$a", Unread: 3, Mentions: 1},
		{Root: "$b", Unread: 1},
	})
	if got.Messages != 6 {
		t.Errorf("messages = %d, want 2 + 3 + 1", got.Messages)
	}
	if got.Mentions != 2 {
		t.Errorf("mentions = %d, want 1 + 1", got.Mentions)
	}
	if len(got.Threads) != 2 {
		t.Errorf("threads = %+v, want both attached", got.Threads)
	}
}

func TestWithThreadsOfNone(t *testing.T) {
	t.Parallel()

	got := withThreads(domain.Unread{Messages: 2}, nil)
	if got.Messages != 2 || len(got.Threads) != 0 {
		t.Errorf("got %+v, want the unread state untouched", got)
	}
}

// A reorder is a change: threads come newest-activity-first.
func TestSameThreadUnread(t *testing.T) {
	t.Parallel()

	base := []domain.ThreadUnread{{Root: "$a", Unread: 2, Mentions: 1}, {Root: "$b", Unread: 1}}
	for _, tc := range []struct {
		name string
		with []domain.ThreadUnread
		want bool
	}{
		{"identical", []domain.ThreadUnread{{Root: "$a", Unread: 2, Mentions: 1}, {Root: "$b", Unread: 1}}, true},
		{"both empty", nil, false},
		{"one fewer", base[:1], false},
		{"a different count", []domain.ThreadUnread{{Root: "$a", Unread: 3, Mentions: 1}, {Root: "$b", Unread: 1}}, false},
		{"a different mention count", []domain.ThreadUnread{{Root: "$a", Unread: 2}, {Root: "$b", Unread: 1}}, false},
		{"a different thread", []domain.ThreadUnread{{Root: "$c", Unread: 2, Mentions: 1}, {Root: "$b", Unread: 1}}, false},
		{"reordered", []domain.ThreadUnread{{Root: "$b", Unread: 1}, {Root: "$a", Unread: 2, Mentions: 1}}, false},
	} {
		if got := sameThreadUnread(base, tc.with); got != tc.want {
			t.Errorf("%s: sameThreadUnread = %v, want %v", tc.name, got, tc.want)
		}
	}
	if !sameThreadUnread(nil, nil) {
		t.Error("two rooms with no threads differ")
	}
}
