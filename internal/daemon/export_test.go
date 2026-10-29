package daemon

import (
	"net/http"
	"slices"

	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
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
