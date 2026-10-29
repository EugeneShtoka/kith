package protoconv

import (
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// DeviceToProto converts a paired device.
func DeviceToProto(d domain.Device) *v1.Device {
	return &v1.Device{Name: d.Name, Id: d.ID, AddedAt: toProtoTime(d.AddedAt), AddedBy: d.AddedBy}
}

// ProtoToDevice converts a paired device back; nil is the zero value.
func ProtoToDevice(pb *v1.Device) domain.Device {
	return domain.Device{Name: pb.GetName(), ID: pb.GetId(), AddedAt: fromProtoTime(pb.GetAddedAt()), AddedBy: pb.GetAddedBy()}
}

// DevicesToProto converts the roster.
func DevicesToProto(ds []domain.Device) []*v1.Device { return mapSlice(ds, DeviceToProto) }

// ProtoToDevices converts the roster back.
func ProtoToDevices(pbs []*v1.Device) []domain.Device { return mapSlice(pbs, ProtoToDevice) }
