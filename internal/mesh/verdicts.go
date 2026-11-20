package mesh

import (
	"bytes"
	"context"
	"crypto"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
)

// Verdicts of the networks that keep none of their own cross bridges as registers
// (crdt.TagVerdictKey, crdt.PersonVerdictKey), each key's concurrent values read as the
// one that wins (domain.TagVerdict.Beats): a release over a move, else the later. A
// value is written only over one it wins over, so no write undoes a release it raced.

// verdictValue is a verdict as its key holds it.
type verdictValue struct {
	ID       string `json:"id"`
	Tag      string `json:"tag"`
	Rule     int    `json:"rule,omitempty"`
	Filter   string `json:"filter,omitempty"`
	At       int64  `json:"at"` // Unix ms
	Released bool   `json:"released,omitempty"`
}

// verdictTag is a tag as verdict keys name it: lower-cased, as the cache keys it.
func verdictTag(tag string) string { return strings.ToLower(strings.TrimSpace(tag)) }

func tagValue(v domain.TagVerdict) verdictValue {
	return verdictValue{ID: string(v.Room), Tag: verdictTag(v.Tag), Rule: int(v.Rule), Filter: v.Filter, At: v.At.UnixMilli(), Released: v.Released}
}

func personValue(v domain.PersonVerdict) verdictValue {
	return verdictValue{ID: v.Person, Tag: verdictTag(v.Tag), Rule: int(v.Rule), Filter: v.Filter, At: v.At.UnixMilli(), Released: v.Released}
}

// tagVerdict is v as a room's verdict; ordering any verdict (Beats, Same) reads it so.
func (v verdictValue) tagVerdict() domain.TagVerdict {
	return domain.TagVerdict{
		Room: domain.RoomID(v.ID), Tag: v.Tag, Rule: domain.MoveRule(v.Rule), Filter: v.Filter,
		At: time.UnixMilli(v.At), Released: v.Released,
	}
}

func (v verdictValue) personVerdict() domain.PersonVerdict {
	return domain.PersonVerdict{
		Person: v.ID, Tag: v.Tag, Rule: domain.MoveRule(v.Rule), Filter: v.Filter,
		At: time.UnixMilli(v.At), Released: v.Released,
	}
}

// write is v as its key's write.
func (v verdictValue) write(person bool) (crdt.Write, error) {
	if v.Released {
		v.Rule, v.Filter = 0, ""
	}
	value, err := json.Marshal(v)
	if err != nil {
		return crdt.Write{}, fmt.Errorf("mesh: encode a verdict: %w", err)
	}
	if person {
		return crdt.Write{Key: crdt.PersonVerdictKey(v.ID, v.Tag), Value: value}, nil
	}
	return crdt.Write{Key: crdt.TagVerdictKey(v.ID, v.Tag), Value: value}, nil
}

// heldVerdict is the value a verdict key's entries read as, whether it is a person's,
// and whether it is a verdict's: of the values, the one that wins over every other in
// dot order, as every replica reads it.
func heldVerdict(key string, entries []crdt.Entry) (best verdictValue, person, ok bool) {
	for _, e := range entries {
		var v verdictValue
		_, _, p, place := crdt.VerdictPlace(key, e.Value)
		if !place || json.Unmarshal(e.Value, &v) != nil {
			continue
		}
		if !ok || v.tagVerdict().Beats(best.tagVerdict()) {
			best, person, ok = v, p, true
		}
	}
	return best, person, ok
}

// heldRetag is the tag a retag key's newest value (by clock, then origin) names.
func heldRetag(key string, entries []crdt.Entry) (from, to string, ok bool) {
	var newest crdt.Entry
	for _, e := range entries {
		if !ok || e.Clock > newest.Clock || e.Clock == newest.Clock && bytes.Compare(e.Dot.Origin[:], newest.Dot.Origin[:]) > 0 {
			newest, ok = e, true
		}
	}
	if !ok {
		return "", "", false
	}
	return crdt.RetagOf(key, newest.Value)
}

// Verdicts is every verdict the mesh holds, each key's value as it reads, and every
// retag. A verdict of a room the mesh holds as merged into another is left out: it
// moves under the room it joined (PublishAliases).
func (s *Store) Verdicts() domain.MeshVerdicts {
	s.mu.Lock()
	defer s.mu.Unlock()
	merged := s.state.Aliases()
	held := s.state.Verdicts()
	out := domain.MeshVerdicts{Retags: map[string]string{}}
	for _, key := range slices.Sorted(maps.Keys(held)) {
		if from, to, ok := heldRetag(key, held[key]); ok {
			out.Retags[from] = to
			continue
		}
		v, person, ok := heldVerdict(key, held[key])
		switch _, gone := merged[v.ID]; {
		case !ok:
		case person:
			out.People = append(out.People, v.personVerdict())
		case !gone:
			out.Tags = append(out.Tags, v.tagVerdict())
		}
	}
	return out
}

// WriteVerdicts writes each verdict of mv that wins over the value the mesh holds of
// it (or that it holds none of), and each retag it holds otherwise, as this bridge's
// ops signed by signer. The check is made under the store's lock, so a value that
// arrived since mv was made is not written over by one it wins over. A cleared verdict
// (neither a move nor a release) deletes what the mesh holds of it: a value written
// concurrently elsewhere survives the deletion.
func (s *Store) WriteVerdicts(ctx context.Context, signer crypto.Signer, mv domain.MeshVerdicts) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	origin, err := devicekey.IDOf(signer.Public())
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	held := s.state.Verdicts()
	var writes []crdt.Write
	add := func(v verdictValue, person bool) error {
		w, err := v.write(person)
		if err != nil {
			return err
		}
		if !v.Released && v.Rule == int(domain.MoveNone) {
			if len(held[w.Key]) > 0 {
				writes = append(writes, crdt.Write{Key: w.Key})
			}
			return nil
		}
		if h, _, ok := heldVerdict(w.Key, held[w.Key]); ok && (h.tagVerdict().Same(v.tagVerdict()) || !v.tagVerdict().Beats(h.tagVerdict())) {
			return nil
		}
		writes = append(writes, w)
		return nil
	}
	for _, v := range mv.Tags {
		if err := add(tagValue(v), false); err != nil {
			return err
		}
	}
	for _, v := range mv.People {
		if err := add(personValue(v), true); err != nil {
			return err
		}
	}
	for _, from := range slices.Sorted(maps.Keys(mv.Retags)) {
		key := crdt.RetagKey(verdictTag(from))
		if _, to, ok := heldRetag(key, held[key]); ok && to == mv.Retags[from] && len(held[key]) == 1 {
			continue
		}
		value, err := json.Marshal(struct {
			To string `json:"to"`
		}{mv.Retags[from]})
		if err != nil {
			return fmt.Errorf("mesh: encode a retag: %w", err)
		}
		writes = append(writes, crdt.Write{Key: key, Value: value})
	}
	return s.writeAllLocked(ctx, signer, origin, writes)
}

// verdictMoves are the writes that move the verdicts state holds of each room merged
// into another under the room it joined: each one there unless that room's own wins
// over it, and the merged room's deleted. A room merged into a room itself merged is
// left, as FoldWrites leaves its reads.
func verdictMoves(state crdt.State, aliases map[string]string) ([]crdt.Write, error) {
	held := state.Verdicts()
	var writes []crdt.Write
	moved := map[string]verdictValue{}
	for _, key := range slices.Sorted(maps.Keys(held)) {
		v, person, ok := heldVerdict(key, held[key])
		into, merged := aliases[v.ID]
		if _, chained := aliases[into]; !ok || person || !merged || chained {
			continue
		}
		writes = append(writes, crdt.Write{Key: key})
		v.ID = into
		to := crdt.TagVerdictKey(into, v.Tag)
		best, ok := moved[to]
		if !ok {
			best, _, ok = heldVerdict(to, held[to])
		}
		if !ok || v.tagVerdict().Beats(best.tagVerdict()) {
			moved[to] = v
		}
	}
	for _, to := range slices.Sorted(maps.Keys(moved)) {
		w, err := moved[to].write(false)
		if err != nil {
			return nil, err
		}
		writes = append(writes, w)
	}
	return writes, nil
}
