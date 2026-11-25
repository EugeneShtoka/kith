package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	"github.com/EugeneShtoka/kith/internal/clientstore"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// fakeBridge is a kithd's answers as kith-proxy reads them: profiles, each with its
// rooms, rail revision and unread counts, rooms' messages numbered as kithd's cache
// numbers changes, and the streams that say something changed. A profile header is
// taken as kithd's profileGate takes it.
type fakeBridge struct {
	backendv1connect.UnimplementedBackendServiceHandler

	mu       sync.Mutex
	profiles []string
	sets     map[string][]string
	msgs     map[string][]*v1.Message
	changes  []*v1.Message
	rail     map[string]uint64
	unread   map[string]int64
	version  int // sets or rails changed
	unreadV  int // unread changed
	ids      int // event IDs given, which a cache cleared never gives again
	bell     chan struct{}
	// epoch names the numbering of changes; a cache cleared numbers anew.
	epoch string
	// onTimeline, when set, runs after CachedTimeline read its page and before it
	// answers: what happens meanwhile is newer than the page.
	onTimeline func(room string)
}

func newFakeBridge(sets map[string][]string, profiles ...string) *fakeBridge {
	return &fakeBridge{
		profiles: profiles, sets: sets, msgs: map[string][]*v1.Message{},
		rail: map[string]uint64{}, unread: map[string]int64{}, bell: make(chan struct{}), epoch: "bridge-one",
	}
}

// ring tells every stream something changed; f.mu is held.
func (f *fakeBridge) ring() {
	close(f.bell)
	f.bell = make(chan struct{})
}

func (f *fakeBridge) listen() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bell
}

// say is a message in room from someone else, which counts as unread.
func (f *fakeBridge) say(room, body string) {
	f.post(room, body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unread[room]++
	f.unreadV++
	f.ring()
}

// post is a message in room that counts as nothing unread: one of your own, sent from
// another device. Only the room's last message moves.
func (f *fakeBridge) post(room, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids++
	m := &v1.Message{
		Id: fmt.Sprintf("$%d", f.ids), RoomId: room, Sender: "@alice:x", Body: body,
		Timestamp: timestamppb.New(time.UnixMilli(int64(1_700_000_000_000 + f.ids*1000))),
	}
	f.msgs[room] = append(f.msgs[room], m)
	f.changes = append(f.changes, m)
	f.ring()
}

// edit rewrites the n-th change's message, as an edit arriving does.
func (f *fakeBridge) edit(room, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	msgs := f.msgs[room]
	if len(msgs) == 0 {
		return
	}
	m := &v1.Message{
		Id: msgs[0].GetId(), RoomId: room, Sender: msgs[0].GetSender(), Body: body,
		Timestamp: msgs[0].GetTimestamp(), Edited: true,
	}
	msgs[0] = m
	f.changes = append(f.changes, m)
	f.ring()
}

// clear is the bridge's cache cleared and filled again from the network, which gave
// room's first message another body: the numbering starts anew, and nothing numbers
// that change, so only reading again finds it.
func (f *fakeBridge) clear(room, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.epoch += "+"
	if msgs := f.msgs[room]; len(msgs) > 0 {
		m := proto.Clone(msgs[0]).(*v1.Message)
		m.Body = body
		msgs[0] = m
	}
	f.changes = nil
	f.ring()
}

// move takes room out of profile from into profile to.
func (f *fakeBridge) move(room, from, to string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets[from] = slices.DeleteFunc(slices.Clone(f.sets[from]), func(r string) bool { return r == room })
	if !slices.Contains(f.sets[to], room) {
		f.sets[to] = append(slices.Clone(f.sets[to]), room)
	}
	f.version++
	f.ring()
}

// bumpRail changes profile's rail.
func (f *fakeBridge) bumpRail(profile string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rail[profile]++
	f.version++
	f.ring()
}

// read marks room read.
func (f *fakeBridge) read(room string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unread[room] = 0
	f.unreadV++
	f.ring()
}

// profile is header's profile as kithd's gate takes it: none is the first.
func (f *fakeBridge) profile(header string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	header = strings.TrimSpace(header)
	if header == "" {
		return f.profiles[0], false, nil
	}
	for _, p := range f.profiles {
		if strings.EqualFold(p, header) {
			return p, true, nil
		}
	}
	cerr := connect.NewError(connect.CodeInternal, fmt.Errorf("no profile %q", header))
	cerr.Meta().Set(sentinelHeader, unknownProfile)
	return "", false, cerr
}

func (f *fakeBridge) set(h http.Header) ([]string, error) {
	p, _, err := f.profile(h.Get(profileHeader))
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sets[p]), nil
}

func (f *fakeBridge) admit(h http.Header, room string) error {
	p, named, err := f.profile(h.Get(profileHeader))
	if err != nil || !named {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.sets[p], room) {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("the profile %s holds no room %s", p, room))
	}
	return nil
}

func (f *fakeBridge) GetConfig(_ context.Context, r *connect.Request[v1.GetConfigRequest]) (*connect.Response[v1.GetConfigResponse], error) {
	if _, _, err := f.profile(r.Header().Get(profileHeader)); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	for _, p := range f.profiles {
		fmt.Fprintf(&b, "[[profile]]\nname = %q\ninclude = [\"*\"]\n\n", p)
	}
	return connect.NewResponse(&v1.GetConfigResponse{Config: b.String(), Revision: "1"}), nil
}

func (f *fakeBridge) Rooms(_ context.Context, r *connect.Request[v1.RoomsRequest]) (*connect.Response[v1.RoomsResponse], error) {
	set, err := f.set(r.Header())
	if err != nil {
		return nil, err
	}
	res := &v1.RoomsResponse{}
	for _, room := range set {
		res.Rooms = append(res.Rooms, &v1.Room{Id: room})
	}
	return connect.NewResponse(res), nil
}

func (f *fakeBridge) Rail(_ context.Context, r *connect.Request[v1.RailRequest]) (*connect.Response[v1.RailResponse], error) {
	p, _, err := f.profile(r.Header().Get(profileHeader))
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&v1.RailResponse{Revision: f.rail[p]}), nil
}

func (f *fakeBridge) CachedUnread(_ context.Context, r *connect.Request[v1.CachedUnreadRequest]) (*connect.Response[v1.CachedUnreadResponse], error) {
	set, err := f.set(r.Header())
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	res := &v1.CachedUnreadResponse{}
	for _, room := range set {
		res.Unread = append(res.Unread, &v1.Unread{RoomId: room, Notifications: f.unread[room]})
	}
	return connect.NewResponse(res), nil
}

func (f *fakeBridge) LastMessages(_ context.Context, r *connect.Request[v1.LastMessagesRequest]) (*connect.Response[v1.LastMessagesResponse], error) {
	set, err := f.set(r.Header())
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	res := &v1.LastMessagesResponse{At: map[string]int64{}}
	for _, room := range set {
		if msgs := f.msgs[room]; len(msgs) > 0 {
			res.At[room] = msgs[len(msgs)-1].GetTimestamp().AsTime().UnixMilli()
		}
	}
	return connect.NewResponse(res), nil
}

func (f *fakeBridge) CachedTimeline(_ context.Context, r *connect.Request[v1.CachedTimelineRequest]) (*connect.Response[v1.CachedTimelineResponse], error) {
	if err := f.admit(r.Header(), r.Msg.GetRoomId()); err != nil {
		return nil, err
	}
	f.mu.Lock()
	page := slices.Clone(f.msgs[r.Msg.GetRoomId()])
	hook := f.onTimeline
	f.mu.Unlock()
	if hook != nil {
		hook(r.Msg.GetRoomId())
	}
	return connect.NewResponse(&v1.CachedTimelineResponse{Messages: page}), nil
}

func (f *fakeBridge) CachedReactions(_ context.Context, r *connect.Request[v1.CachedReactionsRequest]) (*connect.Response[v1.CachedReactionsResponse], error) {
	if err := f.admit(r.Header(), r.Msg.GetRoomId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.CachedReactionsResponse{}), nil
}

func (f *fakeBridge) SenderSlots(_ context.Context, r *connect.Request[v1.SenderSlotsRequest]) (*connect.Response[v1.SenderSlotsResponse], error) {
	if err := f.admit(r.Header(), r.Msg.GetRoomId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.SenderSlotsResponse{}), nil
}

func (f *fakeBridge) SenderPictures(_ context.Context, r *connect.Request[v1.SenderPicturesRequest]) (*connect.Response[v1.SenderPicturesResponse], error) {
	if err := f.admit(r.Header(), r.Msg.GetRoomId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.SenderPicturesResponse{}), nil
}

// empty answers a kept call the fake holds nothing for, in a profile it knows.
func empty[Q, R any](f *fakeBridge, r *connect.Request[Q]) (*connect.Response[R], error) {
	if _, _, err := f.profile(r.Header().Get(profileHeader)); err != nil {
		return nil, err
	}
	return connect.NewResponse(new(R)), nil
}

func (f *fakeBridge) Spaces(_ context.Context, r *connect.Request[v1.SpacesRequest]) (*connect.Response[v1.SpacesResponse], error) {
	return empty[v1.SpacesRequest, v1.SpacesResponse](f, r)
}

func (f *fakeBridge) CachedInvites(_ context.Context, r *connect.Request[v1.CachedInvitesRequest]) (*connect.Response[v1.CachedInvitesResponse], error) {
	return empty[v1.CachedInvitesRequest, v1.CachedInvitesResponse](f, r)
}

func (f *fakeBridge) Drafts(_ context.Context, r *connect.Request[v1.DraftsRequest]) (*connect.Response[v1.DraftsResponse], error) {
	return empty[v1.DraftsRequest, v1.DraftsResponse](f, r)
}

func (f *fakeBridge) Selves(_ context.Context, r *connect.Request[v1.SelvesRequest]) (*connect.Response[v1.SelvesResponse], error) {
	return empty[v1.SelvesRequest, v1.SelvesResponse](f, r)
}

func (f *fakeBridge) ConfigSettings(_ context.Context, r *connect.Request[v1.ConfigSettingsRequest]) (*connect.Response[v1.ConfigSettingsResponse], error) {
	return empty[v1.ConfigSettingsRequest, v1.ConfigSettingsResponse](f, r)
}

func (f *fakeBridge) Accounts(_ context.Context, r *connect.Request[v1.AccountsRequest]) (*connect.Response[v1.AccountsResponse], error) {
	return empty[v1.AccountsRequest, v1.AccountsResponse](f, r)
}

func (f *fakeBridge) Identities(_ context.Context, r *connect.Request[v1.IdentitiesRequest]) (*connect.Response[v1.IdentitiesResponse], error) {
	return empty[v1.IdentitiesRequest, v1.IdentitiesResponse](f, r)
}

func (f *fakeBridge) TagVerdicts(_ context.Context, r *connect.Request[v1.TagVerdictsRequest]) (*connect.Response[v1.TagVerdictsResponse], error) {
	return empty[v1.TagVerdictsRequest, v1.TagVerdictsResponse](f, r)
}

func (f *fakeBridge) PersonVerdicts(_ context.Context, r *connect.Request[v1.PersonVerdictsRequest]) (*connect.Response[v1.PersonVerdictsResponse], error) {
	return empty[v1.PersonVerdictsRequest, v1.PersonVerdictsResponse](f, r)
}

func (f *fakeBridge) Status(_ context.Context, _ *connect.Request[v1.StatusRequest]) (*connect.Response[v1.StatusResponse], error) {
	return connect.NewResponse(&v1.StatusResponse{Ready: true}), nil
}

// Messages numbers the bridge's changes as kithd's cache does, resuming from the
// request's position or telling the client to read again; one asked in no profile
// carries every room.
func (f *fakeBridge) Messages(ctx context.Context, r *connect.Request[v1.MessagesRequest], st *connect.ServerStream[v1.MessagesResponse]) error {
	f.mu.Lock()
	epoch, last := f.epoch, int64(len(f.changes))
	f.mu.Unlock()
	at, readAgain := last, r.Msg.GetSinceEpoch() != ""
	if r.Msg.GetSinceEpoch() == epoch && r.Msg.GetSinceChange() <= last {
		at, readAgain = r.Msg.GetSinceChange(), false
	}
	if err := st.Send(&v1.MessagesResponse{Epoch: epoch, Change: at, ReadAgain: readAgain}); err != nil {
		return err
	}
	for {
		bell := f.listen()
		f.mu.Lock()
		if f.epoch != epoch {
			f.mu.Unlock()
			return nil // cleared: the client reopens, and is told to read again
		}
		pending := slices.Clone(f.changes[at:])
		f.mu.Unlock()
		for _, m := range pending {
			at++
			if err := st.Send(&v1.MessagesResponse{Message: m, Epoch: epoch, Change: at}); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-bell:
		}
	}
}

// Changed says CHANGE_ROOMS each time a set or a rail changes.
func (f *fakeBridge) Changed(ctx context.Context, _ *connect.Request[v1.ChangedRequest], st *connect.ServerStream[v1.ChangedResponse]) error {
	return tell(ctx, f, st, func() int { return f.version }, &v1.ChangedResponse{Change: v1.Change_CHANGE_ROOMS})
}

// UnreadStream sends a frame each time a count changes.
func (f *fakeBridge) UnreadStream(ctx context.Context, _ *connect.Request[v1.UnreadStreamRequest], st *connect.ServerStream[v1.UnreadStreamResponse]) error {
	return tell(ctx, f, st, func() int { return f.unreadV }, &v1.UnreadStreamResponse{Unread: &v1.Unread{}})
}

func (f *fakeBridge) Invites(ctx context.Context, _ *connect.Request[v1.InvitesRequest], st *connect.ServerStream[v1.InvitesResponse]) error {
	return quiet(ctx, st)
}

func (f *fakeBridge) Reactions(ctx context.Context, _ *connect.Request[v1.ReactionsRequest], st *connect.ServerStream[v1.ReactionsResponse]) error {
	return quiet(ctx, st)
}

func (f *fakeBridge) ConfigChanged(ctx context.Context, _ *connect.Request[v1.ConfigChangedRequest], st *connect.ServerStream[v1.ConfigChangedResponse]) error {
	return quiet(ctx, st)
}

// tell sends frame each time version moves.
func tell[R any](ctx context.Context, f *fakeBridge, st *connect.ServerStream[R], version func() int, frame *R) error {
	f.mu.Lock()
	seen := version()
	f.mu.Unlock()
	if err := st.Send(nil); err != nil {
		return err
	}
	for {
		bell := f.listen()
		f.mu.Lock()
		now := version()
		f.mu.Unlock()
		if now != seen {
			seen = now
			if err := st.Send(frame); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-bell:
		}
	}
}

// reachable serves f, and cuts every connection to it on down, as a network a laptop
// left does, refusing new ones until up.
type reachable struct {
	srv  *httptest.Server
	down atomic.Bool
}

func serveFake(t *testing.T, f *fakeBridge) *reachable {
	t.Helper()
	_, h := backendv1connect.NewBackendServiceHandler(f)
	r := &reachable{srv: httptest.NewServer(h)}
	t.Cleanup(r.srv.Close)
	return r
}

func (r *reachable) setDown(down bool) {
	r.down.Store(down)
	if down {
		r.srv.CloseClientConnections()
	}
}

// upstream reaches r whatever host a request names, as daemon.DialBridge does a bridge.
func (r *reachable) upstream() daemon.Upstream {
	addr := r.srv.Listener.Addr().String()
	return daemon.Upstream{Scheme: "http", Client: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			if r.down.Load() {
				return nil, errors.New("the bridge is out of reach")
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		},
	}}}
}

// openTestStore is a store under a key of the test's own.
func openTestStore(t *testing.T, path string, key []byte) *clientstore.Store {
	t.Helper()
	s, err := clientstore.Open(t.Context(), path, key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, sealeddb.KeyLen)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

// servedProxy is a proxy of r serving on a socket of its own, with its store.
type servedProxy struct {
	p      *proxy
	store  *clientstore.Store
	socket string
	stop   func()
}

func serveProxyOf(t *testing.T, r *reachable, store *clientstore.Store, path string, run bool) *servedProxy {
	t.Helper()
	p := newProxy(t.Context(), r.upstream(), store, path, slog.New(slog.DiscardHandler))
	socket := filepath.Join(t.TempDir(), "proxy.sock")
	ln, err := daemon.Listen(t.Context(), socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: p.handler(), ReadHeaderTimeout: time.Minute}
	go func() { _ = srv.Serve(ln) }()
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		if run {
			p.run(ctx)
		}
	}()
	sp := &servedProxy{p: p, store: store, socket: socket}
	var once sync.Once
	sp.stop = func() {
		once.Do(func() {
			cancel()
			<-ran
			_ = srv.Close()
		})
	}
	t.Cleanup(sp.stop)
	return sp
}

// heldView is a client of what the store answers held, served apart from any proxy so
// no probe switches it live meanwhile.
func heldView(t *testing.T, store *clientstore.Store) backendv1connect.BackendServiceClient {
	t.Helper()
	_, h := backendv1connect.NewBackendServiceHandler(&held{store: store}, connect.WithInterceptors(offline, offlineStreams{}))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return backendv1connect.NewBackendServiceClient(srv.Client(), srv.URL)
}

// client is a client of the proxy, as kith or the window is, in profile.
func (sp *servedProxy) client() backendv1connect.BackendServiceClient {
	return backendv1connect.NewBackendServiceClient(daemon.Dial(sp.socket), "http://kithd")
}

// eventually polls ok until it holds or d passes.
func eventually(t *testing.T, d time.Duration, what string, ok func() error) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		err := ok()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %v", what, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// inProfile is req asked in profile.
func inProfile[T any](msg *T, profile string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	if profile != "" {
		req.Header().Set(profileHeader, profile)
	}
	return req
}
