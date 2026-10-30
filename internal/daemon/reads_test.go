package daemon_test

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// readChat is a chat both bridges run. Invented.
const readChat = domain.RoomID("telegram:200/300")

// readingBackend is one bridge's networks and cache as reads see them: a read made in
// kith is kept here and receipted to the network; a read or mark another bridge made is
// kept here alone, for a room this bridge runs.
type readingBackend struct {
	apitest.Nop
	mu       sync.Mutex
	rooms    []domain.Room
	held     map[domain.RoomID]domain.ReadPosition
	marked   map[domain.RoomID]bool
	receipts []domain.RoomID // a receipt sent per read made here
	marks    int             // marks sent to the network
	kept     []domain.ReadPosition
}

func newReadingBackend(rooms ...domain.Room) *readingBackend {
	return &readingBackend{Nop: nopWithChannels(), rooms: rooms, held: map[domain.RoomID]domain.ReadPosition{}, marked: map[domain.RoomID]bool{}}
}

func (b *readingBackend) Rooms(context.Context) ([]domain.Room, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.rooms), nil
}

// MarkRead reads room through event, named e<ms> by the time it was sent: kept here,
// only ever forward, and its receipt sent.
func (b *readingBackend) MarkRead(_ context.Context, room domain.RoomID, event domain.EventID, _ bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	at, _ := strconv.ParseInt(strings.TrimPrefix(string(event), "e"), 10, 64)
	b.receipts = append(b.receipts, room)
	if at > b.held[room].At {
		b.held[room] = domain.ReadPosition{Room: room, Event: event, At: at}
	}
	b.marked[room] = false
	return nil
}

func (b *readingBackend) MarkRoomUnread(_ context.Context, room domain.RoomID, unread bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.marks++
	b.marked[room] = unread
	return nil
}

func (b *readingBackend) HeldReads(_ context.Context, of []domain.ReadPosition) ([]domain.ReadPosition, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []domain.ReadPosition
	for _, q := range of {
		if p, ok := b.held[q.Room]; ok && q.Root == "" {
			out = append(out, p)
		}
	}
	return out, nil
}

func (b *readingBackend) KeepReads(_ context.Context, reads []domain.ReadPosition, marks map[domain.RoomID]bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.kept = slices.Clone(reads)
	runs := func(room domain.RoomID) bool {
		return slices.ContainsFunc(b.rooms, func(r domain.Room) bool { return r.ID == room })
	}
	for _, r := range reads {
		if runs(r.Room) && r.Root == "" && r.At > b.held[r.Room].At {
			b.held[r.Room] = r
		}
	}
	for room, m := range marks {
		if runs(room) {
			b.marked[room] = m
		}
	}
	return nil
}

// MergeRoom merges as the network would: the merged room goes, its read joins.
func (b *readingBackend) MergeRoom(_ context.Context, from, into domain.RoomID) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rooms = []domain.Room{{ID: into, Merged: []domain.RoomID{from}}}
	if p := b.held[from]; p.At > b.held[into].At {
		p.Room = into
		b.held[into] = p
	}
	delete(b.held, from)
	return nil
}

func (b *readingBackend) set(rooms ...domain.Room) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rooms = rooms
}

// state is what the bridge holds of room, the receipts it sent, the marks it sent, and
// whether the mesh last gave it a read of room.
func (b *readingBackend) state(room domain.RoomID) (held domain.ReadPosition, marked bool, receipts, marks int, inMesh bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, r := range b.receipts {
		if r == room {
			receipts++
		}
	}
	inMesh = slices.ContainsFunc(b.kept, func(p domain.ReadPosition) bool { return p.Room == room })
	return b.held[room], b.marked[room], receipts, b.marks, inMesh
}

// A read made on one bridge is read on the other, which sends no receipt of its own: the
// network has it from the bridge where it was made. A room marked unread on one is
// marked on the other, and a read there clears the mark on both; a read through an
// older message (a window open on history) moves neither back.
func TestAReadOnOneBridgeIsReadOnTheOther(t *testing.T) {
	t.Parallel()
	homelab, laptop := newReadingBackend(domain.Room{ID: readChat}), newReadingBackend(domain.Room{ID: readChat})
	a := serveBridge(t, bridgeOpts{backend: homelab})
	b := serveBridge(t, bridgeOpts{backend: laptop})
	join(t, a, b)

	if err := a.socket.MarkRead(t.Context(), readChat, "e1800000000000", false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the laptop keeps the homelab's read", func() bool {
		held, _, _, _, _ := laptop.state(readChat)
		return held.At == 1800000000000
	})

	if err := b.socket.MarkRoomUnread(t.Context(), readChat, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the homelab keeps the laptop's mark", func() bool {
		_, marked, _, _, _ := homelab.state(readChat)
		return marked
	})
	if err := a.socket.MarkRead(t.Context(), readChat, "e1800000060000", false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the laptop keeps the homelab's later read, the mark cleared", func() bool {
		held, marked, _, _, _ := laptop.state(readChat)
		return held.At == 1800000060000 && !marked
	})

	if err := b.socket.MarkRead(t.Context(), readChat, "e1799999999000", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * settle / 5) // anything to cross has crossed
	held, _, receipts, marks, _ := homelab.state(readChat)
	if held.At != 1800000060000 || receipts != 2 || marks != 0 {
		t.Errorf("the homelab holds %+v, sent %d receipts and %d marks; want read through 1800000060000, its own 2 receipts, no mark", held, receipts, marks)
	}
	if _, _, receipts, marks, _ := laptop.state(readChat); receipts != 1 || marks != 1 {
		t.Errorf("the laptop sent %d receipts and %d marks; want its own one of each", receipts, marks)
	}
}

// A read made on a chat's LID on a bridge that has not merged it yet, held by the other
// bridge before that one learns the number: the merge moves it under the number's key,
// and both bridges keep it there; no read is left under the LID's.
func TestAReadOnAChatsLIDLandsUnderItsNumber(t *testing.T) {
	t.Parallel()
	homelab := newReadingBackend(domain.Room{ID: mergedNumber})
	laptop := newReadingBackend(domain.Room{ID: mergedLID})
	a := serveBridge(t, bridgeOpts{backend: homelab})
	b := serveBridge(t, bridgeOpts{backend: laptop})
	join(t, a, b)

	if err := b.socket.MarkRead(t.Context(), mergedLID, "e1800000000000", false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the homelab holds the laptop's read of the LID", func() bool {
		_, _, _, _, inMesh := homelab.state(mergedLID)
		return inMesh
	})
	homelab.set(domain.Room{ID: mergedNumber, Merged: []domain.RoomID{mergedLID}}) // the homelab learns the number
	a.streams.RoomsChanged()
	for name, bridge := range map[string]*readingBackend{"homelab": homelab, "laptop": laptop} {
		eventually(t, "the "+name+" keeps the read under the number, and the mesh none under the LID", func() bool {
			held, _, _, _, _ := bridge.state(mergedNumber)
			_, _, _, _, lid := bridge.state(mergedLID)
			return held.At == 1800000000000 && !lid
		})
	}
	if _, _, receipts, _, _ := homelab.state(mergedNumber); receipts != 0 {
		t.Errorf("the homelab sent %d receipts for a read made on the laptop", receipts)
	}
}
