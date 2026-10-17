//go:build !signal

package main

import (
	"context"
	"errors"
	"log/slog"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"

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

func (unbuilt) Connect(context.Context, string, signalhelper.Sink) error {
	return errUnbuilt
}

func (unbuilt) Send(context.Context, *signalv1.SendRequest) error { return errUnbuilt }

func (unbuilt) Typing(context.Context, *signalv1.TypingRequest) error { return errUnbuilt }

func (unbuilt) MarkRead(context.Context, *signalv1.MarkReadRequest) error { return errUnbuilt }
