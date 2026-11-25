package main

import (
	"context"
	"path/filepath"
	"testing"

	"gioui.org/app"
	"gioui.org/widget"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A link kithd hands the window opens the chat it names among the profile's rooms, at
// its message; one naming none says so; one before the rooms are read waits for them.
func TestAHandedLinkOpensItsChat(t *testing.T) {
	const general domain.RoomID = "matrix:4f1c9e2a7b3d5c60/!general:example.org"
	newWindow := func() *ui {
		return &ui{
			w: new(app.Window), backend: daemon.NewRemote(filepath.Join(t.TempDir(), "none")),
			drafts: newDrafts(), mentions: map[*widget.Editor][]domain.Mention{},
			presents: make(chan domain.RoomID, 1),
		}
	}
	ctx := context.Background()

	u := newWindow()
	u.followLink(ctx, "matrix:roomid/general:example.org")
	if u.link != "matrix:roomid/general:example.org" || u.current != "" {
		t.Fatalf("before the rooms: kept %q, opened %q", u.link, u.current)
	}

	u = newWindow()
	u.rooms = []domain.Room{{ID: "matrix:4f1c9e2a7b3d5c60/!other:example.org"}, {ID: general}}
	u.followLink(ctx, "matrix:roomid/general:example.org/e/ev1")
	u.mu.Lock()
	if u.current != general || u.timeline.jump != domain.TypedEventID(general, "$ev1") || <-u.presents != general {
		t.Errorf("opened %q at %q", u.current, u.timeline.jump)
	}
	u.mu.Unlock()

	u.followLink(ctx, "matrix:roomid/elsewhere:example.org")
	u.mu.Lock()
	if u.current != general || u.note != "That link names no chat in this profile" {
		t.Errorf("a link to no chat here: in %q, said %q", u.current, u.note)
	}
	u.mu.Unlock()
}
