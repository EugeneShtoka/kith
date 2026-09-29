package protoconv

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// toProtoTime sends a zero time as unset, not as the epoch: the UI tests IsZero.
func toProtoTime(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// fromProtoTime keeps unset as the zero time and localizes, because AsTime
// returns UTC and the display layer formats without converting.
func fromProtoTime(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime().Local()
}

// unixMillis is t in milliseconds, with the zero time as 0 rather than the epoch.
func unixMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// fromUnixMillis is the inverse of unixMillis.
func fromUnixMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
