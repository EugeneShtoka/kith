package protoconv

import (
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// ReactionToProto converts one m.annotation event.
func ReactionToProto(r domain.Reaction) *v1.Reaction {
	return &v1.Reaction{
		Id:     string(r.ID),
		RoomId: string(r.RoomID),
		Target: string(r.Target),
		Sender: r.Sender,
		Key:    r.Key,
	}
}

// ReactionsToProto converts a reaction list.
func ReactionsToProto(rs []domain.Reaction) []*v1.Reaction {
	return mapSlice(rs, ReactionToProto)
}

// ProtoToReaction converts one reaction back.
func ProtoToReaction(pb *v1.Reaction) domain.Reaction {
	if pb == nil {
		return domain.Reaction{}
	}
	return domain.Reaction{
		ID:     domain.EventID(pb.GetId()),
		RoomID: domain.RoomID(pb.GetRoomId()),
		Target: domain.EventID(pb.GetTarget()),
		Sender: pb.GetSender(),
		Key:    pb.GetKey(),
	}
}

// ProtoToReactions converts a reaction list back.
func ProtoToReactions(pb []*v1.Reaction) []domain.Reaction {
	return mapSlice(pb, ProtoToReaction)
}

// ReactionUpdateToProto converts one reaction add or removal.
func ReactionUpdateToProto(u domain.ReactionUpdate) *v1.ReactionUpdate {
	return &v1.ReactionUpdate{
		Reaction: ReactionToProto(u.Reaction),
		Removed:  u.Removed,
	}
}

// ProtoToReactionUpdate converts one reaction add or removal back.
func ProtoToReactionUpdate(pb *v1.ReactionUpdate) domain.ReactionUpdate {
	if pb == nil {
		return domain.ReactionUpdate{}
	}
	return domain.ReactionUpdate{
		Reaction: ProtoToReaction(pb.GetReaction()),
		Removed:  pb.GetRemoved(),
	}
}

// TimelinePageToProto converts one page of scrollback.
func TimelinePageToProto(p domain.TimelinePage) *v1.TimelinePage {
	return &v1.TimelinePage{
		Messages:  MessagesToProto(p.Messages),
		Reactions: ReactionsToProto(p.Reactions),
		Next:      p.Next,
	}
}

// ProtoToTimelinePage converts a page back; nil reads as the start of history.
func ProtoToTimelinePage(pb *v1.TimelinePage) domain.TimelinePage {
	if pb == nil {
		return domain.TimelinePage{}
	}
	return domain.TimelinePage{
		Messages:  ProtoToMessages(pb.GetMessages()),
		Reactions: ProtoToReactions(pb.GetReactions()),
		Next:      pb.GetNext(),
	}
}

// DraftToProto converts an outgoing message; mentions travel as composed.
func DraftToProto(d domain.Draft) *v1.Draft {
	return &v1.Draft{
		Body:       d.Body,
		Mentions:   MentionsToProto(d.Mentions),
		ReplyTo:    string(d.ReplyTo),
		ThreadRoot: string(d.ThreadRoot),
		Emote:      d.Emote,
		Edits:      string(d.Edits),
		TxnId:      d.TxnID,
		Plain:      d.Plain,
	}
}

// ProtoToDraft converts an outgoing message back.
func ProtoToDraft(pb *v1.Draft) domain.Draft {
	if pb == nil {
		return domain.Draft{}
	}
	return domain.Draft{
		Body:       pb.GetBody(),
		Mentions:   ProtoToMentions(pb.GetMentions()),
		ReplyTo:    domain.EventID(pb.GetReplyTo()),
		ThreadRoot: domain.EventID(pb.GetThreadRoot()),
		Emote:      pb.GetEmote(),
		Edits:      domain.EventID(pb.GetEdits()),
		TxnID:      pb.GetTxnId(),
		Plain:      pb.GetPlain(),
	}
}

// SearchHitToProto converts one search result; the snippet keeps its highlight markers.
func SearchHitToProto(h domain.SearchHit) *v1.SearchHit {
	return &v1.SearchHit{
		RoomId:     string(h.RoomID),
		EventId:    string(h.EventID),
		Sender:     h.Sender,
		SenderName: h.SenderName,
		Timestamp:  toProtoTime(h.Timestamp),
		Snippet:    h.Snippet,
		FileName:   h.FileName,
		Word:       h.Word,
	}
}

// SearchHitsToProto converts a result list.
func SearchHitsToProto(hs []domain.SearchHit) []*v1.SearchHit {
	return mapSlice(hs, SearchHitToProto)
}

// ProtoToSearchHit converts one search result back.
func ProtoToSearchHit(pb *v1.SearchHit) domain.SearchHit {
	if pb == nil {
		return domain.SearchHit{}
	}
	return domain.SearchHit{
		RoomID:     domain.RoomID(pb.GetRoomId()),
		EventID:    domain.EventID(pb.GetEventId()),
		Sender:     pb.GetSender(),
		SenderName: pb.GetSenderName(),
		Timestamp:  fromProtoTime(pb.GetTimestamp()),
		Snippet:    pb.GetSnippet(),
		FileName:   pb.GetFileName(),
		Word:       pb.GetWord(),
	}
}

// ProtoToSearchHits converts a result list back.
func ProtoToSearchHits(pb []*v1.SearchHit) []domain.SearchHit {
	return mapSlice(pb, ProtoToSearchHit)
}

// ThreadsToProto converts a room's thread summaries.
func ThreadsToProto(ts []domain.Thread) []*v1.Thread {
	return mapSlice(ts, func(t domain.Thread) *v1.Thread {
		return &v1.Thread{
			Root:             string(t.Root),
			RoomId:           string(t.RoomID),
			Count:            int64(t.Count),
			Latest:           string(t.Latest),
			LatestAt:         toProtoTime(t.LatestAt),
			LatestSender:     t.LatestSender,
			LatestSenderName: t.LatestSenderName,
			Anchor:           string(t.Anchor),
			RootLoaded:       t.RootLoaded,
			Title:            t.Title,
			Unread:           int64(t.Unread),
			Mentions:         int64(t.Mentions),
		}
	})
}

// ProtoToThreads converts thread summaries back.
func ProtoToThreads(pb []*v1.Thread) []domain.Thread {
	return mapSlice(pb, func(t *v1.Thread) domain.Thread {
		return domain.Thread{
			Root:             domain.EventID(t.GetRoot()),
			RoomID:           domain.RoomID(t.GetRoomId()),
			Count:            int(t.GetCount()),
			Latest:           domain.EventID(t.GetLatest()),
			LatestAt:         fromProtoTime(t.GetLatestAt()),
			LatestSender:     t.GetLatestSender(),
			LatestSenderName: t.GetLatestSenderName(),
			Anchor:           domain.EventID(t.GetAnchor()),
			RootLoaded:       t.GetRootLoaded(),
			Title:            t.GetTitle(),
			Unread:           int(t.GetUnread()),
			Mentions:         int(t.GetMentions()),
		}
	})
}

// DeletionToProto converts what ended a message; nil means not deleted.
func DeletionToProto(d domain.Deletion) *v1.Deletion {
	if !d.Happened() {
		return nil
	}
	return &v1.Deletion{AtUnixMs: unixMillis(d.At), By: d.By, Reason: d.Reason}
}

// ProtoToDeletion converts it back.
func ProtoToDeletion(pb *v1.Deletion) domain.Deletion {
	if pb == nil {
		return domain.Deletion{}
	}
	return domain.Deletion{By: pb.GetBy(), Reason: pb.GetReason(), At: fromUnixMillis(pb.GetAtUnixMs())}
}
