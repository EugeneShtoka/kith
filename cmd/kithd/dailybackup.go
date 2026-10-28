package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sync"
	"syscall"
	"time"

	"github.com/EugeneShtoka/kith/internal/backup"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// kithd backs the history up as [backup] says (backup.MakeDaily decides whether one is
// due): first a while after start, past the networks' catch-up and the first vacuum,
// then when the next is due, looking at least every dailyEvery, which a machine that
// slept, changed its clock or reloaded its config catches up on. The config is read
// afresh on every look, so a changed [backup] needs no restart.
const (
	dailyFirst = 20 * time.Minute
	dailyEvery = time.Hour
	// dailyLate is how far past the due time a look is set, so one waking on time never
	// finds the interval a hair short.
	dailyLate = time.Second
)

// noKeySaid is what the user is told while there is no backup key: kithd never makes
// one itself, since nobody would see its private half.
const noKeySaid = "Backups are off until you run `kith backup` once."

// errDailyPanicked is a daily backup that panicked.
var errDailyPanicked = errors.New("the daily backup panicked")

// dailyBackups is kithd's daily backup: the config file and storage it backs up, how
// it tells the user (a popup to every profile), and the lock it shares with the vacuum.
type dailyBackups struct {
	log         *slog.Logger
	file        configFile
	storage     domain.Storage
	say         func(body string)
	maintenance *sync.Mutex
}

// run makes the daily backups until ctx ends, which interrupts one running. It blocks.
func (d dailyBackups) run(ctx context.Context) {
	// said is the last thing told, so a lasting state is told once, not hourly.
	var said string
	wait := dailyFirst
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		now := time.Now()
		done, err := d.once(ctx, now)
		if ctx.Err() != nil {
			return
		}
		wait = nextLook(done, now)
		for _, p := range done.Ignored {
			d.log.Warn("not counted as a daily backup: it is not a whole one", "op", "daily backup", "path", p)
		}
		tell, level := d.outcome(done, err)
		if tell != "" && tell != said {
			d.log.Log(ctx, level, tell, "op", "daily backup")
			d.say(tell)
		}
		said = tell
	}
}

// nextLook is how long after a run at now to look again: when the next backup is due,
// but never later than dailyEvery.
func nextLook(done backup.Daily, now time.Time) time.Duration {
	if done.Next.IsZero() {
		return dailyEvery
	}
	return min(dailyEvery, max(done.Next.Sub(now), 0)+dailyLate)
}

// outcome is what one daily run tells the user ("" when nothing: a backup made or
// none due), and how loudly to log it.
func (d dailyBackups) outcome(done backup.Daily, err error) (string, slog.Level) {
	switch {
	case errors.Is(err, backup.ErrNoKey):
		return noKeySaid, slog.LevelInfo
	case errors.Is(err, db.ErrNoRoom):
		return "The scheduled backup is put off: " + err.Error(), slog.LevelWarn
	case errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS):
		return "The scheduled backup failed: " + err.Error() + "\nkithd may not write there: a folder outside " +
			"kith's own needs kithd restarted, or a drop-in under the packaged unit (docs/troubleshooting.md)", slog.LevelError
	case err != nil:
		return "The scheduled backup failed: " + err.Error(), slog.LevelError
	case done.Ran:
		d.log.Info("backed up", "op", "daily backup", "path", done.Made.Path, "deleted", len(done.Deleted))
	}
	return "", slog.LevelInfo
}

// once is one scheduled run at now, by the config as it reads now, holding maintenance,
// a panic in it an error: a failing backup is told, never takes the daemon down. With
// [backup] enabled off it does nothing.
func (d dailyBackups) once(ctx context.Context, now time.Time) (done backup.Daily, err error) {
	cfg, err := d.file.read()
	if err != nil {
		return backup.Daily{}, err
	}
	if invalid := setup.Backups(cfg.Backup); invalid != nil {
		return backup.Daily{}, invalid
	}
	if !cfg.Backup.On() {
		return backup.Daily{}, nil
	}
	storage, err := setup.WithBackups(d.storage, cfg)
	if err != nil {
		return backup.Daily{}, err
	}
	to, _, err := backup.KeyFor(cfg, d.storage)
	if err != nil {
		return backup.Daily{}, err //nolint:wrapcheck // backup words it
	}
	key, err := cacheKey(d.storage)
	if err != nil {
		return backup.Daily{}, fmt.Errorf("%w (unlock the keyring)", err)
	}
	src := backup.Source{
		ConfigPath: d.file.path, Config: cfg, Storage: d.storage, CacheKey: key,
		MediaDir: d.storage.MediaDir(),
	}
	d.maintenance.Lock()
	defer d.maintenance.Unlock()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%w: %v", errDailyPanicked, p)
		}
	}()
	next, err := setup.BackupNext(cfg.Backup) // checked above
	if err != nil {
		return backup.Daily{}, err
	}
	sched := backup.Schedule{Next: next, Keep: cfg.Backup.KeepOrDefault()}
	return backup.MakeDaily(ctx, src, to, storage.BackupsDir(), sched, now) //nolint:wrapcheck // backup words it
}
