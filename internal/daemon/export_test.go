package daemon

import (
	"context"
	"net/http"
	"slices"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Test-only accessors.

// RemoteOver is a Remote whose requests go through hc, which may fail some.
func RemoteOver(hc *http.Client) *Remote { return newRemote(hc) }

// Client is the raw socket API at path, for calls in an order no Remote makes.
func Client(path string) backendv1connect.BackendServiceClient {
	return backendv1connect.NewBackendServiceClient(Dial(path), socketBaseURL)
}

// MessageSubscribers counts every reader of the backend's messages, the notifier included.
func (s *Streams) MessageSubscribers() int { return s.messages.subscribers() }

// ConfigSubscribers counts the clients reading ConfigChanged.
func (s *Streams) ConfigSubscribers() int { return s.configs.subscribers() }

// MessageStreams counts the clients' message streams, which read the cache.
func (s *Streams) MessageStreams() int { return s.tail.readers() }

// NarrowingCalls is every call a profile narrows, by method name.
func NarrowingCalls() []string {
	var out []string
	for name, reach := range reaches {
		if reach == narrows {
			out = append(out, name)
		}
	}
	return out
}

// Published is a notice with the topics it was published to.
type Published struct {
	Notice
	Topics []string
}

// Publications subscribes to what n publishes; take returns what arrived since it was
// last called, without waiting: a notice is queued before Deliver returns.
func (n *Notifications) Publications() (take func() []Published, release func()) {
	id, ch := n.notices.subscribeWith(notifierBuffer)
	take = func() []Published {
		var out []Published
		for {
			select {
			case v := <-ch:
				out = append(out, Published{Notice: Notice{ID: v.id, Alert: v.alert, Code: v.code}, Topics: v.topics})
			default:
				return out
			}
		}
	}
	return take, func() { n.notices.release(id) }
}

// The remote listener's throttle.
const (
	HandshakeBurst = handshakeBurst
	MaxHandshakes  = maxHandshakes
)

// ExpirePairings fires every open pairing's expiry timer now, as ten minutes passing
// would.
func (d *Devices) ExpirePairings() {
	d.mu.Lock()
	open := slices.Clone(d.tickets)
	d.mu.Unlock()
	for _, t := range open {
		t.timer.Reset(0)
	}
}

// DraftsServer is a server's drafts as ReplaceDraft and followDrafts keep them, for a
// test that takes every step itself.
type DraftsServer struct{ s *server }

// NewDraftsServer is the drafts of a server over d.
func NewDraftsServer(d *Daemon) DraftsServer {
	return DraftsServer{&server{Daemon: d, windows: newWindows(), drafts: newDraftPace()}}
}

// Save is a draft write as ReplaceDraft takes it: a window's (rebase) or kith-mcp's.
func (x DraftsServer) Save(ctx context.Context, draft, over domain.StoredDraft, rebase bool) (saved bool, stored domain.StoredDraft, err error) {
	r, err := x.s.ReplaceDraft(ctx, connect.NewRequest(&v1.ReplaceDraftRequest{
		Draft: pc.StoredDraftToProto(draft), Over: pc.StoredDraftToProto(over), Rebase: rebase,
	}))
	if err != nil {
		return false, domain.StoredDraft{}, err
	}
	return r.Msg.GetSaved(), pc.ProtoToStoredDraft(r.Msg.GetStored()), nil
}

// Open has the window client listen and show room ("" for none), as Present says.
func (x DraftsServer) Open(client string, room domain.RoomID) {
	x.s.windows.listen(client, "", "")
	x.s.windows.present(client, room, time.Now())
	x.s.drafts.poke()
}

// Rested is every save made so far draftsIdle old.
func (x DraftsServer) Rested() {
	x.s.drafts.mu.Lock()
	defer x.s.drafts.mu.Unlock()
	for at, t := range x.s.drafts.saved {
		x.s.drafts.saved[at] = t.Add(-draftsIdle)
	}
}

// Follow is one pass of followDrafts.
func (x DraftsServer) Follow(ctx context.Context) {
	store, _, _, signer, err := x.s.Paired.meshParts()
	if err != nil {
		panic(err)
	}
	x.s.syncDrafts(ctx, x.s.Backend.(draftKeeper), store, signer)
}

// VerdictsServer is a server's verdicts as RetagVerdicts and followVerdicts keep them,
// for a test that takes every step itself.
type VerdictsServer struct{ s *server }

// NewVerdictsServer is the verdicts of a server over d.
func NewVerdictsServer(d *Daemon) VerdictsServer {
	return VerdictsServer{&server{Daemon: d, verdicts: make(chan struct{}, 1)}}
}

// Retag is RetagVerdicts as the tag editor asks it.
func (x VerdictsServer) Retag(ctx context.Context, from, to string) error {
	_, err := x.s.RetagVerdicts(ctx, connect.NewRequest(&v1.RetagVerdictsRequest{From: from, To: to}))
	return err
}

// Follow is one pass of followVerdicts.
func (x VerdictsServer) Follow(ctx context.Context) {
	store, _, _, signer, err := x.s.Paired.meshParts()
	if err != nil {
		panic(err)
	}
	x.s.syncVerdicts(ctx, x.s.Backend.(verdictKeeper), store, signer)
}
