package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// backupSweep is how often room keys are offered to the server-side backup, which
// mautrix never uploads to itself. Timed, not per key: keys rotate nearly per message.
const backupSweep = 5 * time.Minute

type backupBackend interface {
	Accounts() []domain.Account
	BackupRoomKeys(ctx context.Context, on domain.RoomOwner) (int, error)
}

// KeyBackup periodically uploads each account's room keys, as this device has them, to
// that account's server-side backup.
type KeyBackup struct {
	b     backupBackend
	every time.Duration
	log   func(level slog.Level, account, line string)
	// said is the last line reported of each account, so a lasting condition is logged
	// once.
	said map[domain.RoomOwner]string
	// soon asks for a sweep before the timer's (see Soon).
	soon chan struct{}
}

// NewKeyBackup returns a sweeper over b's accounts, logging problems at warn and
// progress at info, each line with the account it is about.
func NewKeyBackup(b backupBackend, log func(level slog.Level, account, line string)) *KeyBackup {
	return &KeyBackup{b: b, every: backupSweep, log: log, said: map[domain.RoomOwner]string{}, soon: make(chan struct{}, 1)}
}

// Soon asks for a sweep now, e.g. after a login that followed an empty startup sweep.
// It never blocks.
func (k *KeyBackup) Soon() {
	select {
	case k.soon <- struct{}{}:
	default: // one is already asked for
	}
}

// Run sweeps at startup (keys received while stopped are in no backup) and then every
// interval until ctx ends. It blocks.
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

// pass sweeps every account; one failing never holds up another.
func (k *KeyBackup) pass(ctx context.Context) {
	for _, a := range k.b.Accounts() {
		k.sweep(ctx, a)
	}
}

func (k *KeyBackup) sweep(ctx context.Context, a domain.Account) {
	uploaded, err := k.b.BackupRoomKeys(ctx, a.On)
	switch {
	case errors.Is(err, context.Canceled):
		// Shutdown, not a fault.
	case errors.Is(err, api.ErrNoEncryption):
		k.say(a, "room keys are not being backed up: encryption is not enabled")
	case errors.Is(err, api.ErrNoKeyBackup):
		k.say(a, "room keys are not being backed up: this account has no key backup "+
			"(run `kith --bootstrap-keys` to create one)")
	case err != nil:
		k.say(a, fmt.Sprintf("could not back up room keys: %v", err))
	case uploaded > 0:
		delete(k.said, a.On)
		k.log(slog.LevelInfo, a.Name, fmt.Sprintf("backed up %d room keys", uploaded))
	default:
		delete(k.said, a.On)
	}
}

// say reports line about a unless a's last sweep said the same.
func (k *KeyBackup) say(a domain.Account, line string) {
	if line == k.said[a.On] {
		return
	}
	k.said[a.On] = line
	k.log(slog.LevelWarn, a.Name, line)
}
