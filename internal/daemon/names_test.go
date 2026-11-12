package daemon_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// People named in the tests below, all invented: Svetlana's Telegram account calls
// itself "neke", her bridge puppet is linked to it, Dana is a number the phone book
// names, Bo is the one peer of a direct chat [[display.name]] calls "Bohdan", and Cleo
// is named only by her name in a room.
const (
	svetlana     = "telegram:66919467"
	svetlanaPup  = "matrix:@telegram_66919467:example.org"
	danaNumber   = "whatsapp:15550100001@s.whatsapp.net"
	bo           = "matrix:@bo:example.org"
	cleo         = "matrix:@cleo:example.org"
	inviteRoom   = mx + "!invite:example.org"
	boRoom       = mx + "!bo:example.org"
	knownRoom    = mx + "!names:example.org"
	svetlanaName = "Svetlana"
)

// peopleBackend serves one room's messages, threads, members, hits and a poll's voters,
// the rooms, invitations, and a directory a test may change.
type peopleBackend struct {
	apitest.Nop

	mu      sync.Mutex
	dir     domain.Directory
	rooms   []domain.Room
	members map[domain.RoomID][]domain.Member
	msgs    []domain.Message
	invites []domain.Room
	voters  domain.PollVoters
}

func (b *peopleBackend) Directory(context.Context) (domain.Directory, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dir, nil
}

func (b *peopleBackend) setDirectory(dir domain.Directory) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dir = dir
}

func (b *peopleBackend) Rooms(context.Context) ([]domain.Room, error) { return b.rooms, nil }

func (b *peopleBackend) Selves(context.Context) ([]string, error) { return []string{me}, nil }

func (b *peopleBackend) Members(_ context.Context, room domain.RoomID, _ int) ([]domain.Member, error) {
	return b.members[room], nil
}

// memberName is user's name in room, as the cache's RoomMemberName reads it.
func (b *peopleBackend) memberName(_ context.Context, room domain.RoomID, user string) (string, error) {
	for _, m := range b.members[room] {
		if m.UserID == user {
			return m.DisplayName, nil
		}
	}
	return "", nil
}

func (b *peopleBackend) CachedInvites(context.Context) ([]domain.Room, error) { return b.invites, nil }

func (b *peopleBackend) PollVoters(context.Context, domain.RoomID, domain.EventID) (domain.PollVoters, error) {
	return b.voters, nil
}

func (b *peopleBackend) MentionCandidates(ctx context.Context, room domain.RoomID, limit int) ([]domain.Member, error) {
	return b.Members(ctx, room, limit)
}

func (b *peopleBackend) CachedTimeline(context.Context, domain.RoomID) ([]domain.Message, error) {
	return b.msgs, nil
}

func (b *peopleBackend) Timeline(context.Context, domain.RoomID, string, int) (domain.TimelinePage, error) {
	return domain.TimelinePage{Messages: b.msgs}, nil
}

func (b *peopleBackend) ThreadPage(context.Context, domain.RoomID, domain.EventID, string, int) (domain.TimelinePage, error) {
	return domain.TimelinePage{Messages: b.msgs}, nil
}

func (b *peopleBackend) FetchEvent(_ context.Context, _ domain.RoomID, id domain.EventID) (domain.Message, error) {
	for i := range b.msgs {
		if b.msgs[i].ID == id {
			return b.msgs[i], nil
		}
	}
	return domain.Message{}, nil
}

func (b *peopleBackend) MessagesAround(context.Context, domain.RoomID, domain.EventID, int, int) ([]domain.Message, error) {
	return b.msgs, nil
}

func (b *peopleBackend) ListThreads(context.Context, domain.RoomID) ([]domain.Thread, error) {
	var out []domain.Thread
	for i := range b.msgs {
		m := &b.msgs[i]
		out = append(out, domain.Thread{Root: m.ID, RoomID: m.RoomID, LatestSender: m.Sender, LatestSenderName: m.SenderName})
	}
	return out, nil
}

func (b *peopleBackend) SearchMessages(context.Context, domain.SearchRequest) ([]domain.SearchHit, error) {
	var out []domain.SearchHit
	for i := range b.msgs {
		m := &b.msgs[i]
		out = append(out, domain.SearchHit{RoomID: m.RoomID, EventID: m.ID, Sender: m.Sender, SenderName: m.SenderName, Snippet: m.Body, Mentions: m.Mentions})
	}
	return out, nil
}

// knownWorld is the backend the tests name people in: one message from each, the
// first mentioning Svetlana by her network name; an invitation from Svetlana's puppet
// and one from Cleo; a poll each of them voted in.
func knownWorld() *peopleBackend {
	return &peopleBackend{
		dir: domain.NewDirectory(
			[]domain.PersonName{{Source: "phone", ID: domain.PhoneID("15550100001"), Name: "Dana Levi", Rank: domain.RankSaved}},
			[]domain.PersonLink{{Source: "telegram:bridge", ID: svetlanaPup, Other: svetlana}},
		),
		rooms: []domain.Room{{ID: boRoom, Name: "Bo", IsDirect: true}, {ID: knownRoom, Name: "Names"}},
		members: map[domain.RoomID][]domain.Member{
			boRoom: {{UserID: me, DisplayName: "Me"}, {UserID: bo, DisplayName: "Bo"}},
			knownRoom: {
				{UserID: svetlana, DisplayName: "neke"}, {UserID: svetlanaPup, DisplayName: "neke (Telegram)"},
				{UserID: danaNumber, DisplayName: "+15550100001"}, {UserID: bo, DisplayName: "Bo"}, {UserID: cleo, DisplayName: "Cleo"},
			},
			inviteRoom: {{UserID: cleo, DisplayName: "Cleo"}},
		},
		invites: []domain.Room{
			{ID: mx + "!plans:example.org", Name: "Plans", Membership: domain.MembershipInvite, InvitedBy: svetlanaPup},
			{ID: inviteRoom, Name: "Cleo's", Membership: domain.MembershipInvite, InvitedBy: cleo},
		},
		voters: domain.PollVoters{Voters: []domain.PollVoter{
			{ID: svetlanaPup, Options: []string{"a"}}, {ID: danaNumber, Name: "+15550100001", Options: []string{"a"}},
			{ID: bo, Name: "Bo", Options: []string{"b"}}, {ID: cleo, Options: []string{"b"}},
		}},
		msgs: []domain.Message{
			{
				ID: mx + "$1", RoomID: knownRoom, Sender: svetlana, SenderName: "neke", Body: "hi @neke",
				Mentions: []domain.Mention{{UserID: svetlana, Name: "@neke"}},
			},
			{ID: mx + "$2", RoomID: knownRoom, Sender: svetlanaPup, SenderName: "neke (Telegram)", Body: "bridged", ReplyTo: mx + "$1"},
			{ID: mx + "$3", RoomID: knownRoom, Sender: danaNumber, SenderName: "+15550100001", Body: "from a number"},
			{ID: mx + "$4", RoomID: knownRoom, Sender: bo, SenderName: "Bo", Body: "from the named chat's peer"},
		},
	}
}

// peopleConfig aliases Svetlana's Telegram account and names Bo's chat.
func peopleConfig() config.Config {
	var cfg config.Config
	cfg.Display.Identities = []config.Identity{{Alias: svetlanaName, IDs: []string{svetlana}}}
	cfg.Display.Names = []config.DisplayName{{Target: string(boRoom), Name: "Bohdan"}}
	return cfg
}

// wantNames is what kithd calls each sender of knownWorld.
var wantNames = map[string]string{svetlana: svetlanaName, svetlanaPup: svetlanaName, danaNumber: "Dana Levi", bo: "Bohdan", cleo: "Cleo"}

// servePeople serves b with peopleConfig's names.
func servePeople(t *testing.T, b *peopleBackend) (*daemon.Remote, *harness, *daemon.Names) {
	t.Helper()
	var names *daemon.Names
	h := serve(t, b, func(d *daemon.Daemon) {
		names = daemon.NewNames(b, peopleConfig(), d.Streams.NamesChanged)
		names.UseMemberNames(b.memberName)
		d.Names = names
	})
	r := h.client()
	return r, h, names
}

// Everything kithd sends that names a person carries kithd's name for them: the alias
// (under a linked puppet too), the phone book's, a named chat's, and a mention of
// Svetlana by her network name is "Svetlana" too. The network's own names stay.
func TestKithdNamesPeopleInEverythingItSends(t *testing.T) {
	t.Parallel()
	b := knownWorld()
	r, _, _ := servePeople(t, b)
	ctx := t.Context()

	checkMessages := func(what string, msgs []domain.Message, err error) {
		t.Helper()
		if err != nil || len(msgs) != len(b.msgs) {
			t.Fatalf("%s = %d messages, %v; want %d", what, len(msgs), err, len(b.msgs))
		}
		for i, m := range msgs {
			if m.SenderKnownAs != wantNames[m.Sender] || m.SenderName != b.msgs[i].SenderName {
				t.Errorf("%s: %s known as %q (network: %q), want %q (network: %q)", what, m.Sender, m.SenderKnownAs, m.SenderName, wantNames[m.Sender], b.msgs[i].SenderName)
			}
		}
		if got := msgs[0].Mentions; len(got) != 1 || got[0].KnownAs != svetlanaName || got[0].Name != "@neke" {
			t.Errorf("%s: the mention of Svetlana = %+v, want her known as %q, the text kept", what, got, svetlanaName)
		}
	}
	cached, err := r.CachedTimeline(ctx, knownRoom)
	checkMessages("CachedTimeline", cached, err)
	page, err := r.Timeline(ctx, knownRoom, "", 10)
	checkMessages("Timeline", page.Messages, err)
	page, err = r.ThreadPage(ctx, knownRoom, mx+"$1", "", 10)
	checkMessages("ThreadPage", page.Messages, err)
	around, err := r.MessagesAround(ctx, knownRoom, mx+"$1", 5, 5)
	checkMessages("MessagesAround", around, err)

	// A reply's quote is the message it answers, fetched by ID.
	quoted, err := r.FetchEvent(ctx, knownRoom, b.msgs[1].ReplyTo)
	if err != nil || quoted.SenderKnownAs != svetlanaName {
		t.Errorf("the quoted message's sender is known as %q, %v; want %q", quoted.SenderKnownAs, err, svetlanaName)
	}

	threads, err := r.ListThreads(ctx, knownRoom)
	if err != nil || len(threads) != len(b.msgs) {
		t.Fatalf("ListThreads = %d, %v", len(threads), err)
	}
	for _, th := range threads {
		if th.LatestSenderKnownAs != wantNames[th.LatestSender] {
			t.Errorf("a thread's latest sender %s is known as %q, want %q", th.LatestSender, th.LatestSenderKnownAs, wantNames[th.LatestSender])
		}
	}

	members, err := r.Members(ctx, knownRoom, 10)
	if err != nil || len(members) != 5 {
		t.Fatalf("Members = %+v, %v", members, err)
	}
	for _, m := range members {
		if m.KnownAs != wantNames[m.UserID] {
			t.Errorf("member %s (%q) is known as %q, want %q", m.UserID, m.DisplayName, m.KnownAs, wantNames[m.UserID])
		}
	}

	hits, err := r.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch("from", time.Now()), Rooms: domain.TheseRooms([]domain.RoomID{knownRoom})})
	if err != nil || len(hits) != len(b.msgs) {
		t.Fatalf("SearchMessages = %d hits, %v", len(hits), err)
	}
	for _, hit := range hits {
		if hit.SenderKnownAs != wantNames[hit.Sender] {
			t.Errorf("a hit from %s is known as %q, want %q", hit.Sender, hit.SenderKnownAs, wantNames[hit.Sender])
		}
	}
	if got := hits[0].Mentions; len(got) != 1 || got[0].KnownAs != svetlanaName {
		t.Errorf("a hit's mention of Svetlana = %+v, want her known as %q", got, svetlanaName)
	}

	// An invitation's inviter, by a linked puppet or by their name in the room alone.
	invites, err := r.CachedInvites(ctx)
	if err != nil || len(invites) != len(b.invites) {
		t.Fatalf("CachedInvites = %+v, %v", invites, err)
	}
	for _, inv := range invites {
		if inv.InvitedByKnownAs != wantNames[inv.InvitedBy] {
			t.Errorf("an invitation from %s names them %q, want %q", inv.InvitedBy, inv.InvitedByKnownAs, wantNames[inv.InvitedBy])
		}
	}

	// A poll's voters, whether the network names them or not.
	voters, err := r.PollVoters(ctx, knownRoom, mx+"$1")
	if err != nil || len(voters.Voters) != len(b.voters.Voters) {
		t.Fatalf("PollVoters = %+v, %v", voters, err)
	}
	for i, v := range voters.Voters {
		if v.KnownAs != wantNames[v.ID] || v.Name != b.voters.Voters[i].Name {
			t.Errorf("voter %s (network: %q) is known as %q, want %q", v.ID, v.Name, v.KnownAs, wantNames[v.ID])
		}
	}

	// People a client holds only by ID are named alike when it asks.
	ids := []string{svetlanaPup, danaNumber, bo}
	asked, err := r.KnownAs(ctx, ids)
	if err != nil || len(asked) != len(ids) {
		t.Fatalf("KnownAs = %q, %v", asked, err)
	}
	for i, id := range ids {
		if asked[i] != wantNames[id] {
			t.Errorf("KnownAs(%s) = %q, want %q", id, asked[i], wantNames[id])
		}
	}
}

// Messages streamed as they arrive are named as those read are.
func TestStreamedMessagesAreNamed(t *testing.T) {
	t.Parallel()
	b := knownWorld()
	msgs := make(chan domain.Message, 1)
	b.Msgs = msgs
	r, h, _ := servePeople(t, b)
	go func() { _ = r.Start(context.Background()) }()
	h.waitAttached(1)
	msgs <- b.msgs[0]
	got := take(t, r.Messages(), "the client")
	if got.SenderKnownAs != svetlanaName || len(got.Mentions) != 1 || got.Mentions[0].KnownAs != svetlanaName {
		t.Errorf("streamed %+v, want Svetlana's message and mention known as %q", got, svetlanaName)
	}
}

// Invitations and everyone typing, streamed, are named as the rest of what kithd sends
// names them: typists in typing's order.
func TestStreamedInvitationsAndTypingAreNamed(t *testing.T) {
	t.Parallel()
	b := knownWorld()
	acts, invs := make(chan domain.Activity, 1), make(chan []domain.Room, 1)
	b.Acts, b.Invs = acts, invs
	r, h, _ := servePeople(t, b)
	go func() { _ = r.Start(context.Background()) }()
	h.waitAttached(1)
	invs <- b.invites
	for _, inv := range take(t, r.Invites(), "the client") {
		if inv.InvitedByKnownAs != wantNames[inv.InvitedBy] {
			t.Errorf("a streamed invitation from %s names them %q, want %q", inv.InvitedBy, inv.InvitedByKnownAs, wantNames[inv.InvitedBy])
		}
	}
	typing := []string{cleo, svetlanaPup, danaNumber}
	acts <- domain.Activity{RoomID: knownRoom, Typing: typing}
	got := take(t, r.Activity(), "the client")
	want := []string{"Cleo", svetlanaName, "Dana Levi"}
	if !slices.Equal(got.Typing, typing) || !slices.Equal(got.TypingKnownAs, want) {
		t.Errorf("streamed typing %q known as %q, want %q known as %q", got.Typing, got.TypingKnownAs, typing, want)
	}
}

// waitNamesChanged waits for CHANGE_NAMES on r, failing after settle.
func waitNamesChanged(t *testing.T, r *daemon.Remote) {
	t.Helper()
	deadline := time.After(settle)
	for {
		select {
		case got := <-r.Changed():
			if got == domain.ChangeNames {
				return
			}
		case <-deadline:
			t.Fatal("names changed was never said")
		}
	}
}

// A config reload with a new alias, and a directory write that names someone anew, each
// tell the client names changed; what it reads again carries the new names.
func TestNamesChangedOnReloadAndOnADirectoryWrite(t *testing.T) {
	t.Parallel()
	b := knownWorld()
	r, h, names := servePeople(t, b)
	ctx := t.Context()
	go func() { _ = r.Start(context.Background()) }()
	h.waitAttached(1)
	knownAs := func(user string) string {
		t.Helper()
		members, err := r.Members(ctx, knownRoom, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range members {
			if m.UserID == user {
				return m.KnownAs
			}
		}
		return ""
	}
	if got := knownAs(svetlana); got != svetlanaName {
		t.Fatalf("before: %q, want %q", got, svetlanaName)
	}

	cfg := peopleConfig()
	cfg.Display.Identities[0].Alias = "Sveta"
	names.Use(ctx, cfg)
	waitNamesChanged(t, r)
	if got := knownAs(svetlanaPup); got != "Sveta" {
		t.Errorf("after the reload her puppet is known as %q, want Sveta", got)
	}

	b.setDirectory(domain.NewDirectory(
		[]domain.PersonName{{Source: "phone", ID: domain.PhoneID("15550100001"), Name: "Dana Cohen", Rank: domain.RankSaved}},
		b.dir.Links(),
	))
	go names.Run(ctx)
	names.Changed() // as the cache says after the write
	waitNamesChanged(t, r)
	if got := knownAs(danaNumber); got != "Dana Cohen" {
		t.Errorf("after the directory write Dana is known as %q, want Dana Cohen", got)
	}
}
