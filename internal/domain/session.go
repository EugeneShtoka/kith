package domain

import "time"

// Session is the persisted login, letting a later run authenticate without a password.
type Session struct {
	// Homeserver is the base URL resolved at login (the `.well-known` answer, not what
	// the user typed).
	Homeserver string
	// UserID is the full MXID (@user:server).
	UserID string
	// DeviceID identifies this installation; the E2EE identity is bound to it.
	DeviceID string
	// AccessToken authenticates every request.
	AccessToken string
	// RefreshToken is the OAuth 2.0 refresh token; empty for password and SSO sessions,
	// which keep AccessToken forever (until the server rejects it).
	RefreshToken string `json:",omitempty"`
	// AccessTokenExpiresAt is when AccessToken stops working, as the token endpoint said
	// at exchange. Zero on password and SSO sessions. The mautrix client refreshes before
	// this instant so a request never finds the token already dead.
	AccessTokenExpiresAt time.Time `json:",omitzero"`
	// OAuthClientID is the OAuth 2.0 client_id this session's homeserver issued to kith
	// (dynamic client registration). Empty on password and SSO sessions. Refresh needs
	// it, so it travels with the tokens rather than being re-registered on every start.
	OAuthClientID string `json:",omitempty"`
}
