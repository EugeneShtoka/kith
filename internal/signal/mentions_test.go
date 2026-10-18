package signal_test

import (
	"context"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A mention of a Signal person goes as Signal sends one: U+FFFC where its text was, the
// person named there, counted in UTF-16 units (an emoji is two); a mention of someone on
// another network stays as typed. kith's copy reads as typed. An edit mentions alike.
func TestAMentionGoesAsSignalSendsIt(t *testing.T) {
	r := connected(t, inGroup(msg(groupID, dana, 1700000000100, "trail?")))
	ctx := context.Background()
	gid := domain.RoomID("signal:" + me + "/" + "-_8BAgMEBQ")
	draft := domain.Draft{
		Body: "😀 @Dana Levi and @Ofer, ask @Noa",
		Mentions: []domain.Mention{ // not in the order the text has them
			{UserID: "signal:" + ofer, Name: "@Ofer"},
			{UserID: "whatsapp:972500000000@s.whatsapp.net", Name: "@Noa"},
			{UserID: "signal:" + dana, Name: "@Dana Levi"},
		},
	}
	if err := r.a.Send(ctx, gid, draft); err != nil {
		t.Fatal(err)
	}
	sends := r.sends()
	if len(sends) != 1 {
		t.Fatalf("sent %v", sends)
	}
	s := sends[0]
	if want := "😀 ￼ and ￼, ask @Noa"; s.GetBody() != want {
		t.Errorf("body %q, want %q", s.GetBody(), want)
	}
	m := s.GetMentions()
	if len(m) != 2 || m[0].GetAci() != dana || m[0].GetStart() != 3 || m[0].GetLength() != 1 ||
		m[1].GetAci() != ofer || m[1].GetStart() != 9 || m[0].GetName() != "Dana Levi" {
		t.Errorf("mentions %v", m)
	}
	msgs := r.messages(gid)
	own := msgs[len(msgs)-1]
	if own.Body != draft.Body {
		t.Errorf("kept %q, want it as typed", own.Body)
	}

	edit := draft
	edit.Body, edit.Edits = "😀 @Dana Levi, never mind", own.ID
	if err := r.a.Send(ctx, gid, edit); err != nil {
		t.Fatal(err)
	}
	sends = r.sends()
	if e := sends[len(sends)-1]; e.GetBody() != "😀 ￼, never mind" || len(e.GetMentions()) != 1 || e.GetMentions()[0].GetAci() != dana {
		t.Errorf("edit sent %v", e)
	}
	msgs = r.messages(gid)
	if last := msgs[len(msgs)-1]; last.Body != edit.Body {
		t.Errorf("the edit kept %q, want it as typed", last.Body)
	}
}
