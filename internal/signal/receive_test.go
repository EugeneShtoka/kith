package signal_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/signal"
)

// The account's and two people's ACIs, invented.
const (
	me    = "11111111-1111-4111-8111-111111111111"
	dana  = "22222222-2222-4222-8222-222222222222"
	ofer  = "33333333-3333-4333-8333-333333333333"
	other = "44444444-4444-4444-8444-444444444444" // linked, but no configured account's
)

// A group's ID as Signal gives it (std base64, with a "/" and padding).
var groupID = base64.StdEncoding.EncodeToString([]byte{0xfb, 0xff, 0x01, 0x02, 0x03, 0x04, 0x05})

func msg(chat, sender string, sent uint64, body string) *signalv1.EventsResponse {
	return &signalv1.EventsResponse{Event: &signalv1.EventsResponse_Message{Message: &signalv1.Message{
		Chat: chat, Sender: sender, Sent: sent, Body: body,
	}}}
}

func named(ev *signalv1.EventsResponse, sender, chat string) *signalv1.EventsResponse {
	ev.GetMessage().SenderName, ev.GetMessage().ChatName = sender, chat
	return ev
}

func inGroup(ev *signalv1.EventsResponse) *signalv1.EventsResponse {
	ev.GetMessage().Group = true
	return ev
}

// sql runs a statement on the cache's file behind its back (a fault to inject),
// waiting for the adapter's own writes as the cache does.
func (r *rig) sql(stmt string) {
	r.t.Helper()
	raw, err := sql.Open("sqlite", "file:"+r.cachePath+"?_pragma=busy_timeout(10000)")
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.ExecContext(context.Background(), stmt); err != nil {
		r.t.Fatal(err)
	}
}

// acked is every played event acked so far, in order.
func (r *rig) acked() []string {
	b, _ := os.ReadFile(r.acks)
	text := strings.TrimSpace(string(b))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func (r *rig) room(id domain.RoomID) (domain.Room, bool) {
	rooms, err := r.cache.Rooms(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	for i := range rooms {
		if rooms[i].ID == id {
			return rooms[i], true
		}
	}
	return domain.Room{}, false
}

func (r *rig) messages(id domain.RoomID) []domain.Message {
	msgs, err := r.cache.Messages(context.Background(), id, 100)
	if err != nil {
		r.t.Fatal(err)
	}
	return msgs
}

func direct(chat string) domain.RoomID { return domain.RoomID("signal:" + me + "/" + chat) }

// Messages in, both ways and in groups: each lands in its chat's room, named from what
// the account knows, with its people; and each is acked only once cached.
func TestMessagesAreCachedThenAcked(t *testing.T) {
	group := &signalv1.EventsResponse{Event: &signalv1.EventsResponse_Group{Group: &signalv1.Group{
		Id: groupID, Title: "Hikers", Members: []string{me, dana, ofer},
	}}}
	reply := msg(dana, dana, 1700000000300, "and you?")
	reply.GetMessage().Quote = &signalv1.Quote{Author: me, Sent: 1700000000200}
	r := newRigPlaying(t, "ok", me, me, []*signalv1.EventsResponse{
		named(msg(dana, dana, 1700000000100, "hi"), "Dana", "Dana"),
		named(msg(dana, me, 1700000000200, "hello"), "", "Dana"),
		reply,
		group,
		inGroup(named(msg(groupID, ofer, 1700000000400, "trail?"), "Ofer", "")),
	})
	r.until(10*time.Second, "every event acked", func() bool { return len(r.acked()) == 5 })

	room, ok := r.room(direct(dana))
	if !ok || room.Name != "Dana" || !room.IsDirect {
		t.Errorf("Dana's room = %+v (cached %v)", room, ok)
	}
	got := r.messages(direct(dana))
	var bodies []string
	for _, m := range got {
		bodies = append(bodies, m.Body)
	}
	if !slices.Equal(bodies, []string{"hi", "hello", "and you?"}) {
		t.Fatalf("Dana's messages = %v", bodies)
	}
	if got[0].ID != domain.EventID("signal:"+me+"/"+dana+"/"+dana+".1700000000100") || got[0].Sender != "signal:"+dana {
		t.Errorf("first = %+v", got[0])
	}
	if got[2].ReplyTo != got[1].ID {
		t.Errorf("the reply's target = %s, want %s", got[2].ReplyTo, got[1].ID)
	}

	// The group's ID is base64url in kith's: its "/" would split the ID.
	gid := domain.RoomID("signal:" + me + "/" + base64.RawURLEncoding.EncodeToString([]byte{0xfb, 0xff, 0x01, 0x02, 0x03, 0x04, 0x05}))
	room, ok = r.room(gid)
	if !ok || room.Name != "Hikers" || room.IsDirect {
		t.Errorf("the group = %+v (cached %v)", room, ok)
	}
	if msgs := r.messages(gid); len(msgs) != 1 || msgs[0].Body != "trail?" || msgs[0].SenderName != "Ofer" {
		t.Errorf("the group's messages = %+v", msgs)
	}
	members, err := r.cache.Members(context.Background(), gid, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Errorf("the group's members (but me) = %+v", members)
	}
}

// A message sent from the account's phone reads its chat up to there: what came before
// is not unread.
func TestAnOwnMessageReadsItsChat(t *testing.T) {
	r := newRigPlaying(t, "ok", me, me, []*signalv1.EventsResponse{
		msg(dana, dana, 1700000000100, "one"),
		msg(dana, dana, 1700000000200, "two"),
		msg(dana, me, 1700000000300, "answered"),
		msg(dana, dana, 1700000000400, "three"),
	})
	r.until(10*time.Second, "every event acked", func() bool { return len(r.acked()) == 4 })
	n, _, counted, err := r.cache.CountUnread(context.Background(), r.a.Me(), direct(dana))
	if err != nil {
		t.Fatal(err)
	}
	if !counted || n != 1 {
		t.Errorf("unread = %d (counted %v), want only the message after mine", n, counted)
	}
}

// The phone's contacts name the chats they are; a mention of the account names it and
// marks the message.
func TestContactsAndMentions(t *testing.T) {
	mention := inGroup(msg(groupID, dana, 1700000000200, "ask ￼ about it"))
	mention.GetMessage().Mentions = []*signalv1.Mention{{Start: 4, Length: 1, Aci: me, Name: "Eve"}}
	r := newRigPlaying(t, "ok", me, me, []*signalv1.EventsResponse{
		msg(dana, dana, 1700000000100, "hi"),
		{Event: &signalv1.EventsResponse_Contacts{Contacts: &signalv1.Contacts{Contacts: []*signalv1.Contact{
			{Aci: dana, Name: "Dana Levi", Number: "+15550002"},
		}}}},
		mention,
	})
	r.until(10*time.Second, "every event acked", func() bool { return len(r.acked()) == 3 })
	if room, _ := r.room(direct(dana)); room.Name != "Dana Levi" {
		t.Errorf("after the contacts, Dana's room is %q", room.Name)
	}
	gid := domain.RoomID("signal:" + me + "/" + base64.RawURLEncoding.EncodeToString([]byte{0xfb, 0xff, 0x01, 0x02, 0x03, 0x04, 0x05}))
	msgs := r.messages(gid)
	if len(msgs) != 1 || msgs[0].Body != "ask @Eve about it" || !msgs[0].Mentioned {
		t.Errorf("the mention = %+v", msgs)
	}
}

// What cannot be cached is not acked, so the helper keeps it and sends it again; once
// it can be, it is cached once and acked.
func TestAnEventNotCachedIsNotAcked(t *testing.T) {
	// The cache refuses messages until the table is back.
	r := newRigPrepared(t, "ok", me, me, []*signalv1.EventsResponse{msg(dana, dana, 1700000000100, "kept late")},
		func(r *rig) { r.sql("ALTER TABLE messages RENAME TO messages_away") })
	r.until(5*time.Second, "connected", r.phase(signal.Linked, ""))
	time.Sleep(3 * time.Second)
	if acked := r.acked(); len(acked) != 0 {
		t.Fatalf("acked %v while the cache refused it", acked)
	}
	r.sql("ALTER TABLE messages_away RENAME TO messages")
	r.until(10*time.Second, "acked once cached", func() bool { return len(r.acked()) == 1 })
	if msgs := r.messages(direct(dana)); len(msgs) != 1 || msgs[0].Body != "kept late" {
		t.Errorf("cached = %+v", msgs)
	}
}

// An event for a Signal account no configured account is linked to waits, unacked and
// uncached, while the rest go on; once an account is linked to it again, it is kept.
func TestAnEventForNoAccountWaits(t *testing.T) {
	stray := msg(dana, dana, 1700000000100, "for the other account")
	stray.Account = other
	r := newRigPlaying(t, "ok", me+","+other, me, []*signalv1.EventsResponse{stray, msg(dana, dana, 1700000000200, "mine")})
	r.until(10*time.Second, "mine acked", func() bool { return slices.Contains(r.acked(), "mine") })
	time.Sleep(time.Second)
	if acked := r.acked(); slices.Contains(acked, "for the other account") {
		t.Fatalf("acked %v: the stray was let go", acked)
	}
	if msgs := r.messages(domain.RoomID("signal:" + other + "/" + dana)); len(msgs) != 0 {
		t.Fatalf("the stray was cached before its account came: %+v", msgs)
	}

	if err := r.secrets.StoreSecret("signal|two", other); err != nil {
		t.Fatal(err)
	}
	r.a.UseConfig(context.Background(), config.Config{Signal: config.Signal{Accounts: []config.SignalAccount{{Name: "phone"}, {Name: "two"}}}})
	r.until(10*time.Second, "the stray acked once its account came", func() bool {
		return slices.Contains(r.acked(), "for the other account")
	})
	if msgs := r.messages(domain.RoomID("signal:" + other + "/" + dana)); len(msgs) != 1 {
		t.Errorf("the stray's account's messages = %+v", msgs)
	}
}

// Renaming an account in the config keeps its link: one linked account gone and one
// unlinked come, in one change, is a rename.
func TestARenameKeepsTheLink(t *testing.T) {
	r := newRig(t, "ok", me, me)
	r.until(5*time.Second, "linked", r.phase(signal.Linked, ""))
	r.a.UseConfig(context.Background(), config.Config{Signal: config.Signal{Accounts: []config.SignalAccount{{Name: "il"}}}})
	r.until(5*time.Second, "il linked", func() bool {
		s, ok := r.last()
		return ok && s.Account == "il" && s.Phase == signal.Linked
	})
	if aci, _, _ := r.secrets.Secret("signal|il"); aci != me {
		t.Errorf("il is linked to %q", aci)
	}
	if _, kept, _ := r.secrets.Secret("signal|phone"); kept {
		t.Error("the old name's link was kept too")
	}
	// Two names out and in at once is no rename: neither is guessed.
	r.a.UseConfig(context.Background(), config.Config{Signal: config.Signal{Accounts: []config.SignalAccount{{Name: "a"}, {Name: "b"}}}})
	for _, name := range []string{"a", "b"} {
		if _, linked, _ := r.secrets.Secret("signal|" + name); linked {
			t.Errorf("%s took a link that was il's", name)
		}
	}
}

// A person with no name but a known number is that number, "+digits", which the
// phonebook or another network's name for it names; and they are linked to it, so their
// messages are named the same way.
func TestANumberJoinsAPersonToTheirPhone(t *testing.T) {
	hi := msg(dana, me, 1700000000100, "hi")
	hi.GetMessage().ChatNumber = "+972 50-000-0002"
	r := newRigPlaying(t, "ok", me, me, []*signalv1.EventsResponse{hi})
	r.until(10*time.Second, "acked", func() bool { return len(r.acked()) == 1 })
	if room, _ := r.room(direct(dana)); room.Name != "+972500000002" {
		t.Errorf("Dana's room is %q, want her number", room.Name)
	}
	dir, err := r.cache.Directory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(dir.Identifiers("signal:"+dana), domain.PhoneID("972500000002")) {
		t.Errorf("Dana is not joined to her number: %v", dir.Identifiers("signal:"+dana))
	}
}
