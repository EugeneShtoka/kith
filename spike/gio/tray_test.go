package main

import "testing"

// The tray's disc is ARGB in network order: opaque in the middle, in the unread color
// while unread, clear in the corners.
func TestTheTrayIconIsADisc(t *testing.T) {
	px := func(icon []trayPixmap, x, y int) []byte {
		i := (y*trayPx + x) * 4
		return icon[0].Pix[i : i+4]
	}
	unread := trayIcon(true)
	if got := px(unread, 8, 8); got[0] != 0xff || got[1] != trayUnread.R || got[2] != trayUnread.G || got[3] != trayUnread.B {
		t.Errorf("middle %x, want ff%02x%02x%02x", got, trayUnread.R, trayUnread.G, trayUnread.B)
	}
	if got := px(unread, 0, 0); got[0] != 0 {
		t.Errorf("corner %x, want clear", got)
	}
	if got := px(trayIcon(false), 8, 8); got[1] != trayRead.R {
		t.Errorf("read middle %x, want grey", got)
	}
	if tip := trayTooltip(3); tip.Title != "kith" || tip.Text != "3 unread" {
		t.Errorf("tooltip %+v", tip)
	}
}
