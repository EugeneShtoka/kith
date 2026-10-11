package domain

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
}
