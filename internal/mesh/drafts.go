package mesh

import (
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

// Drafts cross bridges as registers keeping every concurrent version (crdt.DraftKey,
// domain.FoldDraft). The cache keeps the draft every window and kith-mcp writes; the
// store keeps, per place, what the cache last took from the mesh or wrote into it
// (drafts_taken), so a save made here is written over the versions it was made over,
// a version arriving is taken without undoing a save not yet passed on, and a draft the
// cache lost (rebuilt) comes back from the mesh.

// maxDraft bounds a draft version's key and value, leaving room in the op for its
// other fields and this bridge's entry, which an op may carry.
const maxDraft = crdt.MaxOpSize - 64<<10

// draftValue is a draft version as its key holds it.
type draftValue struct {
	Root      string           `json:"root,omitempty"`
	Over      string           `json:"over,omitempty"`
	Body      string           `json:"body,omitempty"`
	Caret     int              `json:"caret,omitempty"`
	Mentions  []domain.Mention `json:"mentions,omitempty"`
	ReplyTo   string           `json:"reply_to,omitempty"`
	Editing   string           `json:"editing,omitempty"`
	EditSaved string           `json:"edit_saved,omitempty"`
	Author    string           `json:"author,omitempty"`
	Updated   int64            `json:"updated,omitempty"` // Unix ms
}

// encodeDraft is d, written over the words over, as its key's value.
func encodeDraft(d domain.StoredDraft, over string) ([]byte, error) {
	v := draftValue{
		Root: string(d.ThreadRoot), Over: over, Body: d.Body, Caret: d.Caret, Mentions: d.Mentions,
		ReplyTo: string(d.ReplyTo), Editing: string(d.Editing), EditSaved: d.EditSaved, Author: d.Author,
	}
	if !d.Updated.IsZero() {
		v.Updated = d.Updated.UnixMilli()
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("mesh: encode a draft: %w", err)
	}
	return out, nil
}

// decodeDraft reads a draft key's value: the version, its place set from key.
func decodeDraft(key string, value []byte) (domain.DraftVersion, bool) {
	room, root, ok := crdt.DraftPlace(key, value)
	var v draftValue
	if !ok || json.Unmarshal(value, &v) != nil {
		return domain.DraftVersion{}, false
	}
	d := domain.StoredDraft{
		RoomID: domain.RoomID(room), ThreadRoot: domain.EventID(root), Body: v.Body, Caret: v.Caret, Mentions: v.Mentions,
		ReplyTo: domain.EventID(v.ReplyTo), Editing: domain.EventID(v.Editing), EditSaved: v.EditSaved, Author: v.Author,
	}
	if v.Updated != 0 {
		d.Updated = time.UnixMilli(v.Updated)
	}
	return domain.DraftVersion{Draft: d, Over: v.Over}, true
}

// readDraft is the draft a key's versions (oldest first) read as, in its place.
func readDraft(room domain.RoomID, root domain.EventID, key string, entries []crdt.Entry) domain.StoredDraft {
	var versions []domain.DraftVersion
	for _, e := range entries {
		if v, ok := decodeDraft(key, e.Value); ok {
			versions = append(versions, v)
		}
	}
	d := domain.FoldDraft(versions)
	d.RoomID, d.ThreadRoot = room, root
	return d
}

// seenOf names a key's versions: their dots.
func seenOf(entries []crdt.Entry) string {
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%s:%d,", e.Dot.Origin, e.Dot.Seq)
	}
	return b.String()
}

// taken is what the cache last took of a place's draft (drafts_taken).
type taken struct {
	room  domain.RoomID
	root  domain.EventID
	draft domain.StoredDraft
	seen  string
	owed  bool
}

// draftKey is the key of d's place.
func draftKey(room domain.RoomID, root domain.EventID) string {
	return crdt.DraftKey(string(room), string(root))
}

// Drafts is every place's draft the mesh holds or the cache took, each with what the
// cache last took of it. A place of a room the mesh holds as merged into another is
// left out: its versions move under the room it joined (PublishAliases).
func (s *Store) Drafts() []domain.MeshDraft {
	s.mu.Lock()
	defer s.mu.Unlock()
	merged := s.state.Aliases()
	versions := s.state.Drafts()
	var out []domain.MeshDraft
	add := func(key string, room domain.RoomID, root domain.EventID) {
		if _, gone := merged[string(room)]; gone {
			return
		}
		t := s.taken[key]
		entries := versions[key]
		out = append(out, domain.MeshDraft{
			Room: room, Root: root, Draft: readDraft(room, root, key, entries),
			Taken: t.draft, Seen: seenOf(entries), Moved: seenOf(entries) != t.seen, Owed: t.owed, Saves: s.saves[key],
		})
	}
	for _, key := range slices.Sorted(maps.Keys(versions)) {
		if v, ok := decodeDraft(key, versions[key][0].Value); ok {
			add(key, v.Draft.RoomID, v.Draft.ThreadRoot)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(s.taken)) {
		if _, held := versions[key]; !held {
			add(key, s.taken[key].room, s.taken[key].root)
		}
	}
	return out
}

// OweDraft records that a save here is about to change the cache's draft of a place,
// before it does: the store then owes the mesh a write of it, however kithd stops.
func (s *Store) OweDraft(ctx context.Context, room domain.RoomID, root domain.EventID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := draftKey(room, root)
	s.saves[key]++
	t, ok := s.taken[key]
	if !ok {
		t = taken{room: room, root: root, draft: domain.StoredDraft{RoomID: room, ThreadRoot: root}}
	}
	if t.owed && ok {
		return nil
	}
	t.owed = true
	return s.keepTakenLocked(ctx, key, t)
}

// SavedDraft records that a save here changed the cache's draft of a place: a write of
// it begun before reads it again.
func (s *Store) SavedDraft(room domain.RoomID, root domain.EventID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves[draftKey(room, root)]++
}

// TookDraft records that the cache took d.Draft, the versions d.Seen names, as Drafts
// read them; owed is set when the cache holds words of its own besides, which the mesh
// is owed.
func (s *Store) TookDraft(ctx context.Context, d domain.MeshDraft, owed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := draftKey(d.Room, d.Root)
	return s.keepTakenLocked(ctx, key, taken{room: d.Room, root: d.Root, draft: d.Draft, seen: d.Seen, owed: owed || s.taken[key].owed})
}

// WriteDraft writes current, the cache's draft of d's place, into the mesh as this
// bridge's version (signed by signer), written over what the cache last took: the
// versions it took are replaced, and a cleared draft is kept as a version that clears
// (crdt.DraftKey). It writes nothing, and reports false, while the mesh holds versions
// the cache has not taken: they are taken first. The place stays owed when a save was
// made after d was read (d.Saves). A draft too large for an op is
// domain.ErrDraftTooLarge.
func (s *Store) WriteDraft(ctx context.Context, signer crypto.Signer, d domain.MeshDraft, current domain.StoredDraft) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	origin, err := devicekey.IDOf(signer.Public())
	if err != nil {
		return false, fmt.Errorf("mesh: %w", err)
	}
	key := draftKey(d.Room, d.Root)
	t, ok := s.taken[key]
	entries := s.state.Drafts()[key]
	if ok && seenOf(entries) != t.seen || !ok && len(entries) > 0 {
		return false, nil
	}
	current.RoomID, current.ThreadRoot = d.Room, d.Root
	owed := s.saves[key] != d.Saves
	if domain.SameDraft(current, t.draft) || current.Empty() && len(entries) == 0 {
		return true, s.keepTakenLocked(ctx, key, taken{room: d.Room, root: d.Root, draft: current, seen: t.seen, owed: owed})
	}
	value, err := encodeDraft(current, domain.DraftWords(t.draft))
	if err != nil {
		return false, err
	}
	if len(key)+len(value) > maxDraft {
		return false, domain.ErrDraftTooLarge
	}
	if err := s.writeLocked(ctx, signer, origin, []crdt.Write{{Key: key, Value: value}}); err != nil {
		return false, err
	}
	return true, s.keepTakenLocked(ctx, key, taken{room: d.Room, root: d.Root, draft: current, seen: seenOf(s.state.Drafts()[key]), owed: owed})
}

// keepTakenLocked keeps t as what the cache last took of key's place.
func (s *Store) keepTakenLocked(ctx context.Context, key string, t taken) error {
	value, err := encodeDraft(t.draft, "")
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO drafts_taken (key, taken, seen, owed) VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET taken = excluded.taken, seen = excluded.seen, owed = excluded.owed`,
		key, value, t.seen, t.owed); err != nil {
		return fmt.Errorf("mesh: keep what the cache took of a draft: %w", err)
	}
	s.taken[key] = t
	return nil
}

// loadTaken reads drafts_taken.
func (s *Store) loadTaken(ctx context.Context) error {
	s.taken, s.saves = map[string]taken{}, map[string]uint64{}
	rows, err := s.db.QueryContext(ctx, `SELECT key, taken, seen, owed FROM drafts_taken`)
	if err != nil {
		return fmt.Errorf("read what the cache took of drafts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key, seen string
		var value []byte
		var owed bool
		if err := rows.Scan(&key, &value, &seen, &owed); err != nil {
			return fmt.Errorf("read what the cache took of drafts: %w", err)
		}
		if v, ok := decodeDraft(key, value); ok {
			s.taken[key] = taken{room: v.Draft.RoomID, root: v.Draft.ThreadRoot, draft: v.Draft, seen: seen, owed: owed}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read what the cache took of drafts: %w", err)
	}
	return nil
}

// draftsSchema is what layout 4 adds: per place, the draft the cache last took from
// the mesh or wrote into it, the versions the mesh held then, and whether a save here
// changed the cache's since.
const draftsSchema = `
CREATE TABLE drafts_taken (
	key   TEXT PRIMARY KEY,
	taken BLOB NOT NULL,
	seen  TEXT NOT NULL,
	owed  INTEGER NOT NULL
) WITHOUT ROWID;
`

// draftMoves are the writes that move the drafts state holds of each room merged into
// another under the room it joined: one version there over both (the joined room's
// words, then the merged room's added), and the merged room's deleted. A room merged
// into a room itself merged is left, as FoldWrites leaves its reads.
func draftMoves(state crdt.State, aliases map[string]string) ([]crdt.Write, error) {
	versions := state.Drafts()
	var writes []crdt.Write
	moved := map[string]domain.StoredDraft{}
	for _, key := range slices.Sorted(maps.Keys(versions)) {
		v, ok := decodeDraft(key, versions[key][0].Value)
		into, merged := aliases[string(v.Draft.RoomID)]
		if _, chained := aliases[into]; !ok || !merged || chained {
			continue
		}
		writes = append(writes, crdt.Write{Key: key})
		from := readDraft(v.Draft.RoomID, v.Draft.ThreadRoot, key, versions[key])
		to := draftKey(domain.RoomID(into), v.Draft.ThreadRoot)
		held, ok := moved[to]
		if !ok {
			held = readDraft(domain.RoomID(into), v.Draft.ThreadRoot, to, versions[to])
		}
		moved[to] = domain.TakeDraft(held, domain.StoredDraft{RoomID: held.RoomID, ThreadRoot: held.ThreadRoot}, withPlace(from, held))
	}
	for _, to := range slices.Sorted(maps.Keys(moved)) {
		d := moved[to]
		over := readDraft(d.RoomID, d.ThreadRoot, to, versions[to])
		if d.Empty() || domain.SameDraft(d, over) {
			continue
		}
		value, err := encodeDraft(d, domain.DraftWords(over))
		if err != nil {
			return nil, err
		}
		writes = append(writes, crdt.Write{Key: to, Value: value})
	}
	return writes, nil
}

// adoptDrafts are the writes that carry into next, a mesh being joined, the drafts old
// (the joining bridge's own state) held: each place's, beside the mesh's words there.
// One that would be too large for an op is left out, its place in kept: the cache
// keeps it (keepOwnLocked).
func adoptDrafts(next, old crdt.State) (writes []crdt.Write, kept []domain.StoredDraft, err error) {
	theirs := next.Drafts()
	for key, entries := range old.Drafts() {
		v, ok := decodeDraft(key, entries[0].Value)
		if !ok {
			continue
		}
		mine := readDraft(v.Draft.RoomID, v.Draft.ThreadRoot, key, entries)
		held := readDraft(v.Draft.RoomID, v.Draft.ThreadRoot, key, theirs[key])
		joined := domain.TakeDraft(mine, domain.StoredDraft{RoomID: mine.RoomID, ThreadRoot: mine.ThreadRoot}, held)
		if joined.Empty() || domain.SameDraft(joined, held) {
			continue
		}
		value, err := encodeDraft(joined, domain.DraftWords(held))
		if err != nil {
			return nil, nil, err
		}
		if len(key)+len(value) > maxDraft {
			kept = append(kept, mine)
			continue
		}
		writes = append(writes, crdt.Write{Key: key, Value: value})
	}
	slices.SortFunc(writes, func(a, b crdt.Write) int { return strings.Compare(a.Key, b.Key) })
	return writes, kept, nil
}

// ownWritesLocked are the writes that carry into next, a mesh being joined, what this
// store holds of reads and drafts (crdt.State.AdoptWrites, adoptDrafts). A draft too
// large for an op is kept by the cache instead, beside the mesh's (keepOwnLocked),
// which harms nothing if the join then fails.
func (s *Store) ownWritesLocked(ctx context.Context, next crdt.State) ([]crdt.Write, error) {
	reads, err := next.AdoptWrites(s.state)
	if err != nil {
		return nil, fmt.Errorf("mesh: %w", err)
	}
	drafts, kept, err := adoptDrafts(next, s.state)
	if err != nil {
		return nil, err
	}
	return slices.Concat(reads, drafts), s.keepOwnLocked(ctx, kept)
}

// keepOwnLocked has the cache keep its own draft of each of places beside the mesh's,
// as if it had taken none: the mesh holds none of its words.
func (s *Store) keepOwnLocked(ctx context.Context, places []domain.StoredDraft) error {
	for i := range places {
		d := &places[i]
		place := domain.StoredDraft{RoomID: d.RoomID, ThreadRoot: d.ThreadRoot}
		t := taken{room: d.RoomID, root: d.ThreadRoot, draft: place, owed: true}
		if err := s.keepTakenLocked(ctx, draftKey(d.RoomID, d.ThreadRoot), t); err != nil {
			return err
		}
	}
	return nil
}

// withPlace is d in place's room and thread.
func withPlace(d, place domain.StoredDraft) domain.StoredDraft {
	d.RoomID, d.ThreadRoot = place.RoomID, place.ThreadRoot
	return d
}
