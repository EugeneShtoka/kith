package whatsapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

func newsletter(id string) types.JID { return types.NewJID(id, types.NewsletterServer) }

func followed(id, name string, role types.NewsletterRole) *types.NewsletterMetadata {
	m := &types.NewsletterMetadata{ID: newsletter(id)}
	m.ThreadMeta.Name.Text = name
	m.ThreadMeta.Description.Text = name + " news"
	if role != "" {
		m.ViewerMeta = &types.NewsletterViewerMetadata{Role: role}
	}
	return m
}

// The channels an account follows are rooms, named and described as WhatsApp has them;
// one WhatsApp says nothing of the viewer for is followed, not run.
func TestChannelsAreRooms(t *testing.T) {
	t.Parallel()
	rooms, known := channelRooms(ownDigits, []*types.NewsletterMetadata{
		followed("1201", "Weather", ""), nil, followed("1202", "Mine", types.NewsletterRoleOwner),
	})
	if len(rooms) != 2 || rooms[0].Name != "Weather" || rooms[0].Topic != "Weather news" || !isChannel(rooms[0].ID) {
		t.Fatalf("rooms = %+v, want the two channels as rooms", rooms)
	}
	if known[rooms[0].ID].role != types.NewsletterRoleSubscriber || known[rooms[1].ID].role != types.NewsletterRoleOwner {
		t.Errorf("roles = %+v, want a follower and an owner", known)
	}
	if isChannel(roomID(ownDigits, types.NewJID("1203", types.GroupServer))) {
		t.Error("a group counts as a channel")
	}
}

// Only a channel's admins post: a follower's message, file or reaction is refused
// before WhatsApp is asked, as is posting in a channel no listing has described.
// Another account's listing leaves this one's channels as they were.
func TestOnlyAChannelsAdminsPost(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, work := Account{Name: "home", Digits: ownDigits}, Account{Name: "work", Digits: "1500000001"}
	a, _, _ := offline(t, home, work)
	_, homeKnown := channelRooms(home.Digits, []*types.NewsletterMetadata{
		followed("1201", "Weather", types.NewsletterRoleSubscriber), followed("1202", "Mine", types.NewsletterRoleAdmin),
	})
	a.useChannels(home, homeKnown)
	_, workKnown := channelRooms(work.Digits, []*types.NewsletterMetadata{followed("1301", "Theirs", types.NewsletterRoleOwner)})
	a.useChannels(work, workKnown)

	weather, mine := roomID(home.Digits, newsletter("1201")), roomID(home.Digits, newsletter("1202"))
	unknown := roomID(home.Digits, newsletter("1299"))
	for name, err := range map[string]error{
		"Send as a follower":     a.Send(ctx, weather, domain.Draft{Body: "hi"}),
		"SendFile as a follower": a.SendFile(ctx, weather, "/nonexistent", ""),
		"Send in an unknown one": a.Send(ctx, unknown, domain.Draft{Body: "hi"}),
	} {
		if !errors.Is(err, api.ErrNoPower) {
			t.Errorf("%s = %v, want ErrNoPower", name, err)
		}
	}
	if err := a.SendReaction(ctx, mine, "whatsapp:"+ownDigits+"/3EB0", "👍"); !errors.Is(err, api.ErrNotOnNetwork) {
		t.Errorf("SendReaction in a channel = %v, want ErrNotOnNetwork", err)
	}
	if err := a.mayPost(mine); err != nil {
		t.Errorf("an admin may not post: %v", err)
	}
	if err := a.mayPost(roomID(work.Digits, newsletter("1301"))); err != nil {
		t.Errorf("work's own channel, after home's listing: %v", err)
	}

	// home's next listing no longer has Mine: posting there is refused again.
	_, homeKnown = channelRooms(home.Digits, []*types.NewsletterMetadata{followed("1201", "Weather", "")})
	a.useChannels(home, homeKnown)
	if err := a.mayPost(mine); !errors.Is(err, api.ErrNoPower) {
		t.Errorf("a channel dropped from the listing = %v, want refused", err)
	}
	if err := a.mayPost(roomID(work.Digits, newsletter("1301"))); err != nil {
		t.Errorf("work's channel after home's second listing: %v", err)
	}
}

// A channel's post lands in the channel's room, sent by the channel under its name —
// not a direct chat with it — and fetched posts come in the same shape.
func TestAChannelPostIsTheChannels(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, cache, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	jid := newsletter("1201")
	_, known := channelRooms(account.Digits, []*types.NewsletterMetadata{followed("1201", "Weather", "")})
	a.useChannels(account, known)

	posts := channelEvents(jid, []*types.NewsletterMessage{
		{MessageServerID: 7, MessageID: "3EB0C1", Timestamp: time.Unix(1_700_000_000, 0), Message: &waE2E.Message{Conversation: new("rain tomorrow")}},
		{MessageServerID: 8, MessageID: "3EB0C2"}, // a live update's shape: no content
	})
	if len(posts) != 1 || posts[0].Info.Chat != jid || posts[0].Info.ServerID != 7 {
		t.Fatalf("channelEvents = %+v, want the one post with content, from the channel", posts)
	}
	a.onMessage(ctx, account, client, &events.Message{Info: posts[0].Info, Message: posts[0].Message})

	room := roomID(account.Digits, jid)
	msgs, err := cache.Messages(ctx, room, 10)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("cached = (%+v, %v), want the post in the channel's room", msgs, err)
	}
	if got := msgs[0]; got.SenderName != "Weather" || got.Body != "rain tomorrow" {
		t.Errorf("post = %+v, want it from Weather", got)
	}
	rooms, err := a.Rooms(ctx)
	if err != nil || len(rooms) != 1 || rooms[0].ID != room || rooms[0].IsDirect {
		t.Errorf("rooms = (%+v, %v), want the channel and no direct chat", rooms, err)
	}
}
