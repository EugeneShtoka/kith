package signal_test

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/EugeneShtoka/kith/internal/api"
	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/signal"
)

// sends is every Send the helper was asked for, in order.
func (r *rig) sends() []*signalv1.SendRequest {
	b, _ := os.ReadFile(r.sent)
	var out []*signalv1.SendRequest
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		req := &signalv1.SendRequest{}
		if line != "" && protojson.Unmarshal([]byte(line), req) == nil && req.GetBody() != "" {
			out = append(out, req)
		}
	}
	return out
}

func connected(t *testing.T, events ...*signalv1.EventsResponse) *rig {
	t.Helper()
	r := newRigPlaying(t, "ok", me, me, events)
	r.until(5*time.Second, "connected", r.phase(signal.Linked, ""))
	r.until(5*time.Second, "every event acked", func() bool { return len(r.acked()) == len(events) })
	return r
}

// A message sent goes from the room's account to its chat, and is cached as the
// account's own (it never comes back to this device), reading the chat.
func TestASentMessageIsCachedAsOwn(t *testing.T) {
	r := connected(t, msg(dana, dana, 1700000000100, "hi"))
	ctx := context.Background()
	if err := r.a.Send(ctx, direct(dana), domain.Draft{Body: "hello back"}); err != nil {
		t.Fatal(err)
	}
	sends := r.sends()
	if len(sends) != 1 || sends[0].GetAccount() != me || sends[0].GetChat() != dana || sends[0].GetGroup() || sends[0].GetBody() != "hello back" {
		t.Fatalf("sent %v", sends)
	}
	msgs := r.messages(direct(dana))
	if len(msgs) != 2 {
		t.Fatalf("cached %+v", msgs)
	}
	own := msgs[1]
	want := domain.EventID("signal:" + me + "/" + dana + "/" + me + "." + strconv.FormatUint(sends[0].GetSent(), 10))
	if own.ID != want || own.Sender != "signal:"+me || own.Body != "hello back" {
		t.Errorf("own = %+v, want ID %s", own, want)
	}
	if n, _, _, _ := r.cache.CountUnread(ctx, r.a.Me(), direct(dana)); n != 0 {
		t.Errorf("unread after answering = %d", n)
	}
}

// A reply quotes its target: author and sent time from the target's ID, text from the
// cache. A group's message goes to the group as Signal names it.
func TestAReplyQuotesItsTargetAndGroupsAreNamedAsSignalNamesThem(t *testing.T) {
	r := connected(t, inGroup(msg(groupID, dana, 1700000000100, "trail?")))
	ctx := context.Background()
	gid := domain.RoomID("signal:" + me + "/" + base64.RawURLEncoding.EncodeToString([]byte{0xfb, 0xff, 0x01, 0x02, 0x03, 0x04, 0x05}))
	target := domain.EventID(string(gid) + "/" + dana + ".1700000000100")
	if err := r.a.Send(ctx, gid, domain.Draft{Body: "yes", ReplyTo: target}); err != nil {
		t.Fatal(err)
	}
	sends := r.sends()
	if len(sends) != 1 {
		t.Fatalf("sent %v", sends)
	}
	s := sends[0]
	if s.GetChat() != groupID || !s.GetGroup() {
		t.Errorf("sent to %q (group %v), want %q", s.GetChat(), s.GetGroup(), groupID)
	}
	if q := s.GetQuote(); q.GetAuthor() != dana || q.GetSent() != 1700000000100 || q.GetText() != "trail?" {
		t.Errorf("quote = %v", q)
	}
	if msgs := r.messages(gid); len(msgs) != 2 || msgs[1].ReplyTo != target {
		t.Errorf("cached %+v", msgs)
	}
}

// A send that failed and is tried again (same TxnID) goes at the same sent time: it is
// the same message to Signal's clients, and cached once.
func TestARetryIsTheSameMessage(t *testing.T) {
	t.Setenv("KITH_FAKE_SIGNAL_FAILS", "1")
	r := connected(t)
	ctx := context.Background()
	draft := domain.Draft{Body: "again", TxnID: "t1"}
	if err := r.a.Send(ctx, direct(dana), draft); err == nil {
		t.Fatal("the first send did not fail")
	}
	time.Sleep(5 * time.Millisecond)
	if err := r.a.Send(ctx, direct(dana), draft); err != nil {
		t.Fatal(err)
	}
	sends := r.sends()
	if len(sends) != 2 || sends[0].GetSent() != sends[1].GetSent() {
		t.Fatalf("sends = %v, want two at one time", sends)
	}
	if msgs := r.messages(direct(dana)); len(msgs) != 1 {
		t.Errorf("cached %d messages", len(msgs))
	}
	if err := r.a.Send(ctx, direct(dana), domain.Draft{Body: "next", TxnID: "t2"}); err != nil {
		t.Fatal(err)
	}
	if sends = r.sends(); sends[2].GetSent() == sends[1].GetSent() {
		t.Error("a new message went at the old one's time")
	}
}

// No connected account for a room, or no room: the send fails, saying so, and nothing
// is cached.
func TestASendWithoutAConnectionFails(t *testing.T) {
	r := newRigPlaying(t, "ok", "", me, nil) // linked in kithd, not in the helper's store
	r.until(5*time.Second, "unlinked", r.phase(signal.Unlinked, ""))
	err := r.a.Send(context.Background(), direct(dana), domain.Draft{Body: "lost"})
	if !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("send = %v, want ErrNetworkOff", err)
	}
	if err := r.a.Send(context.Background(), "signal:nope", domain.Draft{Body: "x"}); err == nil {
		t.Error("a send to no chat succeeded")
	}
	if len(r.sends()) != 0 || len(r.messages(direct(dana))) != 0 {
		t.Error("something was sent or cached")
	}
}

// Typing goes to the room's chat.
func TestTypingGoesToTheChat(t *testing.T) {
	r := connected(t)
	if err := r.a.SendTyping(context.Background(), direct(dana), true, time.Minute); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(r.sent)
	got := &signalv1.TypingRequest{}
	if err := protojson.Unmarshal([]byte(strings.TrimSpace(string(b))), got); err != nil {
		t.Fatal(err)
	}
	if got.GetAccount() != me || got.GetChat() != dana || !got.GetTyping() {
		t.Errorf("typing = %v", got)
	}
}
