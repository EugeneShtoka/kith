package main

import (
	"context"
	"log"

	"gioui.org/io/system"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// kithd hands a clicked matrix:, tg:, WhatsApp or Signal link (`kith --open`) to one
// window: the one with the link's chat open, else the one used last whose profile holds
// it, else the one used last (daemon/windows.go). daemon.Remote holds this window's link
// stream (FollowStream) under a client id it makes up at start and keeps for the run,
// opening it again whenever it ends; the window says on that id what it has open
// (Present) whenever that changes and when it gets the focus.

// present tells kithd the room open ("" none) and that the window is in use now. Called
// with mu held; only the latest room waits, sent in order by presenting.
func (u *ui) present(room domain.RoomID) {
	select {
	case <-u.presents:
	default:
	}
	u.presents <- room
}

// presenting sends each Present in turn until ctx ends.
func (u *ui) presenting(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case room := <-u.presents:
			if err := u.backend.Present(ctx, room); err != nil {
				log.Printf("present: %v", err)
			}
		}
	}
}

// followLink goes where a link kithd handed this window points: the chat it names among
// the profile's rooms, at its message when it names one, and brings the window forward.
// kithd's FollowStream answer names the same chats (domain.LinkedRooms over the
// profile's rooms), which daemon.Remote does not pass on. A link that comes before the
// rooms are read is followed once they are.
func (u *ui) followLink(ctx context.Context, uri string) {
	u.mu.Lock()
	if u.rooms == nil {
		u.link = uri
		u.mu.Unlock()
		return
	}
	chats := domain.LinkedRooms(uri, u.rooms)
	u.mu.Unlock()
	defer u.w.Perform(system.ActionRaise)
	if len(chats) == 0 {
		u.say("That link names no chat in this profile")
		return
	}
	// shortcut: the first chat, as the desktop takes; the TUI asks which account when
	// several are in it.
	room, event := chats[0].ID, domain.LinkedMessage(uri, chats[0].ID)
	u.mu.Lock()
	open := u.current != room || event != ""
	u.mu.Unlock()
	if open {
		u.open(ctx, room)
	}
	u.mu.Lock()
	u.timeline.jump, u.roomAt = event, room
	u.mu.Unlock()
	u.w.Invalidate()
}
