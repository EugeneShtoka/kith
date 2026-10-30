package domain

import "time"

// Device is one device paired with kithd: it reaches every call over [remote] listen.
// ID is its device ID as written (devicekey.ID); AddedBy is the device ID of the one
// that paired it, kithd's own when it was paired over the socket.
type Device struct {
	Name    string
	ID      string
	Role    DeviceRole
	AddedAt time.Time
	AddedBy string
}

// DeviceRole is what a paired device is: a bridge (a kithd) or a client of one.
type DeviceRole string

// The roles a paired device has.
const (
	RoleBridge DeviceRole = "bridge"
	RoleDevice DeviceRole = "device"
)

// PairingTicket is what a new device needs to pair with kithd: its Addresses as a
// device dials them, its device ID to pin, and the Token, good once until Expires.
type PairingTicket struct {
	Token     string
	Addresses []string
	ID        string
	Expires   time.Time
}

// PendingDevice is the device that answered a pairing, waiting to be confirmed;
// Digits are the six both screens show. Role is RoleBridge for a kithd joining the
// mesh.
type PendingDevice struct {
	Name   string
	ID     string
	Digits string
	Role   DeviceRole
}

// MeshStatus is a bridge's view of its mesh: its own ID, the addresses it is dialed at
// and the accounts it runs, the size of its mesh store, whether it was removed from
// the mesh, and each other bridge.
type MeshStatus struct {
	Self       string
	Addresses  []string
	StoreBytes int64
	Removed    bool
	Accounts   []MeshAccount
	Peers      []MeshPeer
}

// MeshPeer is another bridge of the mesh: its roster entry, the addresses it is dialed
// at (none: it only dials), the accounts it runs, when it was last heard (zero: never),
// its wall clock less this bridge's at the time, how many ops this bridge holds that it
// has not acknowledged, whether a sync stream is open with it, and whether it signed
// two ops under one dot (Forked: it is removed for it).
type MeshPeer struct {
	Bridge    Device
	Addresses []string
	Accounts  []MeshAccount
	Heard     time.Time
	ClockGap  time.Duration
	Lag       uint64
	Connected bool
	Forked    bool
}

// MeshAccount is one network account a bridge runs: the network, the account's ID on
// it, and its label.
type MeshAccount struct {
	Network string
	ID      string
	Label   string
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
