package backup

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/db"
)

// kithd makes a backup every Schedule.Every into Storage.BackupsDir and keeps the last
// Schedule.Keep. One is due once Every has passed since the newest made at or before
// now, so a clock set forward or a machine that slept makes one backup, not one per
// interval missed, and a clock set back past every backup there makes one more. Only
// files named as scheduled backups are counted or deleted, so a backup the user put
// there stays. (They keep the name "daily" from when the schedule was fixed.)

// Schedule is how often kithd backs up and how many backups it keeps: [backup] every
// and keep.
type Schedule struct {
	Every time.Duration
	Keep  int
}

// A scheduled backup's name is dailyPrefix, the time it was made (dailyStamp),
// dailySuffix.
const (
	dailyPrefix = "kith-daily-"
	dailyStamp  = "2006-01-02-150405"
	dailySuffix = ".tar.age"
)

// stalePartial is how old a partial scheduled backup must be before it is taken for
// one a stopped kithd left: one being written is never that old.
const stalePartial = 24 * time.Hour

// Daily is what one scheduled run did: whether a backup was due and made, the backups
// deleted to keep Schedule.Keep, the scheduled-named files it did not count because
// they are not whole age files, and when the next backup is due.
type Daily struct {
	Ran     bool
	Made    Made
	Deleted []string
	Ignored []string
	Next    time.Time
}

// MakeDaily makes a backup of src in dir, encrypted to to, when one is due by sched at
// now, then deletes the oldest scheduled backups beyond sched.Keep, whether or not one
// was made. Only whole backups count, and the newest is never deleted. A disk without
// room for it is db.ErrNoRoom, and nothing is written or deleted.
func MakeDaily(ctx context.Context, src Source, to *PublicKey, dir string, sched Schedule, now time.Time) (Daily, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Daily{}, fmt.Errorf("backup: %w", err)
	}
	removeStalePartials(dir, src.Storage.DataDir)
	good, ignored, err := dailyBackups(dir)
	if err != nil {
		return Daily{}, err
	}
	done := Daily{Ignored: ignored}
	if last, ok := lastBy(good, now); ok && now.Sub(last) < sched.Every {
		done.Next = last.Add(sched.Every)
		done.Deleted = rotate(good, "", sched.Keep)
		return done, nil
	}
	if roomErr := roomForDaily(src, dir); roomErr != nil {
		return done, roomErr
	}
	out := filepath.Join(dir, dailyPrefix+now.Format(dailyStamp)+dailySuffix)
	made, err := Create(ctx, src, out, to)
	if err != nil {
		return done, err
	}
	done.Ran, done.Made, done.Next = true, made, now.Add(sched.Every)
	good = append(good, daily{path: out, at: now})
	done.Deleted = rotate(good, out, sched.Keep)
	return done, nil
}

// lastBy is when the newest of good made at or before now was made; false when none
// was (none at all, or a clock set back past them all).
func lastBy(good []daily, now time.Time) (time.Time, bool) {
	var last time.Time
	for _, b := range good {
		if !b.at.After(now) && b.at.After(last) {
			last = b.at
		}
	}
	return last, !last.IsZero()
}

// daily is a whole daily backup and when its name says it was made.
type daily struct {
	path string
	at   time.Time
}

// dailyBackups is every whole daily backup in dir, oldest first by name, and the
// daily-named files that are not whole.
func dailyBackups(dir string) (good []daily, ignored []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("backup: %w", err)
	}
	for _, e := range entries {
		stamp, ok := strings.CutPrefix(e.Name(), dailyPrefix)
		stamp, isDaily := strings.CutSuffix(stamp, dailySuffix)
		if !ok || !isDaily {
			continue
		}
		at, perr := time.ParseInLocation(dailyStamp, stamp, time.Local)
		p := filepath.Join(dir, e.Name())
		if perr != nil || !e.Type().IsRegular() || !whole(p) {
			ignored = append(ignored, p)
			continue
		}
		good = append(good, daily{path: p, at: at})
	}
	slices.SortFunc(good, func(a, b daily) int { return strings.Compare(a.path, b.path) })
	return good, ignored, nil
}

// whole reports whether path starts as an age file encrypted to an X25519 key does. A
// daily backup only gets its name once written, synced and closed (writeArchive), so a
// file under it that does not is one something else damaged or put there. Its content
// cannot be checked further here: only the private key, which this machine never
// holds, opens it.
func whole(path string) bool {
	f, err := os.Open(path) // #nosec G304 -- a file in this instance's backups folder
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReader(f)
	version, err := r.ReadString('\n')
	if err != nil || version != "age-encryption.org/v1\n" {
		return false
	}
	stanza, err := r.ReadString('\n')
	return err == nil && strings.HasPrefix(stanza, "-> X25519 ")
}

// rotate deletes the oldest of good (sorted by name, with made among them when one was)
// beyond keep, never made and never the newest by name, returning what it deleted.
func rotate(good []daily, made string, keep int) []string {
	slices.SortFunc(good, func(a, b daily) int { return strings.Compare(a.path, b.path) })
	var deleted []string
	for i, b := range good {
		if len(good)-i <= max(keep, 1) || b.path == made {
			continue
		}
		if err := os.Remove(b.path); err == nil {
			deleted = append(deleted, b.path)
		}
	}
	return deleted
}

// removeStalePartials deletes what a backup stopped mid-way (a kithd killed) leaves:
// a partial daily backup in dir, and the cache's copy in dataDir, encrypted under a
// key nothing kept.
func removeStalePartials(dir, dataDir string) {
	partials, _ := filepath.Glob(filepath.Join(dir, "."+dailyPrefix+"*"+dailySuffix+".partial-*"))
	copies, _ := filepath.Glob(filepath.Join(dataDir, snapshotPattern))
	for _, p := range append(partials, copies...) {
		if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() && time.Since(info.ModTime()) > stalePartial {
			_ = os.Remove(p)
		}
	}
}

// roomForDaily refuses, as db.ErrNoRoom, a backup the disks cannot hold: the cache's
// copy beside the cache, and the backup, about the cache and pictures, in its folder.
func roomForDaily(src Source, dir string) error {
	cache := fileSize(src.Storage.CachePath()) + fileSize(src.Storage.CachePath()+"-wal")
	var pictures int64
	if files, err := mediaFiles(src.MediaDir); err == nil {
		for _, f := range files {
			pictures += fileSize(f.path)
		}
	}
	if err := db.RoomFor(map[string]int64{src.Storage.DataDir: cache, dir: cache + pictures}); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	return nil
}

// fileSize is path's size, 0 when it cannot be read.
func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
