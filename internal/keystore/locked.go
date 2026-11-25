package keystore

import (
	"context"
	"fmt"

	"github.com/godbus/dbus/v5"
)

// Locked reports whether the Secret Service collection go-keyring reads (login, else the
// default alias) is locked, by reading its Locked property. A read through go-keyring
// asks the service to unlock first, which prompts; with no display a prompt fails, and
// gnome-keyring keeps a file descriptor for every one that does, until it runs out and
// stops answering anyone. Reading the property never prompts. It waits at most Wait; an
// error is a keyring that could not say (none, or not answering), not a locked one.
func Locked() (bool, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return false, fmt.Errorf("keystore: the session bus: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), Wait)
	defer cancel()
	for _, path := range []dbus.ObjectPath{"/org/freedesktop/secrets/collection/login", "/org/freedesktop/secrets/aliases/default"} {
		var v dbus.Variant
		err = conn.Object("org.freedesktop.secrets", path).CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0,
			"org.freedesktop.Secret.Collection", "Locked").Store(&v)
		if err == nil {
			locked, ok := v.Value().(bool)
			if !ok {
				return false, fmt.Errorf("keystore: %s's Locked is %s, not a boolean", path, v.Signature())
			}
			return locked, nil
		}
	}
	return false, fmt.Errorf("keystore: is the keyring locked: %w", err)
}
