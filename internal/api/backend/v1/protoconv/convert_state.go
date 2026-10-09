package protoconv

import (
	"sort"
	"time"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// SpaceToProto converts one space and its direct children.
func SpaceToProto(s domain.Space) *v1.Space {
	return &v1.Space{
		Id:       string(s.ID),
		Name:     s.Name,
		Children: RoomIDsToStrings(s.Children),
		Bridge:   string(s.Bridge),
		Original: s.Original,
		Keeper:   s.Keeper,
		LeaveBy:  string(s.LeaveBy),
	}
}

// SpacesToProto converts a space list.
func SpacesToProto(spaces []domain.Space) []*v1.Space {
	return mapSlice(spaces, SpaceToProto)
}

// ProtoToSpace converts one space back.
func ProtoToSpace(pb *v1.Space) domain.Space {
	if pb == nil {
		return domain.Space{}
	}
	return domain.Space{
		ID:       domain.SpaceID(pb.GetId()),
		Name:     pb.GetName(),
		Children: StringsToRoomIDs(pb.GetChildren()),
		Bridge:   domain.Protocol(pb.GetBridge()),
		Original: pb.GetOriginal(),
		Keeper:   pb.GetKeeper(),
		LeaveBy:  domain.RoomID(pb.GetLeaveBy()),
	}
}

// ProtoToSpaces converts a space list back.
func ProtoToSpaces(pb []*v1.Space) []domain.Space {
	return mapSlice(pb, ProtoToSpace)
}

// UnreadToProto converts one room's unread state.
func UnreadToProto(u domain.Unread) *v1.Unread {
	return &v1.Unread{
		RoomId:        string(u.RoomID),
		Notifications: int64(u.Notifications),
		Highlights:    int64(u.Highlights),
		ReadEvent:     string(u.ReadEvent),
		Messages:      int64(u.Messages),
		Mentions:      int64(u.Mentions),
		Counted:       u.Counted,
		Threads:       threadUnreadsToProto(u.Threads),
		Marked:        u.Marked,
	}
}

func threadUnreadsToProto(ts []domain.ThreadUnread) []*v1.ThreadUnread {
	return mapSlice(ts, func(t domain.ThreadUnread) *v1.ThreadUnread {
		return &v1.ThreadUnread{
			Root:     string(t.Root),
			Unread:   int64(t.Unread),
			Mentions: int64(t.Mentions),
			LatestAt: toProtoTime(t.LatestAt),
			Latest:   string(t.Latest),
			Title:    t.Title,
		}
	})
}

func protoToThreadUnreads(pb []*v1.ThreadUnread) []domain.ThreadUnread {
	return mapSlice(pb, func(t *v1.ThreadUnread) domain.ThreadUnread {
		return domain.ThreadUnread{
			Root:     domain.EventID(t.GetRoot()),
			Unread:   int(t.GetUnread()),
			Mentions: int(t.GetMentions()),
			LatestAt: fromProtoTime(t.GetLatestAt()),
			Latest:   domain.EventID(t.GetLatest()),
			Title:    t.GetTitle(),
		}
	})
}

// UnreadsToProto converts an unread list.
func UnreadsToProto(us []domain.Unread) []*v1.Unread {
	return mapSlice(us, UnreadToProto)
}

// ProtoToUnread converts one room's unread state back.
func ProtoToUnread(pb *v1.Unread) domain.Unread {
	if pb == nil {
		return domain.Unread{}
	}
	return domain.Unread{
		RoomID:        domain.RoomID(pb.GetRoomId()),
		Notifications: int(pb.GetNotifications()),
		Highlights:    int(pb.GetHighlights()),
		ReadEvent:     domain.EventID(pb.GetReadEvent()),
		Messages:      int(pb.GetMessages()),
		Mentions:      int(pb.GetMentions()),
		Counted:       pb.GetCounted(),
		Threads:       protoToThreadUnreads(pb.GetThreads()),
		Marked:        pb.GetMarked(),
	}
}

// ProtoToUnreads converts an unread list back.
func ProtoToUnreads(pb []*v1.Unread) []domain.Unread {
	return mapSlice(pb, ProtoToUnread)
}

// MembersToProto converts a room's members.
func MembersToProto(ms []domain.Member) []*v1.Member {
	return mapSlice(ms, func(m domain.Member) *v1.Member {
		return &v1.Member{UserId: m.UserID, DisplayName: m.DisplayName}
	})
}

// ProtoToMembers converts a room's members back.
func ProtoToMembers(pb []*v1.Member) []domain.Member {
	return mapSlice(pb, func(m *v1.Member) domain.Member {
		return domain.Member{UserID: m.GetUserId(), DisplayName: m.GetDisplayName()}
	})
}

// RoomIDsToStrings converts room IDs to bare strings.
func RoomIDsToStrings(ids []domain.RoomID) []string {
	return mapSlice(ids, func(id domain.RoomID) string { return string(id) })
}

// StringsToRoomIDs converts bare strings to room IDs.
func StringsToRoomIDs(ss []string) []domain.RoomID {
	return mapSlice(ss, func(s string) domain.RoomID { return domain.RoomID(s) })
}

// ActivityToProto converts who is typing in a room.
func ActivityToProto(a domain.Activity) *v1.Activity {
	return &v1.Activity{RoomId: string(a.RoomID), Typing: append([]string(nil), a.Typing...)}
}

// ProtoToActivity converts a room's activity back.
func ProtoToActivity(pb *v1.Activity) domain.Activity {
	if pb == nil {
		return domain.Activity{}
	}
	return domain.Activity{RoomID: domain.RoomID(pb.GetRoomId()), Typing: append([]string(nil), pb.GetTyping()...)}
}

// SenderSlotsToProto converts a room's color slots, sorted by key for
// deterministic messages. Slots are clamped to int32.
func SenderSlotsToProto(slots map[string]int) []*v1.SenderSlot {
	if len(slots) == 0 {
		return nil
	}
	keys := make([]string, 0, len(slots))
	for key := range slots {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]*v1.SenderSlot, 0, len(keys))
	for _, key := range keys {
		out = append(out, &v1.SenderSlot{Key: key, Slot: ClampInt32(max(slots[key], 0))})
	}
	return out
}

// ProtoToSenderSlots converts them back; empty yields nil.
func ProtoToSenderSlots(pb []*v1.SenderSlot) map[string]int {
	if len(pb) == 0 {
		return nil
	}
	out := make(map[string]int, len(pb))
	for _, slot := range pb {
		if slot.GetKey() == "" {
			continue
		}
		out[slot.GetKey()] = int(slot.GetSlot())
	}
	return out
}

// SearchRequestToProto converts a search request.
func SearchRequestToProto(req domain.SearchRequest) *v1.SearchMessagesRequest {
	out := &v1.SearchMessagesRequest{
		Query:     req.Filter.Terms,
		Sender:    req.Filter.Sender,
		Limit:     int64(req.Limit),
		Mentioned: req.Filter.Mentioned,
		HasFile:   req.Filter.HasFile,
		Starred:   req.Filter.Starred,
		Tracked:   req.Filter.Tracked,
		Words:     req.Filter.Words,
	}
	out.RoomIds, out.AllRooms = RoomIDsToStrings(req.Rooms.IDs), req.Rooms.All
	out.Since = unixMillis(req.Filter.Since)
	out.Until = unixMillis(req.Filter.Until)
	return out
}

// ProtoToSearchRequest converts a search request back.
func ProtoToSearchRequest(msg *v1.SearchMessagesRequest) domain.SearchRequest {
	return domain.SearchRequest{
		Filter: domain.SearchFilter{
			Terms:     msg.GetQuery(),
			Sender:    msg.GetSender(),
			Mentioned: msg.GetMentioned(),
			HasFile:   msg.GetHasFile(),
			Starred:   msg.GetStarred(),
			Tracked:   msg.GetTracked(),
			Words:     msg.GetWords(),
			Since:     fromUnixMillis(msg.GetSince()),
			Until:     fromUnixMillis(msg.GetUntil()),
		},
		Rooms: ProtoToRoomSet(msg.GetRoomIds(), msg.GetAllRooms()),
		Limit: int(msg.GetLimit()),
	}
}

// ProtoToRoomSet is the rooms a request names: every room with all, else exactly ids
// (none when there are none).
func ProtoToRoomSet(ids []string, all bool) domain.RoomSet {
	return domain.RoomSet{All: all, IDs: StringsToRoomIDs(ids)}
}

// CompleteRequestToProto converts a word-completion request.
func CompleteRequestToProto(req domain.CompleteRequest) *v1.CompleteWordRequest {
	return &v1.CompleteWordRequest{
		Prefix:       req.Prefix,
		RoomIds:      RoomIDsToStrings(req.RoomIDs),
		SpaceRoomIds: RoomIDsToStrings(req.SpaceRooms),
		Scope:        req.Scope,
		Sources:      req.Sources,
		Limit:        int64(req.Limit),
	}
}

// ProtoToCompleteRequest converts it back.
func ProtoToCompleteRequest(msg *v1.CompleteWordRequest) domain.CompleteRequest {
	return domain.CompleteRequest{
		Prefix:     msg.GetPrefix(),
		RoomIDs:    StringsToRoomIDs(msg.GetRoomIds()),
		SpaceRooms: StringsToRoomIDs(msg.GetSpaceRoomIds()),
		Scope:      msg.GetScope(),
		Sources:    msg.GetSources(),
		Limit:      int(msg.GetLimit()),
	}
}

// WordCandidatesToProto converts a ranked completion list.
func WordCandidatesToProto(candidates []domain.WordCandidate) []*v1.WordCandidate {
	return mapSlice(candidates, func(c domain.WordCandidate) *v1.WordCandidate {
		return &v1.WordCandidate{Word: c.Word, Score: int64(c.Score)}
	})
}

// ProtoToWordCandidates converts it back; the order is the ranking.
func ProtoToWordCandidates(msgs []*v1.WordCandidate) []domain.WordCandidate {
	return mapSlice(msgs, func(m *v1.WordCandidate) domain.WordCandidate {
		return domain.WordCandidate{Word: m.GetWord(), Score: int(m.GetScore())}
	})
}

// StoredDraftToProto converts one persisted draft.
func StoredDraftToProto(draft domain.StoredDraft) *v1.StoredDraft {
	return &v1.StoredDraft{
		RoomId:        string(draft.RoomID),
		Body:          draft.Body,
		Caret:         int64(draft.Caret),
		Mentions:      MentionsToProto(draft.Mentions),
		ReplyTo:       string(draft.ReplyTo),
		Editing:       string(draft.Editing),
		EditSaved:     draft.EditSaved,
		Author:        draft.Author,
		UpdatedUnixMs: unixMillis(draft.Updated),
		ThreadRoot:    string(draft.ThreadRoot),
	}
}

// ProtoToStoredDraft converts one back.
func ProtoToStoredDraft(msg *v1.StoredDraft) domain.StoredDraft {
	return domain.StoredDraft{
		RoomID:     domain.RoomID(msg.GetRoomId()),
		Body:       msg.GetBody(),
		Caret:      int(msg.GetCaret()),
		Mentions:   ProtoToMentions(msg.GetMentions()),
		ReplyTo:    domain.EventID(msg.GetReplyTo()),
		Editing:    domain.EventID(msg.GetEditing()),
		EditSaved:  msg.GetEditSaved(),
		Author:     msg.GetAuthor(),
		Updated:    fromUnixMillis(msg.GetUpdatedUnixMs()),
		ThreadRoot: domain.EventID(msg.GetThreadRoot()),
	}
}

// StoredDraftsToProto converts the whole draft set.
func StoredDraftsToProto(drafts []domain.StoredDraft) []*v1.StoredDraft {
	return mapSlice(drafts, StoredDraftToProto)
}

// ProtoToStoredDrafts converts the whole draft set back.
func ProtoToStoredDrafts(msgs []*v1.StoredDraft) []domain.StoredDraft {
	return mapSlice(msgs, ProtoToStoredDraft)
}

// ModelRequestToProto converts one model-layer request.
func ModelRequestToProto(req domain.ModelRequest) *v1.ModelTaskRequest {
	return &v1.ModelTaskRequest{
		Task:        req.Task,
		RoomId:      string(req.RoomID),
		Draft:       req.Draft,
		Instruction: req.Instruction,
		MaxMessages: int64(req.Span.Count),
		SpanSaid:    req.Span.Said,
		ReplyTo:     string(req.ReplyTo),
		DryRun:      req.DryRun,
		SinceUnixMs: unixMillis(req.Span.Since),
	}
}

// ProtoToModelRequest converts one back.
func ProtoToModelRequest(msg *v1.ModelTaskRequest) domain.ModelRequest {
	return domain.ModelRequest{
		Task:        msg.GetTask(),
		RoomID:      domain.RoomID(msg.GetRoomId()),
		Draft:       msg.GetDraft(),
		Instruction: msg.GetInstruction(),
		ReplyTo:     domain.EventID(msg.GetReplyTo()),
		DryRun:      msg.GetDryRun(),
		Span: domain.SummarySpan{
			Count: int(msg.GetMaxMessages()),
			Said:  msg.GetSpanSaid(),
			Since: fromUnixMillis(msg.GetSinceUnixMs()),
		},
	}
}

// ModelResultToProto converts a model result or refusal.
func ModelResultToProto(result domain.ModelResult) *v1.ModelTaskResponse {
	return &v1.ModelTaskResponse{
		Text:     result.Text,
		Refusal:  result.Refusal,
		Endpoint: result.Endpoint,
		Model:    result.Model,
		Note:     result.Note,
		Options:  result.Options,
	}
}

// ProtoToModelResult converts one back.
func ProtoToModelResult(msg *v1.ModelTaskResponse) domain.ModelResult {
	return domain.ModelResult{
		Text:     msg.GetText(),
		Refusal:  msg.GetRefusal(),
		Endpoint: msg.GetEndpoint(),
		Model:    msg.GetModel(),
		Note:     msg.GetNote(),
		Options:  msg.GetOptions(),
	}
}

// ScheduledToProto converts the pending-send queue. Times are UTC throughout
// the queue, which is persisted as RFC3339.
func ScheduledToProto(queue []domain.ScheduledMessage) []*v1.ScheduledMessage {
	out := make([]*v1.ScheduledMessage, 0, len(queue))
	for i := range queue {
		m := &queue[i]
		out = append(out, &v1.ScheduledMessage{
			Id:         m.ID,
			RoomId:     string(m.RoomID),
			Body:       m.Body,
			ThreadRoot: string(m.ThreadRoot),
			ReplyTo:    string(m.ReplyTo),
			Mentions:   MentionsToProto(m.Mentions),
			TxnId:      m.TxnID,
			Emote:      m.Emote,
			AtMs:       m.At.UTC().UnixMilli(),
			WrittenMs:  m.Written.UTC().UnixMilli(),
		})
	}
	return out
}

// ProtoToScheduled converts one pending send back.
func ProtoToScheduled(pb *v1.ScheduledMessage) domain.ScheduledMessage {
	if pb == nil {
		return domain.ScheduledMessage{}
	}
	msg := domain.ScheduledMessage{
		ID:         pb.GetId(),
		RoomID:     domain.RoomID(pb.GetRoomId()),
		Body:       pb.GetBody(),
		ThreadRoot: domain.EventID(pb.GetThreadRoot()),
		ReplyTo:    domain.EventID(pb.GetReplyTo()),
		Mentions:   ProtoToMentions(pb.GetMentions()),
		TxnID:      pb.GetTxnId(),
		Emote:      pb.GetEmote(),
		At:         time.UnixMilli(pb.GetAtMs()).UTC(),
	}
	if ms := pb.GetWrittenMs(); ms != 0 {
		msg.Written = time.UnixMilli(ms).UTC()
	}
	return msg
}

// ProtoToScheduledList converts a whole queue back.
func ProtoToScheduledList(pb []*v1.ScheduledMessage) []domain.ScheduledMessage {
	out := make([]domain.ScheduledMessage, 0, len(pb))
	for _, m := range pb {
		out = append(out, ProtoToScheduled(m))
	}
	return out
}

// SeatHolderToProto is a window's whereabouts on the wire.
func SeatHolderToProto(h domain.SeatHolder) *v1.SeatHolder {
	out := &v1.SeatHolder{Pid: int64(h.PID), Tty: h.TTY, Host: h.Host}
	if !h.Since.IsZero() {
		out.SinceUnixMs = h.Since.UnixMilli()
	}
	return out
}

// ProtoToSeatHolder is a window's whereabouts read back; nil is none.
func ProtoToSeatHolder(h *v1.SeatHolder) domain.SeatHolder {
	if h == nil {
		return domain.SeatHolder{}
	}
	out := domain.SeatHolder{PID: int(h.GetPid()), TTY: h.GetTty(), Host: h.GetHost()}
	if ms := h.GetSinceUnixMs(); ms != 0 {
		out.Since = time.UnixMilli(ms)
	}
	return out
}
