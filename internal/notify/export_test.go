package notify

import "time"

// ExportedExpireTimeout exposes expireTimeout to external tests.
func ExportedExpireTimeout(d time.Duration) int32 { return expireTimeout(d) }
