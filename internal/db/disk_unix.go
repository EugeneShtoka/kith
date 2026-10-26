//go:build !windows

package db

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// diskOf is the filesystem holding dir.
func diskOf(dir string) (disk, error) {
	dir = filepath.Clean(dir)
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return disk{}, fmt.Errorf("statfs %s: %w", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return disk{}, fmt.Errorf("stat %s: %w", dir, err)
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return disk{}, errors.New("no device for " + dir)
	}
	return disk{id: fmt.Sprint(sys.Dev), free: count(st.Bavail) * count(st.Bsize)}, nil
}

// count is a statfs field, whose type differs by system, as the count or size it is.
func count[T int64 | uint64 | uint32](v T) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}
