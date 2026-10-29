package domain

import "time"

// Device is one device paired with kithd: it reaches every call over [remote] listen.
// ID is its device ID as written (devicekey.ID); AddedBy is the device ID of the one
// that paired it, kithd's own when it was paired over the socket.
type Device struct {
	Name    string
	ID      string
	AddedAt time.Time
	AddedBy string
}
