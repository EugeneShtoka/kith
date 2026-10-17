//go:build signal

// Package meow is the helper's Network over mautrix-signal's signalmeow, whose crypto
// is libsignal through cgo: only kith-signal built with the signal tag links it.
package meow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/store"
	"go.mau.fi/util/dbutil"

	"github.com/EugeneShtoka/kith/internal/logging"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
	"github.com/EugeneShtoka/kith/internal/signalhelper"
)

// Network is Signal through signalmeow, its key store encrypted at path.
type Network struct {
	path string
	log  *slog.Logger

	mu    sync.Mutex
	db    *sql.DB
	store *store.Container
}

// New returns the Network whose key store is at path; Open opens it.
func New(path string, log *slog.Logger) *Network {
	return &Network{path: path, log: log}
}

// zerolog is signalmeow's logger: warnings and up, whatever kith's level, as messagix's.
func (n *Network) zerolog() zerolog.Logger {
	return logging.Zerolog(n.log.With("component", "signalmeow"), zerolog.WarnLevel)
}

// Open opens the key store, creating and upgrading it, and lists its linked devices.
func (n *Network) Open(ctx context.Context, key []byte) ([]signalhelper.Device, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.store != nil {
		return nil, errors.New("signal: the key store is open already")
	}
	db, err := sealeddb.Open(ctx, n.path, key)
	if err != nil {
		return nil, fmt.Errorf("signal: open the key store: %w", err)
	}
	wrapped, err := dbutil.NewWithDB(db, sealeddb.Dialect)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("signal: open the key store: %w", err)
	}
	st := store.NewStore(wrapped, dbutil.ZeroLogger(n.zerolog()))
	if err = st.Upgrade(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("signal: migrate the key store: %w", err)
	}
	devices, err := st.GetAllDevices(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("signal: list linked devices: %w", err)
	}
	n.db, n.store = db, st
	out := make([]signalhelper.Device, 0, len(devices))
	for _, d := range devices {
		out = append(out, signalhelper.Device{ACI: d.ACI.String(), Number: d.Number})
	}
	return out, nil
}

// maxCodes bounds the links drawn for one Link: each lapses after about a minute
// unscanned, and a failure that is no lapse (no network) would otherwise draw forever.
const maxCodes = 10

// Link draws a link to scan, a new one each time the last lapses, until the phone
// scans one. It always asks for the history transfer: the phone offers it only now.
func (n *Network) Link(ctx context.Context, name string, show func(url string) error) (signalhelper.Device, error) {
	n.mu.Lock()
	st := n.store
	n.mu.Unlock()
	if st == nil {
		return signalhelper.Device{}, errors.New("signal: the key store is not open")
	}
	var err error
	for range maxCodes {
		var device signalhelper.Device
		if device, err = linkOnce(ctx, st, name, show); err == nil {
			return device, nil
		}
		if ctx.Err() != nil || errors.Is(err, errShow) {
			return signalhelper.Device{}, err
		}
		n.log.Info("the link lapsed; drawing a new one", "err", err)
	}
	return signalhelper.Device{}, fmt.Errorf("signal: not linked after %d links: %w", maxCodes, err)
}

// errShow marks a link that could not be handed on: kithd is gone, so no new one.
var errShow = errors.New("signal: show the link")

func linkOnce(ctx context.Context, st *store.Container, name string, show func(url string) error) (signalhelper.Device, error) {
	// Ended early (lapse, cancel), the provisioning goroutine still sends its last step.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for resp := range signalmeow.PerformProvisioning(ctx, st, name, true) {
		switch resp.State {
		case signalmeow.StateProvisioningURLReceived:
			if err := show(resp.ProvisioningURL); err != nil {
				return signalhelper.Device{}, fmt.Errorf("%w: %w", errShow, err)
			}
		case signalmeow.StateProvisioningDataReceived:
			data := resp.ProvisioningData
			return signalhelper.Device{ACI: data.ACI.String(), Number: data.Number}, nil
		case signalmeow.StateProvisioningError:
			return signalhelper.Device{}, fmt.Errorf("signal: linking: %w", resp.Err)
		}
	}
	return signalhelper.Device{}, errors.New("signal: linking ended without a device")
}
