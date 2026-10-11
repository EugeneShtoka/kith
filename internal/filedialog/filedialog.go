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

// ErrUnavailable means no portal answered (no desktop session).
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

// Folder asks for one directory. startAt is a hint the backend may ignore.
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
	// Empty parent_window: a terminal has no window handle; the portal accepts that.
	if err := obj.CallWithContext(ctx, chooserIF+".OpenFile", 0, "", title, options).Store(&handle); err != nil {
		if noPortal(err) {
			return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return "", fmt.Errorf("filedialog: portal: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			// Close the chooser still on screen.
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

// noPortal reports whether err means no portal is present, as opposed to a chooser failure.
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
		// 1 is user canceled, 2 is ended otherwise; both mean no file.
		return "", ErrCanceled
	}
	results, ok := sig.Body[1].(map[string]dbus.Variant)
	if !ok {
		return "", errors.New("filedialog: portal sent no results")
	}
	var uris []string
	if variant, found := results["uris"]; found {
		// Malformed uris are a portal bug, not a cancel.
		if err := variant.Store(&uris); err != nil {
			return "", fmt.Errorf("filedialog: portal sent unreadable uris: %w", err)
		}
	}
	if len(uris) == 0 {
		return "", ErrCanceled
	}
	return fromFileURI(uris[0])
}

// fromFileURI turns a file:// URI into a local path.
func fromFileURI(uri string) (string, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("filedialog: unreadable uri %q: %w", uri, err)
	}
	if parsed.Scheme != "" && parsed.Scheme != "file" {
		// e.g. a gvfs or document-portal URI for a non-local file.
		return "", fmt.Errorf("filedialog: %q is not a local file", uri)
	}
	if parsed.Path == "" {
		return "", fmt.Errorf("filedialog: %q names no path", uri)
	}
	return strings.TrimRight(parsed.Path, "/"), nil
}
