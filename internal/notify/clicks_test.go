package notify

import (
	"fmt"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func invoked(id uint32, action string) *dbus.Signal {
	return &dbus.Signal{Name: actionInvoked, Body: []any{id, action}}
}

func closed(id uint32) *dbus.Signal {
	return &dbus.Signal{Name: notificationClosed, Body: []any{id, uint32(2)}}
}

// A click on a popup follows its link, and nothing else does: another action, a popup
// this never showed, one closed or replaced by one with no link, one forgotten among
// many shown since, a signal of another shape. The last click is one that must follow,
// so the first link followed is its, or something above followed too.
func TestAClickFollowsItsPopupsLinkAndNothingElseDoes(t *testing.T) {
	t.Parallel()
	followed := make(chan string, 64)
	c := &Clicks{Open: func(link string) { followed <- link }}

	c.shown(1, "kith:one")
	c.signal(invoked(1, "other"))
	c.signal(invoked(99, clickAction))
	c.shown(2, "kith:two")
	c.signal(closed(2))
	c.signal(invoked(2, clickAction))
	c.shown(3, "kith:three")
	c.shown(3, "") // replaced in place by a popup with no link
	c.signal(invoked(3, clickAction))
	c.signal(&dbus.Signal{Name: actionInvoked})
	c.signal(&dbus.Signal{Name: actionInvoked, Body: []any{"1", clickAction}})
	c.signal(&dbus.Signal{Name: actionInvoked, Body: []any{uint32(1)}})

	// A closed popup's ID shown again is kept as long as any other: its old place in
	// the order does not take it early.
	c.shown(4, "kith:four")
	c.signal(closed(4))
	c.shown(4, "kith:four again")
	for i := range uint32(clickedLinks - 1) {
		c.shown(1000+i, fmt.Sprintf("kith:%d", i))
	}
	// 1 is now the oldest past the bound; 4, shown again later, is still held.
	c.mu.Lock()
	_, one := c.links[1]
	four, kept := c.links[4]
	c.mu.Unlock()
	if one || !kept || four != "kith:four again" {
		t.Errorf("past the bound: 1 held %v, 4 held %v as %q; want only 4's, as shown last", one, kept, four)
	}
	c.signal(invoked(1, clickAction))
	c.signal(invoked(4, clickAction))

	select {
	case got := <-followed:
		if got != "kith:four again" {
			t.Errorf("followed %q first, want only the last click's link", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the last click followed nothing")
	}
}

// A popup replaced in place takes the new popup's link: the click opens what it shows.
func TestAPopupReplacedInPlaceFollowsItsNewLink(t *testing.T) {
	t.Parallel()
	followed := make(chan string, 1)
	c := &Clicks{Open: func(link string) { followed <- link }}
	c.shown(7, "kith:first message")
	c.shown(7, "kith:the room's count")
	c.signal(invoked(7, clickAction))
	select {
	case got := <-followed:
		if got != "kith:the room's count" {
			t.Errorf("followed %q, want the popup now shown's", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the click followed nothing")
	}
}
