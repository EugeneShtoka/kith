package tui

import (
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/unicode/bidi"
)

// Reordering must preserve width and rune count, or the emitted row differs from
// the measured one.
func TestReorderPreservesWidth(t *testing.T) {
	t.Parallel()

	bodies := []string{
		"חחח כן, אה?! מקווה שנצליח לשרוד כמה שיותר 🤣",
		"וואלה מדהים, נשמע שאתה מוכן לפנסיה 🤣 איזה כיף זה עבודה ברגוע ולחיות נורמלי",
		"אמא בסדר, היה לה ממש כשה, במיוחד שהינו צריכים לצאת פעמיים מבולגריה ל3 חודשים",
		"שלום 🤣 hello",
	}
	for _, body := range bodies {
		for _, dir := range []bidi.Direction{bidi.LeftToRight, bidi.RightToLeft} {
			vis := reorder(body, dir)
			before, after := ansi.StringWidth(body), ansi.StringWidth(vis)
			if before != after {
				t.Errorf("reorder(%.20q…, %v): width %d -> %d", body, dir, before, after)
			}
			if r1, r2 := utf8.RuneCountInString(body), utf8.RuneCountInString(vis); r1 != r2 {
				t.Errorf("reorder(%.20q…, %v): runes %d -> %d", body, dir, r1, r2)
			}
		}
	}
}
