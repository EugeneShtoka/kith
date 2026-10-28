package instagram

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mau.fi/util/jsontime"

	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// The account, two people and a group, invented: FBIDs, IG user IDs, chat IDs.
const (
	self      = "1000"
	danaFBID  = 2000
	danaIGID  = "92000"
	oferFBID  = 3000
	danaChat  = "340000000000000001"
	danaLong  = "3400000000000000000000000000000001"
	groupChat = "340000000000000002"
)

var t0 = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

func ms(d time.Duration) jsontime.UnixMilliString { return jsontime.UnixMilliString{Time: t0.Add(d)} }

func text(id string, sender int64, at time.Duration, body string) *slidetypes.Message {
	return &slidetypes.Message{
		ID: id, SenderFBID: sender, TimestampMS: ms(at),
		Content: slidetypes.MessageContentWrapper{TypeName: "SlideMessageText", Content: &slidetypes.MessageContentText{TextBody: body}},
	}
}

func user(fb int64, ig, full, username string) *slidetypes.User {
	return &slidetypes.User{InteropMessagingUserFBID: fb, ID: ig, FullName: full, Username: username}
}

// rig is the adapter with a cache and one connection, offline (no instameow client).
type rig struct {
	t     *testing.T
	a     *Adapter
	c     *conn
	cache *db.Cache
}

func newRig(t *testing.T) *rig {
	t.Helper()
	cache, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "cache.db"), make([]byte, sealeddb.KeyLen))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	a := New(cache, newSecrets(), nil, nil)
	cookies, err := parseCookies(header)
	if err != nil {
		t.Fatal(err)
	}
	c := &conn{
		a: a, account: Account{Name: "me"}, self: self, stop: func() {}, done: make(chan struct{}), inbox: newInbox(),
		session: &Session{Cookies: cookies, Self: self},
	}
	close(c.done) // nothing runs: the rig drives the connection's handlers itself
	a.conns["me"] = c
	return &rig{t: t, a: a, c: c, cache: cache}
}

// thread lists a thread, as the inbox does when connecting.
func (r *rig) thread(t *slidetypes.ThreadInfo) {
	r.a.takeThread(context.Background(), r.c, nil, t)
}

func (r *rig) delta(chat string, e slidetypes.DeltaEvent) {
	r.a.takeDelta(context.Background(), r.c, nil, &slidetypes.Delta{ThreadIGID: chat, Data: e})
}

func (r *rig) room(chat string) domain.Room {
	r.t.Helper()
	rooms, err := r.cache.Rooms(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	for i := range rooms {
		if rooms[i].ID == roomID(self, chat) {
			return rooms[i]
		}
	}
	r.t.Fatalf("no room for %s", chat)
	return domain.Room{}
}

func (r *rig) messages(chat string) []domain.Message {
	r.t.Helper()
	msgs, err := r.cache.Messages(context.Background(), roomID(self, chat), 100)
	if err != nil {
		r.t.Fatal(err)
	}
	return msgs
}

func (r *rig) unread(chat string) int {
	r.t.Helper()
	n, _, _, err := r.cache.CountUnread(context.Background(), []string{person(self)}, roomID(self, chat))
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

func dm() *slidetypes.ThreadInfo {
	return &slidetypes.ThreadInfo{
		ID: danaChat, ThreadID: danaLong, Viewer: user(1000, "91000", "Eve", "eve"),
		Users: []*slidetypes.User{user(danaFBID, danaIGID, "Dana Levi", "dana")},
	}
}

func group() *slidetypes.ThreadInfo {
	return &slidetypes.ThreadInfo{
		ID: groupChat, ThreadID: "long-2", IsGroup: true, ThreadTitle: "Hikers",
		Users: []*slidetypes.User{user(danaFBID, danaIGID, "Dana Levi", "dana"), user(oferFBID, "93000", "", "ofer")},
	}
}

// Threads become rooms: one to one by the other side's name, a group by its title (else
// its people's names), with their people as members and named in the directory.
func TestThreadsBecomeRooms(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.thread(dm())
	r.thread(group())
	untitled := group()
	untitled.ID, untitled.ThreadTitle = "340000000000000003", ""
	r.thread(untitled)
	if room := r.room(danaChat); room.Name != "Dana Levi" || !room.IsDirect {
		t.Errorf("the DM = %+v", room)
	}
	if room := r.room(groupChat); room.Name != "Hikers" || room.IsDirect {
		t.Errorf("the group = %+v", room)
	}
	if room := r.room(untitled.ID); room.Name != "Dana Levi, ofer" {
		t.Errorf("an untitled group = %q", room.Name)
	}
	members, err := r.cache.Members(context.Background(), roomID(self, groupChat), 0)
	if err != nil || len(members) != 2 {
		t.Errorf("members = %+v, %v", members, err)
	}
}

// The account's read position comes from Instagram's read watermark; a thread marked
// unread stays unread.
func TestReadPositionsComeFromTheInbox(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	early := dm()
	early.SlideReadReceipts = []slidetypes.ReadReceipt{{ParticipantFBID: 1000, WatermarkTimestampMS: ms(30 * time.Second)}}
	r.thread(early)
	for i, at := range []time.Duration{time.Minute, 2 * time.Minute, 3 * time.Minute} {
		r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m"+string(rune('1'+i)), danaFBID, at, "hi")})
	}
	if n := r.unread(danaChat); n != 3 {
		t.Fatalf("unread before = %d", n)
	}
	read := dm()
	read.SlideReadReceipts = []slidetypes.ReadReceipt{{ParticipantFBID: 1000, WatermarkTimestampMS: ms(2 * time.Minute)}}
	r.thread(read)
	if n := r.unread(danaChat); n != 1 {
		t.Errorf("unread after the watermark = %d, want 1", n)
	}
}

// Live: a message is cached and announced; one of the account's (from another device)
// reads its thread; edits, unsends (even before the message), reactions (replaced,
// removed, by log ID), reads and threads deleted reach the cache.
func TestDeltasReachTheCache(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	early := dm()
	early.SlideReadReceipts = []slidetypes.ReadReceipt{{ParticipantFBID: 1000, WatermarkTimestampMS: ms(30 * time.Second)}}
	r.thread(early)
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m1", danaFBID, time.Minute, "lunch at 1?")})
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m2", danaFBID, 2*time.Minute, "or 2?")})
	if got := <-r.a.Messages(); got.Body != "lunch at 1?" || got.Sender != person("2000") {
		t.Errorf("announced %+v", got)
	}
	if n := r.unread(danaChat); n != 2 {
		t.Errorf("unread before my answer = %d", n)
	}
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m3", 1000, 3*time.Minute, "2 is fine")})
	if n := r.unread(danaChat); n != 0 {
		t.Errorf("unread after my answer = %d", n)
	}

	edit := &slidetypes.EditMessageEvent{MessageID: "m1", TextBody: "lunch at 12?"}
	edit.SlideEditHistoryEntry.TimestampMS = ms(4 * time.Minute)
	r.delta(danaChat, edit)
	r.delta(danaChat, &slidetypes.DeleteMessageEvent{MessageID: "m2"})
	r.delta(danaChat, &slidetypes.DeleteMessageEvent{MessageID: "m9"})
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m9", danaFBID, 5*time.Minute, "unsent before it came")})
	msgs := r.messages(danaChat)
	byID := map[string]domain.Message{}
	for _, m := range msgs {
		native, _ := messageOf(m.ID)
		byID[native] = m
	}
	if m := byID["m1"]; m.Body != "lunch at 12?" || !m.Edited {
		t.Errorf("edited = %+v", m)
	}
	for _, id := range []string{"m2", "m9"} {
		if m := byID[id]; !m.Redacted || m.Body != "" {
			t.Errorf("%s not unsent: %+v", id, m)
		}
	}

	react := func(emoji string, sender int64, log string) *slidetypes.CreateReactionEvent {
		return &slidetypes.CreateReactionEvent{MessageID: "m1", Reaction: slidetypes.Reaction{Reaction: emoji, SenderFBID: sender, LogMessageID: log}}
	}
	r.delta(danaChat, react("👍", 1000, "l1"))
	r.delta(danaChat, react("❤️", 1000, "l2"))
	r.delta(danaChat, react("😂", danaFBID, "l3"))
	r.delta(danaChat, &slidetypes.DeleteReactionEvent{MessageID: "m1", Reaction: slidetypes.Reaction{LogMessageID: "l3"}})
	rs, err := r.cache.Reactions(context.Background(), roomID(self, danaChat))
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].Key != "❤️" || rs[0].Sender != person(self) {
		t.Errorf("reactions = %+v, want my ❤️ alone", rs)
	}
	// Clients hear each change: my 👍 taken off as my ❤️ replaced it, Dana's 😂 off.
	var stream []string
	for len(r.a.Reactions()) > 0 {
		u := <-r.a.Reactions()
		sign := "+"
		if u.Removed {
			sign = "-"
		}
		stream = append(stream, sign+u.Reaction.Key)
	}
	if want := []string{"+👍", "-👍", "+❤️", "+😂", "-😂"}; !slices.Equal(stream, want) {
		t.Errorf("reaction stream = %v, want %v", stream, want)
	}

	r.delta(danaChat, &slidetypes.MarkUnreadEvent{MarkedAsUnread: true})
	r.delta(danaChat, &slidetypes.DeleteThreadEvent{})
	rooms, _ := r.a.Rooms(context.Background())
	for i := range rooms {
		if rooms[i].ID == roomID(self, danaChat) {
			t.Error("a thread deleted for the account is still a room")
		}
	}
}

// Typing names the long thread ID and an IG user ID: it shows on the thread's room as
// the person, until they stop.
func TestTypingShowsWhoTypes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.thread(dm())
	r.a.takeTyping(r.c, &slidetypes.TypingNotification{ThreadID: danaLong, SenderID: 92000, ActivityStatus: 1})
	r.a.takeTyping(r.c, &slidetypes.TypingNotification{ThreadID: danaLong, SenderID: 92000, ActivityStatus: 0})
	var seen []domain.Activity
	for len(seen) < 2 {
		select {
		case act := <-r.a.Activity():
			seen = append(seen, act)
		case <-time.After(3 * time.Second):
			t.Fatalf("typing seen: %+v", seen)
		}
	}
	first, second := seen[0], seen[1]
	if first.RoomID != roomID(self, danaChat) || !slices.Equal(first.Typing, []string{person("2000")}) || len(second.Typing) != 0 {
		t.Errorf("typing = %+v then %+v", first, second)
	}
}

// Names the account told the directory before stay when it writes them again before
// the inbox is listed (each write replaces all of them).
func TestNamesFromBeforeStay(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	if err := r.cache.SetPeople(ctx, "instagram:"+self, []domain.PersonName{{ID: person("3000"), Name: "Ofer", Rank: domain.RankChosen}}, nil); err != nil {
		t.Fatal(err)
	}
	r.a.seed(ctx, r.c)
	r.thread(dm()) // a new name: everything is written again
	dir, err := r.cache.Directory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range dir.Names() {
		if n.Source == "instagram:"+self {
			names = append(names, n.Name)
		}
	}
	if !slices.Contains(names, "Ofer") || !slices.Contains(names, "Dana Levi") {
		t.Errorf("names = %v, want Ofer kept beside Dana", names)
	}
}

// Messages read as kith shows them: text and mentions of the account, replies, media
// described, a shared post as its text, admin notes hidden.
func TestMessagesAsKithShowsThem(t *testing.T) {
	t.Parallel()
	mention := text("m1", danaFBID, 0, "@eve look")
	mention.Mentions = slidetypes.MentionList{{Offset: 0, Length: 4, UserFBID: 1000}}
	reply := text("m2", danaFBID, 0, "yes")
	reply.RepliedToMessageID = "m1"
	photo := &slidetypes.Message{ID: "m3", Content: slidetypes.MessageContentWrapper{Content: &slidetypes.MessageContentImage{
		Attachments: []*slidetypes.Attachment{{PreviewWidth: 720, PreviewHeight: 960}, {}},
	}}}
	post := &slidetypes.Message{ID: "m5", Content: slidetypes.MessageContentWrapper{Content: &slidetypes.MessageContentXMA{
		XMA: &slidetypes.XMAContent{TitleText: "dana", CaptionBodyText: "sunset", TargetURL: "https://www.instagram.com/p/x/"},
	}}}
	admin := &slidetypes.Message{ID: "m6", Content: slidetypes.MessageContentWrapper{Content: &slidetypes.MessageContentAdminText{}}}

	if m, _, _ := message(self, danaChat, mention); !m.Mentioned || m.Body != "@eve look" {
		t.Errorf("mention = %+v", m)
	}
	if m, _, _ := message(self, danaChat, reply); m.ReplyTo != eventID(self, danaChat, "m1") {
		t.Errorf("reply = %+v", m)
	}
	if m, _, _ := message(self, danaChat, photo); m.Media == nil || m.Media.Type != domain.MediaImage || m.Media.Width != 720 || !strings.Contains(m.Body, "+1") {
		t.Errorf("photo = %+v", m)
	}
	if m, _, _ := message(self, danaChat, post); !strings.Contains(m.Body, "sunset") || !strings.Contains(m.Body, "/p/x/") {
		t.Errorf("shared post = %q", m.Body)
	}
	if _, _, shown := message(self, danaChat, admin); shown {
		t.Error("an admin note is shown")
	}
}

// History opens on the cache: a page of it, with where older history starts.
func TestHistoryOpensOnTheCache(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.thread(dm())
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m1", danaFBID, time.Minute, "one")})
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m2", danaFBID, 2*time.Minute, "two")})
	page, err := r.a.Timeline(context.Background(), roomID(self, danaChat), "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 || page.Next != olderThan+"m1" {
		t.Errorf("page = %d messages, next %q", len(page.Messages), page.Next)
	}
}

// A message of the account's (written on another device) reads its thread up to there,
// so a thread kith knew no read position of is counted from it.
func TestAnOwnMessageReadsItsThread(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.thread(dm())
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m1", danaFBID, time.Minute, "one")})
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m2", 1000, 2*time.Minute, "answered")})
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m3", danaFBID, 3*time.Minute, "two")})
	n, _, counted, err := r.cache.CountUnread(context.Background(), []string{person(self)}, roomID(self, danaChat))
	if err != nil || !counted || n != 1 {
		t.Errorf("unread = %d (counted %v, %v), want only the message after mine", n, counted, err)
	}
}

// A signed-in account is a space holding its rooms, read from the cache: unread
// counts, members, a message and its versions, no encryption; marking read reads it in
// kith; signing out ends the session, keeps or forgets the rooms, and the space goes.
func TestSignedInAccountsAreSpaces(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	if err := saveSession(r.a.secrets, "me", r.c.session); err != nil {
		t.Fatal(err)
	}
	r.a.UseConfig(ctx, withAccounts("me"))
	early := dm()
	early.SlideReadReceipts = []slidetypes.ReadReceipt{{ParticipantFBID: 1000, WatermarkTimestampMS: ms(30 * time.Second)}}
	r.thread(early)
	r.thread(group())
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m1", danaFBID, time.Minute, "hi")})
	room := roomID(self, danaChat)

	spaces, err := r.a.Spaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(spaces) != 1 || spaces[0].Name != "Instagram me" || len(spaces[0].Children) != 2 || spaces[0].ID != accountSpaceID(self) {
		t.Fatalf("spaces = %+v", spaces)
	}
	if parent, _ := r.a.CanonicalParent(ctx, room); parent != accountSpaceID(self) {
		t.Errorf("parent = %s", parent)
	}
	unread, err := r.a.CachedUnread(ctx)
	if err != nil || len(unread) == 0 {
		t.Fatalf("unread = %+v, %v", unread, err)
	}
	if members, _ := r.a.MentionCandidates(ctx, roomID(self, groupChat), 10); len(members) != 2 {
		t.Errorf("mention candidates = %+v", members)
	}
	if enc, _ := r.a.RoomEncryption(ctx, []domain.RoomID{room}); enc[room] {
		t.Error("an Instagram room counts as encrypted")
	}
	msg, err := r.a.FetchEvent(ctx, room, eventID(self, danaChat, "m1"))
	if err != nil || msg.Body != "hi" {
		t.Errorf("fetch = %+v, %v", msg, err)
	}
	if _, err := r.a.FetchEvent(ctx, room, eventID(self, danaChat, "nope")); err == nil {
		t.Error("an uncached message was found")
	}
	if _, _, err := r.a.MessageHistory(ctx, room, msg.ID); err != nil {
		t.Error(err)
	}
	if _, err := r.a.MarkRoomsRead(ctx, []domain.RoomID{room}, false); err != nil {
		t.Fatal(err)
	}
	if n := r.unread(danaChat); n != 0 {
		t.Errorf("unread after reading = %d", n)
	}
	if err := r.a.MarkRoomUnread(ctx, room, true); err == nil {
		t.Error("marking unread claimed to work")
	}

	if err := r.a.SignOut(ctx, "instagram:nobody/account", false); err == nil {
		t.Error("signed out of no account")
	}
	if err := r.a.SignOut(ctx, accountSpaceID(self), true); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := loadSession(r.a.secrets, "me"); ok {
		t.Error("the session outlived signing out")
	}
	if rooms, _ := r.a.Rooms(ctx); len(rooms) != 0 {
		t.Errorf("rooms after signing out and forgetting = %d", len(rooms))
	}
	if spaces, _ = r.a.Spaces(ctx); len(spaces) != 0 {
		t.Errorf("spaces after signing out = %+v", spaces)
	}
}
