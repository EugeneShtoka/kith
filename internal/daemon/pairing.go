package daemon

import (
	"cmp"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"path"
	"slices"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/pairing"
)

// pairingLifetime is how long a pairing token is good for.
const pairingLifetime = 10 * time.Minute

// pairingCutGrace is how long a device a pairing ended without has to read how it
// ended before its connection is cut.
const pairingCutGrace = time.Second

// Errors a pairing is refused with.
var (
	errNotListening   = errors.New("kithd listens nowhere a device can dial: set [remote] listen in the bridge's config.toml")
	errNoPairing      = errors.New("no pairing waits for that token: it was used, refused or has expired; run `kith pair` again")
	errPairingTaken   = errors.New("another device answered this pairing first")
	errNothingPending = errors.New("no device has answered this pairing yet")
	errPairOverSocket = errors.New("pair is a new device's call, over [remote] listen")
	errOnlyPair       = errors.New("this device is not paired: it may only answer a pairing")
)

// ticket is one open pairing: its token, good once until expires, and the device that
// answered it. It lives while the stream that opened it does.
type ticket struct {
	token    pairing.Token
	name     string // what the trusted side named the device; "" leaves it to the device
	expires  time.Time
	timer    *time.Timer
	answered chan struct{} // closed when a device answers
	ended    chan struct{} // closed when the pairing ends

	// Guarded by Devices.mu.
	pending *joiner
	outcome domain.PairingOutcome
}

// joiner is the device pending under a ticket, and the connection it answered over.
type joiner struct {
	domain.PendingDevice
	id   devicekey.ID
	conn *remoteConn
}

// startPairing opens a pairing named name and its ticket. Nothing to dial is refused.
func (d *Devices) startPairing(name string) (*ticket, domain.PairingTicket, error) {
	addrs, err := d.dialable()
	if err != nil {
		return nil, domain.PairingTicket{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, self, err := d.identityLocked()
	if err != nil {
		return nil, domain.PairingTicket{}, err
	}
	t := &ticket{
		token: pairing.NewToken(), name: name, expires: time.Now().Add(pairingLifetime),
		answered: make(chan struct{}), ended: make(chan struct{}),
	}
	t.timer = time.AfterFunc(pairingLifetime, func() { d.endPairing(t, domain.PairingExpired) })
	d.tickets = append(d.tickets, t)
	return t, domain.PairingTicket{Token: t.token.String(), Addresses: addrs, ID: self.String(), Expires: t.expires}, nil
}

// ticketLocked is the open pairing under token, if one is and has not expired; the
// tokens are compared in constant time.
func (d *Devices) ticketLocked(token pairing.Token) *ticket {
	for _, t := range d.tickets {
		if subtle.ConstantTimeCompare(t.token[:], token[:]) == 1 && time.Now().Before(t.expires) {
			return t
		}
	}
	return nil
}

// answer holds the device on c as pending under the token, named name unless the
// trusted side named it: one device per token, and never one already paired.
func (d *Devices) answer(tokenText, name string, c *remoteConn) (*ticket, error) {
	token, err := pairing.ParseToken(tokenText)
	if err != nil {
		return nil, fmt.Errorf("daemon: pair: %w", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.ticketLocked(token)
	switch {
	case t == nil:
		return nil, errNoPairing
	case t.pending != nil:
		return nil, errPairingTaken
	}
	if _, err := d.refreshLocked(); err != nil {
		return nil, err
	}
	self := d.self
	name = cmp.Or(t.name, name)
	for _, p := range d.paired {
		switch {
		case p.id == c.id:
			return nil, fmt.Errorf("%w (as %q)", errAlreadyPaired, p.Name)
		case name != "" && p.Name == name:
			return nil, fmt.Errorf("%w: %q (`kith join --name` gives another)", errNameTaken, name)
		}
	}
	t.pending = &joiner{
		PendingDevice: domain.PendingDevice{Name: name, ID: c.id.String(), Digits: pairing.Digits(self, c.id, token)},
		id:            c.id, conn: c,
	}
	close(t.answered)
	return t, nil
}

// confirm pairs the device pending under the token, as by asks (nil: over the
// socket), or, when accept is false, ends the pairing.
func (d *Devices) confirm(tokenText string, accept bool, by *devicekey.ID) (domain.Device, error) {
	token, err := pairing.ParseToken(tokenText)
	if err != nil {
		return domain.Device{}, fmt.Errorf("daemon: confirm a pairing: %w", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.ticketLocked(token)
	switch {
	case t == nil:
		return domain.Device{}, errNoPairing
	case !accept:
		d.endLocked(t, domain.PairingRefused)
		return domain.Device{}, nil
	case t.pending == nil:
		return domain.Device{}, errNothingPending
	}
	added, err := d.addLocked(t.pending.id, t.pending.Name, by)
	if err != nil {
		d.endLocked(t, domain.PairingRefused)
		return domain.Device{}, err
	}
	// The connection it paired over is the device's now, so unpairing it cuts that too.
	if c := t.pending.conn; !c.gone {
		delete(d.joining, c)
		d.trackLocked(c)
	}
	d.endLocked(t, domain.PairingConfirmed)
	return added, nil
}

// endPairing ends t with outcome, unless it has ended already.
func (d *Devices) endPairing(t *ticket, outcome domain.PairingOutcome) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.endLocked(t, outcome)
}

func (d *Devices) endLocked(t *ticket, outcome domain.PairingOutcome) {
	i := slices.Index(d.tickets, t)
	if i < 0 {
		return
	}
	d.tickets = slices.Delete(d.tickets, i, i+1)
	t.timer.Stop()
	t.outcome = outcome
	close(t.ended)
	time.AfterFunc(pairingCutGrace, func() { d.cutJoiners(t) })
}

// cutJoiners closes the connection of the device t did not pair and, once no pairing
// is open, every connection let in only to pair.
func (d *Devices) cutJoiners(t *ticket) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t.pending != nil && t.outcome != domain.PairingConfirmed {
		d.cutJoinerLocked(t.pending.conn)
	}
	if len(d.tickets) == 0 {
		for c := range d.joining {
			d.cutJoinerLocked(c)
		}
	}
}

func (d *Devices) cutJoinerLocked(c *remoteConn) {
	if _, ok := d.joining[c]; ok {
		_ = c.Conn.Close() // its handlers see the close; nothing else to report
		delete(d.joining, c)
	}
}

// pendingOf and outcomeOf read t under the lock.
func (d *Devices) pendingOf(t *ticket) domain.PendingDevice {
	d.mu.Lock()
	defer d.mu.Unlock()
	return t.pending.PendingDevice
}

func (d *Devices) outcomeOf(t *ticket) domain.PairingOutcome {
	d.mu.Lock()
	defer d.mu.Unlock()
	return t.outcome
}

// onlyPairs reports whether a request came over a connection let in only to pair.
func (d *Devices) onlyPairs(ctx context.Context) bool {
	c, ok := ctx.Value(peerKey{}).(*remoteConn)
	if !ok {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return c.onlyPairs
}

// dialable is every address a device can dial kithd on: each listener's as bound, a
// wildcard one expanded to this machine's addresses. None is refused.
func (d *Devices) dialable() ([]string, error) {
	d.lmu.Lock()
	var bound []string
	for _, addr := range d.want {
		if b, ok := d.bound[addr]; ok {
			bound = append(bound, b)
		}
	}
	d.lmu.Unlock()
	var ifaces []net.Addr
	if slices.ContainsFunc(bound, isWildcard) {
		var err error
		if ifaces, err = net.InterfaceAddrs(); err != nil {
			return nil, fmt.Errorf("daemon: this machine's addresses: %w", err)
		}
	}
	addrs := expandWildcards(bound, ifaces)
	if len(addrs) == 0 {
		return nil, errNotListening
	}
	return addrs, nil
}

// isWildcard reports whether a bound address takes every interface.
func isWildcard(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsUnspecified()
}

// expandWildcards is bound with each wildcard (0.0.0.0, [::]) replaced by every
// address in ifaces a device elsewhere can dial: neither loopback nor link-local (which
// needs an interface named), IPv4 first. [::] takes IPv4 too, as Go listens dual-stack.
func expandWildcards(bound []string, ifaces []net.Addr) []string {
	var out []string
	add := func(addr string) {
		if !slices.Contains(out, addr) {
			out = append(out, addr)
		}
	}
	for _, b := range bound {
		if !isWildcard(b) {
			add(b)
			continue
		}
		host, port, _ := net.SplitHostPort(b) // isWildcard read it
		v6 := net.ParseIP(host).To4() == nil
		for _, want6 := range []bool{false, true} {
			if want6 && !v6 {
				continue
			}
			for _, ia := range ifaces {
				n, ok := ia.(*net.IPNet)
				if !ok || n.IP.IsLoopback() || n.IP.IsLinkLocalUnicast() || (n.IP.To4() == nil) != want6 {
					continue
				}
				add(net.JoinHostPort(n.IP.String(), port))
			}
		}
	}
	return out
}

// pairingGate is the server's interceptor beside profileGate: a connection let in only
// to pair may call Pair and nothing else.
type pairingGate struct{ devices *Devices }

func (g pairingGate) admit(ctx context.Context, procedure string) error {
	if path.Base(procedure) != "Pair" && g.devices.onlyPairs(ctx) {
		return connect.NewError(connect.CodePermissionDenied, errOnlyPair)
	}
	return nil
}

func (g pairingGate) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := g.admit(ctx, req.Spec().Procedure); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

// WrapStreamingClient is unused: the gate is server-only.
func (g pairingGate) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (g pairingGate) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := g.admit(ctx, conn.Spec().Procedure); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

// ── Handlers ──

// StartPairing opens a pairing for as long as the stream is: the ticket, the device
// that answers, then how it ended. The stream going away ends it unconfirmed.
func (s *server) StartPairing(ctx context.Context, r *req[v1.StartPairingRequest], st *connect.ServerStream[v1.StartPairingResponse]) error {
	t, ticket, err := s.Paired.startPairing(r.Msg.GetName())
	if err != nil {
		return pairingErr(err)
	}
	defer s.Paired.endPairing(t, domain.PairingAbandoned)
	if err := sendFrame(st, &v1.StartPairingResponse{Event: &v1.StartPairingResponse_Ticket{Ticket: pc.PairingTicketToProto(ticket)}}); err != nil {
		return err
	}
	select {
	case <-t.answered:
		pending := pc.PendingDeviceToProto(s.Paired.pendingOf(t))
		if err := sendFrame(st, &v1.StartPairingResponse{Event: &v1.StartPairingResponse_Pending{Pending: pending}}); err != nil {
			return err
		}
	case <-t.ended:
	case <-ctx.Done():
		return nil
	}
	select {
	case <-t.ended:
	case <-ctx.Done():
		return nil
	}
	outcome := pc.PairingOutcomeToProto(s.Paired.outcomeOf(t))
	return sendFrame(st, &v1.StartPairingResponse{Event: &v1.StartPairingResponse_Outcome{Outcome: outcome}})
}

func (s *server) ConfirmPairing(ctx context.Context, r *req[v1.ConfirmPairingRequest]) (*resp[v1.ConfirmPairingResponse], error) {
	added, err := s.Paired.confirm(r.Msg.GetToken(), r.Msg.GetAccept(), s.Paired.peer(ctx))
	if err != nil {
		return nil, pairingErr(err)
	}
	return connect.NewResponse(&v1.ConfirmPairingResponse{Device: pc.DeviceToProto(added)}), nil
}

// Pair is a new device answering a pairing: the digits, then how it ended. The device
// going away ends it unconfirmed.
func (s *server) Pair(ctx context.Context, r *req[v1.PairRequest], st *connect.ServerStream[v1.PairResponse]) error {
	c, ok := ctx.Value(peerKey{}).(*remoteConn)
	if !ok {
		return connect.NewError(connect.CodeInvalidArgument, errPairOverSocket)
	}
	t, err := s.Paired.answer(r.Msg.GetToken(), r.Msg.GetName(), c)
	if err != nil {
		return pairingErr(err)
	}
	defer s.Paired.endPairing(t, domain.PairingAbandoned)
	if err := sendFrame(st, &v1.PairResponse{Event: &v1.PairResponse_Digits{Digits: s.Paired.pendingOf(t).Digits}}); err != nil {
		return err
	}
	select {
	case <-t.ended:
	case <-ctx.Done():
		return nil
	}
	outcome := pc.PairingOutcomeToProto(s.Paired.outcomeOf(t))
	return sendFrame(st, &v1.PairResponse{Event: &v1.PairResponse_Outcome{Outcome: outcome}})
}

// pairingErr gives a refused pairing its code; the rest are deviceErr's.
func pairingErr(err error) error {
	var code connect.Code
	switch {
	case errors.Is(err, pairing.ErrNotAToken):
		code = connect.CodeInvalidArgument
	case errors.Is(err, errNoPairing):
		code = connect.CodeNotFound
	case errors.Is(err, errPairingTaken), errors.Is(err, errNothingPending), errors.Is(err, errNotListening):
		code = connect.CodeFailedPrecondition
	default:
		return deviceErr(err)
	}
	return connect.NewError(code, err)
}
