package notify

import (
	"fmt"
	"slices"
	"sync"

	"github.com/godbus/dbus/v5"
)

// Clicks follows the link of a desktop popup clicked: it keeps the links of the popups
// shown, by the notification daemon's ID, and listens for the daemon's clicks once,
// however many notifiers are built with it (kith-notify builds one per read of the
// device file).
type Clicks struct {
	// Open follows a link; it is called on a goroutine of its own.
	Open func(link string)

	mu     sync.Mutex
	links  map[uint32]string
	order  []uint32 // the IDs in links, oldest first
	listen sync.Once
}

// clickedLinks is how many popups' links are kept: far more than are on screen.
const clickedLinks = 256

// The freedesktop signals a popup's click and its closing arrive as.
const (
	actionInvoked      = "org.freedesktop.Notifications.ActionInvoked"
	notificationClosed = "org.freedesktop.Notifications.NotificationClosed"
)

// clickAction is the action a click on the popup itself invokes, by the spec's name.
const clickAction = "default"

// listening starts taking the daemon's signals on conn, the first time it is asked.
func (c *Clicks) listening(conn *dbus.Conn, report reporter) {
	c.listen.Do(func() {
		if err := conn.AddMatchSignal(
			dbus.WithMatchObjectPath("/org/freedesktop/Notifications"),
			dbus.WithMatchInterface("org.freedesktop.Notifications"),
		); err != nil {
			report.failed("desktop", fmt.Errorf("notify: listen for clicks: %w", err))
			return
		}
		signals := make(chan *dbus.Signal, 16)
		conn.Signal(signals)
		go func() {
			for s := range signals {
				c.signal(s)
			}
		}()
	})
}

// shown keeps link as the popup id's, or forgets the popup's link when it has none (a
// popup replaced in place keeps its ID).
func (c *Clicks) shown(id uint32, link string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if link == "" {
		delete(c.links, id)
		c.order = slices.DeleteFunc(c.order, func(held uint32) bool { return held == id })
		return
	}
	if c.links == nil {
		c.links = make(map[uint32]string, clickedLinks)
	}
	if _, held := c.links[id]; !held {
		c.order = append(c.order, id)
	}
	c.links[id] = link
	for len(c.order) > clickedLinks {
		delete(c.links, c.order[0])
		c.order = c.order[1:]
	}
}

// signal follows the link of a popup clicked, and forgets a popup closed. Every
// client of the daemon hears its signals: an ID this never showed has no link.
func (c *Clicks) signal(s *dbus.Signal) {
	if len(s.Body) == 0 {
		return
	}
	id, ok := s.Body[0].(uint32)
	if !ok {
		return
	}
	switch s.Name {
	case actionInvoked:
		if len(s.Body) < 2 || s.Body[1] != clickAction {
			return
		}
		c.mu.Lock()
		link := c.links[id]
		c.mu.Unlock()
		if link != "" {
			go c.Open(link)
		}
	case notificationClosed:
		c.shown(id, "")
	}
}
