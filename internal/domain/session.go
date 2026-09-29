package domain

// Session is the persisted result of a successful login: the credentials that let a
// later run re-authenticate without the password.
type Session struct {
	// Homeserver is the base URL the client talks to, as resolved at login — the
	// `.well-known` answer rather than whatever the user typed, since that is what a
	// later run must reuse to reach the same account.
	Homeserver string
	// UserID is the full MXID (@user:server).
	UserID string
	// DeviceID identifies *this* installation to the homeserver, and is what the E2EE
	// identity is bound to.
	DeviceID string
	// AccessToken authenticates every request.
	AccessToken string
}
