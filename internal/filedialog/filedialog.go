// Package filedialog asks the XDG desktop portal to choose a path. When there is no
// portal (SSH, headless) it says so and the caller falls back to a typed path.
package filedialog

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/godbus/dbus/v5"
)

// ErrCanceled is returned when the person closed the chooser without choosing.
var ErrCanceled = errors.New("filedialog: canceled")

// ErrUnavailable means no portal answered — no desktop session to ask. The caller
// falls back to a typed path, which is the only thing that works over SSH anyway.
var ErrUnavailable = errors.New("filedialog: no desktop file chooser (no XDG portal)")

const (
	portalName  = "org.freedesktop.portal.Desktop"
	portalPath  = "/org/freedesktop/portal/desktop"
	chooserIF   = "org.freedesktop.portal.FileChooser"
	requestIF   = "org.freedesktop.portal.Request"
	responseSig = "org.freedesktop.portal.Request.Response"
)

// File asks for one existing file.
func File(ctx context.Context, title string) (string, error) {
	return open(ctx, title, "", false)
}

// Folder asks for one directory. startAt is where the chooser opens — best effort: a
// backend may ignore it, and one that does is not doing anything wrong.
func Folder(ctx context.Context, title, startAt string) (string, error) {
	return open(ctx, title, startAt, true)
}

// open runs org.freedesktop.portal.FileChooser.OpenFile and waits for the reply.
func open(ctx context.Context, title, startAt string, directory bool) (string, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant("kith_" + rand.Text()), // must be [A-Za-z0-9_]
		"multiple":     dbus.MakeVariant(false),
		"modal":        dbus.MakeVariant(false),
	}
	if directory {
		options["directory"] = dbus.MakeVariant(true)
	}
	if startAt != "" {
		// current_folder is a NUL-terminated byte string, per the portal spec.
		options["current_folder"] = dbus.MakeVariant(append([]byte(startAt), 0))
	}

	signals := make(chan *dbus.Signal, 4)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)
	match := []dbus.MatchOption{dbus.WithMatchInterface(requestIF), dbus.WithMatchMember("Response")}
	if err := conn.AddMatchSignal(match...); err != nil {
		return "", fmt.Errorf("filedialog: watch portal reply: %w", err)
	}
	defer func() { _ = conn.RemoveMatchSignal(match...) }()

	var handle dbus.ObjectPath
	obj := conn.Object(portalName, portalPath)
	// parent_window is empty: a terminal has no window handle to hand over, and the
	// portal reads that as "no parent" rather than as an error.
	if err := obj.CallWithContext(ctx, chooserIF+".OpenFile", 0, "", title, options).Store(&handle); err != nil {
		if noPortal(err) {
			return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return "", fmt.Errorf("filedialog: portal: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			// Closing the request is a courtesy: the chooser is on screen, and it
			// should go away when the thing that asked for it has given up.
			_ = conn.Object(portalName, handle).Call(requestIF+".Close", 0).Err
			return "", fmt.Errorf("filedialog: %w", ctx.Err())
		case sig := <-signals:
			if sig == nil || sig.Name != responseSig || sig.Path != handle {
				continue
			}
			return result(sig)
		}
	}
}

// noPortal reports whether the error is "nothing is there to ask" rather than "the
// chooser went wrong". Only the first is worth falling back from.
func noPortal(err error) bool {
	var dberr dbus.Error
	if !errors.As(err, &dberr) {
		return false
	}
	switch dberr.Name {
	case "org.freedesktop.DBus.Error.ServiceUnknown",
		"org.freedesktop.DBus.Error.NameHasNoOwner",
		"org.freedesktop.DBus.Error.UnknownMethod",
		"org.freedesktop.DBus.Error.UnknownInterface",
		"org.freedesktop.DBus.Error.Spawn.ServiceNotFound":
		return true
	default:
		return false
	}
}

// result reads the response signal: a code, and the URIs chosen.
func result(sig *dbus.Signal) (string, error) {
	if len(sig.Body) < 2 {
		return "", errors.New("filedialog: portal sent an unreadable reply")
	}
	code, _ := sig.Body[0].(uint32)
	if code != 0 {
		// 1 is "user canceled", 2 is "ended some other way". Neither is a failure to
		// report; both mean no file.
		return "", ErrCanceled
	}
	results, ok := sig.Body[1].(map[string]dbus.Variant)
	if !ok {
		return "", errors.New("filedialog: portal sent no results")
	}
	var uris []string
	if variant, found := results["uris"]; found {
		// A malformed answer is a portal bug, not the user canceling: say so.
		if err := variant.Store(&uris); err != nil {
			return "", fmt.Errorf("filedialog: portal sent unreadable uris: %w", err)
		}
	}
	if len(uris) == 0 {
		return "", ErrCanceled
	}
	return fromFileURI(uris[0])
}

// fromFileURI turns a file:// URI into the path everything else here speaks.
func fromFileURI(uri string) (string, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("filedialog: unreadable uri %q: %w", uri, err)
	}
	if parsed.Scheme != "" && parsed.Scheme != "file" {
		// A backend can answer with a document-portal or gvfs URI for something that
		// is not a local file. There is nothing useful to do with one here.
		return "", fmt.Errorf("filedialog: %q is not a local file", uri)
	}
	if parsed.Path == "" {
		return "", fmt.Errorf("filedialog: %q names no path", uri)
	}
	return strings.TrimRight(parsed.Path, "/"), nil
}
