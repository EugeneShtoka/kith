package human

import (
	"testing"
	"time"
)

func TestDurationLeavesOutEmptyUnits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{time.Minute, "1m"},
		{90 * time.Second, "1m 30s"},
		{45 * time.Second, "45s"},
		{2*time.Hour + 5*time.Second, "2h 5s"},
		{time.Hour + 2*time.Minute + 3*time.Second, "1h 2m 3s"},
		{time.Hour, "1h"},
		{90*time.Second + 500*time.Millisecond, "1m 30.5s"},
		{1500 * time.Millisecond, "1.5s"},
		{250 * time.Millisecond, "250ms"},
		{0, "0s"},
	} {
		if got := Duration(tc.d); got != tc.want {
			t.Errorf("Duration(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
