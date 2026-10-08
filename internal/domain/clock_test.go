package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/rivo/uniseg"
)

// A timeline's times are a column: in every style, every minute of the day pads to
// the same width, so no row's body starts a cell off (the CJK styles' words are two
// cells a character).
func TestEveryTimeOfDayFillsTheColumnExactly(t *testing.T) {
	t.Parallel()
	for _, style := range TimeStyles() {
		c, err := ParseClock(style[0], "", "")
		if err != nil {
			t.Fatal(err)
		}
		want := c.TimeWidth()
		for minute := range 24 * 60 {
			at := time.Date(2026, 10, 8, minute/60, minute%60, 0, 0, time.Local)
			if got := uniseg.StringWidth(c.PaddedTime(at)); got != want {
				t.Fatalf("%s: %q is %d cells, the column %d", style[0], c.PaddedTime(at), got, want)
			}
		}
	}
}

func TestTimeStylesWriteTheTime(t *testing.T) {
	t.Parallel()
	morning := time.Date(2026, 10, 8, 9, 5, 7, 0, time.Local)
	evening := time.Date(2026, 10, 8, 21, 30, 0, 0, time.Local)
	for _, tc := range []struct {
		style, morning, evening, seconds string
	}{
		{"", "09:05", "21:30", "09:05:07"},
		{"24h", "09:05", "21:30", "09:05:07"},
		{"12h", "9:05 AM", "9:30 PM", "9:05:07 AM"},
		{"12h-lower", "9:05am", "9:30pm", "9:05:07am"},
		{"zh", "上午9:05", "下午9:30", "上午9:05:07"},
		{"ja", "午前9:05", "午後9:30", "午前9:05:07"},
		{"ko", "오전 9:05", "오후 9:30", "오전 9:05:07"},
	} {
		c, err := ParseClock(tc.style, "", "")
		if err != nil {
			t.Fatalf("%q: %v", tc.style, err)
		}
		if got := c.Time(morning); got != tc.morning {
			t.Errorf("%q morning = %q, want %q", tc.style, got, tc.morning)
		}
		if got := c.Time(evening); got != tc.evening {
			t.Errorf("%q evening = %q, want %q", tc.style, got, tc.evening)
		}
		if got := c.TimeSeconds(morning); got != tc.seconds {
			t.Errorf("%q with seconds = %q, want %q", tc.style, got, tc.seconds)
		}
	}
	if _, err := ParseClock("13h", "", ""); err == nil || !strings.Contains(err.Error(), "24h") {
		t.Errorf("an unknown style = %v, want it refused, naming the ones there are", err)
	}
}

// Runs of Y, M and D are filled in, anything else is written as it stands.
func TestDatePatternsWriteTheDate(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 3, 5, 12, 0, 0, 0, time.Local) // a Thursday
	for _, tc := range []struct{ pattern, want string }{
		{"YYYY-MM-DD", "2026-03-05"},
		{"DD.MM.YYYY", "05.03.2026"},
		{"MM/DD/YY", "03/05/26"},
		{"D/M/YY", "5/3/26"},
		{"DDD, DD MMM YYYY", "Thu, 05 Mar 2026"},
		{"DDDD, D MMMM YYYY", "Thursday, 5 March 2026"},
		{"YYYY年M月D日", "2026年3月5日"},
		{"week of D", "week of 5"},
		{"", "2026-03-05"}, // the short default
	} {
		c, err := ParseClock("", "", tc.pattern)
		if err != nil {
			t.Fatalf("%q: %v", tc.pattern, err)
		}
		if got := c.ShortDate(at); got != tc.want {
			t.Errorf("%q = %q, want %q", tc.pattern, got, tc.want)
		}
	}
	if got := (Clock{}).LongDate(at); got != "Thu, 05 Mar 2026" {
		t.Errorf("the default long date = %q", got)
	}
	for _, bad := range []string{"YYY-MM-DD", "Y", "YYYYY", "MMMMM D", "DDDDD"} {
		if _, err := ParseDatePattern(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
