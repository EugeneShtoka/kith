// Command kith-signal is kith's Signal helper: the one binary that links libsignal
// (cgo), so kithd and kith stay cgo-free. kithd starts it with the socket it is to
// serve SignalService on as file descriptor 3, sends it the key store's key, and keeps
// it alive with heartbeats; when they stop, the helper exits at once.
//
//	kith-signal -store <path>
//
// Built without the signal tag (every build but the helper's own), it serves the
// socket and refuses each call, saying why: there is no Signal in that build.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/EugeneShtoka/kith/internal/signalhelper"
)

// lapse is how long the helper waits for kithd's next heartbeat before exiting.
const lapse = 10 * time.Second

// socketFD is the socket kithd hands the helper: the first of exec's extra files.
const socketFD = 3

func main() {
	store := flag.String("store", "", "the key store's path")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *store == "" {
		log.Error("kith-signal: -store is required")
		os.Exit(2)
	}
	ln, err := net.FileListener(os.NewFile(socketFD, "kithd's socket"))
	if err != nil {
		log.Error("kith-signal: no socket from kithd on descriptor 3 (kithd starts this helper)", "err", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	lapsed := func() {
		log.Error("kith-signal: kithd's heartbeat stopped; exiting")
		os.Exit(1)
	}
	if err := signalhelper.Serve(ctx, ln, network(*store, log), lapse, lapsed, log); err != nil {
		log.Error("kith-signal: serving failed", "err", err)
		stop()
		os.Exit(1) //nolint:gocritic // stop has run
	}
}
