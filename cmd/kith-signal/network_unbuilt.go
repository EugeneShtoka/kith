//go:build !signal

package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/EugeneShtoka/kith/internal/signalhelper"
)

// network is no Signal: this build does not link libsignal.
func network(string, *slog.Logger) signalhelper.Network {
	return unbuilt{}
}

var errUnbuilt = errors.New("kith-signal was built without Signal: build it with `make build-signal` (the signal tag and libsignal_ffi)")

// unbuilt refuses every call, saying why.
type unbuilt struct{}

func (unbuilt) Open(context.Context, []byte) ([]signalhelper.Device, error) {
	return nil, errUnbuilt
}

func (unbuilt) Link(context.Context, string, func(string) error) (signalhelper.Device, error) {
	return signalhelper.Device{}, errUnbuilt
}
