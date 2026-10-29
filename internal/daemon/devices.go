package daemon

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Devices is the roster of devices paired with kithd, the TLS listeners serving them
// ([remote] listen), and each paired device's live connections, so unpairing one cuts
// it off. The roster file is the truth: every change, every listing and every reload
// reads it first, so a hand edit is neither lost nor ignored.
type Devices struct {
	path     string
	identity func() (tls.Certificate, error)
	log      *slog.Logger

	// mu guards the identity once had, the roster and the connections. Taken inside
	// lmu, never the other way round.
	mu     sync.Mutex
	cert   *tls.Certificate
	self   devicekey.ID
	paired []pairedDevice
	conns  map[devicekey.ID]map[*remoteConn]struct{}
	// tickets are the pairings open now; while there is one, an unpaired key is let
	// in to call Pair alone, its connection held in joining.
	tickets []*ticket
	joining map[*remoteConn]struct{}

	lmu       sync.Mutex
	want      []string
	handler   http.Handler // nil while Serve is not running
	base      func(net.Listener) context.Context
	listeners map[string]*http.Server
	bound     map[string]string // each listener's address as bound: a device dials it
	serving   sync.WaitGroup
}

// pairedDevice is a roster entry with its ID parsed.
type pairedDevice struct {
	id devicekey.ID
	domain.Device
}

// NewDevices keeps the roster at path. identity is kithd's own certificate, made from
// its device key; it is asked for when first needed and again after a failure.
func NewDevices(path string, identity func() (tls.Certificate, error), log *slog.Logger) *Devices {
	return &Devices{
		path: path, identity: identity, log: log,
		conns: map[devicekey.ID]map[*remoteConn]struct{}{}, joining: map[*remoteConn]struct{}{},
	}
}

// Errors a device change is refused with.
var (
	errUnknownDevice = errors.New("no device is paired with that ID")
	errAlreadyPaired = errors.New("that device is already paired")
	errNameTaken     = errors.New("another device has that name")
	errOwnID         = errors.New("that is this bridge's own ID; pair the client's (`kith devices id` on it)")
)

// identityLocked is kithd's certificate and ID, made on first use.
func (d *Devices) identityLocked() (tls.Certificate, devicekey.ID, error) {
	if d.cert != nil {
		return *d.cert, d.self, nil
	}
	cert, err := d.identity()
	if err != nil {
		return tls.Certificate{}, devicekey.ID{}, fmt.Errorf("kithd has no device key: %w", err)
	}
	self, err := devicekey.IDOf(cert.Leaf.PublicKey)
	if err != nil {
		return tls.Certificate{}, devicekey.ID{}, fmt.Errorf("kithd's device key: %w", err)
	}
	d.cert, d.self = &cert, self
	return cert, self, nil
}

// List is kithd's own device ID and the devices paired with it.
func (d *Devices) List() (self string, devices []domain.Device, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, id, err := d.identityLocked()
	if err != nil {
		return "", nil, err
	}
	if err := d.refreshLocked(); err != nil {
		return "", nil, err
	}
	for _, p := range d.paired {
		devices = append(devices, p.Device)
	}
	return id.String(), devices, nil
}

// Add pairs the device whose pasted ID is idText under name; by is the device asking
// (none: kithd's own, over the socket).
func (d *Devices) Add(idText, name string, by *devicekey.ID) (domain.Device, error) {
	id, err := devicekey.ParseID(idText)
	if err != nil {
		return domain.Device{}, fmt.Errorf("daemon: pair: %w", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.addLocked(id, name, by)
}

// addLocked pairs id under name, against the roster file as it stands.
func (d *Devices) addLocked(id devicekey.ID, name string, by *devicekey.ID) (domain.Device, error) {
	_, self, err := d.identityLocked()
	if err != nil {
		return domain.Device{}, err
	}
	if id == self {
		return domain.Device{}, errOwnID
	}
	if err := d.refreshLocked(); err != nil {
		return domain.Device{}, err
	}
	for _, p := range d.paired {
		switch {
		case p.id == id:
			return domain.Device{}, fmt.Errorf("%w (as %q)", errAlreadyPaired, p.Name)
		case name != "" && p.Name == name:
			return domain.Device{}, fmt.Errorf("%w: %q", errNameTaken, name)
		}
	}
	if by == nil {
		by = &self
	}
	added := pairedDevice{id: id, Device: domain.Device{
		Name: name, ID: id.String(), AddedAt: time.Now().UTC().Truncate(time.Second), AddedBy: by.String(),
	}}
	next := append(slices.Clone(d.paired), added)
	if err := writeRoster(d.path, next); err != nil {
		return domain.Device{}, err
	}
	d.paired = next
	return added.Device, nil
}

// Remove unpairs the device whose ID is idText and closes its live connections.
func (d *Devices) Remove(idText string) error {
	id, err := devicekey.ParseID(idText)
	if err != nil {
		return fmt.Errorf("daemon: unpair: %w", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.refreshLocked(); err != nil {
		return err
	}
	i := slices.IndexFunc(d.paired, func(p pairedDevice) bool { return p.id == id })
	if i < 0 {
		return errUnknownDevice
	}
	next := slices.Delete(slices.Clone(d.paired), i, i+1)
	if err := writeRoster(d.path, next); err != nil {
		return err
	}
	d.paired = next
	d.cutLocked(id)
	return nil
}

// refreshLocked reads the roster file and cuts off every device it no longer holds. A
// file that will not read changes nothing and is the error.
func (d *Devices) refreshLocked() error {
	paired, err := readRoster(d.path)
	if err != nil {
		return err
	}
	d.paired = paired
	for id := range d.conns {
		if !slices.ContainsFunc(paired, func(p pairedDevice) bool { return p.id == id }) {
			d.cutLocked(id)
		}
	}
	return nil
}

// cutLocked closes every live connection of id.
func (d *Devices) cutLocked(id devicekey.ID) {
	for c := range d.conns[id] {
		_ = c.Conn.Close() // the device is gone either way; its handlers see the close
	}
	delete(d.conns, id)
}

// admit takes a connection whose handshake shows id, if id is paired, tracking it
// under id: in one step with Remove's, so no connection is admitted after its device
// was removed and missed by the cut. While a pairing is open, any other key is let in
// to answer it and nothing else (pairingGate).
func (d *Devices) admit(id devicekey.ID, c *remoteConn) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case c.gone:
		return false
	case slices.ContainsFunc(d.paired, func(p pairedDevice) bool { return p.id == id }):
		c.id, c.admitted = id, true
		d.trackLocked(c)
	case len(d.tickets) > 0:
		c.id, c.admitted, c.onlyPairs = id, true, true
		d.joining[c] = struct{}{}
	default:
		return false
	}
	return true
}

// trackLocked holds c under its device's ID, so unpairing the device cuts it.
func (d *Devices) trackLocked(c *remoteConn) {
	if d.conns[c.id] == nil {
		d.conns[c.id] = map[*remoteConn]struct{}{}
	}
	d.conns[c.id][c] = struct{}{}
}

// forget stops tracking a connection that closed.
func (d *Devices) forget(c *remoteConn) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c.gone = true
	if !c.admitted {
		return
	}
	delete(d.joining, c)
	delete(d.conns[c.id], c)
	if len(d.conns[c.id]) == 0 {
		delete(d.conns, c.id)
	}
}

// peerKey carries a request's remote connection, set by ConnContext.
type peerKey struct{}

// peer is the device a request came from over [remote] listen; nil over the socket.
func (d *Devices) peer(ctx context.Context) *devicekey.ID {
	c, ok := ctx.Value(peerKey{}).(*remoteConn)
	if !ok {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	id := c.id
	return &id
}

// Listen listens on addrs from now on, stopping what is no longer named and starting
// what is newly named, once Serve runs; a reload calls it with the reloaded config's.
func (d *Devices) Listen(addrs []string) {
	d.lmu.Lock()
	defer d.lmu.Unlock()
	d.want = slices.Clone(addrs)
	d.reconcileLocked()
}

// serve starts the listeners with Serve's handler; base is every connection's context.
func (d *Devices) serve(handler http.Handler, base func(net.Listener) context.Context) {
	d.lmu.Lock()
	defer d.lmu.Unlock()
	d.handler, d.base = handler, base
	d.reconcileLocked()
}

// shut closes every listener and every remote connection, and waits for the
// listeners to return; Serve calls it on its way out.
func (d *Devices) shut() {
	d.lmu.Lock()
	d.handler = nil
	d.stopLocked(nil)
	d.lmu.Unlock()
	d.serving.Wait()
}

// stopLocked closes the listeners whose addresses keep does not name, with their
// connections.
func (d *Devices) stopLocked(keep []string) {
	for addr, srv := range d.listeners {
		if !slices.Contains(keep, addr) {
			_ = srv.Close() // its connections close with it; nothing else to report
			delete(d.listeners, addr)
			delete(d.bound, addr)
			d.log.Info("stopped listening for paired devices", "addr", addr)
		}
	}
}

// reconcileLocked makes the listeners match want. Nothing listens without kithd's key
// or a roster that reads: the socket is served regardless.
func (d *Devices) reconcileLocked() {
	if d.handler == nil {
		return
	}
	d.stopLocked(d.want)
	if len(d.want) == 0 {
		return
	}
	d.mu.Lock()
	cert, _, kerr := d.identityLocked()
	rerr := d.refreshLocked()
	d.mu.Unlock()
	if err := errors.Join(kerr, rerr); err != nil {
		d.stopLocked(nil)
		d.log.Error("not listening for paired devices; the socket is still served", "roster", d.path, "err", err)
		return
	}
	if d.listeners == nil {
		d.listeners, d.bound = map[string]*http.Server{}, map[string]string{}
	}
	for _, addr := range d.want {
		if _, ok := d.listeners[addr]; ok {
			continue
		}
		ln, err := (&net.ListenConfig{}).Listen(d.base(nil), "tcp", addr)
		if err != nil {
			d.log.Warn("cannot listen for paired devices; the socket is still served", "addr", addr, "err", err)
			continue
		}
		srv := d.remoteServer(cert)
		d.listeners[addr] = srv
		d.bound[addr] = ln.Addr().String()
		d.serving.Go(func() {
			if serr := srv.ServeTLS(newGate(ln, d), "", ""); !errors.Is(serr, http.ErrServerClosed) {
				d.log.Warn("stopped listening for paired devices", "addr", addr, "err", serr)
				// Gone, so the next reload listens there again.
				d.lmu.Lock()
				if d.listeners[addr] == srv {
					delete(d.listeners, addr)
					delete(d.bound, addr)
				}
				d.lmu.Unlock()
			}
		})
		d.log.Info("listening for paired devices", "addr", ln.Addr().String())
	}
}

// remoteHandshakeTimeout bounds a TLS handshake (and each request's headers) on a
// remote connection: a peer that stalls holds a handshake slot no longer.
const remoteHandshakeTimeout = 10 * time.Second

// remoteServer serves the socket's handler over TLS 1.3 to paired devices only.
func (d *Devices) remoteServer(cert tls.Certificate) *http.Server {
	return &http.Server{
		Handler:           d.handler,
		ReadHeaderTimeout: remoteHandshakeTimeout,
		// Every refused handshake lands here: a scanner's, or a device not paired yet.
		ErrorLog:    slog.NewLogLogger(d.log.Handler(), slog.LevelDebug),
		BaseContext: d.base,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if tc, ok := c.(*tls.Conn); ok {
				if rc, ok := tc.NetConn().(*remoteConn); ok {
					return context.WithValue(ctx, peerKey{}, rc)
				}
			}
			return ctx
		},
		TLSConfig: d.tlsConfig(cert),
	}
}

// errNotPaired refuses a handshake whose key no roster entry names.
var errNotPaired = errors.New("daemon: this device is not paired")

// tlsConfig is mutual TLS with the peer's key pinned: any certificate is asked for,
// none is checked against a CA, and VerifyConnection admits only a paired key. It
// runs on resumed sessions too, but tickets are off anyway, so nothing outlives an
// unpairing.
func (d *Devices) tlsConfig(cert tls.Certificate) *tls.Config {
	config := func(verify func(tls.ConnectionState) error) *tls.Config {
		return &tls.Config{
			MinVersion:             tls.VersionTLS13,
			Certificates:           []tls.Certificate{cert},
			NextProtos:             []string{"h2", "http/1.1"},
			ClientAuth:             tls.RequireAnyClientCert,
			SessionTicketsDisabled: true,
			VerifyConnection:       verify,
		}
	}
	refuse := func(tls.ConnectionState) error { return errNotPaired }
	base := config(refuse)
	// Per connection, so the check knows which connection to track under the ID.
	base.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		c, ok := hello.Conn.(*remoteConn)
		if !ok {
			return config(refuse), nil
		}
		return config(func(state tls.ConnectionState) error {
			defer c.release()
			id, err := devicekey.PeerID(state)
			if err != nil {
				return fmt.Errorf("daemon: %w", err)
			}
			if !d.admit(id, c) {
				d.log.Debug("refused an unpaired device", "id", id.String(), "from", c.RemoteAddr().String())
				return errNotPaired
			}
			return nil
		}), nil
	}
	return base
}

// rosterFile is the roster as written: one [[device]] per paired device.
type rosterFile struct {
	Device []rosterEntry `toml:"device"`
}

type rosterEntry struct {
	Name    string    `toml:"name"`
	ID      string    `toml:"id"`
	AddedAt time.Time `toml:"added_at"`
	AddedBy string    `toml:"added_by"`
}

const rosterHeader = "# The devices paired with this kithd: each may do everything over [remote] listen.\n" +
	"# Change it with `kith devices`.\n\n"

// readRoster reads the roster file; none is no device paired. An entry whose ID does
// not read, a key the file does not have, or a device or name listed twice is an error.
func readRoster(path string) ([]pairedDevice, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- the roster, a path of kithd's own
	if errors.Is(err, os.ErrNotExist) {
		return []pairedDevice{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("daemon: read the device roster: %w", err)
	}
	var f rosterFile
	md, err := toml.Decode(string(body), &f)
	if err != nil {
		return nil, fmt.Errorf("daemon: the device roster %s: %w", path, err)
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		return nil, fmt.Errorf("daemon: the device roster %s: unknown key %s", path, extra[0])
	}
	out := make([]pairedDevice, 0, len(f.Device))
	for _, e := range f.Device {
		id, err := devicekey.ParseID(e.ID)
		if err != nil {
			return nil, fmt.Errorf("daemon: the device roster %s: %w", path, err)
		}
		for _, p := range out {
			if p.id == id || (e.Name != "" && p.Name == e.Name) {
				return nil, fmt.Errorf("daemon: the device roster %s lists %s twice", path, cmp.Or(e.Name, e.ID))
			}
		}
		out = append(out, pairedDevice{id: id, Device: domain.Device{Name: e.Name, ID: id.String(), AddedAt: e.AddedAt, AddedBy: e.AddedBy}})
	}
	return out, nil
}

// writeRoster replaces the roster file atomically, owner-only.
func writeRoster(path string, paired []pairedDevice) error {
	var f rosterFile
	for _, p := range paired {
		f.Device = append(f.Device, rosterEntry{Name: p.Name, ID: p.ID, AddedAt: p.AddedAt, AddedBy: p.AddedBy})
	}
	var body bytes.Buffer
	body.WriteString(rosterHeader)
	if err := toml.NewEncoder(&body).Encode(f); err != nil {
		return fmt.Errorf("daemon: encode the device roster: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("daemon: write the device roster: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".devices-*.toml")
	if err != nil {
		return fmt.Errorf("daemon: write the device roster: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // gone after the rename; a leftover on failure
	_, err = tmp.Write(body.Bytes())
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o600)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return fmt.Errorf("daemon: write the device roster: %w", err)
	}
	return nil
}
