package signal_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// requests is every request of kind the helper was asked for, in order (others in the
// file do not parse as it).
func requests[T proto.Message](r *rig, fresh func() T) []T {
	b, _ := os.ReadFile(r.sent)
	var out []T
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		m := fresh()
		if line != "" && protojson.Unmarshal([]byte(line), m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func sendReqs(r *rig) []*signalv1.SendRequest {
	return requests(r, func() *signalv1.SendRequest { return &signalv1.SendRequest{} })
}

func id(chat, author, sent string) domain.EventID {
	return domain.EventID(string(direct(chat)) + "/" + author + "." + sent)
}

// Editing one's message sends the edit naming its latest version: the message the first
// time, the first edit the second (or Signal's apps make it a new message). Each is
// cached on the message.
func TestEditingOwnMessages(t *testing.T) {
	r := connected(t, msg(dana, me, 1700000000100, "one"))
	ctx := context.Background()
	mine := id(dana, me, "1700000000100")
	for _, body := range []string{"two", "three"} {
		if err := r.a.Send(ctx, direct(dana), domain.Draft{Body: body, Edits: mine}); err != nil {
			t.Fatal(err)
		}
	}
	reqs := sendReqs(r)
	if len(reqs) != 2 || reqs[0].GetEditOf() != 1700000000100 || reqs[1].GetEditOf() != reqs[0].GetSent() {
		t.Fatalf("edits = %v, want the second naming the first", reqs)
	}
	if msgs := r.messages(direct(dana)); len(msgs) != 1 || msgs[0].Body != "three" {
		t.Errorf("cached %+v", msgs)
	}
	// Someone else's message: neither edited nor deleted, nothing sent.
	if err := r.a.Send(ctx, direct(dana), domain.Draft{Body: "x", Edits: id(dana, dana, "1")}); err == nil {
		t.Error("edited Dana's message")
	}
	if err := r.a.Redact(ctx, direct(dana), id(dana, dana, "1"), ""); err == nil {
		t.Error("deleted Dana's message for everyone")
	}
	if n := len(sendReqs(r)); n != 2 {
		t.Errorf("%d requests, want the two edits", n)
	}
}

// A reaction names the message's latest version and replaces the account's own; taking
// it off names the emoji taken off. Each is cached.
func TestReactingAndTakingItOff(t *testing.T) {
	r := connected(t,
		msg(dana, dana, 1700000000100, "one"),
		edit(dana, dana, 1700000000100, 1700000000200, "two"),
	)
	ctx := context.Background()
	target := id(dana, dana, "1700000000100")
	for _, key := range []string{"👍", "❤️"} {
		if err := r.a.SendReaction(ctx, direct(dana), target, key); err != nil {
			t.Fatal(err)
		}
	}
	rs := r.reactions(direct(dana))
	if len(rs) != 1 || rs[0].Key != "❤️" || rs[0].Target != target {
		t.Fatalf("reactions = %+v, want only my ❤️ on the message", rs)
	}
	if err := r.a.Redact(ctx, direct(dana), rs[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	if rs := r.reactions(direct(dana)); len(rs) != 0 {
		t.Errorf("after taking it off: %+v", rs)
	}
	reqs := sendReqs(r)
	if len(reqs) != 3 {
		t.Fatalf("requests = %v", reqs)
	}
	for i, want := range []string{"👍", "❤️", "❤️"} {
		re := reqs[i].GetReaction()
		if re.GetEmoji() != want || re.GetTargetAuthor() != dana || re.GetTargetSent() != 1700000000200 || re.GetRemove() != (i == 2) {
			t.Errorf("request %d = %v", i, re)
		}
	}
}

// Deleting one's message for everyone names its original time, and marks it deleted.
func TestDeletingOwnMessage(t *testing.T) {
	r := connected(t, msg(dana, me, 1700000000100, "oops"))
	if err := r.a.Redact(context.Background(), direct(dana), id(dana, me, "1700000000100"), ""); err != nil {
		t.Fatal(err)
	}
	if reqs := sendReqs(r); len(reqs) != 1 || reqs[0].GetDeleteOf() != 1700000000100 {
		t.Errorf("requests = %v", reqs)
	}
	if msgs := r.messages(direct(dana)); len(msgs) != 1 || !msgs[0].Redacted {
		t.Errorf("cached %+v", msgs)
	}
}

// Reading a chat tells the phone which of the others' messages were read, with receipts
// to them unless the read is private; the read is kith's whatever Signal says.
func TestReadingTellsThePhone(t *testing.T) {
	r := connected(t,
		msg(dana, dana, 1700000000100, "one"),
		msg(dana, me, 1700000000200, "mine"),
		msg(dana, dana, 1700000000300, "two"),
		msg(dana, dana, 1700000000400, "later"),
	)
	ctx := context.Background()
	if err := r.a.MarkRead(ctx, direct(dana), id(dana, dana, "1700000000300"), false); err != nil {
		t.Fatal(err)
	}
	if err := r.a.MarkRead(ctx, direct(dana), id(dana, dana, "1700000000400"), true); err != nil {
		t.Fatal(err)
	}
	reads := requests(r, func() *signalv1.MarkReadRequest { return &signalv1.MarkReadRequest{} })
	var marks []*signalv1.MarkReadRequest
	for _, m := range reads {
		if len(m.GetMessages()) > 0 {
			marks = append(marks, m)
		}
	}
	if len(marks) != 2 {
		t.Fatalf("read marks = %v", marks)
	}
	sent := func(m *signalv1.MarkReadRequest) []uint64 {
		var out []uint64
		for _, x := range m.GetMessages() {
			if x.GetAuthor() != dana {
				t.Errorf("told %s of a read", x.GetAuthor())
			}
			out = append(out, x.GetSent())
		}
		slices.Sort(out)
		return out
	}
	if got := sent(marks[0]); !marks[0].GetReceipts() || !slices.Equal(got, []uint64{1700000000100, 1700000000300}) {
		t.Errorf("first read = %v (receipts %v)", got, marks[0].GetReceipts())
	}
	if got := sent(marks[1]); marks[1].GetReceipts() || !slices.Contains(got, 1700000000400) {
		t.Errorf("private read = %v (receipts %v)", got, marks[1].GetReceipts())
	}
	n, _, _, err := r.cache.CountUnread(ctx, r.a.Me(), direct(dana))
	if err != nil || n != 0 {
		t.Errorf("unread = %d, %v", n, err)
	}
}
