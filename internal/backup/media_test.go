package backup_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/backup"
)

// archived gives m's media archive a copy per name, saying word.
func (m machine) archived(t *testing.T, word string, names ...string) string {
	t.Helper()
	dir := m.storage(t).MediaArchiveDir()
	for _, name := range names {
		write(t, filepath.Join(dir, name), word+" "+name, 0o600)
	}
	return dir
}

// mediaBackUp writes a media backup of m's archive (m made current) and returns its path.
func (m machine) mediaBackUp(t *testing.T) string {
	t.Helper()
	m.as(t)
	out := filepath.Join(t.TempDir(), "kith-media.tar.age")
	made, err := backup.CreateMedia(context.Background(), m.storage(t).MediaArchiveDir(), out, testKey.Recipient())
	if err != nil {
		t.Fatalf("CreateMedia() error = %v", err)
	}
	if len(made.Entries) != 1 || made.Entries[0].Name != "archive/" {
		t.Fatalf("CreateMedia() = %+v; want the archive as one entry", made)
	}
	return out
}

// A media backup puts the archive back into a restored config's, adding what it lacks
// and keeping what it holds, with nothing left staged beside it.
func TestAMediaBackupAddsToTheArchive(t *testing.T) {
	old := newMachine(t)
	old.as(t).fill(t, "kept", 1)
	old.archived(t, "kept", "a.jpg", "b.ogg")
	archive := old.mediaBackUp(t)

	there := newMachine(t).as(t)
	there.fill(t, "local", 1)
	dir := there.archived(t, "local", "a.jpg", "c.mp4")
	asked := 0
	got, err := backup.RestoreMedia(context.Background(), archive, there.configPath, answer(testKey, &asked))
	if err != nil {
		t.Fatalf("RestoreMedia() error = %v", err)
	}
	for name, want := range map[string]string{"a.jpg": "local a.jpg", "b.ogg": "kept b.ogg", "c.mp4": "local c.mp4"} {
		body, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- the test's own file
		if err != nil || string(body) != want {
			t.Errorf("%s = %q, %v; want %q", name, body, err, want)
		}
	}
	if got.Archived != 1 || got.ArchivedThere != 1 || got.ArchiveFolder != dir || asked != 1 {
		t.Errorf("RestoreMedia() = %+v, key asked %d times; want 1 put back, 1 there, in %s, asked once", got, asked, dir)
	}
	if staged, _ := filepath.Glob(filepath.Join(filepath.Dir(dir), ".restore-*")); len(staged) != 0 {
		t.Errorf("left staged: %v", staged)
	}
}

// Each restore refuses the other kind of backup, saying which to use, before touching
// anything.
func TestEachRestoreRefusesTheOtherKind(t *testing.T) {
	old := newMachine(t)
	old.as(t).fill(t, "kept", 1)
	old.archived(t, "kept", "a.jpg")
	media, history := old.mediaBackUp(t), old.backUp(t)

	fresh := newMachine(t).as(t)
	asked := 0
	if _, err := backup.Restore(context.Background(), media, fresh.configPath, false, answer(testKey, &asked), keyOf); err == nil || !strings.Contains(err.Error(), "--media") {
		t.Errorf("Restore(a media backup) = %v; want it to name --media", err)
	}
	if _, err := os.Stat(fresh.configPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Restore(a media backup) wrote the config: %v", err)
	}
	there := newMachine(t).as(t)
	there.fill(t, "local", 1)
	before := fingerprint(t, there.home)
	if _, err := backup.RestoreMedia(context.Background(), history, there.configPath, answer(testKey, &asked)); err == nil || !strings.Contains(err.Error(), "not a media backup") {
		t.Errorf("RestoreMedia(a history backup) = %v; want it refused", err)
	}
	unchanged(t, there.home, before)
}

// A media backup cut short or altered is found before anything is placed.
func TestADamagedMediaBackupPlacesNothing(t *testing.T) {
	old := newMachine(t)
	old.as(t).fill(t, "kept", 1)
	old.archived(t, "kept", "a.jpg", "b.jpg")
	whole, err := os.ReadFile(old.mediaBackUp(t)) // #nosec G304 -- the test's own file
	if err != nil {
		t.Fatal(err)
	}
	damaged := map[string][]byte{}
	for _, at := range []int{20, 200, len(whole) / 2, len(whole) - 300, len(whole) - 1} {
		damaged[fmt.Sprintf("cut at %d", at)] = whole[:at]
	}
	for _, at := range []int{250, len(whole) / 2, len(whole) - 20} {
		flipped := slices.Clone(whole)
		flipped[at] ^= 0x40
		damaged[fmt.Sprintf("byte %d altered", at)] = flipped
	}

	there := newMachine(t).as(t)
	there.fill(t, "local", 1)
	before := fingerprint(t, there.home)
	for name, body := range damaged {
		archive := filepath.Join(t.TempDir(), "kith-media.tar.age")
		write(t, archive, string(body), 0o600)
		asked := 0
		if _, err := backup.RestoreMedia(context.Background(), archive, there.configPath, answer(testKey, &asked)); !errors.Is(err, backup.ErrDamaged) {
			t.Errorf("%s: RestoreMedia() = %v; want ErrDamaged", name, err)
		}
	}
	unchanged(t, there.home, before)
}

// Media backups are named apart and keep their own count: the history's scheduled
// backups beside them are neither counted nor deleted.
func TestMediaBackupsKeepTheirOwnCount(t *testing.T) {
	m := newMachine(t)
	m.as(t).fill(t, "kept", 1)
	src := m.archived(t, "kept", "a.jpg")
	dir := filepath.Join(m.home, "backups")
	var dailies []string
	for i := range 5 {
		dailies = append(dailies, filepath.Base(stand(t, dir, day(-10+i))))
	}
	week := backup.Schedule{Next: after(7 * 24 * time.Hour), Keep: 3}
	for i := range 6 {
		done, err := backup.MakeMedia(context.Background(), src, testKey.Recipient(), dir, week, day(7*i))
		if err != nil || !done.Ran {
			t.Fatalf("MakeMedia(week %d) = %+v, %v", i, done, err)
		}
	}
	want := dailies
	for i := 3; i < 6; i++ {
		want = append(want, "kith-media-"+day(7*i).Format("2006-01-02-150405")+".tar.age")
	}
	if got := names(t, dir); !slices.Equal(got, want) {
		t.Errorf("backups = %v; want %v", got, want)
	}
	if done, err := backup.MakeMedia(context.Background(), src, testKey.Recipient(), dir, week, day(35).Add(time.Hour)); err != nil || done.Ran {
		t.Errorf("MakeMedia an hour after one = %+v, %v; want none due", done, err)
	}
}
