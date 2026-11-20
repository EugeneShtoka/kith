package daemon

import (
	"context"
	"crypto"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// verdictsRecheck is how often followVerdicts looks with nothing to wake it: a filter's
// move made here reaches the mesh by then.
const verdictsRecheck = time.Minute

// verdictKeeper is a backend whose cache keeps the verdicts of the networks with nowhere
// else to keep them (local.Service): those it keeps, and those another bridge made,
// kept where they win over its own.
type verdictKeeper interface {
	KeptVerdicts(ctx context.Context) (domain.MeshVerdicts, error)
	KeepVerdicts(ctx context.Context, mv domain.MeshVerdicts) (bool, error)
}

// pokeVerdicts wakes followVerdicts: a verdict was made here.
func (s *server) pokeVerdicts() {
	select {
	case s.verdicts <- struct{}{}:
	default:
	}
}

// retagInMesh writes into the mesh that tag from became to ("" deleted): each bridge
// whose config no longer has from moves what the mesh holds under it, a verdict made
// concurrently on another bridge among them (followVerdicts). A failure is logged; the
// retag stands here.
func (s *server) retagInMesh(ctx context.Context, from, to string) {
	if domain.SameTag(from, to) {
		return
	}
	store, _, _, signer, err := s.Paired.meshParts()
	if err != nil {
		return
	}
	retag := domain.MeshVerdicts{Retags: map[string]string{from: to}}
	if err := store.WriteVerdicts(context.WithoutCancel(ctx), signer, retag); err != nil {
		s.Log.Warn("write a retag into the mesh", "err", err)
	}
	s.pokeVerdicts()
}

// followVerdicts keeps the verdicts of the networks with nowhere else to keep them
// crossing the mesh: at start, after every change to the mesh or the rooms (a room of an
// account logged in later takes its verdicts then), a verdict made here, and every
// verdictsRecheck.
func (s *server) followVerdicts(ctx context.Context) {
	keeper, ok := s.Backend.(verdictKeeper)
	if !ok {
		return
	}
	id, changes := s.Streams.changes.watchWith(subBuffer)
	defer s.Streams.changes.release(id)
	for {
		var meshChanged <-chan struct{}
		if store, _, _, signer, err := s.Paired.meshParts(); err == nil {
			meshChanged = store.Changed() // before reading, so nothing committed after is missed
			s.syncVerdicts(ctx, keeper, store, signer)
		}
		timer := time.NewTimer(verdictsRecheck)
		for waiting := true; waiting; {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case c, ok := <-changes:
				if !ok {
					changes = nil
				}
				waiting = ok && c.what != domain.ChangeRooms
			case <-meshChanged:
				waiting = false
			case <-s.verdicts:
				waiting = false
			case <-timer.C:
				waiting = false
			}
		}
		timer.Stop()
	}
}

// syncVerdicts moves under its new name what the mesh and the cache hold under each tag
// renamed or deleted that the mesh's config no longer has (foldRetags), then offers
// each side the other's verdicts, which it keeps where they win (passVerdicts).
func (s *server) syncVerdicts(ctx context.Context, keeper verdictKeeper, store RosterStore, signer crypto.Signer) {
	held := store.Verdicts()
	kept, err := keeper.KeptVerdicts(ctx)
	if err != nil {
		s.Log.Warn("read the verdicts to pass to the mesh", "err", err)
		return
	}
	changed := false
	if len(held.Retags) > 0 {
		if held, kept, changed, err = s.foldRetags(ctx, keeper, store, signer, held, kept); err != nil {
			s.Log.Warn("move the verdicts of a renamed tag", "err", err)
			return
		}
	}
	toMesh, toCache := passVerdicts(held, kept, store.Aliases())
	if werr := store.WriteVerdicts(ctx, signer, toMesh); werr != nil {
		s.Log.Warn("write verdicts into the mesh", "err", werr)
	}
	took, err := keeper.KeepVerdicts(ctx, toCache)
	if err != nil {
		s.Log.Warn("keep the verdicts made on other bridges", "err", err)
	}
	if changed || took {
		s.Notifications.TagMarked(ctx)
	}
}

// foldRetags moves, for each retag whose tag the mesh's config no longer has, what the
// mesh holds under it and what the cache holds (domain.MeshVerdicts.Retag): one written
// on another bridge before it saw the retag among them, which survives the retag's
// deletion. A config renaming the tag back, or making it anew, holds it, which ends the
// retag. It is the verdicts as they then read, and whether the cache's changed.
func (s *server) foldRetags(ctx context.Context, keeper verdictKeeper, store RosterStore, signer crypto.Signer,
	held, kept domain.MeshVerdicts,
) (domain.MeshVerdicts, domain.MeshVerdicts, bool, error) {
	tags, ok := s.meshTags(store)
	if !ok {
		return held, kept, false, nil
	}
	changed := false
	for _, from := range slices.Sorted(maps.Keys(held.Retags)) {
		to := held.Retags[from]
		if domain.SameTag(from, to) || slices.ContainsFunc(tags, func(t config.Tag) bool { return domain.SameTag(t.Name, from) }) {
			continue
		}
		if err := store.WriteVerdicts(ctx, signer, held.Retag(from, to, time.Now())); err != nil {
			return held, kept, changed, fmt.Errorf("daemon: %w", err)
		}
		took, err := keeper.KeepVerdicts(ctx, kept.Retag(from, to, time.Now()))
		if err != nil {
			return held, kept, changed, fmt.Errorf("daemon: %w", err)
		}
		changed = changed || took
		if held = store.Verdicts(); took {
			if kept, err = keeper.KeptVerdicts(ctx); err != nil {
				return held, kept, changed, fmt.Errorf("daemon: %w", err)
			}
		}
	}
	return held, kept, changed, nil
}

// meshTags is the tags of the config the mesh holds (config.toml's before the config
// first crossed): every bridge holding a retag holds the config that dropped its tag.
func (s *server) meshTags(store RosterStore) ([]config.Tag, bool) {
	if mesh, _ := store.Config(); mesh["tag"] != nil {
		cfg, err := config.FromLeaves(map[string][]byte{"tag": mesh["tag"]})
		return cfg.Tags, err == nil
	}
	if s.Config == nil {
		return nil, false
	}
	snap, err := s.Config.Read()
	return snap.Config.Tags, err == nil
}

// passVerdicts is what of the mesh's verdicts (held) and the cache's (kept) each is
// offered of the other's: each the other holds none of, or another of. Each side keeps
// only what wins over its own (mesh.Store.WriteVerdicts, db.Cache.KeepVerdicts),
// checked as it writes. A room's the mesh holds as merged into another stays where it
// is: the mesh moves its own (mesh.Store.PublishAliases), and the cache its own as the
// room's network merges it.
func passVerdicts(held, kept domain.MeshVerdicts, merged map[domain.RoomID]domain.RoomID) (toMesh, toCache domain.MeshVerdicts) {
	tags := slices.DeleteFunc(slices.Clone(kept.Tags), func(v domain.TagVerdict) bool {
		_, gone := merged[v.Room]
		return gone
	})
	toMesh.Tags, toCache.Tags = differing(held.Tags, tags,
		func(v domain.TagVerdict) [2]string { return [2]string{string(v.Room), strings.ToLower(v.Tag)} }, domain.TagVerdict.Same)
	toMesh.People, toCache.People = differing(held.People, kept.People,
		func(v domain.PersonVerdict) [2]string { return [2]string{v.Person, strings.ToLower(v.Tag)} }, domain.PersonVerdict.Same)
	return toMesh, toCache
}

// differing is what of held and kept, verdicts by key, the other holds none of or
// another of.
func differing[V any](held, kept []V, key func(V) [2]string, same func(V, V) bool) (fromKept, fromHeld []V) {
	byKey := func(list []V) map[[2]string]V {
		out := make(map[[2]string]V, len(list))
		for _, v := range list {
			out[key(v)] = v
		}
		return out
	}
	h, k := byKey(held), byKey(kept)
	for _, c := range kept {
		if m, ok := h[key(c)]; !ok || !same(c, m) {
			fromKept = append(fromKept, c)
		}
	}
	for _, m := range held {
		if c, ok := k[key(m)]; !ok || !same(c, m) {
			fromHeld = append(fromHeld, m)
		}
	}
	return fromKept, fromHeld
}
