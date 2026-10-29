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

// PairingTicket is what a new device needs to pair with kithd: its Addresses as a
// device dials them, its device ID to pin, and the Token, good once until Expires.
type PairingTicket struct {
	Token     string
	Addresses []string
	ID        string
	Expires   time.Time
}

// PendingDevice is the device that answered a pairing, waiting to be confirmed;
// Digits are the six both screens show.
type PendingDevice struct {
	Name   string
	ID     string
	Digits string
}

// PairingOutcome is how a pairing ended.
type PairingOutcome int

const (
	// PairingConfirmed is a device paired.
	PairingConfirmed PairingOutcome = iota + 1
	// PairingRefused is the trusted device saying no.
	PairingRefused
	// PairingExpired is ten minutes passing unconfirmed.
	PairingExpired
	// PairingAbandoned is the other side going away first.
	PairingAbandoned
)
