// Package protoconv maps domain types to and from the backend v1 wire types,
// shared by the daemon's handlers and the Remote client.
package protoconv

import (
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// RoomToProto converts one room.
func RoomToProto(r domain.Room) *v1.Room {
	return &v1.Room{
		Id:          string(r.ID),
		Name:        r.Name,
		IsDirect:    r.IsDirect,
		Members:     r.Members,
		Membership:  string(r.Membership),
		InvitedBy:   r.InvitedBy,
		Replacement: string(r.Replacement),
		Topic:       r.Topic,
	}
}

// RoomsToProto converts a room list, preserving order.
func RoomsToProto(rooms []domain.Room) []*v1.Room {
	return mapSlice(rooms, RoomToProto)
}

// MediaToProto converts attachment metadata; nil means no attachment.
func MediaToProto(m *domain.Media) *v1.Media {
	if m == nil {
		return nil
	}
	return &v1.Media{
		Type:   string(m.Type),
		Name:   m.Name,
		Mime:   m.Mime,
		Width:  int64(m.Width),
		Height: int64(m.Height),
		Size:   int64(m.Size),
	}
}

// MentionsToProto converts @-mentions, preserving order.
func MentionsToProto(ms []domain.Mention) []*v1.Mention {
	return mapSlice(ms, func(m domain.Mention) *v1.Mention {
		return &v1.Mention{UserId: m.UserID, Name: m.Name, RoomId: m.RoomID}
	})
}

// RevisionsToProto converts a message's versions.
func RevisionsToProto(revs []domain.Revision) []*v1.Revision {
	return mapSlice(revs, func(r domain.Revision) *v1.Revision {
		return &v1.Revision{Id: string(r.ID), Body: r.Body, Html: r.HTML, At: toProtoTime(r.At)}
	})
}

// ProtoToRevisions converts a message's versions back.
func ProtoToRevisions(pb []*v1.Revision) []domain.Revision {
	return mapSlice(pb, func(r *v1.Revision) domain.Revision {
		return domain.Revision{
			ID:   domain.EventID(r.GetId()),
			Body: r.GetBody(),
			HTML: r.GetHtml(),
			At:   fromProtoTime(r.GetAt()),
		}
	})
}

// MessageToProto converts one timeline message.
func MessageToProto(m domain.Message) *v1.Message {
	return &v1.Message{
		Id:               string(m.ID),
		RoomId:           string(m.RoomID),
		Sender:           m.Sender,
		SenderName:       m.SenderName,
		Body:             m.Body,
		Html:             m.HTML,
		Timestamp:        toProtoTime(m.Timestamp),
		Redacted:         m.Redacted,
		RedactedBy:       m.RedactedBy,
		RedactedReason:   m.RedactedReason,
		RedactedAtUnixMs: unixMillis(m.RedactedAt),
		Edited:           m.Edited,
		EditedAtUnixMs:   unixMillis(m.EditedAt),
		RevisionId:       string(m.RevisionID),
		Reverted:         m.Reverted,
		ReplyTo:          string(m.ReplyTo),
		Mentioned:        m.Mentioned,
		Media:            MediaToProto(m.Media),
		Mentions:         MentionsToProto(m.Mentions),
		ThreadRoot:       string(m.ThreadRoot),
		Emote:            m.Emote,
	}
}

// MessagesToProto converts a message list.
func MessagesToProto(msgs []domain.Message) []*v1.Message {
	return mapSlice(msgs, MessageToProto)
}

// ProtoToRoom converts one room back. Like every ProtoTo* converter, nil yields
// the zero value rather than panicking.
func ProtoToRoom(pb *v1.Room) domain.Room {
	if pb == nil {
		return domain.Room{}
	}
	return domain.Room{
		ID:          domain.RoomID(pb.GetId()),
		Name:        pb.GetName(),
		IsDirect:    pb.GetIsDirect(),
		Members:     pb.GetMembers(),
		Membership:  domain.Membership(pb.GetMembership()),
		InvitedBy:   pb.GetInvitedBy(),
		Replacement: domain.RoomID(pb.GetReplacement()),
		Topic:       pb.GetTopic(),
	}
}

// ProtoToRooms converts a room list back.
func ProtoToRooms(pb []*v1.Room) []domain.Room {
	return mapSlice(pb, ProtoToRoom)
}

// ProtoToMedia converts attachment metadata back.
func ProtoToMedia(pb *v1.Media) *domain.Media {
	if pb == nil {
		return nil
	}
	return &domain.Media{
		Type:   domain.MediaType(pb.GetType()),
		Name:   pb.GetName(),
		Mime:   pb.GetMime(),
		Width:  int(pb.GetWidth()),
		Height: int(pb.GetHeight()),
		Size:   int(pb.GetSize()),
	}
}

// ProtoToMentions converts @-mentions back.
func ProtoToMentions(pb []*v1.Mention) []domain.Mention {
	return mapSlice(pb, func(m *v1.Mention) domain.Mention {
		return domain.Mention{UserID: m.GetUserId(), Name: m.GetName(), RoomID: m.GetRoomId()}
	})
}

// ProtoToMessage converts one timeline message back.
func ProtoToMessage(pb *v1.Message) domain.Message {
	if pb == nil {
		return domain.Message{}
	}
	return domain.Message{
		ID:             domain.EventID(pb.GetId()),
		RoomID:         domain.RoomID(pb.GetRoomId()),
		Sender:         pb.GetSender(),
		SenderName:     pb.GetSenderName(),
		Body:           pb.GetBody(),
		HTML:           pb.GetHtml(),
		Timestamp:      fromProtoTime(pb.GetTimestamp()),
		Redacted:       pb.GetRedacted(),
		RedactedBy:     pb.GetRedactedBy(),
		RedactedReason: pb.GetRedactedReason(),
		RedactedAt:     fromUnixMillis(pb.GetRedactedAtUnixMs()),
		Edited:         pb.GetEdited(),
		EditedAt:       fromUnixMillis(pb.GetEditedAtUnixMs()),
		RevisionID:     domain.EventID(pb.GetRevisionId()),
		Reverted:       pb.GetReverted(),
		ReplyTo:        domain.EventID(pb.GetReplyTo()),
		Mentioned:      pb.GetMentioned(),
		Media:          ProtoToMedia(pb.GetMedia()),
		Mentions:       ProtoToMentions(pb.GetMentions()),
		ThreadRoot:     domain.EventID(pb.GetThreadRoot()),
		Emote:          pb.GetEmote(),
	}
}

// ProtoToMessages converts a message list back.
func ProtoToMessages(pb []*v1.Message) []domain.Message {
	return mapSlice(pb, ProtoToMessage)
}
