package backup

import (
	"bufio"
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/db"
)

// kithd makes scheduled backups into Storage.BackupsDir and keeps the last
// Schedule.Keep. One is due once Schedule.Next of the newest made at or before now has
// come, so a clock set forward or a machine that slept makes one backup, not one per
// time missed, and a clock set back past every backup there makes one more. Only files
// named as scheduled backups are counted or deleted, so a backup the user put there
// stays. (The code keeps the name "daily" from when the schedule was fixed.)

// Schedule is when kithd backs up and how many backups it keeps: Next is when the
// backup after one made at last is due ([backup] every or cron), Keep [backup] keep.
type Schedule struct {
	Next func(last time.Time) time.Time
	Keep int
}

// A scheduled backup's name is dailyPrefix, the time it was made (dailyStamp),
// dailySuffix. Those made before were named formerDailyPrefix, and still count and are
// deleted with the rest.
const (
	dailyPrefix       = "kith-backup-"
	formerDailyPrefix = "kith-daily-"
	// mediaBackupPrefix names a scheduled media backup (MakeMedia) instead.
	mediaBackupPrefix = "kith-media-"
	dailyStamp        = "2006-01-02-150405"
	dailySuffix       = ".tar.age"
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
	return makeScheduled(dir, []string{dailyPrefix, formerDailyPrefix}, src.Storage.DataDir, sched, now,
		func() error { return roomForDaily(src, dir) },
		func(out string) (Made, error) { return Create(ctx, src, out, to) })
}

// MakeMedia is MakeDaily for media backups of the archive in archiveDir, sealed under
// cacheKey (CreateMedia): named apart, so each kind keeps its own sched.Keep.
func MakeMedia(ctx context.Context, archiveDir string, cacheKey []byte, to *PublicKey, dir string, sched Schedule, now time.Time) (Daily, error) {
	return makeScheduled(dir, []string{mediaBackupPrefix}, "", sched, now,
		func() error { return roomForMedia(archiveDir, dir) },
		func(out string) (Made, error) { return CreateMedia(ctx, archiveDir, cacheKey, out, to) })
}

// makeScheduled makes one scheduled backup named prefixes[0] in dir when due (room
// checks the disks hold it, create writes it), and rotates those named any of
// prefixes; dataDir is where a stopped one may have left the cache's copy, "" for none.
func makeScheduled(dir string, prefixes []string, dataDir string, sched Schedule, now time.Time, room func() error, create func(out string) (Made, error)) (Daily, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Daily{}, fmt.Errorf("backup: %w", err)
	}
	removeStalePartials(dir, prefixes, dataDir)
	good, ignored, err := dailyBackups(dir, prefixes)
	if err != nil {
		return Daily{}, err
	}
	done := Daily{Ignored: ignored}
	if last, ok := lastBy(good, now); ok && now.Before(sched.Next(last)) {
		done.Next = sched.Next(last)
		done.Deleted = rotate(good, "", sched.Keep)
		return done, nil
	}
	if roomErr := room(); roomErr != nil {
		return done, roomErr
	}
	out := filepath.Join(dir, prefixes[0]+now.Format(dailyStamp)+dailySuffix)
	made, err := create(out)
	if err != nil {
		return done, err
	}
	done.Ran, done.Made, done.Next = true, made, sched.Next(now)
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

// dailyBackups is every whole scheduled backup named one of prefixes in dir, oldest
// first, and the files so named that are not whole. A name whose time does not parse
// is not one kithd gave (kith backup's own, kith-backup-<date>-<minute>, say), so it is
// neither.
func dailyBackups(dir string, prefixes []string) (good []daily, ignored []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("backup: %w", err)
	}
	for _, e := range entries {
		at, ok := scheduledAt(e.Name(), prefixes)
		if !ok {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if !e.Type().IsRegular() || !whole(p) {
			ignored = append(ignored, p)
			continue
		}
		good = append(good, daily{path: p, at: at})
	}
	slices.SortFunc(good, oldestFirst)
	return good, ignored, nil
}

// scheduledAt is when the scheduled backup named name was made; false when name is not
// one of prefixes, a time and dailySuffix.
func scheduledAt(name string, prefixes []string) (time.Time, bool) {
	for _, prefix := range prefixes {
		stamp, ok := strings.CutPrefix(name, prefix)
		stamp, isDaily := strings.CutSuffix(stamp, dailySuffix)
		if !ok || !isDaily {
			continue
		}
		at, err := time.ParseInLocation(dailyStamp, stamp, time.Local)
		return at, err == nil
	}
	return time.Time{}, false
}

// oldestFirst orders backups by when they were made, then by name: the former and the
// current names sort apart, so a name alone is no order.
func oldestFirst(a, b daily) int {
	return cmp.Or(a.at.Compare(b.at), strings.Compare(a.path, b.path))
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

// rotate deletes the oldest of good (with made among them when one was) beyond keep,
// never made and never the newest, returning what it deleted.
func rotate(good []daily, made string, keep int) []string {
	slices.SortFunc(good, oldestFirst)
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
// a partial scheduled backup named one of prefixes in dir, and the cache's copy in
// dataDir (when not ""), encrypted under a key nothing kept.
func removeStalePartials(dir string, prefixes []string, dataDir string) {
	var partials []string
	for _, prefix := range prefixes {
		found, _ := filepath.Glob(filepath.Join(dir, "."+prefix+"*"+dailySuffix+".partial-*"))
		partials = append(partials, found...)
	}
	var copies []string
	if dataDir != "" {
		copies, _ = filepath.Glob(filepath.Join(dataDir, snapshotPattern))
	}
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
	if files, err := mediaFiles(src.MediaDir, mediaPrefix); err == nil {
		for _, f := range files {
			pictures += fileSize(f.path)
		}
	}
	if err := db.RoomFor(map[string]int64{src.Storage.DataDir: cache, dir: cache + pictures}); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	return nil
}

// roomForMedia refuses, as db.ErrNoRoom, a media backup of archiveDir that dir's disk
// cannot hold.
func roomForMedia(archiveDir, dir string) error {
	var size int64
	if files, err := mediaFiles(archiveDir, archivePrefix); err == nil {
		for _, f := range files {
			size += fileSize(f.path)
		}
	}
	if err := db.RoomFor(map[string]int64{dir: size}); err != nil {
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
