package domain

import (
	"fmt"
	"time"
)

// SeatHolder is where the window holding the daemon's seat runs, shown to a refused client.
type SeatHolder struct {
	PID   int
	TTY   string
	Host  string
	Since time.Time
}

// String reads like "pid 4312 on /dev/pts/3, since 10:41".
func (h SeatHolder) String() string {
	where := fmt.Sprintf("pid %d", h.PID)
	if h.TTY != "" {
		where += " on " + h.TTY
	}
	if h.Host != "" {
		where += " (" + h.Host + ")"
	}
	if !h.Since.IsZero() {
		where += ", since " + h.Since.Format("15:04")
	}
	return where
}
