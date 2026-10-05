package main

import (
	"github.com/EugeneShtoka/kith/internal/session"
)

// keyringSecrets keeps a network's credentials in the OS secret store, under this
// instance: two instances signed in to one workspace hold two sessions.
type keyringSecrets struct {
	service, scope string
}

func (k keyringSecrets) ref(ref string) string { return k.scope + "|" + ref }

// Secret is the value kept under ref; ok false when there is none.
func (k keyringSecrets) Secret(ref string) (string, bool, error) {
	value, err := session.Secret(k.service, k.ref(ref))
	return value, value != "", err //nolint:wrapcheck // session's error says what is missing
}

// StoreSecret keeps value under ref.
func (k keyringSecrets) StoreSecret(ref, value string) error {
	return session.StoreSecret(k.service, k.ref(ref), value) //nolint:wrapcheck // as Secret
}

// DeleteSecret removes what is kept under ref.
func (k keyringSecrets) DeleteSecret(ref string) error {
	return session.DeleteSecret(k.service, k.ref(ref)) //nolint:wrapcheck // as Secret
}
