package signal_test

import (
	"context"
	"maps"
	"strings"
	"testing"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

func historyOf(chat string, group bool, readThrough uint64, msgs ...*signalv1.EventsResponse) *signalv1.EventsResponse {
	h := &signalv1.History{Chat: chat, Group: group, ReadThrough: readThrough}
	for _, ev := range msgs {
		m := ev.GetMessage()
		m.Group = group
		h.Messages = append(h.Messages, m)
	}
	return &signalv1.EventsResponse{Event: &signalv1.EventsResponse_History{History: h}}
}

// The phone's transferred history becomes rooms: a one-to-one chat named for the other
// side, the chat with the account itself as Note to Self, a group by its title, each
// with its messages, read as far as the account had read, with its timer. It is history,
// not news: no message is announced. A part handed again (a sync cut short) is the same
// messages, and an archived file loads with the hash it came with.
func TestTheTransferredHistoryBecomesRooms(t *testing.T) {
	photo := carrying(msg(dana, me, 1700000000200, "the trail"), "p2")
	photo.GetMessage().Attachments[0].PlaintextHash = []byte("h2")
	dm := historyOf(dana, false, 1700000000200,
		named(msg(dana, dana, 1700000000100, "lunch?"), "Dana Levi", "Dana Levi"),
		photo,
		msg(dana, dana, 1700000000300, "not read yet"),
	)
	dm.GetHistory().Timer, dm.GetHistory().TimerVersion = hour, 2
	r := connected(t,
		dm,
		historyOf(me, false, 1700000000150, msg(me, me, 1700000000150, "buy milk")),
		groupWithTimer(0),
		historyOf(groupID, true, 0, msg(groupID, dana, 1700000000120, "trail?")),
		historyOf(ofer, false, 0, named(msg(ofer, me, 1700000000400, "only I wrote here"), "", "Ofer Cohen")),
		dm, // again: the sync resumed
	)
	ctx := context.Background()
	gid := domain.RoomID("signal:" + me + "/" + "-_8BAgMEBQ")
	for _, c := range []struct {
		room domain.RoomID
		name string
		msgs int
	}{{direct(dana), "Dana Levi", 3}, {direct(me), "Note to Self", 1}, {gid, "Hikers", 1}, {direct(ofer), "Ofer Cohen", 1}} {
		room, ok := r.room(c.room)
		if !ok || room.Name != c.name {
			t.Errorf("%s: room %+v (%v), want %q", c.room, room, ok, c.name)
		}
		if n := len(r.messages(c.room)); n != c.msgs {
			t.Errorf("%s: %d messages, want %d", c.room, n, c.msgs)
		}
	}
	if n, _, _, _ := r.cache.CountUnread(ctx, r.a.Me(), direct(dana)); n != 1 {
		t.Errorf("unread = %d, want the one after what was read", n)
	}
	if n := len(r.a.Messages()); n != 0 {
		t.Errorf("history announced %d messages", n)
	}
	own := domain.EventID(string(direct(dana)) + "/" + me + ".1700000000200")
	if data, err := r.a.LoadImage(ctx, direct(dana), own); err != nil || !strings.Contains(string(data), "checked by h2") {
		t.Errorf("the archived photo loaded %q, %v", data, err)
	}
	if timer, version := r.sentTimer(direct(dana), "and now?"); timer != hour || version != 2 {
		t.Errorf("a send after the history carried %d v%d, want the chat's %d v2", timer, version, hour)
	}
}

// A note written on the phone to the account itself lands in Note to Self.
func TestANoteToSelfIsNamedSo(t *testing.T) {
	r := connected(t, msg(me, me, 1700000000100, "buy milk"))
	if room, ok := r.room(direct(me)); !ok || room.Name != "Note to Self" || len(r.messages(direct(me))) != 1 {
		t.Errorf("room %+v (%v), %d messages", room, ok, len(r.messages(direct(me))))
	}
}

// The archive's reactions land on its messages, one per sender, but never over what
// kith heard live: whatever order the live changes and the history (handed again when
// a sync resumes) come in, a sender's last live change stands, else the archive's.
func TestArchivedReactionsNeverUndoLiveOnes(t *testing.T) {
	const sent = 1700000000100
	target := domain.EventID(string(direct(dana)) + "/" + dana + ".1700000000100")
	page := func() *signalv1.EventsResponse {
		h := historyOf(dana, false, 0, msg(dana, dana, sent, "lunch?"))
		h.GetHistory().Messages[0].Reactions = []*signalv1.Reaction{{Sender: me, Emoji: "👍"}, {Sender: dana, Emoji: "🎉"}}
		return h
	}
	lives := map[string]*signalv1.EventsResponse{
		"mine replaced":   react(dana, me, "❤️", false, dana, sent),
		"mine taken off":  react(dana, me, "", true, dana, sent),
		"another's added": react(dana, ofer, "😮", false, dana, sent),
	}
	orders := []string{"H", "HL", "LH", "HLH", "LHH", "HHL"}
	for name, live := range lives {
		for _, order := range orders {
			t.Run(name+"/"+order, func(t *testing.T) {
				if name == "mine taken off" && order[0] == 'L' {
					t.Skip("left: a removal heard before its message's history is undone by the archive")
				}
				want := map[string]string{"signal:" + me: "👍", "signal:" + dana: "🎉"}
				var events []*signalv1.EventsResponse
				for _, step := range order {
					if step == 'H' {
						events = append(events, page())
						continue
					}
					events = append(events, live)
					if r := live.GetReaction(); r.GetRemove() {
						delete(want, "signal:"+r.GetSender())
					} else {
						want["signal:"+r.GetSender()] = r.GetEmoji()
					}
				}
				r := connected(t, events...)
				got := map[string]string{}
				for _, rx := range r.reactions(direct(dana)) {
					if rx.Target != target {
						t.Errorf("%+v is not on the archived message", rx)
					}
					got[rx.Sender] = rx.Key
				}
				if !maps.Equal(got, want) {
					t.Errorf("reactions = %v, want %v", got, want)
				}
			})
		}
	}
}
