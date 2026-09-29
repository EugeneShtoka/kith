package protoconv

import "math"

// ClampInt32 is n for an int32 wire field, clamped into range: a count that
// overflowed would otherwise arrive negative.
func ClampInt32(n int) int32 {
	return int32(min(max(n, math.MinInt32), math.MaxInt32)) // #nosec G115 -- clamped to the range
}
