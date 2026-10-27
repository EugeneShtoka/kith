package backup_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/backup"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// day is noon local time, n days after a fixed date.
func day(n int) time.Time {
	return time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local).AddDate(0, 0, n)
}

// defaultKeep is how many scheduled backups [backup] keeps by default, and
// defaultSchedule the schedule an unset [backup] is.
const defaultKeep = config.DefaultBackupKeep

// after is a schedule's Next for a fixed interval, as [backup] every makes one.
func after(d time.Duration) func(time.Time) time.Time {
	return func(last time.Time) time.Time { return last.Add(d) }
}

var defaultSchedule = backup.Schedule{Next: after(config.DefaultBackupEvery), Keep: defaultKeep}

// daily runs MakeDaily for m at now into dir, on the default schedule.
func (m machine) daily(t *testing.T, ctx context.Context, dir string, now time.Time) (backup.Daily, error) {
	t.Helper()
	return m.scheduled(t, ctx, dir, defaultSchedule, now)
}

// scheduled runs MakeDaily for m at now into dir, on sched.
func (m machine) scheduled(t *testing.T, ctx context.Context, dir string, sched backup.Schedule, now time.Time) (backup.Daily, error) {
	t.Helper()
	cfg, err := config.Load(m.configPath)
	if err != nil {
		t.Fatal(err)
	}
	return backup.MakeDaily(ctx, m.source(t, cfg), testKey.Recipient(), dir, sched, now)
}

// names is the file names in dir, sorted.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// dailyName is the name MakeDaily gives a backup made at at.
func dailyName(at time.Time) string {
	return "kith-daily-" + at.Format("2006-01-02-150405") + ".tar.age"
}

// stand is a file under a daily backup's name that starts as a whole one does, standing
// in for a backup made earlier.
func stand(t *testing.T, dir string, at time.Time) string {
	t.Helper()
	p := filepath.Join(dir, dailyName(at))
	write(t, p, "age-encryption.org/v1\n-> X25519 stand-in\n"+strings.Repeat("x", 64), 0o600)
	return p
}

// One backup a day: a second run that day makes none, the next day's does, and only
// the last Keep stay, whatever day the run before was.
func TestADailyBackupIsMadeOnceADayAndTheLastSevenKept(t *testing.T) {
	m := newMachine(t)
	m.as(t).fill(t, "kept", 20)
	dir := t.TempDir()
	first, err := m.daily(t, context.Background(), dir, day(0))
	if err != nil || !first.Ran {
		t.Fatalf("MakeDaily(day 0) = %+v, %v; want a backup made", first, err)
	}
	again, err := m.daily(t, context.Background(), dir, day(0).Add(5*time.Hour))
	if err != nil || again.Ran {
		t.Fatalf("MakeDaily(day 0, later) = %+v, %v; want none made: there is one from today", again, err)
	}
	// Days skipped (a machine asleep) and kept going past Keep.
	for _, n := range []int{1, 2, 5, 6, 7, 9, 10, 11} {
		got, err := m.daily(t, context.Background(), dir, day(n))
		if err != nil || !got.Ran {
			t.Fatalf("MakeDaily(day %d) = %+v, %v; want one made", n, got, err)
		}
	}
	var want []string
	for _, n := range []int{5, 6, 7, 9, 10, 11} {
		want = append(want, dailyName(day(n)))
	}
	want = append([]string{dailyName(day(2))}, want...)
	if got := names(t, dir); !slices.Equal(got, want) {
		t.Fatalf("the backups kept are %v; want the last %d: %v", got, defaultKeep, want)
	}
}

// A backup is due once every has passed since the newest one, not before, and says when
// the next is due; a long gap (a machine off, or every changed since) makes one, not
// one per interval missed.
func TestABackupIsDueEveryInterval(t *testing.T) {
	m := newMachine(t)
	m.as(t).fill(t, "kept", 5)
	dir := t.TempDir()
	sched := backup.Schedule{Next: after(6 * time.Hour), Keep: defaultKeep}
	t0 := day(0)
	first, err := m.scheduled(t, context.Background(), dir, sched, t0)
	if err != nil || !first.Ran || !first.Next.Equal(t0.Add(6*time.Hour)) {
		t.Fatalf("the first run = %+v, %v; want one made, the next due 6h later", first, err)
	}
	early, err := m.scheduled(t, context.Background(), dir, sched, t0.Add(6*time.Hour-time.Second))
	if err != nil || early.Ran || !early.Next.Equal(t0.Add(6*time.Hour)) {
		t.Fatalf("a run a second early = %+v, %v; want none made, the next still due at 6h", early, err)
	}
	due, err := m.scheduled(t, context.Background(), dir, sched, t0.Add(6*time.Hour))
	if err != nil || !due.Ran {
		t.Fatalf("a run at 6h = %+v, %v; want one made", due, err)
	}
	// Three days off, then every made shorter: one backup, and none right after it.
	sched.Next = after(time.Hour)
	back := t0.Add(78 * time.Hour)
	if got, err := m.scheduled(t, context.Background(), dir, sched, back); err != nil || !got.Ran {
		t.Fatalf("the run after three days = %+v, %v; want one made", got, err)
	}
	if got, err := m.scheduled(t, context.Background(), dir, sched, back.Add(time.Minute)); err != nil || got.Ran {
		t.Fatalf("a run a minute later = %+v, %v; want none: the gap made one, not one an hour", got, err)
	}
	if n := len(names(t, dir)); n != 3 {
		t.Errorf("%d backups in the folder; want 3", n)
	}
}

// By a cron schedule, slots the machine was off for are not made up: one backup when it
// is back, none again until the next slot after it.
func TestACronScheduleSkipsTheSlotsMissed(t *testing.T) {
	m := newMachine(t)
	m.as(t).fill(t, "kept", 5)
	dir := t.TempDir()
	next, nerr := setup.BackupNext(config.Backup{Cron: "0 3 * * *"})
	if nerr != nil {
		t.Fatal(nerr)
	}
	sched := backup.Schedule{Next: next, Keep: defaultKeep}
	at3 := func(n int) time.Time { return time.Date(2026, 10, 1+n, 3, 0, 0, 0, time.Local) }
	if got, err := m.scheduled(t, context.Background(), dir, sched, at3(0)); err != nil || !got.Ran {
		t.Fatalf("the run at day 0's slot = %+v, %v; want one made", got, err)
	}
	back := at3(3).Add(7 * time.Hour) // off through three slots
	got, err := m.scheduled(t, context.Background(), dir, sched, back)
	if err != nil || !got.Ran || !got.Next.Equal(at3(4)) {
		t.Fatalf("the run when back = %+v, %v; want one made, the next at day 4's slot", got, err)
	}
	if got, err := m.scheduled(t, context.Background(), dir, sched, back.Add(time.Hour)); err != nil || got.Ran {
		t.Fatalf("a run an hour later = %+v, %v; want none: the missed slots are not made up", got, err)
	}
	if n := len(names(t, dir)); n != 2 {
		t.Errorf("%d backups in the folder; want 2", n)
	}
}

// keep lowered below what the folder holds deletes the oldest whole ones at the next
// run, even when no backup is due, and never the newest, a damaged one or the user's.
func TestKeepLoweredDeletesOnlyTheOldestWholeOnes(t *testing.T) {
	m := newMachine(t)
	m.as(t).fill(t, "kept", 5)
	dir := t.TempDir()
	var whole []string
	for n := 1; n <= defaultKeep; n++ {
		whole = append(whole, stand(t, dir, day(n)))
	}
	broken := filepath.Join(dir, dailyName(day(0)))
	write(t, broken, "not an age file", 0o600)
	mine := filepath.Join(dir, "kith-backup-by-hand.tar.age")
	write(t, mine, "mine", 0o600)

	got, err := m.scheduled(t, context.Background(), dir, backup.Schedule{Next: after(24 * time.Hour), Keep: 2}, day(defaultKeep).Add(time.Hour))
	if err != nil || got.Ran {
		t.Fatalf("MakeDaily(keep 2, none due) = %+v, %v; want none made", got, err)
	}
	slices.Sort(got.Deleted)
	if want := whole[:defaultKeep-2]; !slices.Equal(got.Deleted, want) {
		t.Fatalf("deleted %v; want the oldest %d whole ones %v", got.Deleted, len(want), want)
	}
	for _, p := range append(whole[defaultKeep-2:], broken, mine) {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s is gone: %v", filepath.Base(p), err)
		}
	}
}

// A backup that fails deletes nothing, however low keep is: failing backups never eat
// the good ones.
func TestAFailedBackupDeletesNothingWhateverKeepSays(t *testing.T) {
	m := newMachine(t)
	m.as(t).fill(t, "kept", 300)
	dir := t.TempDir()
	for n := 1; n <= 4; n++ {
		stand(t, dir, day(n))
	}
	before := names(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := m.scheduled(t, ctx, dir, backup.Schedule{Next: after(time.Hour), Keep: 1}, day(20))
	if !errors.Is(err, context.Canceled) || got.Ran || len(got.Deleted) != 0 {
		t.Fatalf("MakeDaily(canceled, keep 1) = %+v, %v; want context.Canceled and nothing deleted", got, err)
	}
	if after := names(t, dir); !slices.Equal(after, before) {
		t.Fatalf("the folder holds %v; want it as it was, %v", after, before)
	}
}

// A clock set back makes today's backup though later-named ones are there, and that
// backup, the newest made, is never the one rotation deletes.
func TestABackupMadeAfterTheClockWentBackIsKept(t *testing.T) {
	m := newMachine(t)
	m.as(t).fill(t, "kept", 5)
	dir := t.TempDir()
	for n := 20; n < 20+defaultKeep; n++ {
		stand(t, dir, day(n))
	}
	got, err := m.daily(t, context.Background(), dir, day(3))
	if err != nil || !got.Ran {
		t.Fatalf("MakeDaily(earlier day) = %+v, %v; want one made", got, err)
	}
	if _, err := os.Stat(got.Made.Path); err != nil {
		t.Fatalf("the backup just made is gone: %v (deleted %v)", err, got.Deleted)
	}
	if len(got.Deleted) != 0 {
		t.Errorf("deleted %v; want none: the later ones are the last %d by name, the new one is never deleted", got.Deleted, defaultKeep)
	}
}

// Only whole daily backups count: one that is not (damaged, or a partial a stopped
// kithd left under the final name) neither stands for today's nor counts towards the
// seven, and is not deleted; a stale partial is cleared, a fresh one (being written) and
// a file the user put there are left alone.
func TestOnlyWholeDailyBackupsCountAndNothingElseIsDeleted(t *testing.T) {
	m := newMachine(t)
	m.as(t).fill(t, "kept", 5)
	dir := t.TempDir()
	var whole []string
	for n := 1; n < defaultKeep; n++ { // six whole ones
		whole = append(whole, stand(t, dir, day(n)))
	}
	broken := filepath.Join(dir, dailyName(day(10).Add(-2*time.Hour)))
	write(t, broken, "not an age file", 0o600) // today's, but not whole
	cut := filepath.Join(dir, dailyName(day(0)))
	write(t, cut, "age-encr", 0o600)
	mine := filepath.Join(dir, "my-own-backup.tar.age")
	write(t, mine, "mine", 0o600)
	stale := filepath.Join(dir, "."+dailyName(day(8))+".partial-123")
	write(t, stale, "half", 0o600)
	if err := os.Chtimes(stale, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, "."+dailyName(day(10))+".partial-456")
	write(t, fresh, "being written", 0o600)

	got, err := m.daily(t, context.Background(), dir, day(10))
	if err != nil || !got.Ran {
		t.Fatalf("MakeDaily() = %+v, %v; want one made: today's file is not whole", got, err)
	}
	if len(got.Deleted) != 0 {
		t.Errorf("deleted %v; want none: six whole ones and today's are seven", got.Deleted)
	}
	if !slices.Contains(got.Ignored, broken) || !slices.Contains(got.Ignored, cut) {
		t.Errorf("Ignored = %v; want the two that are not whole", got.Ignored)
	}
	for _, p := range append(whole, broken, cut, mine, fresh, got.Made.Path) {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s is gone: %v", filepath.Base(p), err)
		}
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("a partial a stopped kithd left two days ago is still there")
	}
}

// A daily backup interrupted (kithd stopping) leaves no file and deletes none: rotation
// only follows a backup made. More than Keep are there (as a clock set back leaves), so
// a rotation would have something to delete.
func TestAnInterruptedDailyBackupDeletesNothing(t *testing.T) {
	m := newMachine(t)
	m.as(t).fill(t, "kept", 300)
	dir := t.TempDir()
	for n := 1; n <= defaultKeep+2; n++ {
		stand(t, dir, day(n))
	}
	before := names(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := m.daily(t, ctx, dir, day(20))
	if !errors.Is(err, context.Canceled) || got.Ran {
		t.Fatalf("MakeDaily(canceled) = %+v, %v; want context.Canceled", got, err)
	}
	if after := names(t, dir); !slices.Equal(after, before) {
		t.Fatalf("the folder holds %v; want it as it was, %v", after, before)
	}
}

// A backup made without its pictures restores without them, leaving the picture cache
// as it was.
func TestABackupWithoutPicturesRestoresWithout(t *testing.T) {
	old := newMachine(t)
	old.as(t).fill(t, "kept", 5)
	cfg, err := config.Load(old.configPath)
	if err != nil {
		t.Fatal(err)
	}
	src := old.source(t, cfg)
	src.MediaDir = ""
	out := filepath.Join(t.TempDir(), "b.tar.age")
	if _, cerr := backup.Create(context.Background(), src, out, testKey.Recipient()); cerr != nil {
		t.Fatal(cerr)
	}
	fresh := newMachine(t).as(t)
	asked := 0
	got, err := backup.Restore(context.Background(), out, fresh.configPath, false, answer(testKey, &asked), keyOf)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if got.Pictures != 0 {
		t.Errorf("Restore() put back %d pictures; the backup held none", got.Pictures)
	}
	if _, err := os.Stat(fresh.storage(t).MediaDir()); err == nil {
		t.Error("a picture cache was made with nothing to put in it")
	}
}

// Pictures restored into a picture cache that has some already add to it: one there
// already (named by its message, so the same picture) stays as it is.
func TestRestoredPicturesAddToThePictureCache(t *testing.T) {
	old := newMachine(t)
	old.as(t).fill(t, "kept", 5)
	archive := old.backUp(t)
	there := newMachine(t).as(t)
	there.fill(t, "local", 5)
	media := there.storage(t).MediaDir()
	if err := os.Remove(filepath.Join(media, "b2.jpg")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(media, "c3.gif"), "only here", 0o600)
	asked := 0
	got, err := backup.Restore(context.Background(), archive, there.configPath, true, answer(testKey, &asked), keyOf)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	for name, want := range map[string]string{"a1.png": "local picture a", "b2.jpg": "kept picture b", "c3.gif": "only here"} {
		body, err := os.ReadFile(filepath.Join(media, name)) // #nosec G304 -- the test's own file
		if err != nil || string(body) != want {
			t.Errorf("%s = %q, %v; want %q", name, body, err, want)
		}
	}
	if got.Pictures != 1 || got.PicturesThere != 1 {
		t.Errorf("Restore() = %d put back, %d there; want 1 and 1", got.Pictures, got.PicturesThere)
	}
}

// A backup key is read as the user saved it, alone or in the file kith offered to
// write; anything else is not one.
func TestABackupKeyIsReadAsSaved(t *testing.T) {
	t.Parallel()
	saved := "# kith backup key, made 2026-10-27\n# public key: " + testKey.Recipient().String() + "\n" + testKey.String() + "\n"
	for _, text := range []string{testKey.String(), "  " + testKey.String() + "\n", saved} {
		id, err := backup.ParseKey(text)
		if err != nil || id.String() != testKey.String() {
			t.Errorf("ParseKey(%q) = %v; want the key", text, err)
		}
	}
	for _, text := range []string{"", "# only a comment\n", "AGE-SECRET-KEY-1NOTAKEY", testKey.Recipient().String()} {
		if _, err := backup.ParseKey(text); !errors.Is(err, backup.ErrWrongKey) {
			t.Errorf("ParseKey(%q) = %v; want ErrWrongKey", text, err)
		}
	}
}

// No key on the machine is ErrNoKey; a key kept reads back the same.
func TestTheBackupKeyKeptReadsBack(t *testing.T) {
	m := newMachine(t)
	m.as(t).fill(t, "kept", 1)
	s := m.storage(t)
	if _, err := backup.LoadKey(s); !errors.Is(err, backup.ErrNoKey) {
		t.Fatalf("LoadKey() with none = %v; want ErrNoKey", err)
	}
	if err := backup.SaveKey(s, testKey.Recipient()); err != nil {
		t.Fatal(err)
	}
	to, err := backup.LoadKey(s)
	if err != nil || to.String() != testKey.Recipient().String() {
		t.Fatalf("LoadKey() = %v, %v; want the key kept", to, err)
	}
}
