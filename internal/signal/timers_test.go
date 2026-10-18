package signal_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

const (
	hour = 3600
	week = 7 * 24 * hour
)

// timed is a message sent with a chat's timer, set by the change numbered version.
func timed(ev *signalv1.EventsResponse, seconds, version uint32) *signalv1.EventsResponse {
	ev.GetMessage().Timer, ev.GetMessage().TimerVersion = seconds, version
	return ev
}

func timerChange(chat, sender string, group bool, seconds, version uint32) *signalv1.EventsResponse {
	return &signalv1.EventsResponse{Event: &signalv1.EventsResponse_Timer{Timer: &signalv1.Timer{
		Chat: chat, Group: group, Sender: sender, Seconds: seconds, Version: version,
	}}}
}

func groupWithTimer(seconds uint32) *signalv1.EventsResponse {
	return &signalv1.EventsResponse{Event: &signalv1.EventsResponse_Group{Group: &signalv1.Group{
		Id: groupID, Title: "Hikers", Members: []string{me, dana}, Timer: seconds,
	}}}
}

// sentTimer is the timer and its number the send of body carried.
func (r *rig) sentTimer(room domain.RoomID, body string) (uint32, uint32) {
	r.t.Helper()
	if err := r.a.Send(context.Background(), room, domain.Draft{Body: body}); err != nil {
		r.t.Fatal(err)
	}
	for _, s := range r.sends() {
		if s.GetBody() == body {
			return s.GetTimer(), s.GetTimerVersion()
		}
	}
	r.t.Fatalf("%q was not sent", body)
	return 0, 0
}

// What kith sends in a chat with disappearing messages carries the chat's timer and the
// number of the change that set it, learned from the chat's changes and messages (the
// other side's, the account's other devices'). An older change never undoes a newer: a
// message sent before the timer changed, delivered after, leaves it. A group's timer
// is the group's, whatever its messages carry. kith's own sends keep the timer.
func TestASendCarriesTheChatsTimer(t *testing.T) {
	gid := domain.RoomID("signal:" + me + "/" + "-_8BAgMEBQ")
	cases := []struct {
		name    string
		room    domain.RoomID
		events  []*signalv1.EventsResponse
		timer   uint32
		version uint32
	}{
		{"a message carries it", direct(dana), []*signalv1.EventsResponse{
			timed(msg(dana, dana, 1700000000100, "hi"), week, 2),
		}, week, 2},
		{"the phone's own message carries it", direct(dana), []*signalv1.EventsResponse{
			timed(msg(dana, me, 1700000000100, "hi"), hour, 4),
		}, hour, 4},
		{"a late message from before a change leaves it", direct(dana), []*signalv1.EventsResponse{
			timed(msg(dana, dana, 1700000000200, "after"), week, 2),
			timed(msg(dana, dana, 1700000000100, "before"), 0, 1),
		}, week, 2},
		{"a change turns it off, and an older one does not undo that", direct(dana), []*signalv1.EventsResponse{
			timed(msg(dana, dana, 1700000000100, "hi"), week, 2),
			timerChange(dana, dana, false, 0, 3),
			timed(msg(dana, dana, 1700000000050, "late"), week, 2),
		}, 0, 3},
		{"an unnumbered change, before any numbered one", direct(dana), []*signalv1.EventsResponse{
			timerChange(dana, me, false, hour, 0),
		}, hour, 0},
		{"a group's is the group's", gid, []*signalv1.EventsResponse{
			groupWithTimer(hour),
			inGroup(timed(msg(groupID, dana, 1700000000100, "trail?"), 0, 0)),
		}, hour, 0},
		{"a group's change", gid, []*signalv1.EventsResponse{
			groupWithTimer(hour),
			inGroup(msg(groupID, dana, 1700000000100, "trail?")),
			timerChange(groupID, dana, true, week, 0),
		}, week, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := connected(t, c.events...)
			for i, body := range []string{"one", "two"} { // the first send must not lose it
				if timer, version := r.sentTimer(c.room, body); timer != c.timer || version != c.version {
					t.Errorf("send %d carried %d v%d, want %d v%d", i+1, timer, version, c.timer, c.version)
				}
			}
		})
	}
}

// deletedWithin reports whether the cached message id is deleted within limit.
func (r *rig) deletedWithin(room domain.RoomID, id domain.EventID, limit time.Duration) bool {
	r.t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		msgs := r.messages(room)
		for i := range msgs {
			if msgs[i].ID == id && msgs[i].Redacted {
				return true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// A disappearing message is deleted once its timer has run: the account's own from its
// sending, one from the phone's history from when the phone had started it, someone
// else's only once the account has read it. Clients hear of it as of a deletion.
func TestADisappearingMessageIsDeletedWhenItsTimeRuns(t *testing.T) {
	long := uint64(time.Now().Add(-2 * time.Hour).UnixMilli())
	archived := historyOf(ofer, false, 0, msg(ofer, ofer, long, "from the phone's history"))
	archived.GetHistory().Messages[0].Timer, archived.GetHistory().Messages[0].TimerStarted = hour, long+60_000
	r := connected(t,
		timed(msg(dana, me, long, "mine, an hour"), hour, 1),
		timed(msg(dana, dana, long+1, "hers, a second once read"), 1, 1),
		archived,
	)
	ctx := context.Background()
	id := func(room domain.RoomID, sender string, sent uint64) domain.EventID {
		return domain.EventID(fmt.Sprintf("%s/%s.%d", room, sender, sent))
	}
	if !r.deletedWithin(direct(dana), id(direct(dana), me, long), 10*time.Second) {
		t.Error("the account's own message outlived its timer")
	}
	if !r.deletedWithin(direct(ofer), id(direct(ofer), ofer, long), 10*time.Second) {
		t.Error("a history message outlived the timer the phone had started")
	}
	hers := id(direct(dana), dana, long+1)
	if r.deletedWithin(direct(dana), hers, 6*time.Second) {
		t.Fatal("someone else's message went before it was read")
	}
	if err := r.a.MarkRead(ctx, direct(dana), hers, false); err != nil {
		t.Fatal(err)
	}
	if !r.deletedWithin(direct(dana), hers, 10*time.Second) {
		t.Error("a read message outlived its timer")
	}
	heard := false
	for len(r.a.Messages()) > 0 {
		if m := <-r.a.Messages(); m.ID == hers && m.Redacted {
			heard = true
		}
	}
	if !heard {
		t.Error("clients did not hear the message go")
	}
}
