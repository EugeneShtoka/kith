package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// attachOrWait attaches to kithd for the interface, first sitting out a kithd that
// cannot start yet (api.ErrDaemonWaiting) and saying why, so kith opens by itself once
// it can. Quit while waiting, it returns no backend and no error.
//
// A one-shot command does not wait: attach fails with the reason at once, since what
// kithd waits for (an unlocked keyring) takes someone acting elsewhere, and a script
// should not hang on it.
func attachOrWait(ctx context.Context, configPath string) (attached, error) {
	at, err := attach(ctx, configPath, readyTimeout)
	for errors.Is(err, api.ErrDaemonWaiting) {
		if !waitOut(ctx, at.storage, func(line string) { fmt.Fprintln(os.Stderr, line) }) {
			return attached{}, nil
		}
		at, err = attach(ctx, configPath, readyTimeout)
	}
	return at, err
}

// waitOut tells why kithd cannot start yet, and each new reason, until it can; false
// when ctx (Ctrl-C) ended it first.
func waitOut(ctx context.Context, storage domain.Storage, tell func(line string)) bool {
	first := true
	return daemon.WaitOut(ctx, storage, func(why string) {
		if first {
			first = false
			tell("kith: kithd is waiting: " + why + "\n      kith opens by itself once kithd can start (Ctrl-C to quit)")
			return
		}
		tell("kith: kithd is still waiting: " + why)
	}) == nil
}
