package main

import (
	"testing"
	"time"
)

// --since takes a date or an age, --last a number of messages; both may be given, and
// a number given to --since is refused rather than read as a count.
func TestExportLimitsByDateOrCount(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	span, err := exportSpan("7d", 500, now)
	if err != nil || !span.Since.Equal(now.Add(-7*24*time.Hour)) || span.Count != 500 {
		t.Errorf("--since 7d --last 500 = %+v, %v", span, err)
	}
	if span, err := exportSpan("", 0, now); err != nil || span.Given() {
		t.Errorf("no limit = %+v, %v; want everything", span, err)
	}
	for _, c := range []struct {
		since string
		last  int
	}{{"500", 0}, {"", -1}, {"someday", 0}} {
		if _, err := exportSpan(c.since, c.last, now); err == nil {
			t.Errorf("--since %q --last %d was taken", c.since, c.last)
		}
	}
}
