package audio

import "net"

// Attach and AttachVLC expose the protocol half of a session for tests against a stub.
func Attach(conn net.Conn, speed float64) *Session {
	return attachTo(conn, State{Speed: speed}, mpvIPC{}, nil, "")
}

func AttachVLC(conn net.Conn, speed float64) *Session {
	return attachTo(conn, State{Speed: speed}, vlcRC{}, nil, "")
}

// Dialect reports which control language a program name implies.
func Dialect(program string) string {
	if _, vlc := dialectOf(program).(vlcRC); vlc {
		return "vlc"
	}
	return "mpv"
}
