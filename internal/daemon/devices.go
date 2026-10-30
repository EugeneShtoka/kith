package daemon

import (
	"bytes"
	"cmp"
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
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

	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Devices is the roster of devices paired with kithd, the TLS listeners serving them
// ([remote] listen), and each paired device's live connections, so unpairing one cuts
// it off. The roster is kept as signed ops in the mesh store; the roster file is
// rendered from it, and every change, every listing and every reload reads the file
// first, so a hand edit becomes ops of this bridge's and is not lost.
type Devices struct {
	path     string
	store    func() (RosterStore, error)
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
	// refreshed is a roster read once (refreshLocked), and rendered the roster file as
	// kithd last read or wrote it, known its entries: a change arriving by sync renders
	// over the file only while it still is that, and a hand edit is read against it.
	refreshed bool
	rendered  []byte
	known     []pairedDevice
	// outOfMesh is this bridge found removed from its mesh, said once.
	outOfMesh bool

	// wake pokes the mesh's loop (mesh.go) when the listeners change.
	wake chan struct{}
	// meshing runs the mesh's loop while Serve does; stopMesh ends it.
	meshing  sync.WaitGroup
	stopMesh context.CancelFunc

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

// RosterStore keeps the roster as signed ops and syncs it with the mesh's other bridges:
// the mesh store (internal/mesh), which kithd opens. Clients link this package, so it
// names the store only through this.
type RosterStore interface {
	// Roster is every member, this bridge among them, oldest first, and the IDs of
	// those removed.
	Roster() (members []domain.Device, removed []string, err error)
	// ChangeRoster writes each device of set, and the removal of each ID in remove, as
	// one op signed by signer: a bridge of the roster, or on an empty store the
	// genesis, whose set names the signer.
	ChangeRoster(ctx context.Context, signer crypto.Signer, set []domain.Device, remove []string) error
	// Bridge writes the entry of the bridge d names with its public key (PKIX) and the
	// addresses it is dialed at, signed by signer; this bridge's own takes the signer's.
	Bridge(ctx context.Context, signer crypto.Signer, d domain.Device, key []byte, addresses []string) error
	// PublishAccounts writes the accounts this bridge runs (bridge/<ID>/accounts),
	// signed by signer, when they changed.
	PublishAccounts(ctx context.Context, signer crypto.Signer, accounts []domain.MeshAccount) error
	// PublishAliases writes each room merged here into another (alias/<room>), signed
	// by signer, unless the mesh holds it so already.
	PublishAliases(ctx context.Context, signer crypto.Signer, aliases map[domain.RoomID]domain.RoomID) error
	// Aliases is every room the mesh holds as merged into another.
	Aliases() map[domain.RoomID]domain.RoomID
	// Sync runs one sync session of this bridge (signer, its device key) with the
	// bridge peer over frames until either ends it; dialed is whether this side dialed.
	Sync(ctx context.Context, signer crypto.Signer, peer devicekey.ID, dialed bool, frames MeshFrames, log *slog.Logger) error
	// Join makes this bridge one of the mesh of sponsor, which has just confirmed its
	// pairing, over frames: the mesh's members it admits from then on.
	Join(ctx context.Context, self, sponsor devicekey.ID, signer crypto.Signer, frames MeshFrames) ([]domain.Device, error)
	// Connected reports whether a sync stream with peer is open.
	Connected(peer devicekey.ID) bool
	// Changed is closed after the store's next commit.
	Changed() <-chan struct{}
	// Status is the mesh as this bridge (self) sees it.
	Status(self devicekey.ID) (domain.MeshStatus, error)
}

// MeshFrames is one sync stream as either side sees it (mesh.Frames).
type MeshFrames = interface {
	Send(*meshv1.SyncFrame) error
	Receive() (*meshv1.SyncFrame, error)
}

// NewDevices keeps the roster in the mesh store, rendered as the file at path. store
// and identity (kithd's own certificate, made from its device key) are asked for when
// first needed and again after a failure.
func NewDevices(path string, store func() (RosterStore, error), identity func() (tls.Certificate, error), log *slog.Logger) *Devices {
	return &Devices{
		path: path, store: store, identity: identity, log: log,
		conns: map[devicekey.ID]map[*remoteConn]struct{}{}, joining: map[*remoteConn]struct{}{},
		wake: make(chan struct{}, 1),
	}
}

// Errors a device change is refused with.
var (
	errUnknownDevice = errors.New("no device is paired with that ID")
	errAlreadyPaired = errors.New("that device is already paired")
	errNameTaken     = errors.New("another device has that name")
	errOwnID         = errors.New("that is this bridge's own ID; pair the client's (`kith devices id` on it)")
	errOwnBridge     = errors.New("that is this bridge's own ID: unpairing it would leave no bridge to pair a device with")
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
	if _, err := d.refreshLocked(); err != nil {
		return "", nil, err
	}
	for _, p := range d.paired {
		devices = append(devices, p.Device)
	}
	return d.self.String(), devices, nil
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
	return d.addLocked(id, domain.Device{Name: name, Role: domain.RoleDevice}, nil, by)
}

// addLocked pairs id as as says (its name and role; a bridge's key, PKIX, and the
// addresses it is dialed at in joined), against the roster as it stands. A device
// unpaired before is paired again.
func (d *Devices) addLocked(id devicekey.ID, as domain.Device, joined *joiner, by *devicekey.ID) (domain.Device, error) {
	name := as.Name
	r, err := d.refreshLocked()
	if err != nil {
		return domain.Device{}, err
	}
	self := d.self
	if id == self {
		return domain.Device{}, errOwnID
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
	added := as
	added.ID, added.AddedAt, added.AddedBy = id.String(), time.Now().UTC().Truncate(time.Second), by.String()
	if added.Role == domain.RoleBridge && joined != nil {
		if err := r.store.Bridge(context.Background(), r.signer, added, joined.key, joined.addrs); err != nil {
			return domain.Device{}, fmt.Errorf("daemon: change the device roster: %w", err)
		}
		return added, d.settleLocked(&r)
	}
	if err := d.changeLocked(r, []domain.Device{added}, nil); err != nil {
		return domain.Device{}, err
	}
	return added, nil
}

// Remove unpairs the device whose ID is idText and closes its live connections.
func (d *Devices) Remove(idText string) error {
	id, err := devicekey.ParseID(idText)
	if err != nil {
		return fmt.Errorf("daemon: unpair: %w", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	r, err := d.refreshLocked()
	if err != nil {
		return err
	}
	if id == d.self {
		return errOwnBridge
	}
	if !slices.ContainsFunc(d.paired, func(p pairedDevice) bool { return p.id == id }) {
		return errUnknownDevice
	}
	return d.changeLocked(r, nil, []string{id.String()})
}

// rosterRead is the roster as a refresh found it: the store, kithd's key to sign a
// change with, and the file as read, which a change renders over only if it still
// stands, with its entries.
type rosterRead struct {
	store   RosterStore
	signer  crypto.Signer
	file    []byte
	entries []pairedDevice
}

// errNotInMesh is a mesh store whose roster does not list this bridge's key.
var errNotInMesh = errors.New("this bridge's device key is not a bridge of its mesh store: the key changed since the store was made")

// errOutOfMesh is a mesh store whose roster lists this bridge as removed: another
// bridge unpaired it, and the removal arrived by sync.
var errOutOfMesh = errors.New("this bridge was removed from its mesh: it no longer listens, and syncs only to pass that on; `kith join --bridge` joins a mesh again")

// refreshLocked reads the roster: the first time, the file into the mesh store as its
// genesis; then a hand edit of the file into ops of this bridge's. It renders the file
// from the store when the two differ, and cuts off every device the roster no longer
// holds. What does not read or open changes nothing and is the error.
func (d *Devices) refreshLocked() (rosterRead, error) {
	cert, self, err := d.identityLocked()
	if err != nil {
		return rosterRead{}, err
	}
	signer, ok := cert.PrivateKey.(crypto.Signer)
	if !ok {
		return rosterRead{}, errors.New("daemon: kithd's device key cannot sign")
	}
	store, err := d.store()
	if err != nil {
		return rosterRead{}, fmt.Errorf("daemon: the mesh store: %w", err)
	}
	members, removed, err := store.Roster()
	if err != nil {
		return rosterRead{}, fmt.Errorf("daemon: the mesh store: %w", err)
	}
	file, body, err := readRoster(d.path)
	if err != nil {
		return rosterRead{}, err
	}
	r := rosterRead{store: store, signer: signer, file: body, entries: file}
	if len(members) == 0 {
		err = d.migrateLocked(&r, file)
		return r, err
	}
	if !slices.ContainsFunc(members, func(m domain.Device) bool { return m.ID == self.String() && m.Role == domain.RoleBridge }) {
		if slices.Contains(removed, self.String()) {
			return rosterRead{}, fmt.Errorf("daemon: %w", errOutOfMesh)
		}
		return rosterRead{}, fmt.Errorf("daemon: %w; move the mesh store (mesh-*.db beside %s) aside to make it again from that file", errNotInMesh, d.path)
	}
	// A file that is not there (deleted, or a store restored without it) is no edit: it
	// is written again from the store.
	var set []domain.Device
	var remove []string
	if body != nil {
		base := d.known
		if !d.refreshed { // since the start, nothing but a hand edit changed the file
			base = make([]pairedDevice, 0, len(members))
			for _, m := range members {
				base = append(base, pairedDevice{Device: m})
			}
		}
		set, remove = d.handEdits(file, base, members, removed)
	}
	if len(set)+len(remove) > 0 {
		if err = store.ChangeRoster(context.Background(), signer, set, remove); err != nil {
			return rosterRead{}, fmt.Errorf("daemon: read a hand edit of the device roster %s: %w", d.path, err)
		}
	}
	err = d.settleLocked(&r)
	return r, err
}

// migrateLocked makes the mesh store's genesis from the roster file, read once: this
// bridge as its first member, then every device the file lists. The file is kept as
// .pre-mesh first, so a migration cut off anywhere is made again, whole, from it.
func (d *Devices) migrateLocked(r *rosterRead, file []pairedDevice) error {
	if r.file != nil {
		if err := writeRoster(d.path+".pre-mesh", r.file); err != nil {
			return err
		}
	}
	self := d.self.String()
	set := []domain.Device{{ID: self, Role: domain.RoleBridge, AddedAt: time.Now().UTC().Truncate(time.Second), AddedBy: self}}
	for _, p := range file {
		if p.id != d.self { // this bridge is its own genesis
			p.Role = domain.RoleDevice
			set = append(set, p.Device)
		}
	}
	if err := r.store.ChangeRoster(context.Background(), r.signer, set, nil); err != nil {
		return fmt.Errorf("daemon: move the device roster %s into the mesh store: %w", d.path, err)
	}
	d.log.Info("the device roster is kept in the mesh store now", "roster", d.path, "devices", len(set)-1)
	return d.settleLocked(r)
}

// handEdits is what the file changes of the roster, as ops of this bridge's: an entry
// taken out is a removal, one put in an add, a name changed a rename. Removing wins: a
// device the roster has unpaired stays unpaired though the file lists it (a stale
// copy, or an editor open since), and is paired again only by `kith devices add`.
// The file is read against base, the entries kithd last wrote or read there, not
// against the roster: a change another bridge made, arriving by sync before kithd
// rendered it, is neither taken out nor renamed back by a file that never showed it.
func (d *Devices) handEdits(file, base []pairedDevice, members []domain.Device, removed []string) (set []domain.Device, remove []string) {
	self := d.self.String()
	find := func(list []pairedDevice, id string) (pairedDevice, bool) {
		i := slices.IndexFunc(list, func(p pairedDevice) bool { return p.ID == id })
		if i < 0 {
			return pairedDevice{}, false
		}
		return list[i], true
	}
	for _, b := range base {
		_, inFile := find(file, b.ID)
		if b.ID != self && !inFile && slices.ContainsFunc(members, func(m domain.Device) bool { return m.ID == b.ID }) {
			remove = append(remove, b.ID)
		}
	}
	var stale []string
	for _, p := range file {
		i := slices.IndexFunc(members, func(m domain.Device) bool { return m.ID == p.ID })
		was, inBase := find(base, p.ID)
		switch {
		case p.ID == self:
		case i >= 0 && (members[i].Name == p.Name || inBase && was.Name == p.Name):
		case i >= 0:
			renamed := members[i]
			renamed.Name = p.Name
			set = append(set, renamed)
		case inBase:
			// Unpaired by sync since kithd last wrote the file: not a hand edit.
		case slices.Contains(removed, p.ID):
			stale = append(stale, p.ID)
		default:
			added := p.Device
			added.Role = domain.RoleDevice
			if added.AddedAt.IsZero() {
				added.AddedAt = time.Now().UTC().Truncate(time.Second)
			}
			added.AddedBy = cmp.Or(added.AddedBy, self)
			set = append(set, added)
		}
	}
	if len(set)+len(remove) > 0 {
		d.log.Info("read a hand edit of the device roster", "roster", d.path, "set", len(set), "unpaired", remove)
	}
	if len(stale) > 0 {
		d.log.Warn("the device roster lists devices unpaired before: they stay unpaired; `kith devices add` pairs one again",
			"roster", d.path, "ids", stale)
	}
	return set, remove
}

// changeLocked writes set and the removals as one op of this bridge's, and settles.
func (d *Devices) changeLocked(r rosterRead, set []domain.Device, remove []string) error {
	if err := r.store.ChangeRoster(context.Background(), r.signer, set, remove); err != nil {
		return fmt.Errorf("daemon: change the device roster: %w", err)
	}
	return d.settleLocked(&r)
}

// settleLocked takes the store's roster as the devices served, cuts off every device it
// no longer holds, and renders the file from it, unless the file changed since r read
// it: that hand edit is read at the next change, listing or reload, not written over.
// What it renders is r's file from then on, so a change that follows renders over it;
// and the file kithd knows of, so a change arriving by sync does too (synced).
func (d *Devices) settleLocked(r *rosterRead) error {
	defer func() { d.refreshed, d.rendered, d.known = true, r.file, r.entries }()
	members, _, err := r.store.Roster()
	if err != nil {
		return fmt.Errorf("daemon: the mesh store: %w", err)
	}
	paired := make([]pairedDevice, 0, len(members))
	for _, m := range members {
		id, perr := devicekey.ParseID(m.ID)
		if perr != nil {
			return fmt.Errorf("daemon: the mesh store: %w", perr)
		}
		if id != d.self {
			paired = append(paired, pairedDevice{id: id, Device: m})
		}
	}
	d.paired = paired
	for id := range d.conns {
		if !slices.ContainsFunc(paired, func(p pairedDevice) bool { return p.id == id }) {
			d.cutLocked(id)
		}
	}
	body, err := encodeRoster(paired)
	if err != nil || bytes.Equal(body, r.file) {
		return err
	}
	now, err := os.ReadFile(d.path) // #nosec G304 -- the roster, a path of kithd's own
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("daemon: read the device roster: %w", err)
	}
	if !bytes.Equal(now, r.file) {
		return nil
	}
	if err := writeRoster(d.path, body); err != nil {
		return err
	}
	r.file, r.entries = body, paired
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
// to answer it and nothing else (pairingGate), its public key (key, PKIX) kept for a
// bridge's roster entry.
func (d *Devices) admit(id devicekey.ID, key []byte, c *remoteConn) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case c.gone:
		return false
	case slices.ContainsFunc(d.paired, func(p pairedDevice) bool { return p.id == id }):
		c.id, c.admitted = id, true
		d.trackLocked(c)
	case len(d.tickets) > 0:
		c.id, c.key, c.admitted, c.onlyPairs = id, key, true, true
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

// deviceOf is the paired device a request came from, as its ID; "" over the socket.
func (s *server) deviceOf(ctx context.Context) string {
	if id := s.Paired.peer(ctx); id != nil {
		return id.String()
	}
	return ""
}

// Listen listens on addrs from now on, stopping what is no longer named and starting
// what is newly named, once Serve runs; a reload calls it with the reloaded config's.
func (d *Devices) Listen(addrs []string) {
	d.lmu.Lock()
	defer d.lmu.Unlock()
	d.want = slices.Clone(addrs)
	d.reconcileLocked()
}

// serve starts the listeners with Serve's handler, and the mesh's loop, which dials the
// other bridges; base is every connection's context.
func (d *Devices) serve(handler http.Handler, base func(net.Listener) context.Context) {
	d.lmu.Lock()
	defer d.lmu.Unlock()
	d.handler, d.base = handler, base
	d.reconcileLocked()
	ctx, stop := context.WithCancel(base(nil))
	d.stopMesh = stop
	d.meshing.Go(func() { d.runMesh(ctx) })
}

// shut closes every listener and every remote connection, ends the mesh's loop and
// its streams, and waits for them all to return; Serve calls it on its way out.
func (d *Devices) shut() {
	d.lmu.Lock()
	d.handler = nil
	d.stopLocked(nil)
	if d.stopMesh != nil {
		d.stopMesh()
	}
	d.lmu.Unlock()
	d.serving.Wait()
	d.meshing.Wait()
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
	defer d.poke() // the addresses this bridge is dialed at may have changed
	d.stopLocked(d.want)
	if len(d.want) == 0 {
		return
	}
	d.mu.Lock()
	cert, _, kerr := d.identityLocked()
	_, rerr := d.refreshLocked()
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
			key, err := x509.MarshalPKIXPublicKey(state.PeerCertificates[0].PublicKey) // PeerID read it
			if err != nil {
				return fmt.Errorf("daemon: %w", err)
			}
			if !d.admit(id, key, c) {
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

// rosterEntry is a paired device as written. Its role is kithd's: one written by hand
// is read for what it is, a device, unless the roster already lists it.
type rosterEntry struct {
	Name    string            `toml:"name"`
	ID      string            `toml:"id"`
	Role    domain.DeviceRole `toml:"role,omitempty"`
	AddedAt time.Time         `toml:"added_at"`
	AddedBy string            `toml:"added_by"`
}

const rosterHeader = "# The devices paired with this kithd: each may do everything over [remote] listen.\n" +
	"# Kept in the mesh store and written here from it. Change it with `kith devices`; a\n" +
	"# hand edit is read back at the next change, listing or reload.\n\n"

// readRoster reads the roster file, and its bytes; none is no device paired, and no
// bytes. An entry whose ID or role does not read, a key the file does not have, or a
// device or name listed twice is an error.
func readRoster(path string) ([]pairedDevice, []byte, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- the roster, a path of kithd's own
	if errors.Is(err, os.ErrNotExist) {
		return []pairedDevice{}, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("daemon: read the device roster: %w", err)
	}
	var f rosterFile
	md, err := toml.Decode(string(body), &f)
	if err != nil {
		return nil, nil, fmt.Errorf("daemon: the device roster %s: %w", path, err)
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		return nil, nil, fmt.Errorf("daemon: the device roster %s: unknown key %s", path, extra[0])
	}
	out := make([]pairedDevice, 0, len(f.Device))
	for _, e := range f.Device {
		id, err := devicekey.ParseID(e.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("daemon: the device roster %s: %w", path, err)
		}
		switch e.Role {
		case "", domain.RoleDevice, domain.RoleBridge:
		default:
			return nil, nil, fmt.Errorf("daemon: the device roster %s: %s has role %q, not device or bridge", path, cmp.Or(e.Name, e.ID), e.Role)
		}
		for _, p := range out {
			if p.id == id || (e.Name != "" && p.Name == e.Name) {
				return nil, nil, fmt.Errorf("daemon: the device roster %s lists %s twice", path, cmp.Or(e.Name, e.ID))
			}
		}
		out = append(out, pairedDevice{id: id, Device: domain.Device{
			Name: e.Name, ID: id.String(), Role: cmp.Or(e.Role, domain.RoleDevice), AddedAt: e.AddedAt, AddedBy: e.AddedBy,
		}})
	}
	return out, body, nil
}

// encodeRoster is the roster file listing paired.
func encodeRoster(paired []pairedDevice) ([]byte, error) {
	var f rosterFile
	for _, p := range paired {
		f.Device = append(f.Device, rosterEntry{Name: p.Name, ID: p.ID, Role: p.Role, AddedAt: p.AddedAt, AddedBy: p.AddedBy})
	}
	var body bytes.Buffer
	body.WriteString(rosterHeader)
	if err := toml.NewEncoder(&body).Encode(f); err != nil {
		return nil, fmt.Errorf("daemon: encode the device roster: %w", err)
	}
	return body.Bytes(), nil
}

// writeRoster replaces the roster file at path (or its copy) with body atomically,
// owner-only.
func writeRoster(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("daemon: write the device roster: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".devices-*.toml")
	if err != nil {
		return fmt.Errorf("daemon: write the device roster: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // gone after the rename; a leftover on failure
	_, err = tmp.Write(body)
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
