package protoconv

import (
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// PublicRoomsToProto maps a slice of domain public rooms to their wire shape.
func PublicRoomsToProto(rooms []domain.PublicRoom) []*v1.PublicRoom {
	if len(rooms) == 0 {
		return nil
	}
	out := make([]*v1.PublicRoom, 0, len(rooms))
	for _, r := range rooms {
		out = append(out, publicRoomToProto(r))
	}
	return out
}

// ProtoToPublicRooms is PublicRoomsToProto's inverse, for the client side.
func ProtoToPublicRooms(rooms []*v1.PublicRoom) []domain.PublicRoom {
	if len(rooms) == 0 {
		return nil
	}
	out := make([]domain.PublicRoom, 0, len(rooms))
	for _, r := range rooms {
		if r == nil {
			continue
		}
		out = append(out, domain.PublicRoom{
			ID:            domain.RoomID(r.GetRoomId()),
			Alias:         r.GetAlias(),
			Name:          r.GetName(),
			Topic:         r.GetTopic(),
			Members:       int(r.GetMembers()),
			WorldReadable: r.GetWorldReadable(),
			Encrypted:     r.GetEncrypted(),
		})
	}
	return out
}

func publicRoomToProto(r domain.PublicRoom) *v1.PublicRoom {
	return &v1.PublicRoom{
		RoomId:        string(r.ID),
		Alias:         r.Alias,
		Name:          r.Name,
		Topic:         r.Topic,
		Members:       ClampInt32(r.Members),
		WorldReadable: r.WorldReadable,
		Encrypted:     r.Encrypted,
	}
}
