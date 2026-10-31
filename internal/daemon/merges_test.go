package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/schedule"
)

// A WhatsApp chat known by the person's LID, merged into their number's. Invented.
const (
	mergedLID    = domain.RoomID("whatsapp:15550100/88001234567@lid")
	mergedNumber = domain.RoomID("whatsapp:15550100/15550199@s.whatsapp.net")
)

// mergingBackend lists rooms that change while the daemon runs, and merges a room
// another bridge merged.
type mergingBackend struct {
	apitest.Nop
	mu     sync.Mutex
	rooms  []domain.Room
	merges [][2]domain.RoomID
}

func (b *mergingBackend) Rooms(context.Context) ([]domain.Room, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rooms, nil
}

func (b *mergingBackend) set(rooms []domain.Room) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rooms = rooms
}

// MergeRoom merges as a network would: the merged room goes, and the one it joined
// lists it.
func (b *mergingBackend) MergeRoom(_ context.Context, from, into domain.RoomID) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.merges = append(b.merges, [2]domain.RoomID{from, into})
	b.rooms = []domain.Room{{ID: into, Merged: []domain.RoomID{from}}}
	return nil
}

// merged is the merges another bridge's alias brought here.
func (b *mergingBackend) merged() [][2]domain.RoomID {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.merges)
}

// A chat one bridge merged (it learned the number) is merged on the other bridge too,
// which has not: the alias crosses the mesh, and the other bridge merges once. A bridge
// that merged the chat itself as well takes the alias as its own and writes none again.
func TestAChatMergedOnOneBridgeMergesOnTheOther(t *testing.T) {
	t.Parallel()
	for _, both := range []bool{false, true} {
		t.Run(map[bool]string{false: "one learned it", true: "both learned it"}[both], func(t *testing.T) {
			t.Parallel()
			merged := []domain.Room{{ID: mergedNumber, Merged: []domain.RoomID{mergedLID}}}
			homelab := &mergingBackend{Nop: nopWithChannels(), rooms: merged}
			laptop := &mergingBackend{Nop: nopWithChannels(), rooms: []domain.Room{{ID: mergedLID}}}
			if both {
				laptop.set(merged)
			}
			a := serveBridge(t, bridgeOpts{backend: homelab})
			b := serveBridge(t, bridgeOpts{backend: laptop})
			join(t, a, b)
			if both {
				time.Sleep(2 * settle / 5) // the aliases cross, if any is to be merged
				if got := laptop.merged(); len(got) > 0 {
					t.Fatalf("the laptop merged %v again", got)
				}
				return
			}
			eventually(t, "the laptop merges the chat the homelab merged", func() bool {
				got := laptop.merged()
				return len(got) == 1 && got[0] == [2]domain.RoomID{mergedLID, mergedNumber}
			})
			time.Sleep(2 * settle / 5) // nothing merges it twice
			if got := laptop.merged(); len(got) != 1 {
				t.Errorf("the laptop merged %v", got)
			}
		})
	}
}

// A chat merged into another, whether before kithd started (a follow-up cut off) or
// while it runs: the config's entries naming it and a message scheduled to it name and
// go to the chat it joined.
func TestWhatNamesAMergedChatNamesTheOneItJoined(t *testing.T) {
	t.Parallel()
	for _, atStart := range []bool{true, false} {
		t.Run(map[bool]string{true: "before start", false: "while running"}[atStart], func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			var cfg config.Config
			cfg.Profiles = []config.Profile{{Name: "work", Include: []string{"room:" + string(mergedLID)}}}
			cfg.Tags = []config.Tag{{Name: "Family", Picked: []string{string(mergedLID)}}}
			if err := config.Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			backend := &mergingBackend{Nop: nopWithChannels(), rooms: []domain.Room{{ID: mergedLID}, {ID: mergedNumber}}}
			merged := []domain.Room{{ID: mergedNumber, Merged: []domain.RoomID{mergedLID}}}
			if atStart {
				backend.set(merged)
			}
			scheduler := daemon.NewScheduler(schedule.New(filepath.Join(dir, "scheduled.toml")), backend,
				func() time.Duration { return time.Hour }, nil)
			if _, err := scheduler.Add(domain.ScheduledMessage{ID: "s1", RoomID: mergedLID, Body: "later", At: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			streams := daemon.NewStreams()
			sock := filepath.Join(dir, "kithd.sock")
			ln, err := daemon.Listen(context.Background(), sock)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			served := make(chan error, 1)
			go func() {
				served <- daemon.Serve(ctx, ln, complete(t, &daemon.Daemon{
					Backend: backend, Streams: streams, Config: daemon.NewConfigFile(path), Scheduler: scheduler,
				}))
			}()
			t.Cleanup(func() {
				cancel()
				if err := <-served; err != nil {
					t.Errorf("Serve = %v", err)
				}
			})
			if !atStart {
				time.Sleep(50 * time.Millisecond) // the first look, before the merge
				backend.set(merged)
				streams.RoomsChanged()
			}
			eventually(t, "the config and the schedule name the number's chat", func() bool {
				data, err := os.ReadFile(path)
				list := scheduler.List()
				return err == nil && !strings.Contains(string(data), string(mergedLID)) &&
					strings.Count(string(data), string(mergedNumber)) == 2 &&
					len(list) == 1 && list[0].RoomID == mergedNumber
			})
		})
	}
}
