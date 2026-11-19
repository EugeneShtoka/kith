package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"

	"github.com/BurntSushi/toml"
)

// kithd's systemd unit sets PrivateTmp=, which gives it a /tmp and a /var/tmp of its
// own, emptied whenever it stops: scheduled backups under either vanish then. Such a
// folder is allowed, and warned about: in the log when kithd starts or a reload moves
// the backups there, and to clients in Status until it is dismissed for that folder.
// The folders dismissed are kept in a file, for good.

// backupWarningsFile is what survives a restart.
type backupWarningsFile struct {
	// Dismissed is the folders whose warning was dismissed.
	Dismissed []string `toml:"dismissed"`
}

// BackupWarning is the warning about the folders the scheduled backups go to.
type BackupWarning struct {
	path string
	log  *slog.Logger
	// changed tells the attached clients the warning changed (Streams.BackupWarningChanged).
	changed func()

	mu   sync.Mutex
	file backupWarningsFile
	// dirs is the folders under /tmp or /var/tmp the backups go to now, in order.
	dirs []string
}

// NewBackupWarning reads the folders dismissed from path; a file that cannot be read
// dismissed none.
func NewBackupWarning(path string, changed func(), log *slog.Logger) *BackupWarning {
	w := &BackupWarning{path: path, log: log, changed: changed}
	if _, err := toml.DecodeFile(path, &w.file); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn("read which backup folders were dismissed failed; none were", "path", path, "err", err)
		w.file = backupWarningsFile{}
	}
	return w
}

// backupWhy is the warning about dir.
func backupWhy(dir string) string {
	return fmt.Sprintf("Backups in %s vanish when kithd stops: its systemd unit gives it a /tmp and "+
		"/var/tmp of its own (PrivateTmp=), emptied then. Choose a folder elsewhere to keep them.", dir)
}

// Use takes the folders the scheduled backups go to now, logging each under /tmp or
// /var/tmp it did not have, and tells the clients when that changes the warning.
func (w *BackupWarning) Use(dirs []string) {
	var tmp []string
	for _, d := range dirs {
		d = filepath.Clean(d)
		if underTmp(d) && !slices.Contains(tmp, d) {
			tmp = append(tmp, d)
		}
	}
	w.mu.Lock()
	before := w.currentLocked()
	for _, d := range tmp {
		if !slices.Contains(w.dirs, d) {
			w.log.Warn(backupWhy(d), "op", "backup folder")
		}
	}
	w.dirs = tmp
	after := w.currentLocked()
	w.mu.Unlock()
	if after != before {
		w.changed()
	}
}

// Current is the folder to warn about, and the warning; "" when there is none.
func (w *BackupWarning) Current() (dir, why string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if dir = w.currentLocked(); dir != "" {
		why = backupWhy(dir)
	}
	return dir, why
}

// currentLocked is the first folder warned about that was not dismissed.
func (w *BackupWarning) currentLocked() string {
	for _, d := range w.dirs {
		if !slices.Contains(w.file.Dismissed, d) {
			return d
		}
	}
	return ""
}

// Dismiss stops the warning about dir for good. Only a folder warned about now is
// kept, so the file holds only folders the backups went to.
func (w *BackupWarning) Dismiss(dir string) error {
	dir = filepath.Clean(dir)
	w.mu.Lock()
	if !slices.Contains(w.dirs, dir) || slices.Contains(w.file.Dismissed, dir) {
		w.mu.Unlock()
		return nil
	}
	before := w.currentLocked()
	file := backupWarningsFile{Dismissed: append(slices.Clone(w.file.Dismissed), dir)}
	if err := writeTOML(w.path, file, "the dismissed backup folders"); err != nil {
		w.mu.Unlock()
		return err
	}
	w.file = file
	after := w.currentLocked()
	w.mu.Unlock()
	if after != before {
		w.changed()
	}
	return nil
}
