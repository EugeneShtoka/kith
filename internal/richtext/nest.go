package richtext

import (
	"html"
	"slices"
	"strings"
)

// Mark is one formatting range of a text as markup, in byte offsets: a network that
// formats by ranges (Telegram's entities, Signal's styles) gives its ranges as marks.
type Mark struct {
	Start, End  int
	Open, Close string
}

// Nested renders marks as properly nested markup: at each boundary, close ending
// marks (reopening any closed early), then open starting ones, longest outermost.
func Nested(text string, marks []Mark) string {
	bounds := []int{0, len(text)}
	for _, m := range marks {
		bounds = append(bounds, m.Start, m.End)
	}
	slices.Sort(bounds)
	bounds = slices.Compact(bounds)
	var out strings.Builder
	var open []Mark
	for i, at := range bounds {
		// Close what ends here; reopen what was closed on the way.
		if lowest := slices.IndexFunc(open, func(m Mark) bool { return m.End == at }); lowest >= 0 {
			var reopen []Mark
			for len(open) > lowest {
				top := open[len(open)-1]
				open = open[:len(open)-1]
				out.WriteString(top.Close)
				if top.End != at {
					reopen = append(reopen, top)
				}
			}
			for _, m := range slices.Backward(reopen) {
				out.WriteString(m.Open)
				open = append(open, m)
			}
		}
		starting := slices.DeleteFunc(slices.Clone(marks), func(m Mark) bool { return m.Start != at })
		slices.SortStableFunc(starting, func(a, b Mark) int { return b.End - a.End })
		for _, m := range starting {
			out.WriteString(m.Open)
			open = append(open, m)
		}
		if i+1 < len(bounds) {
			out.WriteString(html.EscapeString(text[at:bounds[i+1]]))
		}
	}
	for _, m := range slices.Backward(open) {
		out.WriteString(m.Close)
	}
	return out.String()
}
