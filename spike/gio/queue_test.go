package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gioui.org/widget"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The queue lists this room's messages, a refused one with Try again; the switch lists
// every room's, each named by its room.
func TestTheQueueListsThisRoomThenEvery(t *testing.T) {
	now := time.Now()
	u := &ui{current: "r1", rooms: []domain.Room{{ID: "r1", Name: "One"}, {ID: "r2", Name: "Two"}}}
	q := &queueView{room: "r1", read: true, btns: map[string]*[2]widget.Clickable{}, card: &historyView{}}
	q.entries = []domain.ScheduledMessage{
		{ID: "a", RoomID: "r1", Body: "hello", At: now.Add(time.Hour), Written: now},
		{ID: "b", RoomID: "r2", Body: "elsewhere", At: now.Add(2 * time.Hour), Written: now},
		{ID: "c", RoomID: "r1", Body: "no", At: now.Add(3 * time.Hour), Written: now, Refused: "too large"},
	}
	lines := func() string {
		var out []string
		for _, r := range u.queueRows(context.Background(), q, now)[1:] {
			var labels []string
			for _, b := range r.buttons {
				labels = append(labels, b.label)
			}
			_, body, _ := strings.Cut(r.body, " — ")
			out = append(out, fmt.Sprintf("%s|%s|%s", r.head, body, strings.Join(labels, "+")))
		}
		return strings.Join(out, "; ")
	}
	if got, want := lines(), "|hello|Cancel; refused: too large|no|Try again+Cancel"; got != want {
		t.Errorf("this room: %s, want %s", got, want)
	}
	u.queueRows(context.Background(), q, now)[0].buttons[0].do()
	if got, want := lines(), "One|hello|Cancel; Two|elsewhere|Cancel; One · refused: too large|no|Try again+Cancel"; got != want || q.card.title != "Queued in every room" {
		t.Errorf("every room: %s (%s), want %s", got, q.card.title, want)
	}
	q.entries = nil
	if got := lines(); got != "Nothing scheduled||" {
		t.Errorf("empty: %s", got)
	}
}
