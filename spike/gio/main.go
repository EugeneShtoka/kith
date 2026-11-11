// Command kith-gio is a spike: kith's desktop window drawn by Gio instead of Electron,
// to see what startup and memory a native window costs. It attaches to the local kithd
// as the terminal client does (daemon.Remote) and shows rooms, a timeline with images,
// and a composer. Nothing here is meant to merge.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

var started = time.Now()

func main() {
	configPath := flag.String("config", "", "kith's config (default: the usual one)")
	profile := flag.String("profile", "", "the [[profile]] to run in (default: the config's first)")
	measure := flag.Bool("measure", false, "print timings and RSS once the first room is drawn, and exit")
	room := flag.String("room", "", "open this room instead of the most recent")
	play := flag.String("play", "", "and play this message's attachment in it")
	flag.Parse()

	backend, err := attach(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	if *profile != "" {
		backend.UseProfile(*profile)
	}
	go func() {
		w := new(app.Window)
		w.Option(app.Title("kith (gio)"), app.Size(unit.Dp(1100), unit.Dp(760)))
		if err := run(w, backend, *measure, domain.RoomID(*room), domain.EventID(*play)); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

// attach reaches the local kithd's socket as kith-relay does; it starts nothing.
func attach(configPath string) (*daemon.Remote, error) {
	path := configPath
	if path == "" {
		resolved, err := config.Path()
		if err != nil {
			return nil, err
		}
		path = resolved
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	storage, err := setup.StorageFor(cfg, path)
	if err != nil {
		return nil, err
	}
	socket, err := daemon.SocketPath(storage)
	if err != nil {
		return nil, err
	}
	return daemon.NewRemote(socket), nil
}

func run(w *app.Window, backend *daemon.Remote, measure bool, room domain.RoomID, play domain.EventID) error {
	ui := newUI(w, backend)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ui.loadRooms(ctx, room, play)
	go func() { _ = backend.Start(ctx) }()
	go ui.follow(ctx)

	var ops op.Ops
	first := true
	for {
		switch e := w.Event().(type) {
		case app.X11ViewEvent:
			ui.mu.Lock()
			ui.view = e.Window
			ui.mu.Unlock()
		case app.DestroyEvent:
			ui.mu.Lock()
			ui.stopLocked()
			ui.mu.Unlock()
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			ui.layout(ctx, gtx)
			e.Frame(gtx.Ops)
			if first {
				first = false
				ui.mark("first frame")
			}
			if measure && ui.drawnTimeline() {
				ui.mark("first room's timeline drawn")
				fmt.Fprintln(os.Stderr, strings.Join(ui.marks, "\n"))
				var mem runtime.MemStats
				runtime.ReadMemStats(&mem)
				fmt.Fprintf(os.Stderr, "RSS: %s, Go heap in use %d MB, Go runtime total %d MB\n",
					rss(), mem.HeapInuse>>20, mem.Sys>>20)
				if f, err := os.ReadFile("/proc/self/smaps_rollup"); err == nil {
					fmt.Fprint(os.Stderr, string(f))
				}
				return nil
			}
		}
	}
}

// rss is this process's resident set, from /proc.
func rss() string {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return err.Error()
	}
	for line := range strings.SplitSeq(string(status), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "VmRSS:"))
		}
	}
	return "unknown"
}

// byRecent orders rooms newest first by their latest cached message; rooms with none
// go last, by name.
func byRecent(rooms []domain.Room, last map[domain.RoomID]time.Time) {
	slices.SortStableFunc(rooms, func(a, b domain.Room) int {
		if c := last[b.ID].Compare(last[a.ID]); c != 0 {
			return c
		}
		return strings.Compare(a.DisplayName(), b.DisplayName())
	})
}
