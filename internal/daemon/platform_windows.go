//go:build windows

package daemon

// restrictSocket is a no-op: a Windows AF_UNIX socket is authorized by ACL and
// inherits %LOCALAPPDATA%\kith's owner-only ACL.
func restrictSocket(string) error { return nil }

// secureDir is a no-op: %LOCALAPPDATA% is per user and its ACL is owner-only.
func secureDir(string) error { return nil }
