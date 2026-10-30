package protoconv

import (
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// RailToProto converts the rail kithd serves a profile.
func RailToProto(r domain.ServedRail) *v1.RailResponse {
	return &v1.RailResponse{
		Revision: r.Revision,
		Rows:     mapSlice(r.Rows, railRowToProto),
		Rooms:    mapSlice(r.Rooms, servedRoomToProto),
	}
}

// ProtoToRail converts a served rail back.
func ProtoToRail(pb *v1.RailResponse) domain.ServedRail {
	return domain.ServedRail{
		Revision: pb.GetRevision(),
		Rows:     mapSlice(pb.GetRows(), protoToRailRow),
		Rooms:    mapSlice(pb.GetRooms(), protoToServedRoom),
	}
}

// railRowKind is what makes a row, for a client that draws the kinds apart: the
// domain row says it by its key and space.
func railRowKind(r domain.RailRow) v1.RailRowKind {
	switch {
	case r.Space != "":
		return v1.RailRowKind_RAIL_ROW_KIND_SPACE
	case r.Key == domain.RailFallbackKey:
		return v1.RailRowKind_RAIL_ROW_KIND_ALL
	}
	return v1.RailRowKind_RAIL_ROW_KIND_TAG
}

func railRowToProto(r domain.RailRow) *v1.RailRow {
	return &v1.RailRow{
		Key: r.Key, Label: r.Label, Kind: railRowKind(r), Space: string(r.Space),
		Home: string(r.Home), Owner: string(r.Owner),
		Sticky: r.Sticky, First: r.First, CountInLabel: r.CountInLabel, HideWhenEmpty: r.HideWhenEmpty,
		SepAfter: r.SepAfter, Rooms: RoomIDsToStrings(r.Rooms),
	}
}

func protoToRailRow(pb *v1.RailRow) domain.RailRow {
	return domain.RailRow{
		Key: pb.GetKey(), Label: pb.GetLabel(), Space: domain.SpaceID(pb.GetSpace()),
		Home: domain.Protocol(pb.GetHome()), Owner: domain.RoomOwner(pb.GetOwner()),
		Sticky: pb.GetSticky(), First: pb.GetFirst(), CountInLabel: pb.GetCountInLabel(), HideWhenEmpty: pb.GetHideWhenEmpty(),
		SepAfter: pb.GetSepAfter(), Rooms: StringsToRoomIDs(pb.GetRooms()),
	}
}

func servedRoomToProto(r domain.ServedRoom) *v1.RailRoom {
	return &v1.RailRoom{Facts: roomFactsToProto(r.Facts), In: r.In, Owner: r.Owner, Silenced: r.Silenced}
}

func protoToServedRoom(pb *v1.RailRoom) domain.ServedRoom {
	return domain.ServedRoom{Facts: protoToRoomFacts(pb.GetFacts()), In: pb.GetIn(), Owner: pb.GetOwner(), Silenced: pb.GetSilenced()}
}

// roomFactsToProto converts what a place entry can match about a room.
func roomFactsToProto(f domain.RoomFacts) *v1.RoomFacts {
	return &v1.RoomFacts{
		Id: f.ID, Name: f.Name, Spaces: f.Spaces, SpaceLabels: f.SpaceLabels, Direct: f.Direct,
		Protocol: string(f.Protocol), Tags: f.Tags, ArchivedIn: f.ArchivedIn, Caught: f.Caught,
	}
}

// protoToRoomFacts converts a room's facts back.
func protoToRoomFacts(pb *v1.RoomFacts) domain.RoomFacts {
	return domain.RoomFacts{
		ID: pb.GetId(), Name: pb.GetName(), Spaces: pb.GetSpaces(), SpaceLabels: pb.GetSpaceLabels(),
		Direct: pb.GetDirect(), Protocol: domain.Protocol(pb.GetProtocol()), Tags: pb.GetTags(),
		ArchivedIn: pb.GetArchivedIn(), Caught: pb.GetCaught(),
	}
}
