//go:build windows

package audio

import "errors"

// ErrUnsupported: on Windows mpv's IPC is a named pipe and VLC has no --rc-unix, so
// fail up front rather than wait out socketWait.
var ErrUnsupported = errors.New("audio: in-app voice-note playback is not supported on Windows yet " +
	"(mpv's control channel there is a named pipe) — save the file and open it instead")

func controllable() error { return ErrUnsupported }
