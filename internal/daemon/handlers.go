package daemon

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// server implements BackendService: a thin adapter from the wire to d.Backend,
// the stream fan-out, and the daemon's own state.
type server struct {
	*Daemon
	// lifetime is the daemon's context, for committed work that must outlive the
	// request (see MarkRoomsRead). Nil falls back to the request's context.
	lifetime context.Context //nolint:containedctx // the process's lifetime, deliberately held
	// seat is the one window served (Seat).
	seat *seat
}

var _ backendv1connect.BackendServiceHandler = (*server)(nil)

type (
	req[T any]  = connect.Request[T]
	resp[T any] = connect.Response[T]
)

// Reload re-reads the daemon's configuration and applies it, or returns the
// error that stopped it having changed nothing.
type Reload func(ctx context.Context) error

func roomID(id string) domain.RoomID   { return domain.RoomID(id) }
func eventID(id string) domain.EventID { return domain.EventID(id) }

// ── Room ──

func (s *server) Rooms(ctx context.Context, _ *req[v1.RoomsRequest]) (*resp[v1.RoomsResponse], error) {
	rooms, err := s.Backend.Rooms(ctx)
	return reply(&v1.RoomsResponse{Rooms: pc.RoomsToProto(rooms)}, err)
}

func (s *server) RefreshRooms(ctx context.Context, _ *req[v1.RefreshRoomsRequest]) (*resp[v1.RefreshRoomsResponse], error) {
	rooms, err := s.Backend.RefreshRooms(ctx)
	return reply(&v1.RefreshRoomsResponse{Rooms: pc.RoomsToProto(rooms)}, err)
}

func (s *server) CanonicalParent(ctx context.Context, r *req[v1.CanonicalParentRequest]) (*resp[v1.CanonicalParentResponse], error) {
	parent, err := s.Backend.CanonicalParent(ctx, roomID(r.Msg.GetRoomId()))
	return reply(&v1.CanonicalParentResponse{SpaceId: string(parent)}, err)
}

func (s *server) LastMessages(ctx context.Context, _ *req[v1.LastMessagesRequest]) (*resp[v1.LastMessagesResponse], error) {
	latest, err := s.Backend.LastMessages(ctx)
	at := make(map[string]int64, len(latest))
	for id, when := range latest {
		at[string(id)] = when.UnixMilli()
	}
	return reply(&v1.LastMessagesResponse{At: at}, err)
}

func (s *server) MarkRoomUnread(ctx context.Context, r *req[v1.MarkRoomUnreadRequest]) (*resp[v1.MarkRoomUnreadResponse], error) {
	return reply(&v1.MarkRoomUnreadResponse{}, s.Backend.MarkRoomUnread(ctx, roomID(r.Msg.GetRoomId()), r.Msg.GetUnread()))
}

func (s *server) StarMessage(ctx context.Context, r *req[v1.StarMessageRequest]) (*resp[v1.StarMessageResponse], error) {
	err := s.Backend.StarMessage(ctx, roomID(r.Msg.GetRoomId()), eventID(r.Msg.GetEventId()), r.Msg.GetStarred())
	return reply(&v1.StarMessageResponse{}, err)
}

func (s *server) StarredIn(ctx context.Context, r *req[v1.StarredInRequest]) (*resp[v1.StarredInResponse], error) {
	ids, err := s.Backend.StarredIn(ctx, roomID(r.Msg.GetRoomId()))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return reply(&v1.StarredInResponse{EventIds: out}, err)
}

func (s *server) SpamRooms(ctx context.Context, _ *req[v1.SpamRoomsRequest]) (*resp[v1.SpamRoomsResponse], error) {
	verdicts, err := s.Backend.SpamRooms(ctx)
	return reply(&v1.SpamRoomsResponse{Rooms: pc.SpamToProto(verdicts)}, err)
}

func (s *server) MarkSpam(ctx context.Context, r *req[v1.MarkSpamRequest]) (*resp[v1.MarkSpamResponse], error) {
	return reply(&v1.MarkSpamResponse{}, s.Backend.MarkSpam(ctx, pc.ProtoToSpam(r.Msg.GetVerdict())))
}

func (s *server) MarkRead(ctx context.Context, r *req[v1.MarkReadRequest]) (*resp[v1.MarkReadResponse], error) {
	err := s.Backend.MarkRead(ctx, roomID(r.Msg.GetRoomId()), eventID(r.Msg.GetEventId()), r.Msg.GetPrivate())
	return reply(&v1.MarkReadResponse{}, err)
}

// MarkRoomsRead runs under the daemon's lifetime, not the request's: the user
// already committed to marking a space read, and net/http cancels a request's
// context when the client disconnects, which would leave the space half cleared.
func (s *server) MarkRoomsRead(ctx context.Context, r *req[v1.MarkRoomsReadRequest]) (*resp[v1.MarkRoomsReadResponse], error) {
	if s.lifetime != nil {
		ctx = s.lifetime
	}
	res, err := s.Backend.MarkRoomsRead(ctx, pc.StringsToRoomIDs(r.Msg.GetRoomIds()), r.Msg.GetPrivate())
	return reply(&v1.MarkRoomsReadResponse{
		Marked:     pc.ClampInt32(res.Marked),
		Skipped:    pc.ClampInt32(res.Skipped),
		Failed:     pc.ClampInt32(res.Failed),
		FirstError: res.FirstError,
	}, err)
}

// ── Space ──

func (s *server) Spaces(ctx context.Context, _ *req[v1.SpacesRequest]) (*resp[v1.SpacesResponse], error) {
	spaces, err := s.Backend.Spaces(ctx)
	return reply(&v1.SpacesResponse{Spaces: pc.SpacesToProto(spaces)}, err)
}

func (s *server) RefreshSpaces(ctx context.Context, _ *req[v1.RefreshSpacesRequest]) (*resp[v1.RefreshSpacesResponse], error) {
	spaces, err := s.Backend.RefreshSpaces(ctx)
	return reply(&v1.RefreshSpacesResponse{Spaces: pc.SpacesToProto(spaces)}, err)
}

func (s *server) AddToSpace(ctx context.Context, r *req[v1.AddToSpaceRequest]) (*resp[v1.AddToSpaceResponse], error) {
	err := s.Backend.AddToSpace(ctx, domain.SpaceID(r.Msg.GetSpaceId()), roomID(r.Msg.GetRoomId()))
	return reply(&v1.AddToSpaceResponse{}, err)
}

func (s *server) RemoveFromSpace(ctx context.Context, r *req[v1.RemoveFromSpaceRequest]) (*resp[v1.RemoveFromSpaceResponse], error) {
	err := s.Backend.RemoveFromSpace(ctx, domain.SpaceID(r.Msg.GetSpaceId()), roomID(r.Msg.GetRoomId()))
	return reply(&v1.RemoveFromSpaceResponse{}, err)
}

// ── Timeline ──

func (s *server) MessageHistory(ctx context.Context, r *req[v1.MessageHistoryRequest]) (*resp[v1.MessageHistoryResponse], error) {
	revs, deletion, err := s.Backend.MessageHistory(ctx, roomID(r.Msg.GetRoomId()), eventID(r.Msg.GetEventId()))
	return reply(&v1.MessageHistoryResponse{
		Revisions: pc.RevisionsToProto(revs),
		Deletion:  pc.DeletionToProto(deletion),
	}, err)
}

func (s *server) CachedTimeline(ctx context.Context, r *req[v1.CachedTimelineRequest]) (*resp[v1.CachedTimelineResponse], error) {
	msgs, err := s.Backend.CachedTimeline(ctx, roomID(r.Msg.GetRoomId()))
	return reply(&v1.CachedTimelineResponse{Messages: pc.MessagesToProto(msgs)}, err)
}

func (s *server) Timeline(ctx context.Context, r *req[v1.TimelineRequest]) (*resp[v1.TimelineResponse], error) {
	page, err := s.Backend.Timeline(ctx, roomID(r.Msg.GetRoomId()), r.Msg.GetFrom(), int(r.Msg.GetLimit()))
	return reply(&v1.TimelineResponse{Page: pc.TimelinePageToProto(page)}, err)
}

func (s *server) FetchEvent(ctx context.Context, r *req[v1.FetchEventRequest]) (*resp[v1.FetchEventResponse], error) {
	msg, err := s.Backend.FetchEvent(ctx, roomID(r.Msg.GetRoomId()), eventID(r.Msg.GetEventId()))
	return reply(&v1.FetchEventResponse{Message: pc.MessageToProto(msg)}, err)
}

func (s *server) Send(ctx context.Context, r *req[v1.SendRequest]) (*resp[v1.SendResponse], error) {
	return reply(&v1.SendResponse{}, s.Backend.Send(ctx, roomID(r.Msg.GetRoomId()), pc.ProtoToDraft(r.Msg.GetDraft())))
}

func (s *server) Redact(ctx context.Context, r *req[v1.RedactRequest]) (*resp[v1.RedactResponse], error) {
	err := s.Backend.Redact(ctx, roomID(r.Msg.GetRoomId()), eventID(r.Msg.GetEventId()), r.Msg.GetReason())
	return reply(&v1.RedactResponse{}, err)
}

func (s *server) SendTyping(ctx context.Context, r *req[v1.SendTypingRequest]) (*resp[v1.SendTypingResponse], error) {
	timeout := time.Duration(r.Msg.GetTimeoutMs()) * time.Millisecond
	return reply(&v1.SendTypingResponse{}, s.Backend.SendTyping(ctx, roomID(r.Msg.GetRoomId()), r.Msg.GetTyping(), timeout))
}

// SendFile refuses relative paths: the daemon's working directory is systemd's,
// not the user's.
func (s *server) SendFile(ctx context.Context, r *req[v1.SendFileRequest]) (*resp[v1.SendFileResponse], error) {
	path := r.Msg.GetPath()
	if !filepath.IsAbs(path) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("daemon: send file: the path must be absolute"))
	}
	return reply(&v1.SendFileResponse{}, s.Backend.SendFile(ctx, roomID(r.Msg.GetRoomId()), path, r.Msg.GetCaption()))
}

// ── Unread ──

func (s *server) CachedUnread(ctx context.Context, _ *req[v1.CachedUnreadRequest]) (*resp[v1.CachedUnreadResponse], error) {
	unread, err := s.Backend.CachedUnread(ctx)
	return reply(&v1.CachedUnreadResponse{Unread: pc.UnreadsToProto(unread)}, err)
}

func (s *server) UnreadStream(ctx context.Context, _ *req[v1.UnreadStreamRequest], st *connect.ServerStream[v1.UnreadStreamResponse]) error {
	return serveStream(ctx, s.Streams.unread, st, func(u domain.Unread) *v1.UnreadStreamResponse {
		return &v1.UnreadStreamResponse{Unread: pc.UnreadToProto(u)}
	})
}

// ── Thread ──

func (s *server) ListThreads(ctx context.Context, r *req[v1.ListThreadsRequest]) (*resp[v1.ListThreadsResponse], error) {
	threads, err := s.Backend.ListThreads(ctx, roomID(r.Msg.GetRoomId()))
	return reply(&v1.ListThreadsResponse{Threads: pc.ThreadsToProto(threads)}, err)
}

func (s *server) ThreadPage(ctx context.Context, r *req[v1.ThreadPageRequest]) (*resp[v1.ThreadPageResponse], error) {
	page, err := s.Backend.ThreadPage(ctx, roomID(r.Msg.GetRoomId()),
		eventID(r.Msg.GetRoot()), r.Msg.GetFrom(), int(r.Msg.GetLimit()))
	return reply(&v1.ThreadPageResponse{Page: pc.TimelinePageToProto(page)}, err)
}

func (s *server) MarkThreadRead(ctx context.Context, r *req[v1.MarkThreadReadRequest]) (*resp[v1.MarkThreadReadResponse], error) {
	err := s.Backend.MarkThreadRead(ctx, roomID(r.Msg.GetRoomId()),
		eventID(r.Msg.GetRoot()), eventID(r.Msg.GetEventId()), r.Msg.GetPrivate())
	return reply(&v1.MarkThreadReadResponse{}, err)
}

// ── Reaction ──

func (s *server) CachedReactions(ctx context.Context, r *req[v1.CachedReactionsRequest]) (*resp[v1.CachedReactionsResponse], error) {
	rs, err := s.Backend.CachedReactions(ctx, roomID(r.Msg.GetRoomId()))
	return reply(&v1.CachedReactionsResponse{Reactions: pc.ReactionsToProto(rs)}, err)
}

func (s *server) Reactions(ctx context.Context, _ *req[v1.ReactionsRequest], st *connect.ServerStream[v1.ReactionsResponse]) error {
	return serveStream(ctx, s.Streams.reactions, st, func(u domain.ReactionUpdate) *v1.ReactionsResponse {
		return &v1.ReactionsResponse{Update: pc.ReactionUpdateToProto(u)}
	})
}

func (s *server) SendReaction(ctx context.Context, r *req[v1.SendReactionRequest]) (*resp[v1.SendReactionResponse], error) {
	err := s.Backend.SendReaction(ctx, roomID(r.Msg.GetRoomId()), eventID(r.Msg.GetTarget()), r.Msg.GetKey())
	return reply(&v1.SendReactionResponse{}, err)
}

func (s *server) ReactionRefusals(ctx context.Context, _ *req[v1.ReactionRefusalsRequest]) (*resp[v1.ReactionRefusalsResponse], error) {
	refusals, err := s.Backend.ReactionRefusals(ctx)
	return reply(&v1.ReactionRefusalsResponse{Refusals: pc.ReactionRefusalsToProto(refusals)}, err)
}

func (s *server) RecordReactionRefusal(ctx context.Context, r *req[v1.RecordReactionRefusalRequest]) (*resp[v1.RecordReactionRefusalResponse], error) {
	return reply(&v1.RecordReactionRefusalResponse{}, s.Backend.RecordReactionRefusal(ctx, r.Msg.GetProtocol(), r.Msg.GetEmoji()))
}

// ── Emoji ──

func (s *server) RecordEmoji(ctx context.Context, r *req[v1.RecordEmojiRequest]) (*resp[v1.RecordEmojiResponse], error) {
	err := s.Backend.RecordEmoji(ctx, domain.EmojiKind(r.Msg.GetKind()), roomID(r.Msg.GetRoomId()), r.Msg.GetEmoji())
	return reply(&v1.RecordEmojiResponse{}, err)
}

func (s *server) EmojiScores(ctx context.Context, r *req[v1.EmojiScoresRequest]) (*resp[v1.EmojiScoresResponse], error) {
	scores, err := s.Backend.EmojiScores(ctx, domain.EmojiKind(r.Msg.GetKind()), roomID(r.Msg.GetRoomId()),
		pc.StringsToRoomIDs(r.Msg.GetSpaceRooms()), r.Msg.GetScope())
	out := make(map[string]int32, len(scores))
	for emoji, score := range scores {
		out[emoji] = pc.ClampInt32(score)
	}
	return reply(&v1.EmojiScoresResponse{Scores: out}, err)
}

// ── Media ──

func (s *server) LoadImage(ctx context.Context, r *req[v1.LoadImageRequest]) (*resp[v1.LoadImageResponse], error) {
	data, err := s.Backend.LoadImage(ctx, roomID(r.Msg.GetRoomId()), eventID(r.Msg.GetEventId()))
	return reply(&v1.LoadImageResponse{Data: data}, err)
}

// ── Member ──

func (s *server) Members(ctx context.Context, r *req[v1.MembersRequest]) (*resp[v1.MembersResponse], error) {
	members, err := s.Backend.Members(ctx, roomID(r.Msg.GetRoomId()), int(r.Msg.GetLimit()))
	return reply(&v1.MembersResponse{Members: pc.MembersToProto(members)}, err)
}

func (s *server) RefreshMembers(ctx context.Context, r *req[v1.RefreshMembersRequest]) (*resp[v1.RefreshMembersResponse], error) {
	members, err := s.Backend.RefreshMembers(ctx, roomID(r.Msg.GetRoomId()))
	return reply(&v1.RefreshMembersResponse{Members: pc.MembersToProto(members)}, err)
}

func (s *server) MentionCandidates(ctx context.Context, r *req[v1.MentionCandidatesRequest]) (*resp[v1.MentionCandidatesResponse], error) {
	members, err := s.Backend.MentionCandidates(ctx, roomID(r.Msg.GetRoomId()), int(r.Msg.GetLimit()))
	return reply(&v1.MentionCandidatesResponse{Members: pc.MembersToProto(members)}, err)
}

func (s *server) SearchSenders(ctx context.Context, r *req[v1.SearchSendersRequest]) (*resp[v1.SearchSendersResponse], error) {
	senders, err := s.Backend.SearchSenders(ctx, pc.ProtoToRoomSet(r.Msg.GetRoomIds(), r.Msg.GetAllRooms()), int(r.Msg.GetLimit()))
	return reply(&v1.SearchSendersResponse{Members: pc.MembersToProto(senders)}, err)
}

func (s *server) DirectCandidates(ctx context.Context, r *req[v1.DirectCandidatesRequest]) (*resp[v1.DirectCandidatesResponse], error) {
	members, err := s.Backend.DirectCandidates(ctx, int(r.Msg.GetLimit()))
	return reply(&v1.DirectCandidatesResponse{Members: pc.MembersToProto(members)}, err)
}

func (s *server) SenderSlots(ctx context.Context, r *req[v1.SenderSlotsRequest]) (*resp[v1.SenderSlotsResponse], error) {
	slots, err := s.Backend.SenderSlots(ctx, roomID(r.Msg.GetRoomId()))
	return reply(&v1.SenderSlotsResponse{Slots: pc.SenderSlotsToProto(slots)}, err)
}

func (s *server) SaveSenderSlots(ctx context.Context, r *req[v1.SaveSenderSlotsRequest]) (*resp[v1.SaveSenderSlotsResponse], error) {
	err := s.Backend.SaveSenderSlots(ctx, roomID(r.Msg.GetRoomId()), pc.ProtoToSenderSlots(r.Msg.GetSlots()))
	return reply(&v1.SaveSenderSlotsResponse{}, err)
}

// ── Membership ──

func (s *server) CachedInvites(ctx context.Context, _ *req[v1.CachedInvitesRequest]) (*resp[v1.CachedInvitesResponse], error) {
	invites, err := s.Backend.CachedInvites(ctx)
	return reply(&v1.CachedInvitesResponse{Invites: pc.RoomsToProto(invites)}, err)
}

// Invites sends every frame, empty included: an empty set means the last
// invitation was answered.
func (s *server) Invites(ctx context.Context, _ *req[v1.InvitesRequest], st *connect.ServerStream[v1.InvitesResponse]) error {
	return serveStream(ctx, s.Streams.invites, st, func(rooms []domain.Room) *v1.InvitesResponse {
		return &v1.InvitesResponse{Invites: pc.RoomsToProto(rooms)}
	})
}

func (s *server) JoinRoom(ctx context.Context, r *req[v1.JoinRoomRequest]) (*resp[v1.JoinRoomResponse], error) {
	id, err := s.Backend.JoinRoom(ctx, r.Msg.GetRoomIdOrAlias(), r.Msg.GetVia())
	return reply(&v1.JoinRoomResponse{RoomId: string(id)}, err)
}

func (s *server) LeaveRoom(ctx context.Context, r *req[v1.LeaveRoomRequest]) (*resp[v1.LeaveRoomResponse], error) {
	return reply(&v1.LeaveRoomResponse{}, s.Backend.LeaveRoom(ctx, roomID(r.Msg.GetRoomId())))
}

func (s *server) InviteUser(ctx context.Context, r *req[v1.InviteUserRequest]) (*resp[v1.InviteUserResponse], error) {
	return reply(&v1.InviteUserResponse{}, s.Backend.InviteUser(ctx, roomID(r.Msg.GetRoomId()), r.Msg.GetUserId()))
}

func (s *server) KickUser(ctx context.Context, r *req[v1.KickUserRequest]) (*resp[v1.KickUserResponse], error) {
	return reply(&v1.KickUserResponse{}, s.Backend.KickUser(ctx, roomID(r.Msg.GetRoomId()), r.Msg.GetUserId(), r.Msg.GetReason()))
}

func (s *server) BanUser(ctx context.Context, r *req[v1.BanUserRequest]) (*resp[v1.BanUserResponse], error) {
	return reply(&v1.BanUserResponse{}, s.Backend.BanUser(ctx, roomID(r.Msg.GetRoomId()), r.Msg.GetUserId(), r.Msg.GetReason()))
}

func (s *server) UnbanUser(ctx context.Context, r *req[v1.UnbanUserRequest]) (*resp[v1.UnbanUserResponse], error) {
	return reply(&v1.UnbanUserResponse{}, s.Backend.UnbanUser(ctx, roomID(r.Msg.GetRoomId()), r.Msg.GetUserId()))
}

// CreateRoom returns the ID even alongside an error (the room exists but a
// later step failed). Connect cannot carry both, so the error travels as Warning.
func (s *server) CreateRoom(ctx context.Context, r *req[v1.CreateRoomRequest]) (*resp[v1.CreateRoomResponse], error) {
	id, err := s.Backend.CreateRoom(ctx, domain.NewRoom{
		Name:      r.Msg.GetName(),
		Space:     r.Msg.GetSpace(),
		Encrypted: r.Msg.GetEncrypted(),
		Public:    r.Msg.GetPublic(),
		Parent:    domain.SpaceID(r.Msg.GetParent()),
		Invite:    r.Msg.GetInvite(),
		Direct:    r.Msg.GetDirect(),
	})
	if err != nil && id == "" {
		return nil, rpcErr(err)
	}
	out := &v1.CreateRoomResponse{RoomId: string(id)}
	if err != nil {
		out.Warning = err.Error()
	}
	return connect.NewResponse(out), nil
}

// ── Search ──

func (s *server) SearchMessages(ctx context.Context, r *req[v1.SearchMessagesRequest]) (*resp[v1.SearchMessagesResponse], error) {
	hits, err := s.Backend.SearchMessages(ctx, pc.ProtoToSearchRequest(r.Msg))
	return reply(&v1.SearchMessagesResponse{Hits: pc.SearchHitsToProto(hits)}, err)
}

func (s *server) CompleteWord(ctx context.Context, r *req[v1.CompleteWordRequest]) (*resp[v1.CompleteWordResponse], error) {
	candidates, err := s.Backend.CompleteWord(ctx, pc.ProtoToCompleteRequest(r.Msg))
	return reply(&v1.CompleteWordResponse{Candidates: pc.WordCandidatesToProto(candidates)}, err)
}

func (s *server) ModelTask(ctx context.Context, r *req[v1.ModelTaskRequest]) (*resp[v1.ModelTaskResponse], error) {
	result, err := s.Backend.ModelTask(ctx, pc.ProtoToModelRequest(r.Msg))
	return reply(pc.ModelResultToProto(result), err)
}

func (s *server) ReplaceDraft(ctx context.Context, r *req[v1.ReplaceDraftRequest]) (*resp[v1.ReplaceDraftResponse], error) {
	draft := pc.ProtoToStoredDraft(r.Msg.GetDraft())
	var saved bool
	var wrote error
	err := s.seat.write(r.Msg.GetClient(), func() error {
		saved, wrote = s.Backend.ReplaceDraft(ctx, draft, pc.ProtoToStoredDraft(r.Msg.GetOver()))
		return nil
	})
	return reply(&v1.ReplaceDraftResponse{Saved: saved}, cmp.Or(err, wrote))
}

// Seat gives the caller the seat and keeps it for as long as the stream is open, or
// says who has it (see api.Seat).
// errWhatsAppOff refuses pairing while [whatsapp] is not enabled.
var errWhatsAppOff = fmt.Errorf("%w: [whatsapp] enabled is not set in the config kithd runs with", api.ErrNetworkOff)

func (s *server) PairWhatsApp(
	ctx context.Context, r *req[v1.PairWhatsAppRequest], st *connect.ServerStream[v1.PairWhatsAppResponse],
) error {
	if s.WhatsApp == nil {
		return rpcErr(errWhatsAppOff)
	}
	linked, err := s.WhatsApp.PairWhatsApp(ctx, r.Msg.GetAccount(), func(code string) error {
		return sendFrame(st, &v1.PairWhatsAppResponse{Event: &v1.PairWhatsAppResponse_Code{Code: code}})
	})
	if err != nil {
		return rpcErr(err)
	}
	return sendFrame(st, &v1.PairWhatsAppResponse{Event: &v1.PairWhatsAppResponse_Linked{Linked: linked}})
}

// errMatrixOff refuses a Matrix login while the config names no Matrix account.
var errMatrixOff = fmt.Errorf("%w: the config kithd runs with sets no homeserver and user", api.ErrNetworkOff)

func (s *server) LoginMatrix(ctx context.Context, r *req[v1.LoginMatrixRequest]) (*resp[v1.LoginMatrixResponse], error) {
	if s.Matrix == nil {
		return nil, rpcErr(errMatrixOff)
	}
	in, err := s.Matrix.LoginMatrix(ctx, r.Msg.GetPassword())
	return reply(&v1.LoginMatrixResponse{UserId: in.UserID, DeviceId: in.DeviceID, Started: in.Started}, err)
}

// errSlackOff refuses a Slack sign-in while [slack] is off.
var errSlackOff = fmt.Errorf("%w: [slack] is not enabled in the config kithd runs with", api.ErrNetworkOff)

func (s *server) SignInSlack(ctx context.Context, r *req[v1.SignInSlackRequest]) (*resp[v1.SignInSlackResponse], error) {
	if s.Slack == nil {
		return nil, rpcErr(errSlackOff)
	}
	in, err := s.Slack.SignInSlack(ctx, r.Msg.GetAccount(), r.Msg.GetToken(), r.Msg.GetCookie())
	return reply(&v1.SignInSlackResponse{Workspace: in.Workspace, User: in.User}, err)
}

func (s *server) Seat(ctx context.Context, r *req[v1.SeatRequest], st *connect.ServerStream[v1.SeatResponse]) error {
	sat, err := s.seat.take(ctx, r.Msg.GetClient(), r.Msg.GetForce(), pc.ProtoToSeatHolder(r.Msg.GetWhere()))
	if errors.Is(err, api.ErrSeatTaken) {
		s.seat.mu.Lock()
		other := s.seat.where
		s.seat.mu.Unlock()
		return sendFrame(st, &v1.SeatResponse{Event: &v1.SeatResponse_Taken{Taken: pc.SeatHolderToProto(other)}})
	}
	if err != nil {
		return rpcErr(err)
	}
	defer s.seat.leave(sat)
	if err := sendFrame(st, &v1.SeatResponse{Event: &v1.SeatResponse_Granted{Granted: true}}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return nil
	case <-sat.aside:
	}
	if err := sendFrame(st, &v1.SeatResponse{Event: &v1.SeatResponse_StepAside{StepAside: true}}); err != nil {
		return err
	}
	// The window saves its drafts, then closes the stream; one that does not answer
	// is let go of here once the seat is given over without it.
	select {
	case <-ctx.Done():
	case <-sat.over:
	}
	return nil
}

// sendFrame sends one stream frame, as a wire error when it cannot.
func sendFrame[T any](st *connect.ServerStream[T], frame *T) error {
	if err := st.Send(frame); err != nil {
		return rpcErr(fmt.Errorf("send stream frame: %w", err))
	}
	return nil
}

func (s *server) Drafts(ctx context.Context, _ *req[v1.DraftsRequest]) (*resp[v1.DraftsResponse], error) {
	drafts, err := s.Backend.Drafts(ctx)
	return reply(&v1.DraftsResponse{Drafts: pc.StoredDraftsToProto(drafts)}, err)
}

func (s *server) RoomsWith(ctx context.Context, r *req[v1.RoomsWithRequest]) (*resp[v1.RoomsWithResponse], error) {
	rooms, err := s.Backend.RoomsWith(ctx, r.Msg.GetUserIds(), pc.ProtoToRoomSet(r.Msg.GetRoomIds(), r.Msg.GetAllRooms()), int(r.Msg.GetLimit()))
	return reply(&v1.RoomsWithResponse{Rooms: pc.RoomsToProto(rooms)}, err)
}

func (s *server) MessagesAround(ctx context.Context, r *req[v1.MessagesAroundRequest]) (*resp[v1.MessagesAroundResponse], error) {
	msgs, err := s.Backend.MessagesAround(ctx, roomID(r.Msg.GetRoomId()), eventID(r.Msg.GetEventId()),
		int(r.Msg.GetBefore()), int(r.Msg.GetAfter()))
	return reply(&v1.MessagesAroundResponse{Messages: pc.MessagesToProto(msgs)}, err)
}

// RoomEncryption answers with the encrypted subset; the backend has already
// reported unknown rooms as encrypted.
func (s *server) RoomEncryption(ctx context.Context, r *req[v1.RoomEncryptionRequest]) (*resp[v1.RoomEncryptionResponse], error) {
	rooms := pc.StringsToRoomIDs(r.Msg.GetRoomIds())
	state, err := s.Backend.RoomEncryption(ctx, rooms)
	var encrypted []string
	for _, room := range rooms {
		if state[room] {
			encrypted = append(encrypted, string(room))
		}
	}
	return reply(&v1.RoomEncryptionResponse{EncryptedRoomIds: encrypted}, err)
}

// ── Sync ──

func (s *server) Messages(ctx context.Context, _ *req[v1.MessagesRequest], st *connect.ServerStream[v1.MessagesResponse]) error {
	return serveStream(ctx, s.Streams.messages, st, func(m domain.Message) *v1.MessagesResponse {
		return &v1.MessagesResponse{Message: pc.MessageToProto(m)}
	})
}

// Follow hands a matrix URI to the attached client; nobody listening is a
// false, not an error.
// Follow hands a link to the window that sits: at once when it listens, else when its
// link stream opens (it is starting up, or reconnecting). Delivered is false only with
// no window, and `kith --open` then starts one.
func (s *server) Follow(_ context.Context, r *req[v1.FollowRequest]) (*resp[v1.FollowResponse], error) {
	uri := r.Msg.GetUri()
	delivered := s.Streams.Follow(uri) || s.seat.keepLink(uri)
	return connect.NewResponse(&v1.FollowResponse{Delivered: delivered}), nil
}

func (s *server) FollowStream(ctx context.Context, _ *req[v1.FollowStreamRequest], st *connect.ServerStream[v1.FollowStreamResponse]) error {
	if uri := s.seat.takeLink(); uri != "" {
		if err := sendFrame(st, &v1.FollowStreamResponse{Uri: uri}); err != nil {
			return err
		}
	}
	return serveStream(ctx, s.Streams.follows, st, func(uri string) *v1.FollowStreamResponse {
		return &v1.FollowStreamResponse{Uri: uri}
	})
}

func (s *server) ActivityStream(ctx context.Context, _ *req[v1.ActivityStreamRequest], st *connect.ServerStream[v1.ActivityStreamResponse]) error {
	return serveStream(ctx, s.Streams.activity, st, func(a domain.Activity) *v1.ActivityStreamResponse {
		return &v1.ActivityStreamResponse{Activity: pc.ActivityToProto(a)}
	})
}

// ── Verification ──

func (s *server) Verifications(ctx context.Context, _ *req[v1.VerificationsRequest], st *connect.ServerStream[v1.VerificationsResponse]) error {
	return serveStream(ctx, s.Streams.verifications, st, func(v domain.Verification) *v1.VerificationsResponse {
		return &v1.VerificationsResponse{Verification: pc.VerificationToProto(v)}
	})
}

func (s *server) StartVerification(ctx context.Context, _ *req[v1.StartVerificationRequest]) (*resp[v1.StartVerificationResponse], error) {
	txnID, err := s.Backend.StartVerification(ctx)
	return reply(&v1.StartVerificationResponse{TxnId: txnID}, err)
}

func (s *server) AcceptVerification(ctx context.Context, r *req[v1.AcceptVerificationRequest]) (*resp[v1.AcceptVerificationResponse], error) {
	return reply(&v1.AcceptVerificationResponse{}, s.Backend.AcceptVerification(ctx, r.Msg.GetTxnId()))
}

func (s *server) ConfirmSAS(ctx context.Context, r *req[v1.ConfirmSASRequest]) (*resp[v1.ConfirmSASResponse], error) {
	return reply(&v1.ConfirmSASResponse{}, s.Backend.ConfirmSAS(ctx, r.Msg.GetTxnId()))
}

func (s *server) CancelVerification(ctx context.Context, r *req[v1.CancelVerificationRequest]) (*resp[v1.CancelVerificationResponse], error) {
	return reply(&v1.CancelVerificationResponse{}, s.Backend.CancelVerification(ctx, r.Msg.GetTxnId()))
}

// ── Maintenance ──

// Status answers before the daemon is ready: clients poll it while waiting.
func (s *server) Status(_ context.Context, _ *req[v1.StatusRequest]) (*resp[v1.StatusResponse], error) {
	ready, syncedAt, lastErr := s.State.Snapshot()
	out := &v1.StatusResponse{Ready: ready, LastError: lastErr}
	if !syncedAt.IsZero() {
		out.SyncedAt = timestamppb.New(syncedAt)
	}
	for _, n := range s.State.Networks() {
		row := &v1.NetworkStatus{Network: n.Network, Account: n.Account, Phase: phaseToProto(n.Phase), Detail: n.Detail}
		if !n.At.IsZero() {
			row.OnlineAt = timestamppb.New(n.At)
		}
		out.Networks = append(out.Networks, row)
	}
	return connect.NewResponse(out), nil
}

// phaseToProto is a Phase on the wire.
func phaseToProto(p Phase) v1.NetworkPhase {
	switch p {
	case PhaseLoggedOut:
		return v1.NetworkPhase_NETWORK_PHASE_LOGGED_OUT
	case PhaseConnecting:
		return v1.NetworkPhase_NETWORK_PHASE_CONNECTING
	case PhaseOnline:
		return v1.NetworkPhase_NETWORK_PHASE_ONLINE
	case PhaseFailed:
		return v1.NetworkPhase_NETWORK_PHASE_FAILED
	default:
		return v1.NetworkPhase_NETWORK_PHASE_UNSPECIFIED
	}
}

func (s *server) Selves(ctx context.Context, _ *req[v1.SelvesRequest]) (*resp[v1.SelvesResponse], error) {
	ids, err := s.Backend.Selves(ctx)
	return reply(&v1.SelvesResponse{Ids: ids}, err)
}

// protoToPhase is a wire phase as a Phase; one this build does not know is 0.
func protoToPhase(p v1.NetworkPhase) Phase {
	switch p {
	case v1.NetworkPhase_NETWORK_PHASE_LOGGED_OUT:
		return PhaseLoggedOut
	case v1.NetworkPhase_NETWORK_PHASE_CONNECTING:
		return PhaseConnecting
	case v1.NetworkPhase_NETWORK_PHASE_ONLINE:
		return PhaseOnline
	case v1.NetworkPhase_NETWORK_PHASE_FAILED:
		return PhaseFailed
	case v1.NetworkPhase_NETWORK_PHASE_UNSPECIFIED:
	}
	return 0
}

func (s *server) ClearCache(ctx context.Context, _ *req[v1.ClearCacheRequest]) (*resp[v1.ClearCacheResponse], error) {
	return reply(&v1.ClearCacheResponse{}, s.Backend.ClearCache(ctx))
}

func (s *server) RestoreKeyBackup(ctx context.Context, r *req[v1.RestoreKeyBackupRequest]) (*resp[v1.RestoreKeyBackupResponse], error) {
	keys, err := s.Backend.RestoreKeyBackup(ctx, r.Msg.GetSecret())
	return reply(&v1.RestoreKeyBackupResponse{Keys: int64(keys)}, err)
}

func (s *server) ExportRoomKeys(ctx context.Context, r *req[v1.ExportRoomKeysRequest]) (*resp[v1.ExportRoomKeysResponse], error) {
	data, err := s.Backend.ExportRoomKeys(ctx, r.Msg.GetPassphrase())
	return reply(&v1.ExportRoomKeysResponse{Data: data}, err)
}

func (s *server) ImportRoomKeys(ctx context.Context, r *req[v1.ImportRoomKeysRequest]) (*resp[v1.ImportRoomKeysResponse], error) {
	imported, total, err := s.Backend.ImportRoomKeys(ctx, r.Msg.GetPassphrase(), r.Msg.GetData())
	return reply(&v1.ImportRoomKeysResponse{Imported: int64(imported), Total: int64(total)}, err)
}

// BootstrapKeyBackup reports a failure after the recovery key exists in
// Incomplete, not as an error, so the key is never lost.
func (s *server) BootstrapKeyBackup(ctx context.Context, r *req[v1.BootstrapKeyBackupRequest]) (*resp[v1.BootstrapKeyBackupResponse], error) {
	made, err := s.Backend.BootstrapKeyBackup(ctx, r.Msg.GetPassword())
	return reply(pc.KeyBackupToProto(made), err)
}

func (s *server) DetectLanguages(ctx context.Context, _ *req[v1.DetectLanguagesRequest]) (*resp[v1.DetectLanguagesResponse], error) {
	found, err := s.Backend.DetectLanguages(ctx)
	return reply(pc.SpellSuggestionToProto(found), err)
}

func (s *server) InstallDictionary(ctx context.Context, r *req[v1.InstallDictionaryRequest]) (*resp[v1.InstallDictionaryResponse], error) {
	return reply(&v1.InstallDictionaryResponse{}, s.Backend.InstallDictionary(ctx, r.Msg.GetTag()))
}

func (s *server) InstallFrequencies(ctx context.Context, r *req[v1.InstallFrequenciesRequest]) (*resp[v1.InstallFrequenciesResponse], error) {
	return reply(&v1.InstallFrequenciesResponse{}, s.Backend.InstallFrequencies(ctx, r.Msg.GetTag()))
}

func (s *server) DetectModel(ctx context.Context, _ *req[v1.DetectModelRequest]) (*resp[v1.DetectModelResponse], error) {
	found, err := s.Backend.DetectModel(ctx)
	return reply(pc.ModelSuggestionToProto(found), err)
}

func (s *server) InstallModel(ctx context.Context, r *req[v1.InstallModelRequest]) (*resp[v1.InstallModelResponse], error) {
	return reply(&v1.InstallModelResponse{}, s.Backend.InstallModel(ctx, r.Msg.GetTag()))
}

func (s *server) CheckSpelling(ctx context.Context, r *req[v1.CheckSpellingRequest]) (*resp[v1.CheckSpellingResponse], error) {
	found, err := s.Backend.CheckSpelling(ctx, r.Msg.GetText())
	return reply(&v1.CheckSpellingResponse{Misspellings: pc.MisspellingsToProto(found)}, err)
}

func (s *server) LearnWord(ctx context.Context, r *req[v1.LearnWordRequest]) (*resp[v1.LearnWordResponse], error) {
	return reply(&v1.LearnWordResponse{}, s.Backend.LearnWord(ctx, r.Msg.GetWord(), r.Msg.GetForever()))
}

func (s *server) AllowRareWord(ctx context.Context, r *req[v1.AllowRareWordRequest]) (*resp[v1.AllowRareWordResponse], error) {
	return reply(&v1.AllowRareWordResponse{}, s.Backend.AllowRareWord(ctx, r.Msg.GetWord()))
}

// ── Notification ──

// noNotifier refuses when the daemon was built without a notifier (test
// harnesses): an empty set would falsely read as "nothing is silenced".
func (s *server) noNotifier() error {
	if s.Notifications != nil {
		return nil
	}
	return connect.NewError(connect.CodeUnimplemented, errors.New("daemon: this daemon does not deliver notifications"))
}

func invalid(msg string) error {
	return connect.NewError(connect.CodeInvalidArgument, errors.New("daemon: set dnd: "+msg))
}

// SetDND refuses a rule that changes nothing or has already lapsed, either of
// which would look like the call silently doing nothing.
func (s *server) SetDND(_ context.Context, r *req[v1.SetDNDRequest]) (*resp[v1.SetDNDResponse], error) {
	if err := s.noNotifier(); err != nil {
		return nil, err
	}
	if r.Msg.GetRule() == nil {
		return nil, invalid("no rule given; say what to silence")
	}
	rule := pc.TempRuleFromProto(r.Msg.GetRule())
	if rule.Show == nil && rule.Ring == nil {
		return nil, invalid("the rule changes nothing; set show or ring")
	}
	if !rule.Until.IsZero() && !time.Now().Before(rule.Until) {
		return nil, invalid("that deadline has already passed")
	}
	return connect.NewResponse(&v1.SetDNDResponse{Rules: pc.TempRulesToProto(s.Notifications.SetDND(rule))}), nil
}

func (s *server) ClearDND(_ context.Context, r *req[v1.ClearDNDRequest]) (*resp[v1.ClearDNDResponse], error) {
	if err := s.noNotifier(); err != nil {
		return nil, err
	}
	var left notify.Temps
	if r.Msg.GetAll() {
		left = s.Notifications.ClearAllDND()
	} else {
		left = s.Notifications.ClearDND(r.Msg.GetMatch(), r.Msg.GetSender())
	}
	return connect.NewResponse(&v1.ClearDNDResponse{Rules: pc.TempRulesToProto(left)}), nil
}

func (s *server) DND(_ context.Context, _ *req[v1.DNDRequest]) (*resp[v1.DNDResponse], error) {
	if err := s.noNotifier(); err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.DNDResponse{Rules: pc.TempRulesToProto(s.Notifications.DND())}), nil
}

// ReloadConfig reports a config that will not parse as InvalidArgument: it is
// the caller's input, and the message is what the user must fix.
func (s *server) ReloadConfig(ctx context.Context, _ *req[v1.ReloadConfigRequest]) (*resp[v1.ReloadConfigResponse], error) {
	if s.Reload == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("daemon: this daemon was started without a config to re-read"))
	}
	if err := s.Reload(ctx); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&v1.ReloadConfigResponse{}), nil
}

// ── Schedule ──

var errNoScheduler = errors.New("daemon: scheduled messages are unavailable on this daemon")

// Schedule assigns the ID itself: it is the cancel handle, and a client-chosen
// one could collide.
func (s *server) Schedule(_ context.Context, r *req[v1.ScheduleRequest]) (*resp[v1.ScheduleResponse], error) {
	if s.Scheduler == nil {
		return nil, rpcErr(errNoScheduler)
	}
	msg := pc.ProtoToScheduled(r.Msg.GetMessage())
	msg.ID = newScheduleID()
	if msg.Written.IsZero() {
		msg.Written = time.Now().UTC()
	}
	return reply(&v1.ScheduleResponse{Id: msg.ID}, s.Scheduler.Add(msg))
}

func (s *server) ScheduledMessages(_ context.Context, _ *req[v1.ScheduledMessagesRequest]) (*resp[v1.ScheduledMessagesResponse], error) {
	if s.Scheduler == nil {
		return nil, rpcErr(errNoScheduler)
	}
	return connect.NewResponse(&v1.ScheduledMessagesResponse{Messages: pc.ScheduledToProto(s.Scheduler.List())}), nil
}

func (s *server) CancelScheduled(_ context.Context, r *req[v1.CancelScheduledRequest]) (*resp[v1.CancelScheduledResponse], error) {
	if s.Scheduler == nil {
		return nil, rpcErr(errNoScheduler)
	}
	return reply(&v1.CancelScheduledResponse{}, s.Scheduler.Cancel(r.Msg.GetId()))
}

// newScheduleID is random rather than a counter because the queue outlives the
// process: a restarted counter could reuse an ID still in the file.
func newScheduleID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
