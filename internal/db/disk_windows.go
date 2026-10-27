//go:build windows

package db

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Plain syscall because this package imports only the standard library.
var procGetDiskFreeSpaceExW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

// diskOf is the volume holding dir.
func diskOf(dir string) (disk, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return disk{}, fmt.Errorf("resolve %s: %w", dir, err)
	}
	p, err := syscall.UTF16PtrFromString(abs)
	if err != nil {
		return disk{}, fmt.Errorf("encode %s: %w", dir, err)
	}
	var free uint64
	if ok, _, callErr := procGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&free)), 0, 0); ok == 0 {
		return disk{}, fmt.Errorf("free space on %s: %w", dir, callErr)
	}
	return disk{id: strings.ToLower(filepath.VolumeName(abs)), free: free}, nil
}
