package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// DefaultMaxBytes caps a log file before rotation; with path+".1" kept, the log
// uses at most twice this.
const DefaultMaxBytes = 1 << 20

// FileName is the client's log file inside its state directory.
const FileName = "kith.log"

// File is an append-only log rotated to path+".1" when a write would pass the cap.
// Safe for concurrent use.
type File struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

// OpenFile opens the log at path (0600, parents 0700); maxBytes <= 0 means
// DefaultMaxBytes.
func OpenFile(path string, maxBytes int64) (*File, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("logging: create log directory: %w", err)
	}
	lf := &File{path: path, max: maxBytes}
	if err := lf.open(); err != nil {
		return nil, err
	}
	return lf, nil
}

// Path is where the log is written.
func (lf *File) Path() string { return lf.path }

func (lf *File) open() error {
	f, err := os.OpenFile(lf.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("logging: open log file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close() // the Stat error is the one worth reporting
		return fmt.Errorf("logging: stat log file: %w", err)
	}
	lf.f, lf.size = f, info.Size()
	return nil
}

// Write appends p, rotating first if it would pass the cap. A line longer than the
// cap is written whole into a fresh file.
func (lf *File) Write(p []byte) (int, error) {
	lf.mu.Lock()
	defer lf.mu.Unlock()
	if lf.f == nil {
		return 0, os.ErrClosed
	}
	if lf.size > 0 && lf.size+int64(len(p)) > lf.max {
		if err := lf.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := lf.f.Write(p)
	lf.size += int64(n)
	if err != nil {
		return n, fmt.Errorf("logging: write log file: %w", err)
	}
	return n, nil
}

// rotate moves the current file to path+".1" and opens a fresh one. Called with mu held.
func (lf *File) rotate() error {
	if err := lf.f.Close(); err != nil {
		return fmt.Errorf("logging: close log file for rotation: %w", err)
	}
	lf.f = nil
	// Reopen even if the rename fails, so logging continues past the cap.
	renameErr := os.Rename(lf.path, lf.path+".1")
	if err := lf.open(); err != nil {
		return err
	}
	if renameErr != nil {
		return fmt.Errorf("logging: rotate log file: %w", renameErr)
	}
	return nil
}

// Close closes the file; later writes fail with os.ErrClosed.
func (lf *File) Close() error {
	lf.mu.Lock()
	defer lf.mu.Unlock()
	if lf.f == nil {
		return nil
	}
	err := lf.f.Close()
	lf.f = nil
	if err != nil {
		return fmt.Errorf("logging: close log file: %w", err)
	}
	return nil
}
