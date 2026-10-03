package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
)

// backupSweep is how often room keys are offered to the server-side backup, which
// mautrix never uploads to on its own. A schedule rather than per key: keys rotate
// close to per message in a busy room.
const backupSweep = 5 * time.Minute

type backupBackend interface {
	BackupRoomKeys(ctx context.Context) (int, error)
}

// KeyBackup uploads room keys this device holds into the account's server-side
// backup, on a timer, for as long as the daemon runs.
type KeyBackup struct {
	b     backupBackend
	every time.Duration
	log   func(level slog.Level, line string)
	// said is the last line reported, so a lasting condition is logged once.
	said string
	// soon asks for a sweep before the timer's (see Soon).
	soon chan struct{}
}

// NewKeyBackup returns a sweeper over b, reporting through log: problems at warn,
// progress at info.
func NewKeyBackup(b backupBackend, log func(level slog.Level, line string)) *KeyBackup {
	return &KeyBackup{b: b, every: backupSweep, log: log, soon: make(chan struct{}, 1)}
}

// Soon asks for a sweep now rather than at the next interval: the account just
// logged in, after the startup sweep found nothing to back up. It never blocks.
func (k *KeyBackup) Soon() {
	select {
	case k.soon <- struct{}{}:
	default: // one is already asked for
	}
}

// Run sweeps once at startup (keys received while stopped are in no backup) and
// then every interval, until ctx ends. It blocks.
func (k *KeyBackup) Run(ctx context.Context) {
	k.pass(ctx)
	ticker := time.NewTicker(k.every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			k.pass(ctx)
		case <-k.soon:
			k.pass(ctx)
		}
	}
}

// pass runs one upload and reports what changed.
func (k *KeyBackup) pass(ctx context.Context) {
	uploaded, err := k.b.BackupRoomKeys(ctx)
	switch {
	case errors.Is(err, context.Canceled):
		// Shutdown, not a fault.
	case errors.Is(err, api.ErrNoEncryption):
		k.say("room keys are not being backed up: encryption is not enabled")
	case errors.Is(err, api.ErrNoKeyBackup):
		k.say("room keys are not being backed up: this account has no key backup " +
			"(run `kith --bootstrap-keys` to create one)")
	case err != nil:
		k.say(fmt.Sprintf("could not back up room keys: %v", err))
	case uploaded > 0:
		k.said = ""
		k.log(slog.LevelInfo, fmt.Sprintf("backed up %d room keys", uploaded))
	default:
		k.said = ""
	}
}

// say reports line unless it is the same thing the last pass said.
func (k *KeyBackup) say(line string) {
	if line == k.said {
		return
	}
	k.said = line
	k.log(slog.LevelWarn, line)
}
