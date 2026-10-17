//go:build signal

package main

import (
	"log/slog"

	"github.com/EugeneShtoka/kith/internal/signalhelper"
	"github.com/EugeneShtoka/kith/internal/signalhelper/meow"
)

// network is Signal itself: this build links libsignal.
func network(store string, log *slog.Logger) signalhelper.Network {
	return meow.New(store, log)
}
