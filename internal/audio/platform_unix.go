//go:build !windows

package audio

// controllable reports whether a player can be driven here: yes, over a unix socket.
func controllable() error { return nil }
