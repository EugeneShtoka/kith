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

// DeviceRole is what a paired device is: a bridge (a kithd), a client of one, or a
// mailbox (kith-mailbox), which the bridges dial and which connects to none of them.
type DeviceRole string

// The roles a paired device has.
const (
	RoleBridge  DeviceRole = "bridge"
	RoleDevice  DeviceRole = "device"
	RoleMailbox DeviceRole = "mailbox"
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
// the mesh, each other bridge, and each mailbox.
type MeshStatus struct {
	Self       string
	Addresses  []string
	StoreBytes int64
	Removed    bool
	Accounts   []MeshAccount
	Peers      []MeshPeer
	Mailboxes  []MeshMailbox
}

// MeshMailbox is a mailbox of the mesh: its roster entry, the addresses it is dialed
// at, when it was last heard (zero: never), how many ops of each origin this bridge
// holds that it does not, whether a sync stream is open with it, and the size of its
// store as it said last.
type MeshMailbox struct {
	Mailbox    Device
	Addresses  []string
	Heard      time.Time
	Lag        []MeshLag
	Connected  bool
	StoreBytes uint64
}

// MeshRecovered is what recovering a mesh from a mailbox brought back: how many keys
// (read positions, drafts, config, verdicts…), and the old mesh's members, kept as
// removed in the new one, to pair again.
type MeshRecovered struct {
	Keys    int
	Members []Device
}

// MeshLag is how many ops of Origin (a device ID) a peer lacks.
type MeshLag struct {
	Origin string
	Ops    uint64
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

// ConfigConflict is a key of the shared config that bridges wrote concurrently with
// different values: each value, the one applied first.
type ConfigConflict struct {
	Path   string // the key's TOML path
	Values []ConfigVersion
}

// ConfigVersion is one value of a shared config key: as TOML writes it after `key = `
// ("unset" for none), the bridge that wrote it, and when.
type ConfigVersion struct {
	Value  string
	Bridge Device
	At     time.Time
}

// ConfigSetAside is what a bridge joining a mesh set aside of its own config: the file
// it is kept in, and each shared key whose value differed from the mesh's.
type ConfigSetAside struct {
	File string
	Keys []ConfigChange
}

// ConfigChange is a shared key whose value this bridge had (Was) and the mesh's (Now),
// as ConfigVersion writes them.
type ConfigChange struct {
	Path, Was, Now string
}
