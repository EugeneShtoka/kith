package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/backup"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// kithd backs the history up once a day (backup.MakeDaily decides whether one is
// due): first a while after start, past the networks' catch-up and the first vacuum,
// then every hour, which a machine that slept or changed its clock catches up on.
const (
	dailyFirst = 20 * time.Minute
	dailyEvery = time.Hour
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
		wait = dailyEvery
		done, err := d.once(ctx, time.Now())
		if ctx.Err() != nil {
			return
		}
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

// outcome is what one daily run tells the user ("" when nothing: a backup made or
// none due), and how loudly to log it.
func (d dailyBackups) outcome(done backup.Daily, err error) (string, slog.Level) {
	switch {
	case errors.Is(err, backup.ErrNoKey):
		return noKeySaid, slog.LevelInfo
	case errors.Is(err, db.ErrNoRoom):
		return "The daily backup is put off: " + err.Error(), slog.LevelWarn
	case err != nil:
		return "The daily backup failed: " + err.Error(), slog.LevelError
	case done.Ran:
		d.log.Info("backed up", "op", "daily backup", "path", done.Made.Path, "deleted", len(done.Deleted))
	}
	return "", slog.LevelInfo
}

// once is one daily run at now, holding maintenance, a panic in it an error: a failing
// backup is told, never takes the daemon down.
func (d dailyBackups) once(ctx context.Context, now time.Time) (done backup.Daily, err error) {
	to, err := backup.LoadKey(d.storage)
	if err != nil {
		return backup.Daily{}, err //nolint:wrapcheck // backup words it
	}
	cfg, err := d.file.read()
	if err != nil {
		return backup.Daily{}, err
	}
	key, err := cacheKey(d.storage)
	if err != nil {
		return backup.Daily{}, fmt.Errorf("%w (unlock the keyring)", err)
	}
	src := backup.Source{
		ConfigPath: d.file.path, Config: cfg, Storage: d.storage, CacheKey: key,
		MediaDir: setup.MediaDir(cfg, d.storage),
	}
	d.maintenance.Lock()
	defer d.maintenance.Unlock()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%w: %v", errDailyPanicked, p)
		}
	}()
	return backup.MakeDaily(ctx, src, to, d.storage.BackupsDir(), now) //nolint:wrapcheck // backup words it
}
