package daemon

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/devicekey"
)

// What a remote listener lets through before TLS: each address (an IPv6 /64 counting
// as one) has a bucket of handshakeBurst handshakes, refilled one per handshakeEvery,
// and no more than maxHandshakes run at once across addresses. A paired device opens
// a handful of connections per start or network change; a flood from one address
// soon waits two seconds per try while every other address still gets through.
const (
	handshakeBurst = 10
	handshakeEvery = 2 * time.Second
	maxHandshakes  = 32
	// maxBuckets bounds what the throttle remembers; past it, full buckets (addresses
	// quiet long enough to refill) are forgotten first.
	maxBuckets = 4096
	// acceptRetry is the pause after an accept that failed but left the listener open.
	acceptRetry = 100 * time.Millisecond
)

// gate is a remote listener's throttle: Accept hands on a connection only within its
// address's bucket and while a handshake slot is free, closing the rest unread.
type gate struct {
	net.Listener
	devices *Devices
	slots   chan struct{}
	done    chan struct{}
	close   sync.Once

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newGate(ln net.Listener, devices *Devices) *gate {
	return &gate{
		Listener: ln, devices: devices,
		slots: make(chan struct{}, maxHandshakes), done: make(chan struct{}),
		buckets: map[string]*bucket{},
	}
}

// Accept waits for a handshake slot, then for a connection its address's bucket
// admits. The slot is the connection's until its handshake ends or it closes.
func (g *gate) Accept() (net.Conn, error) {
	for {
		select {
		case g.slots <- struct{}{}:
		case <-g.done:
			return nil, net.ErrClosed
		}
		c, err := g.Listener.Accept()
		if err != nil {
			<-g.slots
			if errors.Is(err, net.ErrClosed) {
				return nil, net.ErrClosed
			}
			// Out of descriptors, say: wait and try again, as http.Server does.
			select {
			case <-time.After(acceptRetry):
				continue
			case <-g.done:
				return nil, net.ErrClosed
			}
		}
		if !g.allow(c.RemoteAddr(), time.Now()) {
			<-g.slots
			_ = c.Close() // throttled: closed before a byte of TLS
			continue
		}
		return newRemoteConn(c, g.devices, func() { <-g.slots }), nil
	}
}

// Close stops Accept, including one waiting for a slot.
func (g *gate) Close() error {
	g.close.Do(func() { close(g.done) })
	if err := g.Listener.Close(); err != nil {
		return fmt.Errorf("daemon: close a remote listener: %w", err)
	}
	return nil
}

// allow takes a token from addr's bucket, if it has one.
func (g *gate) allow(addr net.Addr, now time.Time) bool {
	key := throttleKey(addr)
	g.mu.Lock()
	defer g.mu.Unlock()
	b, ok := g.buckets[key]
	if !ok {
		if len(g.buckets) >= maxBuckets {
			g.pruneLocked(now)
		}
		b = &bucket{tokens: handshakeBurst, at: now}
		g.buckets[key] = b
	}
	b.tokens = min(handshakeBurst, b.tokens+float64(now.Sub(b.at))/float64(handshakeEvery))
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// pruneLocked forgets the buckets that have refilled; if that frees nothing (a flood
// from many addresses), it forgets them all, which costs at most one burst each.
func (g *gate) pruneLocked(now time.Time) {
	for key, b := range g.buckets {
		if b.tokens+float64(now.Sub(b.at))/float64(handshakeEvery) >= handshakeBurst {
			delete(g.buckets, key)
		}
	}
	if len(g.buckets) >= maxBuckets {
		clear(g.buckets)
	}
}

// throttleKey is the address a bucket is kept for: the IPv4 address, or the IPv6 /64,
// which one subscriber usually holds whole.
func throttleKey(addr net.Addr) string {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return addr.String()
	}
	if v4 := tcp.IP.To4(); v4 != nil {
		return v4.String()
	}
	return tcp.IP.Mask(net.CIDRMask(64, 128)).String()
}

// remoteConn is one connection from a remote listener: it holds a handshake slot until
// its handshake ends, and once admitted is tracked under its device's ID (guarded by
// Devices.mu) so unpairing closes it.
type remoteConn struct {
	net.Conn
	devices *Devices
	once    sync.Once
	slot    func()

	id       devicekey.ID
	admitted bool
	gone     bool
	// onlyPairs is a key let in, unpaired, to answer a pairing: it may call Pair alone.
	onlyPairs bool
}

func newRemoteConn(c net.Conn, devices *Devices, slot func()) *remoteConn {
	return &remoteConn{Conn: c, devices: devices, slot: slot}
}

// release gives back the handshake slot; only the first call does.
func (c *remoteConn) release() { c.once.Do(c.slot) }

func (c *remoteConn) Close() error {
	c.release()
	c.devices.forget(c)
	if err := c.Conn.Close(); err != nil {
		return fmt.Errorf("daemon: close a remote connection: %w", err)
	}
	return nil
}
