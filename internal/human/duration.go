// Package human formats values for people to read: messages, errors and log lines.
package human

import (
	"fmt"
	"strings"
	"time"
)

// Duration formats a span as Go does, but leaves out the empty units and puts a
// space between the rest: "1m", "1m 30s", "2h 5s", "1m 30.5s". Under a minute it is
// Go's own form ("45s", "1.5s", "250ms").
//
// Every duration a person reads goes through here; scripts/durationcheck.go fails
// the build on one handed to fmt as it is (Go's own form prints "1m0s").
func Duration(d time.Duration) string {
	if d < time.Minute {
		return d.String()
	}
	var parts []string
	if h := int64(d / time.Hour); h > 0 {
		parts = append(parts, fmt.Sprintf("%dh", h))
	}
	if m := int64(d % time.Hour / time.Minute); m > 0 {
		parts = append(parts, fmt.Sprintf("%dm", m))
	}
	if rest := d % time.Minute; rest > 0 {
		parts = append(parts, rest.String())
	}
	return strings.Join(parts, " ")
}
