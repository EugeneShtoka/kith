package main

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	"github.com/EugeneShtoka/kith/internal/clientstore"
	"github.com/EugeneShtoka/kith/internal/daemon"
)

// proxy serves this machine's clients: forwarded to the bridge while it answers, from
// the store while it does not (held). With no store (no keyring to seal one with) it
// only forwards.
type proxy struct {
	up    daemon.Upstream
	store *clientstore.Store
	// path is the store's file, deleted when the bridge refuses this device's key.
	path   string
	client backendv1connect.BackendServiceClient
	state  *state
	// gone is the store deleted: the proxy only forwards from then on.
	gone atomic.Bool
	log  *slog.Logger
}

func newProxy(ctx context.Context, up daemon.Upstream, store *clientstore.Store, path string, log *slog.Logger) *proxy {
	p := &proxy{
		up: up, store: store, path: path, log: log,
		client: backendv1connect.NewBackendServiceClient(up.Client, up.Scheme+"://kithd"),
	}
	// A store holding a profile answers at once, before the bridge has said a word: a
	// client starting offline is answered, and one starting online is switched over in
	// liveAfter.
	heldNow := false
	if store != nil {
		names, err := knownProfiles(ctx, store)
		heldNow = err == nil && len(names) > 0
	}
	p.state = newState(heldNow, time.Now())
	return p
}

// handler serves BackendService (daemon.HandleBackend): each call forwarded or held as
// the proxy stands as it arrives, and ended when it switches.
func (p *proxy) handler() http.Handler {
	forward := p.up.Forward(p.log)
	fromStore := http.NotFoundHandler()
	if p.store != nil {
		_, fromStore = backendv1connect.NewBackendServiceHandler(&held{store: p.store},
			connect.WithInterceptors(offline, offlineStreams{}))
	}
	mux := http.NewServeMux()
	daemon.HandleBackend(mux, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isHeld, turn := p.state.now()
		ctx, cancel := within(r.Context(), turn)
		defer cancel()
		r = r.WithContext(ctx)
		if isHeld && !p.gone.Load() {
			fromStore.ServeHTTP(w, r)
			return
		}
		forward.ServeHTTP(w, r)
	}))
	return mux
}

// run keeps the store current and the proxy held or live until ctx ends.
func (p *proxy) run(ctx context.Context) {
	if p.store == nil {
		<-ctx.Done()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go newSyncer(p.store, p.client, p.log).run(ctx)
	p.watch(ctx, cancel)
}

// watch asks the bridge whether it answers every probeEvery, and switches the proxy by
// hysteresis. A bridge refusing this device's key has the store deleted (decision 8):
// a machine unpaired keeps nothing it was unpaired from; stop then ends the syncer.
func (p *proxy) watch(ctx context.Context, stop context.CancelFunc) {
	var h hysteresis
	t := time.NewTicker(probeEvery)
	defer t.Stop()
	for {
		pctx, cancel := context.WithTimeout(ctx, probeWait)
		_, err := p.client.Status(pctx, connect.NewRequest(&v1.StatusRequest{}))
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil && connect.CodeOf(p.up.Failure(err)) == connect.CodePermissionDenied {
			p.refused(stop)
			return
		}
		isHeld, _ := p.state.now()
		now := time.Now()
		p.state.set(h.heard(err == nil, now, isHeld), now)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// refused deletes the store: the bridge refused this device's key.
func (p *proxy) refused(stop context.CancelFunc) {
	p.gone.Store(true)
	p.state.set(false, time.Now())
	stop()
	if err := p.store.Close(); err != nil {
		p.log.Warn("closing the store", "err", err)
	}
	if err := clientstore.Remove(p.path); err != nil {
		p.log.Warn("deleting the store of a bridge that refused this device", "err", err)
		return
	}
	p.log.Info("the bridge refused this device's key: its store is deleted")
}
