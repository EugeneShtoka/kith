// Package protoconv maps domain types to and from the backend v1 wire types,
// shared by the daemon's handlers and the Remote client.
package protoconv

import (
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
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
		Archived:    r.Archived,
		Forum:       r.Forum,
	}
}

// RoomsToProto converts a room list, preserving order.
func RoomsToProto(rooms []domain.Room) []*v1.Room {
	return mapSlice(rooms, RoomToProto)
}

// MediaToProto converts attachment metadata; nil means no attachment.
// PollToProto is a message's poll on the wire; nil for none.
func PollToProto(p *domain.Poll) *v1.Poll {
	if p == nil {
		return nil
	}
	options := make([]*v1.PollOption, len(p.Options))
	for i, o := range p.Options {
		options[i] = &v1.PollOption{Id: o.ID, Text: o.Text, Votes: int64(o.Votes), Mine: o.Mine}
	}
	return &v1.Poll{
		Id: p.ID, Question: p.Question, Options: options,
		Multiple: p.Multiple, Closed: p.Closed, Quiz: p.Quiz, Voters: int64(p.Voters),
	}
}

// ProtoToPoll is the inverse of PollToProto.
func ProtoToPoll(pb *v1.Poll) *domain.Poll {
	if pb == nil {
		return nil
	}
	options := make([]domain.PollOption, len(pb.GetOptions()))
	for i, o := range pb.GetOptions() {
		options[i] = domain.PollOption{ID: o.GetId(), Text: o.GetText(), Votes: int(o.GetVotes()), Mine: o.GetMine()}
	}
	return &domain.Poll{
		ID: pb.GetId(), Question: pb.GetQuestion(), Options: options,
		Multiple: pb.GetMultiple(), Closed: pb.GetClosed(), Quiz: pb.GetQuiz(), Voters: int(pb.GetVoters()),
	}
}

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
		return &v1.Revision{Id: string(r.ID), Body: r.Body, Format: FormattedToProto(r.Format), At: toProtoTime(r.At)}
	})
}

// ProtoToRevisions converts a message's versions back.
func ProtoToRevisions(pb []*v1.Revision) []domain.Revision {
	return mapSlice(pb, func(r *v1.Revision) domain.Revision {
		return domain.Revision{
			ID:     domain.EventID(r.GetId()),
			Body:   r.GetBody(),
			Format: ProtoToFormatted(r.GetFormat()),
			At:     fromProtoTime(r.GetAt()),
		}
	})
}

// FormattedToProto converts formatting to how it draws; nil when there is none. The
// markup it was stored as stays in the daemon.
func FormattedToProto(f richtext.Formatted) *v1.Formatted {
	if f.IsZero() {
		return nil
	}
	return &v1.Formatted{Text: f.Text(), Spans: mapSlice(f.Spans(), func(s richtext.Span) *v1.Span {
		return &v1.Span{
			Start: int64(s.Start), End: int64(s.End),
			Bold: s.Bold, Italic: s.Italic, Code: s.Code, Strike: s.Strike, Underline: s.Underline,
			Link: s.Link, Quote: s.Quote, Heading: s.Heading, Spoiler: s.Spoiler,
			Reason: s.Reason, Href: s.Href,
		}
	})}
}

// ProtoToFormatted converts formatting back, as drawn only.
func ProtoToFormatted(pb *v1.Formatted) richtext.Formatted {
	if pb == nil {
		return richtext.Formatted{}
	}
	return richtext.Drawn(pb.GetText(), mapSlice(pb.GetSpans(), func(s *v1.Span) richtext.Span {
		return richtext.Span{
			Start: int(s.GetStart()), End: int(s.GetEnd()),
			Bold: s.GetBold(), Italic: s.GetItalic(), Code: s.GetCode(), Strike: s.GetStrike(),
			Underline: s.GetUnderline(), Link: s.GetLink(), Quote: s.GetQuote(),
			Heading: s.GetHeading(), Spoiler: s.GetSpoiler(), Reason: s.GetReason(), Href: s.GetHref(),
		}
	}))
}

// MessageToProto converts one timeline message.
func MessageToProto(m domain.Message) *v1.Message {
	return &v1.Message{
		Id:               string(m.ID),
		RoomId:           string(m.RoomID),
		Sender:           m.Sender,
		SenderName:       m.SenderName,
		Body:             m.Body,
		Format:           FormattedToProto(m.Format),
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
		Placeholder:      m.Placeholder,
		Poll:             PollToProto(m.Poll),
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
		Archived:    pb.GetArchived(),
		Forum:       pb.GetForum(),
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
		Format:         ProtoToFormatted(pb.GetFormat()),
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
		Placeholder:    pb.GetPlaceholder(),
		Poll:           ProtoToPoll(pb.GetPoll()),
	}
}

// ProtoToMessages converts a message list back.
func ProtoToMessages(pb []*v1.Message) []domain.Message {
	return mapSlice(pb, ProtoToMessage)
}
