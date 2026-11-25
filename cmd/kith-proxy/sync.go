package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	"github.com/EugeneShtoka/kith/internal/clientstore"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// profileHeader names the profile a call is asked in (kithd's profileGate).
const profileHeader = "Kith-Profile"

const (
	// messagesStream is the store's position in the bridge's Messages stream, asked in
	// no profile, so it carries every room this device may read.
	messagesStream = "messages"
	// profilesKey keeps the bridge's profiles, in its config's order, as JSON.
	profilesKey = "profiles"
)

// refreshAfter is how long a profile's answers wait after the frame that dirtied them,
// so a burst of frames is read once.
const refreshAfter = 3 * time.Second

// retryAfter is how long a stream that ended waits before it is opened again.
const retryAfter = 2 * time.Second

// msg is a generated message type and its pointer.
type msg[T any] interface {
	*T
	proto.Message
}

// keptCall is a call whose answer the store keeps per profile: asked with an empty
// request, as every client asks it.
type keptCall struct {
	method string
	fetch  func(ctx context.Context, profile string) ([]byte, error)
}

// keep is call as a keptCall named method.
func keep[Q, R any, PQ msg[Q], PR msg[R]](method string, call func(context.Context, *connect.Request[Q]) (*connect.Response[R], error)) keptCall {
	return keptCall{method: method, fetch: func(ctx context.Context, profile string) ([]byte, error) {
		req := connect.NewRequest(new(Q))
		req.Header().Set(profileHeader, profile)
		res, err := call(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", method, err)
		}
		b, err := proto.Marshal(PR(res.Msg))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", method, err)
		}
		return b, nil
	}}
}

// requestKey is how the store keys a request: its deterministic encoding, in hex.
func requestKey(m proto.Message) (string, error) {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("encode the request: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// emptyKey is the key of every keptCall's request.
const emptyKey = ""

// syncer keeps the store current with the bridge while it is reachable: the bridge's
// Messages stream into the store's rows, each profile's kept answers read again on
// each frame that may change them, and each room's history filled newest first.
type syncer struct {
	store  *clientstore.Store
	client backendv1connect.BackendServiceClient
	log    *slog.Logger

	mu    sync.Mutex
	dirty map[string]bool
	wake  chan struct{}
}

func newSyncer(store *clientstore.Store, client backendv1connect.BackendServiceClient, log *slog.Logger) *syncer {
	return &syncer{store: store, client: client, log: log, dirty: map[string]bool{}, wake: make(chan struct{}, 1)}
}

// calls is every keptCall.
func (s *syncer) calls() []keptCall {
	c := s.client
	return []keptCall{
		keep("Rooms", c.Rooms), keep("Spaces", c.Spaces), keep("Rail", c.Rail),
		keep("CachedUnread", c.CachedUnread), keep("LastMessages", c.LastMessages),
		keep("CachedInvites", c.CachedInvites), keep("Drafts", c.Drafts), keep("Selves", c.Selves),
		keep("GetConfig", c.GetConfig), keep("ConfigSettings", c.ConfigSettings), keep("Accounts", c.Accounts),
		keep("Identities", c.Identities), keep("TagVerdicts", c.TagVerdicts), keep("PersonVerdicts", c.PersonVerdicts),
		keep("Status", c.Status),
	}
}

// run keeps the store current until ctx ends: the profiles the bridge's config names,
// each one's answers, its streams, and the fill. A config naming other profiles starts
// it over.
func (s *syncer) run(ctx context.Context) {
	for ctx.Err() == nil {
		profiles, err := s.profiles(ctx)
		if err != nil {
			s.log.Debug("reading the bridge's profiles", "err", err)
			sleep(ctx, retryAfter)
			continue
		}
		s.runProfiles(ctx, profiles)
	}
}

// profiles is the bridge's profiles as its config names them now, kept in the store.
func (s *syncer) profiles(ctx context.Context) ([]string, error) {
	res, err := s.client.GetConfig(ctx, connect.NewRequest(&v1.GetConfigRequest{}))
	if err != nil {
		return nil, fmt.Errorf("GetConfig: %w", err)
	}
	names, err := profileNames(res.Msg.GetConfig())
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(names)
	if err != nil {
		return nil, fmt.Errorf("encode the profiles: %w", err)
	}
	if err := s.store.PutMeta(ctx, profilesKey, string(b)); err != nil {
		return nil, err
	}
	return names, nil
}

// profileNames is the profiles a bridge's config names, in its order.
func profileNames(text string) ([]string, error) {
	cfg, err := config.Decode(text)
	if err != nil {
		return nil, fmt.Errorf("the bridge's config: %w", err)
	}
	profiles, err := setup.Profiles(cfg)
	if err != nil {
		return nil, fmt.Errorf("the bridge's profiles: %w", err)
	}
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = p.Name
	}
	return names, nil
}

// runProfiles keeps the store current for profiles until ctx ends or the config names
// others.
func (s *syncer) runProfiles(ctx context.Context, profiles []string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	for _, p := range profiles {
		s.markDirty(p)
		wg.Go(func() { s.follow(ctx, p, s.changed) })
		wg.Go(func() { s.follow(ctx, p, s.unread) })
		wg.Go(func() { s.follow(ctx, p, s.invites) })
	}
	wg.Go(func() { s.follow(ctx, "", s.messages) })
	wg.Go(func() { s.follow(ctx, "", s.reactions) })
	wg.Go(func() { s.refresh(ctx, profiles) })
	wg.Go(func() { s.fill(ctx) })
	wg.Go(func() {
		if err := s.configChanged(ctx, profiles); err == nil {
			cancel() // the profiles changed: start over with the new ones
		}
	})
	wg.Wait()
}

// follow runs stream in profile until ctx ends, opening it again each time it ends.
// A stream says only what changes once it is open, so each opening has profile's
// answers read again: what changed while it was closed is in them.
func (s *syncer) follow(ctx context.Context, profile string, stream func(context.Context, string) error) {
	for ctx.Err() == nil {
		if profile != "" {
			s.markDirty(profile)
		}
		if err := stream(ctx, profile); err != nil && ctx.Err() == nil {
			s.log.Debug("a stream from the bridge ended", "profile", profile, "err", err)
		}
		sleep(ctx, retryAfter)
	}
}

// markDirty has profile's answers read again soon.
func (s *syncer) markDirty(profiles ...string) {
	s.mu.Lock()
	for _, p := range profiles {
		s.dirty[p] = true
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// refresh reads the dirty profiles' answers again, refreshAfter after they were
// dirtied, until ctx ends.
func (s *syncer) refresh(ctx context.Context, profiles []string) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
		sleep(ctx, refreshAfter)
		s.mu.Lock()
		dirty := s.dirty
		s.dirty = map[string]bool{}
		s.mu.Unlock()
		for _, p := range profiles {
			if dirty[p] {
				if err := s.read(ctx, p); err != nil && ctx.Err() == nil {
					s.log.Debug("reading a profile's answers", "profile", p, "err", err)
					s.markDirty(p)
					sleep(ctx, retryAfter)
				}
			}
		}
	}
}

// read keeps each keptCall's answer in profile, and its room set as Rooms names it.
// One failing leaves the others kept and the profile dirty.
func (s *syncer) read(ctx context.Context, profile string) error {
	var errs []error
	for _, call := range s.calls() {
		b, err := call.fetch(ctx, profile)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if call.method == "Rooms" {
			var rooms v1.RoomsResponse
			if err := proto.Unmarshal(b, &rooms); err != nil {
				errs = append(errs, fmt.Errorf("Rooms: %w", err))
				continue
			}
			ids := make([]string, 0, len(rooms.GetRooms()))
			for _, r := range rooms.GetRooms() {
				ids = append(ids, r.GetId())
			}
			if err := s.store.SetRooms(ctx, profile, ids); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if err := s.store.PutAnswer(ctx, call.method, profile, emptyKey, b); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// messages writes the bridge's Messages stream into the store from where it stood. A
// stream that cannot resume (read_again) has every room filled again and every
// profile's answers read again.
func (s *syncer) messages(ctx context.Context, _ string) error {
	epoch, change, err := s.store.Position(ctx, messagesStream)
	if err != nil {
		return err
	}
	st, err := s.client.Messages(ctx, connect.NewRequest(&v1.MessagesRequest{SinceEpoch: epoch, SinceChange: change}))
	if err != nil {
		return fmt.Errorf("Messages: %w", err)
	}
	defer func() { _ = st.Close() }()
	for st.Receive() {
		m := st.Msg()
		if m.GetReadAgain() {
			// What the stream skipped may have moved any answer, not only the rooms'.
			if err := s.store.Unfill(ctx); err != nil {
				return err
			}
			s.markDirtyAll(ctx)
		}
		var rows []clientstore.Message
		room := ""
		if msg := m.GetMessage(); msg != nil {
			b, err := proto.Marshal(msg)
			if err != nil {
				return fmt.Errorf("encode a message: %w", err)
			}
			room = msg.GetRoomId()
			rows = []clientstore.Message{{Event: msg.GetId(), At: msg.GetTimestamp().AsTime(), Message: b}}
		}
		if err := s.store.PutStream(ctx, room, rows, messagesStream, m.GetEpoch(), m.GetChange()); err != nil {
			return err
		}
		if room != "" {
			s.markDirtyAll(ctx)
		}
	}
	return fmt.Errorf("Messages: %w", st.Err())
}

// markDirtyAll dirties every profile the store knows: a message moves unread counts
// and last messages wherever its room is.
func (s *syncer) markDirtyAll(ctx context.Context) {
	names, err := s.knownProfiles(ctx)
	if err != nil {
		return
	}
	s.markDirty(names...)
}

func (s *syncer) knownProfiles(ctx context.Context) ([]string, error) {
	return knownProfiles(ctx, s.store)
}

// knownProfiles is the profiles the store last read from the bridge's config.
func knownProfiles(ctx context.Context, store *clientstore.Store) ([]string, error) {
	text, err := store.Meta(ctx, profilesKey)
	if err != nil || text == "" {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal([]byte(text), &names); err != nil {
		return nil, fmt.Errorf("the profiles kept: %w", err)
	}
	return names, nil
}

// changed dirties profile on each frame of its Changed stream; names changing has
// every room filled again, its messages carrying names.
func (s *syncer) changed(ctx context.Context, profile string) error {
	req := connect.NewRequest(&v1.ChangedRequest{})
	req.Header().Set(profileHeader, profile)
	st, err := s.client.Changed(ctx, req)
	if err != nil {
		return fmt.Errorf("Changed: %w", err)
	}
	defer func() { _ = st.Close() }()
	for st.Receive() {
		if st.Msg().GetChange() == v1.Change_CHANGE_NAMES {
			if err := s.store.Unfill(ctx); err != nil {
				return err
			}
		}
		s.markDirty(profile)
	}
	return fmt.Errorf("Changed: %w", st.Err())
}

// unread dirties profile on each frame of its UnreadStream.
func (s *syncer) unread(ctx context.Context, profile string) error {
	req := connect.NewRequest(&v1.UnreadStreamRequest{})
	req.Header().Set(profileHeader, profile)
	st, err := s.client.UnreadStream(ctx, req)
	if err != nil {
		return fmt.Errorf("UnreadStream: %w", err)
	}
	defer func() { _ = st.Close() }()
	for st.Receive() {
		s.markDirty(profile)
	}
	return fmt.Errorf("UnreadStream: %w", st.Err())
}

// invites dirties profile on each frame of its Invites stream.
func (s *syncer) invites(ctx context.Context, profile string) error {
	req := connect.NewRequest(&v1.InvitesRequest{})
	req.Header().Set(profileHeader, profile)
	st, err := s.client.Invites(ctx, req)
	if err != nil {
		return fmt.Errorf("Invites: %w", err)
	}
	defer func() { _ = st.Close() }()
	for st.Receive() {
		s.markDirty(profile)
	}
	return fmt.Errorf("Invites: %w", st.Err())
}

// reactions reads a room's reactions again on each frame naming it.
//
// shortcut: a reaction made while this stream was closed is read only at the room's
// next fill; read the held rooms' reactions again at each opening if that shows.
func (s *syncer) reactions(ctx context.Context, _ string) error {
	st, err := s.client.Reactions(ctx, connect.NewRequest(&v1.ReactionsRequest{}))
	if err != nil {
		return fmt.Errorf("Reactions: %w", err)
	}
	defer func() { _ = st.Close() }()
	for st.Receive() {
		if room := st.Msg().GetUpdate().GetReaction().GetRoomId(); room != "" {
			if err := s.keepRoom(ctx, room, roomCalls[:1]); err != nil {
				s.log.Debug("reading a room's reactions", "room", room, "err", err)
			}
		}
	}
	return fmt.Errorf("Reactions: %w", st.Err())
}

// configChanged returns nil when the bridge's config names other profiles than
// profiles, and an error when its stream ends first.
func (s *syncer) configChanged(ctx context.Context, profiles []string) error {
	st, err := s.client.ConfigChanged(ctx, connect.NewRequest(&v1.ConfigChangedRequest{}))
	if err != nil {
		return fmt.Errorf("ConfigChanged: %w", err)
	}
	defer func() { _ = st.Close() }()
	for st.Receive() {
		names, err := profileNames(st.Msg().GetConfig())
		if err != nil {
			continue
		}
		if !slices.Equal(names, profiles) {
			return nil
		}
		s.markDirty(profiles...)
	}
	return fmt.Errorf("ConfigChanged: %w", st.Err())
}

// roomCalls are the answers the store keeps about each room, whichever profile asks.
var roomCalls = []string{"CachedReactions", "SenderSlots", "SenderPictures"}

// fill fills each room's history that no page has filled, until ctx ends, looking
// again whenever there is none left.
func (s *syncer) fill(ctx context.Context) {
	for ctx.Err() == nil {
		rooms, err := s.store.Unfilled(ctx)
		if err != nil {
			s.log.Debug("rooms to fill", "err", err)
		}
		for _, room := range rooms {
			if ctx.Err() != nil {
				return
			}
			if err := s.fillRoom(ctx, room); err != nil {
				s.log.Debug("filling a room", "room", room, "err", err)
				break
			}
		}
		sleep(ctx, retryAfter)
	}
}

// fillRoom keeps room's newest history as the bridge's cache holds it, and its other
// answers.
func (s *syncer) fillRoom(ctx context.Context, room string) error {
	asked, err := s.store.Ask(ctx)
	if err != nil {
		return err
	}
	res, err := s.client.CachedTimeline(ctx, connect.NewRequest(&v1.CachedTimelineRequest{RoomId: room}))
	if err != nil {
		return fmt.Errorf("CachedTimeline: %w", err)
	}
	rows := make([]clientstore.Message, 0, len(res.Msg.GetMessages()))
	for _, m := range res.Msg.GetMessages() {
		b, err := proto.Marshal(m)
		if err != nil {
			return fmt.Errorf("encode a message: %w", err)
		}
		rows = append(rows, clientstore.Message{Event: m.GetId(), At: m.GetTimestamp().AsTime(), Message: b})
	}
	if err := s.keepRoom(ctx, room, roomCalls); err != nil {
		return err
	}
	return s.store.PutPage(ctx, room, rows, asked)
}

// keepRoom keeps the answers named methods (of roomCalls) give about room.
func (s *syncer) keepRoom(ctx context.Context, room string, methods []string) error {
	for _, method := range methods {
		var res proto.Message
		var err error
		switch method {
		case "CachedReactions":
			var r *connect.Response[v1.CachedReactionsResponse]
			r, err = s.client.CachedReactions(ctx, connect.NewRequest(&v1.CachedReactionsRequest{RoomId: room}))
			if err == nil {
				res = r.Msg
			}
		case "SenderSlots":
			var r *connect.Response[v1.SenderSlotsResponse]
			r, err = s.client.SenderSlots(ctx, connect.NewRequest(&v1.SenderSlotsRequest{RoomId: room}))
			if err == nil {
				res = r.Msg
			}
		case "SenderPictures":
			var r *connect.Response[v1.SenderPicturesResponse]
			r, err = s.client.SenderPictures(ctx, connect.NewRequest(&v1.SenderPicturesRequest{RoomId: room}))
			if err == nil {
				res = r.Msg
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		b, err := proto.Marshal(res)
		if err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		if err := s.store.PutRoomAnswer(ctx, method, room, b); err != nil {
			return err
		}
	}
	return nil
}

// sleep waits d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
