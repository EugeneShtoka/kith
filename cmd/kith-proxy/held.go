package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	"github.com/EugeneShtoka/kith/internal/clientstore"
)

// timelineLimit is how many messages a held CachedTimeline answers, as kithd's does.
const timelineLimit = 2000

// errOffline is every call the store cannot answer while the bridge is out of reach.
var errOffline = errors.New("kith-proxy: the bridge is out of reach, and this device holds no answer to this")

// held answers from the store while the bridge is out of reach: what it keeps, in the
// profile asked, and nothing a profile's last room set lacks. Every other call is
// unavailable, as it is when kithd is not reachable. Its streams stay open, saying
// nothing new, until the proxy is live again and ends them.
type held struct {
	backendv1connect.UnimplementedBackendServiceHandler
	store *clientstore.Store
}

// offline turns every call held does not answer into unavailable, which clients retry.
var offline = connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		res, err := next(ctx, req)
		if connect.CodeOf(err) == connect.CodeUnimplemented {
			return nil, connect.NewError(connect.CodeUnavailable, errOffline)
		}
		return res, err
	}
})

// offlineStreams is offline for streams.
type offlineStreams struct{}

func (offlineStreams) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc { return next }

func (offlineStreams) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (offlineStreams) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		err := next(ctx, conn)
		if connect.CodeOf(err) == connect.CodeUnimplemented {
			return connect.NewError(connect.CodeUnavailable, errOffline)
		}
		return err
	}
}

// sentinelHeader and unknownProfile tag an unknown profile as kithd does (rpcErr), so
// a client restores api.ErrUnknownProfile from it.
const (
	sentinelHeader = "Mx-Sentinel"
	unknownProfile = "unknown-profile"
)

// profile is the profile header names as the bridge's config spells it, and whether it
// named one: none is the first, as kithd takes it.
func (h *held) profile(ctx context.Context, header string) (name string, named bool, err error) {
	names, err := knownProfiles(ctx, h.store)
	if err != nil {
		return "", false, connect.NewError(connect.CodeUnavailable, err)
	}
	if len(names) == 0 {
		return "", false, connect.NewError(connect.CodeUnavailable, errOffline)
	}
	header = strings.TrimSpace(header)
	if header == "" {
		return names[0], false, nil
	}
	for _, n := range names {
		if strings.EqualFold(n, header) {
			return n, true, nil
		}
	}
	cerr := connect.NewError(connect.CodeInternal, fmt.Errorf("kith-proxy: no profile %q", header))
	cerr.Meta().Set(sentinelHeader, unknownProfile)
	return "", false, cerr
}

// answer is method's kept answer to req in its profile.
func answer[Q, R any, PQ msg[Q], PR msg[R]](ctx context.Context, h *held, method string, req *connect.Request[Q]) (*connect.Response[R], error) {
	profile, _, err := h.profile(ctx, req.Header().Get(profileHeader))
	if err != nil {
		return nil, err
	}
	key, err := requestKey(PQ(req.Msg))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	b, ok, err := h.store.Answer(ctx, method, profile, key)
	if err != nil || !ok {
		return nil, connect.NewError(connect.CodeUnavailable, errors.Join(errOffline, err))
	}
	return decoded[R, PR](b)
}

func decoded[R any, PR msg[R]](b []byte) (*connect.Response[R], error) {
	res := new(R)
	if err := proto.Unmarshal(b, PR(res)); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("kith-proxy: a kept answer: %w", err))
	}
	return connect.NewResponse(res), nil
}

// admit refuses a room the profile header names does not hold, as kithd does (a
// client naming no profile is refused nothing).
func (h *held) admit(ctx context.Context, header, room string) error {
	profile, named, err := h.profile(ctx, header)
	if err != nil || !named {
		return err
	}
	holds, err := h.store.Holds(ctx, profile, room)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, err)
	}
	if !holds {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("kith-proxy: the profile %s holds no room %s", profile, room))
	}
	return nil
}

// roomAnswer is method's kept answer about the room req names.
func roomAnswer[R any, PR msg[R]](ctx context.Context, h *held, method string, header http.Header, room string) (*connect.Response[R], error) {
	if err := h.admit(ctx, header.Get(profileHeader), room); err != nil {
		return nil, err
	}
	b, ok, err := h.store.RoomAnswer(ctx, method, room)
	if err != nil || !ok {
		return nil, connect.NewError(connect.CodeUnavailable, errors.Join(errOffline, err))
	}
	return decoded[R, PR](b)
}

func (h *held) Rooms(ctx context.Context, r *connect.Request[v1.RoomsRequest]) (*connect.Response[v1.RoomsResponse], error) {
	return answer[v1.RoomsRequest, v1.RoomsResponse](ctx, h, "Rooms", r)
}

func (h *held) Spaces(ctx context.Context, r *connect.Request[v1.SpacesRequest]) (*connect.Response[v1.SpacesResponse], error) {
	return answer[v1.SpacesRequest, v1.SpacesResponse](ctx, h, "Spaces", r)
}

func (h *held) Rail(ctx context.Context, r *connect.Request[v1.RailRequest]) (*connect.Response[v1.RailResponse], error) {
	return answer[v1.RailRequest, v1.RailResponse](ctx, h, "Rail", r)
}

func (h *held) CachedUnread(ctx context.Context, r *connect.Request[v1.CachedUnreadRequest]) (*connect.Response[v1.CachedUnreadResponse], error) {
	return answer[v1.CachedUnreadRequest, v1.CachedUnreadResponse](ctx, h, "CachedUnread", r)
}

func (h *held) LastMessages(ctx context.Context, r *connect.Request[v1.LastMessagesRequest]) (*connect.Response[v1.LastMessagesResponse], error) {
	return answer[v1.LastMessagesRequest, v1.LastMessagesResponse](ctx, h, "LastMessages", r)
}

func (h *held) CachedInvites(ctx context.Context, r *connect.Request[v1.CachedInvitesRequest]) (*connect.Response[v1.CachedInvitesResponse], error) {
	return answer[v1.CachedInvitesRequest, v1.CachedInvitesResponse](ctx, h, "CachedInvites", r)
}

func (h *held) Drafts(ctx context.Context, r *connect.Request[v1.DraftsRequest]) (*connect.Response[v1.DraftsResponse], error) {
	return answer[v1.DraftsRequest, v1.DraftsResponse](ctx, h, "Drafts", r)
}

func (h *held) Selves(ctx context.Context, r *connect.Request[v1.SelvesRequest]) (*connect.Response[v1.SelvesResponse], error) {
	return answer[v1.SelvesRequest, v1.SelvesResponse](ctx, h, "Selves", r)
}

func (h *held) GetConfig(ctx context.Context, r *connect.Request[v1.GetConfigRequest]) (*connect.Response[v1.GetConfigResponse], error) {
	return answer[v1.GetConfigRequest, v1.GetConfigResponse](ctx, h, "GetConfig", r)
}

func (h *held) ConfigSettings(ctx context.Context, r *connect.Request[v1.ConfigSettingsRequest]) (*connect.Response[v1.ConfigSettingsResponse], error) {
	return answer[v1.ConfigSettingsRequest, v1.ConfigSettingsResponse](ctx, h, "ConfigSettings", r)
}

func (h *held) Accounts(ctx context.Context, r *connect.Request[v1.AccountsRequest]) (*connect.Response[v1.AccountsResponse], error) {
	return answer[v1.AccountsRequest, v1.AccountsResponse](ctx, h, "Accounts", r)
}

func (h *held) Identities(ctx context.Context, r *connect.Request[v1.IdentitiesRequest]) (*connect.Response[v1.IdentitiesResponse], error) {
	return answer[v1.IdentitiesRequest, v1.IdentitiesResponse](ctx, h, "Identities", r)
}

func (h *held) TagVerdicts(ctx context.Context, r *connect.Request[v1.TagVerdictsRequest]) (*connect.Response[v1.TagVerdictsResponse], error) {
	return answer[v1.TagVerdictsRequest, v1.TagVerdictsResponse](ctx, h, "TagVerdicts", r)
}

func (h *held) PersonVerdicts(ctx context.Context, r *connect.Request[v1.PersonVerdictsRequest]) (*connect.Response[v1.PersonVerdictsResponse], error) {
	return answer[v1.PersonVerdictsRequest, v1.PersonVerdictsResponse](ctx, h, "PersonVerdicts", r)
}

func (h *held) Status(ctx context.Context, r *connect.Request[v1.StatusRequest]) (*connect.Response[v1.StatusResponse], error) {
	return answer[v1.StatusRequest, v1.StatusResponse](ctx, h, "Status", r)
}

func (h *held) CachedReactions(ctx context.Context, r *connect.Request[v1.CachedReactionsRequest]) (*connect.Response[v1.CachedReactionsResponse], error) {
	return roomAnswer[v1.CachedReactionsResponse](ctx, h, "CachedReactions", r.Header(), r.Msg.GetRoomId())
}

func (h *held) SenderSlots(ctx context.Context, r *connect.Request[v1.SenderSlotsRequest]) (*connect.Response[v1.SenderSlotsResponse], error) {
	return roomAnswer[v1.SenderSlotsResponse](ctx, h, "SenderSlots", r.Header(), r.Msg.GetRoomId())
}

func (h *held) SenderPictures(ctx context.Context, r *connect.Request[v1.SenderPicturesRequest]) (*connect.Response[v1.SenderPicturesResponse], error) {
	return roomAnswer[v1.SenderPicturesResponse](ctx, h, "SenderPictures", r.Header(), r.Msg.GetRoomId())
}

// CachedTimeline is the room's newest messages the store holds: the newest page the
// bridge answered and every message its stream brought since.
func (h *held) CachedTimeline(ctx context.Context, r *connect.Request[v1.CachedTimelineRequest]) (*connect.Response[v1.CachedTimelineResponse], error) {
	room := r.Msg.GetRoomId()
	if err := h.admit(ctx, r.Header().Get(profileHeader), room); err != nil {
		return nil, err
	}
	rows, err := h.store.Timeline(ctx, room, timelineLimit)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	res := &v1.CachedTimelineResponse{Messages: make([]*v1.Message, 0, len(rows))}
	for _, b := range rows {
		m := new(v1.Message)
		if err := proto.Unmarshal(b, m); err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("kith-proxy: a kept message: %w", err))
		}
		res.Messages = append(res.Messages, m)
	}
	return connect.NewResponse(res), nil
}

// changesBehind is how far behind a held stream may resume before it is told to read
// again, as kithd's tailBehind.
const changesBehind = 5000

// Messages is the store's own numbering of what it holds, resumed as kithd resumes
// its own: a client from the bridge's numbering is told to read again, and reads the
// store; once the proxy is live, the stream ends, and reopened through the bridge is
// told to read again there.
func (h *held) Messages(ctx context.Context, r *connect.Request[v1.MessagesRequest], st *connect.ServerStream[v1.MessagesResponse]) error {
	profile, named, err := h.profile(ctx, r.Header().Get(profileHeader))
	if err != nil {
		return err
	}
	last, err := h.store.Seq(ctx)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, err)
	}
	epoch := h.store.Epoch()
	from, readAgain := last, r.Msg.GetSinceEpoch() != ""
	if since := r.Msg.GetSinceChange(); r.Msg.GetSinceEpoch() == epoch && since <= last && last-since <= changesBehind {
		from, readAgain = since, false
	}
	if err := st.Send(&v1.MessagesResponse{Epoch: epoch, Change: from, ReadAgain: readAgain}); err != nil {
		return fmt.Errorf("kith-proxy: send: %w", err)
	}
	scope := ""
	if named {
		scope = profile
	}
	if err := h.replay(ctx, st, from, last, scope); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

// replay sends the changes numbered after from up to last, of the rooms profile holds
// ("": every room).
func (h *held) replay(ctx context.Context, st *connect.ServerStream[v1.MessagesResponse], from, last int64, profile string) error {
	for from < last {
		changes, err := h.store.ChangesAfter(ctx, from, 256)
		if err != nil {
			return connect.NewError(connect.CodeUnavailable, err)
		}
		if len(changes) == 0 {
			return nil
		}
		for _, c := range changes {
			from = c.Seq
			if profile != "" {
				holds, herr := h.store.Holds(ctx, profile, c.Room)
				if herr != nil {
					return connect.NewError(connect.CodeUnavailable, herr)
				}
				if !holds {
					continue
				}
			}
			m := new(v1.Message)
			if uerr := proto.Unmarshal(c.Message, m); uerr != nil {
				return connect.NewError(connect.CodeUnavailable, uerr)
			}
			if serr := st.Send(&v1.MessagesResponse{Message: m, Epoch: h.store.Epoch(), Change: c.Seq}); serr != nil {
				return fmt.Errorf("kith-proxy: send: %w", serr)
			}
		}
	}
	return nil
}

// quiet holds a stream open, saying nothing, until the proxy is live again.
func quiet[R any](ctx context.Context, st *connect.ServerStream[R]) error {
	if err := st.Send(nil); err != nil {
		return fmt.Errorf("kith-proxy: open a stream: %w", err)
	}
	<-ctx.Done()
	return nil
}

func (h *held) UnreadStream(ctx context.Context, _ *connect.Request[v1.UnreadStreamRequest], st *connect.ServerStream[v1.UnreadStreamResponse]) error {
	return quiet(ctx, st)
}

func (h *held) Reactions(ctx context.Context, _ *connect.Request[v1.ReactionsRequest], st *connect.ServerStream[v1.ReactionsResponse]) error {
	return quiet(ctx, st)
}

func (h *held) Invites(ctx context.Context, _ *connect.Request[v1.InvitesRequest], st *connect.ServerStream[v1.InvitesResponse]) error {
	return quiet(ctx, st)
}

func (h *held) Changed(ctx context.Context, _ *connect.Request[v1.ChangedRequest], st *connect.ServerStream[v1.ChangedResponse]) error {
	return quiet(ctx, st)
}

func (h *held) ConfigChanged(ctx context.Context, _ *connect.Request[v1.ConfigChangedRequest], st *connect.ServerStream[v1.ConfigChangedResponse]) error {
	return quiet(ctx, st)
}

func (h *held) NotificationStream(ctx context.Context, _ *connect.Request[v1.NotificationStreamRequest], st *connect.ServerStream[v1.NotificationStreamResponse]) error {
	return quiet(ctx, st)
}

func (h *held) ActivityStream(ctx context.Context, _ *connect.Request[v1.ActivityStreamRequest], st *connect.ServerStream[v1.ActivityStreamResponse]) error {
	return quiet(ctx, st)
}

func (h *held) FollowStream(ctx context.Context, _ *connect.Request[v1.FollowStreamRequest], st *connect.ServerStream[v1.FollowStreamResponse]) error {
	return quiet(ctx, st)
}

func (h *held) Verifications(ctx context.Context, _ *connect.Request[v1.VerificationsRequest], st *connect.ServerStream[v1.VerificationsResponse]) error {
	return quiet(ctx, st)
}
