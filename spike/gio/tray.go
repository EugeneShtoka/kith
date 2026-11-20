package main

import (
	"context"
	"fmt"
	"image/color"
	"log"
	"os"
	"sync"

	"gioui.org/io/system"
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

// The tray icon, as the desktop's (main.ts trayIcon): a disc, grey, or red while the
// profile has something unread, its tooltip the count, a click raising the window. It is
// a StatusNotifierItem on the session bus, which every Linux tray that is not XEmbed's
// reads (KDE, GNOME's AppIndicator extension, waybar, xfce4-panel's): the same protocol
// Electron's Tray speaks there, through godbus, which kith already uses.
// shortcut: the item registers once, at start; a tray started after the window does not
// show it until the window restarts (watch NameOwnerChanged for the watcher to fix).
// shortcut: no XEmbed tray (polybar, stalonetray, trayer): Electron's has none either.

const (
	sniIface    = "org.kde.StatusNotifierItem"
	sniPath     = dbus.ObjectPath("/StatusNotifierItem")
	trayPx      = 16
	trayRadius2 = 6.5 * 6.5
)

var (
	trayRead   = color.NRGBA{R: 0x80, G: 0x80, B: 0x80, A: 0xff}
	trayUnread = color.NRGBA{R: 0xb4, G: 0x23, B: 0x18, A: 0xff}
)

// trayPixmap is one image as StatusNotifierItem carries it: ARGB32, network byte order.
type trayPixmap struct {
	W, H int32
	Pix  []byte
}

// trayTip is a StatusNotifierItem tooltip: icon name, pixmaps, title, text.
type trayTip struct {
	Icon   string
	Pixmap []trayPixmap
	Title  string
	Text   string
}

// tray is the item while it is up: the count to show next, and a kick to show it.
type tray struct {
	mu    sync.Mutex
	count int
	kick  chan struct{}
}

// trayIcon is the disc, in the unread color when unread.
func trayIcon(unread bool) []trayPixmap {
	c := trayRead
	if unread {
		c = trayUnread
	}
	pix := make([]byte, 0, trayPx*trayPx*4)
	for y := range trayPx {
		for x := range trayPx {
			dx, dy := float64(x)-7.5, float64(y)-7.5
			if dx*dx+dy*dy <= trayRadius2 {
				pix = append(pix, 0xff, c.R, c.G, c.B)
			} else {
				pix = append(pix, 0, 0, 0, 0)
			}
		}
	}
	return []trayPixmap{{W: trayPx, H: trayPx, Pix: pix}}
}

// trayTooltip is what the tooltip says for n unread.
func trayTooltip(n int) trayTip {
	text := ""
	if n > 0 {
		text = fmt.Sprintf("%d unread", n)
	}
	return trayTip{Pixmap: []trayPixmap{}, Title: "kith", Text: text}
}

// startTray puts the item on the session bus and keeps it showing the count set; nil
// when there is no session bus. Without a tray to show it, it says so once and waits.
func (u *ui) startTray(ctx context.Context) *tray {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		log.Printf("tray: %v", err)
		return nil
	}
	name := fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())
	props, err := exportItem(conn, name, func() { u.w.Perform(system.ActionRaise) })
	if err != nil {
		log.Printf("tray: %v", err)
		closeBus(conn)
		return nil
	}
	watcher := conn.Object("org.kde.StatusNotifierWatcher", "/StatusNotifierWatcher")
	if err := watcher.Call("org.kde.StatusNotifierWatcher.RegisterStatusNotifierItem", 0, name).Err; err != nil {
		log.Printf("tray: no tray to show it in: %v", err)
	}
	t := &tray{kick: make(chan struct{}, 1)}
	go t.show(ctx, conn, props)
	return t
}

// exportItem takes name on conn and serves the item there: a click raises the window,
// the rest of its methods do nothing.
func exportItem(conn *dbus.Conn, name string, raise func()) (*prop.Properties, error) {
	if _, err := conn.RequestName(name, dbus.NameFlagDoNotQueue); err != nil {
		return nil, fmt.Errorf("taking %s: %w", name, err)
	}
	click := func(_, _ int32) *dbus.Error { raise(); return nil }
	methods := map[string]any{
		"Activate": click, "SecondaryActivate": click,
		"ContextMenu": func(_, _ int32) *dbus.Error { return nil },
		"Scroll":      func(_ int32, _ string) *dbus.Error { return nil },
	}
	if err := conn.ExportMethodTable(methods, sniPath, sniIface); err != nil {
		return nil, fmt.Errorf("serving the item: %w", err)
	}
	fixed := func(v any) *prop.Prop { return &prop.Prop{Value: v, Emit: prop.EmitFalse} }
	props, err := prop.Export(conn, sniPath, prop.Map{sniIface: {
		"Category": fixed("Communications"), "Id": fixed("kith"), "Title": fixed("kith"),
		"Status": fixed("Active"), "IconName": fixed(""), "IconPixmap": fixed(trayIcon(false)),
		"ToolTip": fixed(trayTooltip(0)), "ItemIsMenu": fixed(false),
	}})
	if err != nil {
		return nil, fmt.Errorf("serving its properties: %w", err)
	}
	return props, nil
}

// show puts each count set on the item, until ctx ends.
func (t *tray) show(ctx context.Context, conn *dbus.Conn, props *prop.Properties) {
	defer closeBus(conn)
	shown := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.kick:
		}
		t.mu.Lock()
		n := t.count
		t.mu.Unlock()
		if (n > 0) != (shown > 0) {
			props.SetMust(sniIface, "IconPixmap", trayIcon(n > 0))
			emit(conn, "NewIcon")
		}
		props.SetMust(sniIface, "ToolTip", trayTooltip(n))
		emit(conn, "NewToolTip")
		shown = n
	}
}

func closeBus(conn *dbus.Conn) {
	if err := conn.Close(); err != nil {
		log.Printf("tray: %v", err)
	}
}

// emit sends the item's signal that one of its properties changed.
func emit(conn *dbus.Conn, signal string) {
	if err := conn.Emit(sniPath, sniIface+"."+signal); err != nil {
		log.Printf("tray: %v", err)
	}
}

// set shows n unread; it never waits on the bus (the window calls it while laying out).
func (t *tray) set(n int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.count = n
	t.mu.Unlock()
	select {
	case t.kick <- struct{}{}:
	default: // a kick is pending, and it reads the count then
	}
}
