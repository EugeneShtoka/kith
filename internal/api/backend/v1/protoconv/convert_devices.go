package protoconv

import (
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
	"google.golang.org/protobuf/types/known/durationpb"
)

// DeviceToProto converts a paired device.
func DeviceToProto(d domain.Device) *v1.Device {
	return &v1.Device{Name: d.Name, Id: d.ID, Role: string(d.Role), AddedAt: toProtoTime(d.AddedAt), AddedBy: d.AddedBy}
}

// ProtoToDevice converts a paired device back; nil is the zero value.
func ProtoToDevice(pb *v1.Device) domain.Device {
	return domain.Device{
		Name: pb.GetName(), ID: pb.GetId(), Role: domain.DeviceRole(pb.GetRole()), AddedAt: fromProtoTime(pb.GetAddedAt()), AddedBy: pb.GetAddedBy(),
	}
}

// DevicesToProto converts the roster.
func DevicesToProto(ds []domain.Device) []*v1.Device { return mapSlice(ds, DeviceToProto) }

// ProtoToDevices converts the roster back.
func ProtoToDevices(pbs []*v1.Device) []domain.Device { return mapSlice(pbs, ProtoToDevice) }

// PairingTicketToProto converts a pairing's ticket.
func PairingTicketToProto(t domain.PairingTicket) *v1.PairingTicket {
	return &v1.PairingTicket{Token: t.Token, Addresses: t.Addresses, Id: t.ID, Expires: toProtoTime(t.Expires)}
}

// ProtoToPairingTicket converts a pairing's ticket back; nil is the zero value.
func ProtoToPairingTicket(pb *v1.PairingTicket) domain.PairingTicket {
	return domain.PairingTicket{
		Token: pb.GetToken(), Addresses: pb.GetAddresses(), ID: pb.GetId(), Expires: fromProtoTime(pb.GetExpires()),
	}
}

// PendingDeviceToProto converts the device a pairing waits to confirm.
func PendingDeviceToProto(p domain.PendingDevice) *v1.PendingDevice {
	return &v1.PendingDevice{Name: p.Name, Id: p.ID, Digits: p.Digits, Role: string(p.Role)}
}

// ProtoToPendingDevice converts it back; nil is the zero value.
func ProtoToPendingDevice(pb *v1.PendingDevice) domain.PendingDevice {
	return domain.PendingDevice{Name: pb.GetName(), ID: pb.GetId(), Digits: pb.GetDigits(), Role: domain.DeviceRole(pb.GetRole())}
}

// MeshStatusToProto converts a bridge's view of its mesh.
func MeshStatusToProto(s domain.MeshStatus) *v1.MeshStatusResponse {
	return &v1.MeshStatusResponse{
		Self: s.Self, Addresses: s.Addresses, StoreBytes: s.StoreBytes, Removed: s.Removed,
		Accounts: mapSlice(s.Accounts, meshAccountToProto), Peers: mapSlice(s.Peers, meshPeerToProto),
		Mailboxes: mapSlice(s.Mailboxes, meshMailboxToProto),
	}
}

// ProtoToMeshStatus converts it back; nil is the zero value.
func ProtoToMeshStatus(pb *v1.MeshStatusResponse) domain.MeshStatus {
	return domain.MeshStatus{
		Self: pb.GetSelf(), Addresses: pb.GetAddresses(), StoreBytes: pb.GetStoreBytes(), Removed: pb.GetRemoved(),
		Accounts: mapSlice(pb.GetAccounts(), protoToMeshAccount), Peers: mapSlice(pb.GetPeers(), protoToMeshPeer),
		Mailboxes: mapSlice(pb.GetMailboxes(), protoToMeshMailbox),
	}
}

func meshMailboxToProto(m domain.MeshMailbox) *v1.MeshMailbox {
	return &v1.MeshMailbox{
		Mailbox: DeviceToProto(m.Mailbox), Addresses: m.Addresses, Heard: toProtoTime(m.Heard),
		Lag:       mapSlice(m.Lag, func(l domain.MeshLag) *v1.MeshLag { return &v1.MeshLag{Origin: l.Origin, Ops: l.Ops} }),
		Connected: m.Connected, StoreBytes: m.StoreBytes,
	}
}

func protoToMeshMailbox(pb *v1.MeshMailbox) domain.MeshMailbox {
	return domain.MeshMailbox{
		Mailbox: ProtoToDevice(pb.GetMailbox()), Addresses: pb.GetAddresses(), Heard: fromProtoTime(pb.GetHeard()),
		Lag:       mapSlice(pb.GetLag(), func(l *v1.MeshLag) domain.MeshLag { return domain.MeshLag{Origin: l.GetOrigin(), Ops: l.GetOps()} }),
		Connected: pb.GetConnected(), StoreBytes: pb.GetStoreBytes(),
	}
}

func meshPeerToProto(p domain.MeshPeer) *v1.MeshPeer {
	return &v1.MeshPeer{
		Bridge: DeviceToProto(p.Bridge), Addresses: p.Addresses, Accounts: mapSlice(p.Accounts, meshAccountToProto), Heard: toProtoTime(p.Heard),
		ClockGap: durationpb.New(p.ClockGap), Lag: p.Lag, Connected: p.Connected, Forked: p.Forked,
	}
}

func protoToMeshPeer(pb *v1.MeshPeer) domain.MeshPeer {
	return domain.MeshPeer{
		Bridge: ProtoToDevice(pb.GetBridge()), Addresses: pb.GetAddresses(), Accounts: mapSlice(pb.GetAccounts(), protoToMeshAccount),
		Heard: fromProtoTime(pb.GetHeard()), ClockGap: pb.GetClockGap().AsDuration(),
		Lag: pb.GetLag(), Connected: pb.GetConnected(), Forked: pb.GetForked(),
	}
}

func meshAccountToProto(a domain.MeshAccount) *v1.MeshAccount {
	return &v1.MeshAccount{Network: a.Network, Id: a.ID, Label: a.Label}
}

func protoToMeshAccount(pb *v1.MeshAccount) domain.MeshAccount {
	return domain.MeshAccount{Network: pb.GetNetwork(), ID: pb.GetId(), Label: pb.GetLabel()}
}

// pairingOutcomes maps the domain enum to the wire enum, both directions.
var pairingOutcomes = map[domain.PairingOutcome]v1.PairingOutcome{
	domain.PairingConfirmed: v1.PairingOutcome_PAIRING_OUTCOME_CONFIRMED,
	domain.PairingRefused:   v1.PairingOutcome_PAIRING_OUTCOME_REFUSED,
	domain.PairingExpired:   v1.PairingOutcome_PAIRING_OUTCOME_EXPIRED,
	domain.PairingAbandoned: v1.PairingOutcome_PAIRING_OUTCOME_ABANDONED,
}

// PairingOutcomeToProto converts how a pairing ended; unknown is UNSPECIFIED.
func PairingOutcomeToProto(o domain.PairingOutcome) v1.PairingOutcome { return pairingOutcomes[o] }

// ProtoToPairingOutcome converts it back; ok is false for anything unknown, which no
// caller may read as confirmed.
func ProtoToPairingOutcome(pb v1.PairingOutcome) (domain.PairingOutcome, bool) {
	for o, wire := range pairingOutcomes {
		if wire == pb {
			return o, true
		}
	}
	return 0, false
}

// ConfigSetAsideToProto converts what a bridge joining a mesh set aside of its config.
func ConfigSetAsideToProto(s domain.ConfigSetAside) *v1.ConfigSetAside {
	return &v1.ConfigSetAside{File: s.File, Keys: mapSlice(s.Keys, func(c domain.ConfigChange) *v1.ConfigChange {
		return &v1.ConfigChange{Path: c.Path, Was: c.Was, Now: c.Now}
	})}
}

// ProtoToConfigSetAside converts it back; nil is the zero value.
func ProtoToConfigSetAside(pb *v1.ConfigSetAside) domain.ConfigSetAside {
	return domain.ConfigSetAside{File: pb.GetFile(), Keys: mapSlice(pb.GetKeys(), func(c *v1.ConfigChange) domain.ConfigChange {
		return domain.ConfigChange{Path: c.GetPath(), Was: c.GetWas(), Now: c.GetNow()}
	})}
}

// ConfigConflictsToProto converts the shared config's keys in conflict.
func ConfigConflictsToProto(cs []domain.ConfigConflict) []*v1.ConfigConflict {
	return mapSlice(cs, func(c domain.ConfigConflict) *v1.ConfigConflict {
		return &v1.ConfigConflict{Path: c.Path, Values: mapSlice(c.Values, func(v domain.ConfigVersion) *v1.ConfigVersion {
			return &v1.ConfigVersion{Value: v.Value, Bridge: DeviceToProto(v.Bridge), At: toProtoTime(v.At)}
		})}
	})
}

// ProtoToConfigConflicts converts them back.
func ProtoToConfigConflicts(pbs []*v1.ConfigConflict) []domain.ConfigConflict {
	return mapSlice(pbs, func(c *v1.ConfigConflict) domain.ConfigConflict {
		return domain.ConfigConflict{Path: c.GetPath(), Values: mapSlice(c.GetValues(), func(v *v1.ConfigVersion) domain.ConfigVersion {
			return domain.ConfigVersion{Value: v.GetValue(), Bridge: ProtoToDevice(v.GetBridge()), At: fromProtoTime(v.GetAt())}
		})}
	})
}
