package telegram

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/gotd/td/tgtest"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// ezra is somebody the account has no chat with, found by @username or number.
var ezra = &tg.User{ID: 9, AccessHash: 90, FirstName: "Ezra", Username: "Ezra", Phone: "15550100009"}

// salsa is a public channel the account is not in.
var salsa = &tg.Channel{ID: 21, AccessHash: 210, Title: "Novo Salsa", Username: "novosalsa", Broadcast: true, Left: true, Photo: &tg.ChatPhotoEmpty{}}

// answering registers a handler that decodes the request into req and replies with
// what reply returns, or its RPC error.
func answering[R bin.Decoder](f *fakeTelegram, id uint32, req R, reply func(R) (bin.Encoder, *tgerr.Error)) {
	var mu sync.Mutex
	f.cluster.Dispatch(2, "dc2").HandleFunc(id, func(s *tgtest.Server, r *tgtest.Request) error {
		mu.Lock()
		defer mu.Unlock()
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		msg, rpcErr := reply(req)
		if rpcErr != nil {
			return s.SendErr(r, rpcErr)
		}
		return sendResult(s, r, msg)
	})
}

// resolvesTo answers @username and phone lookups: ezra by either, salsa by its name.
func resolvesTo(f *fakeTelegram) {
	answering(f, tg.ContactsResolveUsernameRequestTypeID, &tg.ContactsResolveUsernameRequest{},
		func(req *tg.ContactsResolveUsernameRequest) (bin.Encoder, *tgerr.Error) {
			switch strings.ToLower(req.Username) {
			case "ezra":
				return &tg.ContactsResolvedPeer{Peer: &tg.PeerUser{UserID: ezra.ID}, Users: []tg.UserClass{ezra}}, nil
			case "novosalsa":
				return &tg.ContactsResolvedPeer{Peer: &tg.PeerChannel{ChannelID: salsa.ID}, Chats: []tg.ChatClass{salsa}}, nil
			}
			return nil, tgerr.New(400, "USERNAME_NOT_OCCUPIED")
		})
	answering(f, tg.ContactsResolvePhoneRequestTypeID, &tg.ContactsResolvePhoneRequest{},
		func(req *tg.ContactsResolvePhoneRequest) (bin.Encoder, *tgerr.Error) {
			if req.Phone != ezra.Phone {
				return nil, tgerr.New(400, "PHONE_NOT_OCCUPIED")
			}
			return &tg.ContactsResolvedPeer{Peer: &tg.PeerUser{UserID: ezra.ID}, Users: []tg.UserClass{ezra}}, nil
		})
}

// Somebody found by @username or number is a private chat to write in at once: its
// access hash is kept, so the first message reaches them before any listing has the
// chat.
func TestALinkToSomebodyNewIsAChatToWriteIn(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.serveUpdates(&updatesOf{pts: 1})
	resolvesTo(f)
	var mu sync.Mutex
	var sentTo []tg.InputPeerClass
	answering(f, tg.MessagesSendMessageRequestTypeID, &tg.MessagesSendMessageRequest{},
		func(req *tg.MessagesSendMessageRequest) (bin.Encoder, *tgerr.Error) {
			mu.Lock()
			sentTo = append(sentTo, req.Peer)
			mu.Unlock()
			return &tg.UpdateShortSentMessage{ID: 1, Date: int(time.Now().Unix()), Pts: 2, PtsCount: 1}, nil
		})
	st := openStore(t)
	a, _ := loggedInWithStore(t, f, st)
	ctx := t.Context()
	want := domain.Room{ID: "telegram:42/9", Name: "Ezra", IsDirect: true, Handle: "ezra"}
	for _, uri := range []string{"tg://resolve?domain=ezra", "https://t.me/+15550100009"} {
		target, err := a.ResolveLink(ctx, uri)
		if err != nil {
			t.Fatalf("%s: %v", uri, err)
		}
		if target.Join || target.Room.ID != want.ID || target.Room.Name != want.Name || !target.Room.IsDirect || target.Room.Handle != want.Handle {
			t.Errorf("%s = %+v, want a chat with Ezra to write in", uri, target)
		}
	}
	if hash, ok, _ := st.GetUserAccessHash(ctx, 42, ezra.ID); !ok || hash != ezra.AccessHash {
		t.Errorf("Ezra's hash = (%d, %v), want it kept", hash, ok)
	}
	if err := a.Send(ctx, want.ID, domain.Draft{Body: "hi", TxnID: "t1"}); err != nil {
		t.Fatalf("the first message to Ezra: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if p, ok := sentTo[0].(*tg.InputPeerUser); !ok || p.UserID != ezra.ID || p.AccessHash != ezra.AccessHash {
		t.Errorf("sent to %+v, want Ezra by his hash", sentTo[0])
	}
}

// A public channel the account is not in is offered to join, and joining it by its
// @username names its room.
func TestALinkToAChannelNotJoinedAsksAndJoins(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.serveUpdates(&updatesOf{pts: 1})
	resolvesTo(f)
	var joined *tg.InputChannel
	answering(f, tg.ChannelsJoinChannelRequestTypeID, &tg.ChannelsJoinChannelRequest{},
		func(req *tg.ChannelsJoinChannelRequest) (bin.Encoder, *tgerr.Error) {
			joined, _ = req.Channel.(*tg.InputChannel)
			in := *salsa
			in.Left = false
			return &tg.MessagesChatInviteJoinResultOk{Updates: &tg.Updates{Date: 1000, Chats: []tg.ChatClass{&in}}}, nil
		})
	a, _ := loggedInWithStore(t, f, openStore(t))
	ctx := t.Context()
	target, err := a.ResolveLink(ctx, "https://t.me/novosalsa")
	if err != nil {
		t.Fatal(err)
	}
	if !target.Join || target.Room.Name != "Novo Salsa" || target.About != "channel" {
		t.Errorf("target = %+v, want a channel to join", target)
	}
	room, err := a.JoinLink(ctx, "tg://resolve?domain=novosalsa")
	if err != nil {
		t.Fatal(err)
	}
	if room != "telegram:42/-1000000000021" || room != target.Room.ID {
		t.Errorf("joined %q, want the channel's room %q", room, target.Room.ID)
	}
	if joined == nil || joined.ChannelID != salsa.ID || joined.AccessHash != salsa.AccessHash {
		t.Errorf("joined %+v, want the channel by its hash", joined)
	}
	if _, err := a.JoinLink(ctx, "tg://resolve?domain=ezra"); err == nil {
		t.Error("joined a person")
	}
}

// An invite names the chat it is to: one to join (saying what it is), one the account
// is in, or one whose admins approve who joins.
func TestAnInviteLinkIsJoined(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.serveUpdates(&updatesOf{pts: 1})
	answering(f, tg.MessagesCheckChatInviteRequestTypeID, &tg.MessagesCheckChatInviteRequest{},
		func(req *tg.MessagesCheckChatInviteRequest) (bin.Encoder, *tgerr.Error) {
			switch req.Hash {
			case "AlreadyIn":
				return &tg.ChatInviteAlready{Chat: &tg.Chat{ID: 11, Title: "Book club", Photo: &tg.ChatPhotoEmpty{}}}, nil
			case "Approved":
				return &tg.ChatInvite{Title: "Hikers", Megagroup: true, Channel: true, RequestNeeded: true, Photo: &tg.PhotoEmpty{}}, nil
			}
			return &tg.ChatInvite{Title: "Hikers", Megagroup: true, Channel: true, ParticipantsCount: 12, Photo: &tg.PhotoEmpty{}}, nil
		})
	answering(f, tg.MessagesImportChatInviteRequestTypeID, &tg.MessagesImportChatInviteRequest{},
		func(req *tg.MessagesImportChatInviteRequest) (bin.Encoder, *tgerr.Error) {
			switch req.Hash {
			case "Approved":
				return nil, tgerr.New(400, "INVITE_REQUEST_SENT")
			case "AlreadyIn":
				return nil, tgerr.New(400, "USER_ALREADY_PARTICIPANT")
			}
			return &tg.MessagesChatInviteJoinResultOk{Updates: &tg.Updates{Date: 1000, Chats: []tg.ChatClass{
				&tg.Channel{ID: 31, AccessHash: 310, Title: "Hikers", Megagroup: true, Photo: &tg.ChatPhotoEmpty{}},
			}}}, nil
		})
	st := openStore(t)
	a, _ := loggedInWithStore(t, f, st)
	ctx := t.Context()

	target, err := a.ResolveLink(ctx, "https://t.me/+NewOne")
	if err != nil || !target.Join || target.Room.Name != "Hikers" || target.About != "group, 12 members" {
		t.Errorf("an invite to join = (%+v, %v), want Hikers to join", target, err)
	}
	if room, joinErr := a.JoinLink(ctx, "tg://join?invite=NewOne"); joinErr != nil || room != "telegram:42/-1000000000031" {
		t.Errorf("joining = (%q, %v), want Hikers' room", room, joinErr)
	}
	if hash, ok, _ := st.GetChannelAccessHash(ctx, 42, 31); !ok || hash != 310 {
		t.Errorf("the joined channel's hash = (%d, %v), want it kept", hash, ok)
	}

	target, err = a.ResolveLink(ctx, "https://t.me/joinchat/AlreadyIn")
	if err != nil || target.Join || target.Room.ID != "telegram:42/-11" {
		t.Errorf("an invite to a chat the account is in = (%+v, %v), want its room to open", target, err)
	}
	if room, err := a.JoinLink(ctx, "https://t.me/+AlreadyIn"); err != nil || room != "telegram:42/-11" {
		t.Errorf("joining a chat the account is in = (%q, %v), want its room", room, err)
	}

	target, _ = a.ResolveLink(ctx, "https://t.me/+Approved")
	if !strings.Contains(target.About, "approves") {
		t.Errorf("about = %q, want it to say an admin approves", target.About)
	}
	if _, err := a.JoinLink(ctx, "https://t.me/+Approved"); err == nil || !strings.Contains(err.Error(), "approve") {
		t.Errorf("joining where an admin approves = %v, want it to say the request waits", err)
	}
}

// A private channel's link opens it only for members; one not listed is not found.
func TestAPrivateChannelLinkIsNotLookedUp(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.serveUpdates(&updatesOf{pts: 1})
	a, _ := loggedInWithStore(t, f, openStore(t))
	if _, err := a.ResolveLink(t.Context(), "https://t.me/c/12345/7"); err == nil {
		t.Error("a private channel's link resolved")
	}
}

// With several accounts, links are looked up and joined on the same one every time
// (the first by name), so a join goes where the lookup asked it.
func TestLinksUseOneAccount(t *testing.T) {
	t.Parallel()
	a := &Adapter{conns: map[string]*conn{}}
	for i, name := range []string{"work", "home", "zeta", "alpha2"} {
		a.conns[name] = &conn{account: Account{Name: name}, user: int64(i + 1)}
	}
	for range 20 {
		c, err := a.linkConn()
		if err != nil || c.account.Name != "alpha2" {
			t.Fatalf("linkConn = (%q, %v), want alpha2 every time", c.account.Name, err)
		}
	}
	if _, err := (&Adapter{conns: map[string]*conn{"home": {account: Account{Name: "home"}}}}).linkConn(); err == nil {
		t.Error("an account not yet connected was used")
	}
}
