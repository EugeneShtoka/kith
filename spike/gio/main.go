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
	profile := flag.String("profile", "", "the [[profile]] to run in (default: the device's, else the config's first)")
	measure := flag.Bool("measure", false, "print timings and RSS once the first room is drawn, and exit")
	room := flag.String("room", "", "open this room instead of the most recent")
	play := flag.String("play", "", "and play this message's attachment in it")
	flag.Parse()

	backend, local, pal, keys, err := attach(*configPath, *profile)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		w := new(app.Window)
		w.Option(app.Title("kith (gio)"), app.Size(unit.Dp(1100), unit.Dp(760)))
		if err := run(w, backend, local, pal, keys, *measure, domain.RoomID(*room), domain.EventID(*play)); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

// attach reaches the local kithd's socket as kith-relay does, starting nothing, in
// profile: the one named, else the one the device remembers, else the config's first.
// local is whether badges count cached messages rather than notifications; pal is the
// desktop's theme, as [gui.theme] sets it; keys are the device's [keys].
func attach(configPath, profile string) (backend *daemon.Remote, local bool, pal palette, keys config.Keys, err error) {
	path := configPath
	if path == "" {
		if path, err = config.Path(); err != nil {
			return nil, false, palette{}, config.Keys{}, err
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, false, palette{}, config.Keys{}, fmt.Errorf("reading %s: %w", path, err)
	}
	storage, err := setup.StorageFor(cfg, path)
	if err != nil {
		return nil, false, palette{}, config.Keys{}, err
	}
	socket, err := daemon.SocketPath(storage)
	if err != nil {
		return nil, false, palette{}, config.Keys{}, err
	}
	source, err := setup.UnreadSource(cfg.Display.Unread)
	if err != nil {
		return nil, false, palette{}, config.Keys{}, err
	}
	backend = daemon.NewRemote(socket)
	dev, derr := config.LoadDevice(config.DevicePath(path))
	if derr != nil {
		log.Printf("the device file: %v (the config's first profile, the shared theme)", derr)
	}
	if profile == "" {
		profile = dev.ProfileInUse
	}
	if profile != "" {
		profiles, perr := setup.Profiles(cfg)
		if perr != nil {
			return nil, false, palette{}, config.Keys{}, perr
		}
		i := slices.IndexFunc(profiles, func(p domain.Profile) bool { return strings.EqualFold(p.Name, strings.TrimSpace(profile)) })
		if i < 0 {
			return nil, false, palette{}, config.Keys{}, fmt.Errorf("no [[profile]] %q in %s", profile, path)
		}
		backend.UseProfile(profiles[i].Name)
	}
	dev.Keys.FillDefaults() // a device file that failed to load is all defaults
	return backend, source != config.UnreadNotifications, themeOf(cfg, dev), dev.Keys, nil
}

func run(w *app.Window, backend *daemon.Remote, local bool, pal palette, keys config.Keys, measure bool, room domain.RoomID, play domain.EventID) error {
	ui := newUI(w, backend, local, pal, keys)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ui.loadRooms(ctx, room, play)
	go func() { _ = backend.Start(ctx) }()
	go ui.follow(ctx)
	go ui.presenting(ctx)

	var ops op.Ops
	first := true
	for {
		switch e := w.Event().(type) {
		case app.ConfigEvent:
			ui.mu.Lock()
			gained := e.Config.Focused && !ui.focused
			ui.focused = e.Config.Focused
			if gained {
				ui.present(ui.current) // in use now: the window a link goes to
			}
			ui.mu.Unlock()
			ui.markRead(ctx)
			if gained {
				go ui.loadDrafts(ctx) // written elsewhere meanwhile
			}
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
