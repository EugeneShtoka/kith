package notify

import "time"

// ExportedExpireTimeout exposes the freedesktop timeout mapping to the package's
// external tests.
func ExportedExpireTimeout(d time.Duration) int32 { return expireTimeout(d) }
