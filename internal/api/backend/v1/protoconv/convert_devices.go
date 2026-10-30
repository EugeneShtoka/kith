package protoconv

import (
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
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
	return &v1.PendingDevice{Name: p.Name, Id: p.ID, Digits: p.Digits}
}

// ProtoToPendingDevice converts it back; nil is the zero value.
func ProtoToPendingDevice(pb *v1.PendingDevice) domain.PendingDevice {
	return domain.PendingDevice{Name: pb.GetName(), ID: pb.GetId(), Digits: pb.GetDigits()}
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
