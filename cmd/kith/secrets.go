package main

import (
	"github.com/EugeneShtoka/kith/internal/session"
)

// commandSecrets keeps user commands' secrets in the OS keyring, under the config's
// keyring service (tui.Secrets).
type commandSecrets struct{ service string }

// Secret is the value kept under ref; ok false when there is none.
func (k commandSecrets) Secret(ref string) (string, bool, error) {
	value, err := session.Secret(k.service, ref)
	return value, value != "", err //nolint:wrapcheck // session's error says what is missing
}

// StoreSecret keeps value under ref.
func (k commandSecrets) StoreSecret(ref, value string) error {
	return session.StoreSecret(k.service, ref, value) //nolint:wrapcheck // as Secret
}
