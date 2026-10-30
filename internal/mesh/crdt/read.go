package crdt

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Read positions and unread marks (planning/design-mesh.md, Read positions): a room's
// position (read/<room>), a thread's (thread/<room>/<root>), and the room's mark
// (marked/<room>). A position is a max register: of the values its key holds, the
// furthest is the position, and a writer writes only one further than it holds, so no
// write lowers it. A mark is a register whose newest value wins. Either kind is
// deleted by an empty value: a merged room's, moved under the room it joined.
const (
	readPrefix   = "read/"
	threadPrefix = "thread/"
	markedPrefix = "marked/"
)

// ReadKey is the key of room's read position.
func ReadKey(room string) string { return readPrefix + room }

// ThreadKey is the key of the read position of room's thread root.
func ThreadKey(room, root string) string { return threadPrefix + room + "/" + root }

// MarkedKey is the key of room's unread mark.
func MarkedKey(room string) string { return markedPrefix + room }

// Read is how far a room, or one of its threads (Root, "" for the room's own
// timeline), is read: through Event, a message sent at At (Unix ms).
type Read struct {
	Room, Root, Event string
	At                int64
}

// after reports whether r is further than o: later, or as late through a later event.
func (r Read) after(o Read) bool { return r.At > o.At || (r.At == o.At && r.Event > o.Event) }

// key is r's key.
func (r Read) key() string {
	if r.Root == "" {
		return ReadKey(r.Room)
	}
	return ThreadKey(r.Room, r.Root)
}

// position is a position's value: what its key does not say.
type position struct {
	At    int64  `json:"at"`
	Event string `json:"event,omitempty"`
	Root  string `json:"root,omitempty"`
}

// write is r as its key's write.
func (r Read) write() (Write, error) {
	v, err := json.Marshal(position{At: r.At, Event: r.Event, Root: r.Root})
	if err != nil {
		return Write{}, fmt.Errorf("crdt: encode a read position: %w", err)
	}
	return Write{Key: r.key(), Value: v}, nil
}

// readOf reads a position key's value: the read it holds, and whether it is one.
func readOf(key string, value []byte) (Read, bool) {
	var p position
	if json.Unmarshal(value, &p) != nil || p.At < 0 {
		return Read{}, false
	}
	r := Read{Root: p.Root, Event: p.Event, At: p.At}
	if room, ok := strings.CutPrefix(key, readPrefix); ok && room != "" && p.Root == "" {
		r.Room = room
		return r, true
	}
	rest, ok := strings.CutPrefix(key, threadPrefix)
	if room, cut := strings.CutSuffix(rest, "/"+p.Root); ok && cut && room != "" && p.Root != "" {
		r.Room = room
		return r, true
	}
	return Read{}, false
}

// readReadWrite checks w as a read position's or mark's write, reporting whether it is
// one: a room's key (and a thread's root), holding a position or a mark, or nothing
// (deleted).
func readReadWrite(w Write) (bool, error) {
	switch {
	case strings.HasPrefix(w.Key, readPrefix), strings.HasPrefix(w.Key, threadPrefix):
		if len(w.Value) == 0 {
			return true, checkDeleted(w.Key)
		}
		if _, ok := readOf(w.Key, w.Value); !ok {
			return true, fmt.Errorf("%w: %s holds no read position", ErrBadWrite, w.Key)
		}
		return true, nil
	case strings.HasPrefix(w.Key, markedPrefix):
		if w.Key == markedPrefix || (len(w.Value) > 0 && string(w.Value) != "true" && string(w.Value) != "false") {
			return true, fmt.Errorf("%w: %s holds no unread mark", ErrBadWrite, w.Key)
		}
		return true, nil
	}
	return false, nil
}

// checkDeleted checks the key of a position deleted: a room's, or a room's and a root.
func checkDeleted(key string) error {
	if room, ok := strings.CutPrefix(key, readPrefix); ok && room != "" {
		return nil
	}
	rest, _ := strings.CutPrefix(key, threadPrefix)
	if i := strings.LastIndex(rest, "/"); i > 0 && i < len(rest)-1 {
		return nil
	}
	return fmt.Errorf("%w: %s names no room", ErrBadWrite, key)
}

// Reads is every read position s holds: of each key's values, the furthest.
func (s State) Reads() []Read {
	var out []Read
	for key, entries := range s.Entries {
		if r, ok := s.furthest(key, entries); ok {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b Read) int { return strings.Compare(a.key(), b.key()) })
	return out
}

// furthest is the furthest read of a position key's entries; false for a key of
// another kind, or none.
func (s State) furthest(key string, entries []Entry) (Read, bool) {
	var best Read
	found := false
	for _, e := range entries {
		if r, ok := readOf(key, e.Value); ok && (!found || r.after(best)) {
			best, found = r, true
		}
	}
	return best, found
}

// Mark is a room's unread mark and the write that set it.
type Mark struct {
	Marked bool
	Dot    Dot
}

// Marks is every room's unread mark: of a key's values, the newest by (clock, origin).
func (s State) Marks() map[string]Mark {
	out := map[string]Mark{}
	for key, entries := range s.Entries {
		room, ok := strings.CutPrefix(key, markedPrefix)
		if !ok || len(entries) == 0 {
			continue
		}
		best := entries[0]
		for _, e := range entries[1:] {
			if later(e, best) {
				best = e
			}
		}
		out[room] = Mark{Marked: string(best.Value) == "true", Dot: best.Dot}
	}
	return out
}

// ReadWrites are the writes that read reads here: each that s holds no position at or
// past, and each read room's mark cleared, when s holds it marked.
func (s State) ReadWrites(reads []Read) ([]Write, error) {
	writes := map[string]Write{}
	marks := s.Marks()
	for _, r := range reads {
		w, err := r.write()
		if err != nil {
			return nil, err
		}
		if r.Root == "" && marks[r.Room].Marked {
			writes[MarkedKey(r.Room)] = Write{Key: MarkedKey(r.Room), Value: []byte("false")}
		}
		if held, ok := s.furthest(w.Key, s.Entries[w.Key]); ok && !r.after(held) {
			continue
		}
		if prev, ok := writes[w.Key]; ok && !r.after(mustRead(prev)) {
			continue
		}
		writes[w.Key] = w
	}
	return sortedWrites(writes), nil
}

// mustRead is the read a position write this package made holds.
func mustRead(w Write) Read {
	r, _ := readOf(w.Key, w.Value)
	return r
}

// MarkWrite is the write that marks room unread, or clears its mark; false when s
// holds it so already.
func (s State) MarkWrite(room string, marked bool) (Write, bool) {
	key := MarkedKey(room)
	if m, ok := s.Marks()[room]; ok && m.Marked == marked && len(s.Entries[key]) == 1 {
		return Write{}, false
	}
	return Write{Key: key, Value: []byte(fmt.Sprint(marked))}, true
}

// FoldWrites are the writes that move what s holds of each room merged into another
// (aliases, merged room to the room it joined) under the room it joined: its read
// positions written there, unless that one's is as far; its mark, when that one is not
// marked; and its own keys deleted. So a read written under a merged room's key by a
// bridge that had not yet seen the merge is written again under the joined room's by
// every bridge holding the merge, as its own op. A room merged into a room itself
// merged is left as it is: no chain of merges moves anything round in a loop.
func (s State) FoldWrites(aliases map[string]string) ([]Write, error) {
	writes := map[string]Write{}
	reads := map[string]Read{}
	for key, entries := range s.Entries {
		r, ok := s.furthest(key, entries)
		if !ok {
			continue
		}
		into, merged := aliases[r.Room]
		if _, chained := aliases[into]; !merged || chained {
			continue
		}
		writes[key] = Write{Key: key}
		r.Room = into
		to := r.key()
		if held, ok := s.furthest(to, s.Entries[to]); ok && !r.after(held) {
			continue
		}
		if prev, ok := reads[to]; ok && !r.after(prev) {
			continue
		}
		reads[to] = r
	}
	for _, r := range reads {
		w, err := r.write()
		if err != nil {
			return nil, err
		}
		writes[w.Key] = w
	}
	marks := s.Marks()
	for _, from := range slices.Sorted(maps.Keys(marks)) {
		into, merged := aliases[from]
		if _, chained := aliases[into]; !merged || chained {
			continue
		}
		writes[MarkedKey(from)] = Write{Key: MarkedKey(from)}
		if marks[from].Marked && !marks[into].Marked {
			writes[MarkedKey(into)] = Write{Key: MarkedKey(into), Value: []byte("true")}
		}
	}
	return sortedWrites(writes), nil
}

// AdoptWrites are the writes that carry into s, a mesh being joined, what old (the
// joining bridge's own state) held of reads: each position further than s's, and each
// mark of a room s holds no mark of.
func (s State) AdoptWrites(old State) ([]Write, error) {
	writes, err := s.ReadWrites(old.Reads())
	if err != nil {
		return nil, err
	}
	writes = slices.DeleteFunc(writes, func(w Write) bool { return strings.HasPrefix(w.Key, markedPrefix) })
	marks := s.Marks()
	for room, m := range old.Marks() {
		if _, held := marks[room]; !held {
			writes = append(writes, Write{Key: MarkedKey(room), Value: []byte(fmt.Sprint(m.Marked))})
		}
	}
	slices.SortFunc(writes, func(a, b Write) int { return strings.Compare(a.Key, b.Key) })
	return writes, nil
}

// sortedWrites is writes by key.
func sortedWrites(writes map[string]Write) []Write {
	out := make([]Write, 0, len(writes))
	for _, key := range slices.Sorted(maps.Keys(writes)) {
		out = append(out, writes[key])
	}
	return out
}
