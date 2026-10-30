package richtext

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// confSpan is a Span as testdata/spans.json writes it, an absent field its zero.
type confSpan struct {
	Start     int    `json:"start"`
	End       int    `json:"end"`
	Bold      bool   `json:"bold,omitempty"`
	Italic    bool   `json:"italic,omitempty"`
	Code      bool   `json:"code,omitempty"`
	Strike    bool   `json:"strike,omitempty"`
	Underline bool   `json:"underline,omitempty"`
	Link      bool   `json:"link,omitempty"`
	Quote     bool   `json:"quote,omitempty"`
	Heading   bool   `json:"heading,omitempty"`
	Spoiler   bool   `json:"spoiler,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Href      string `json:"href,omitempty"`
}

// The spans table is what kithd sends for each markup (Parse), its offsets in UTF-8
// bytes, and the text each span covers: the desktop's tests hold its conversion to
// JavaScript's UTF-16 indices to the same pieces.
func TestTheSpansTableIsWhatParseDraws(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/spans.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Cases []struct {
			Name   string     `json:"name"`
			Markup string     `json:"markup"`
			Text   string     `json:"text"`
			Spans  []confSpan `json:"spans"`
			Pieces []string   `json:"pieces"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatal(err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("spans.json has no cases")
	}
	for _, c := range table.Cases {
		text, spans := Parse(Sanitize(c.Markup))
		got := make([]confSpan, len(spans))
		pieces := make([]string, len(spans))
		for i, s := range spans {
			got[i] = confSpan(s)
			pieces[i] = text[s.Start:s.End]
		}
		if text != c.Text || !reflect.DeepEqual(got, c.Spans) || !reflect.DeepEqual(pieces, c.Pieces) {
			t.Errorf("%s: Parse(%q) = %q, %+v (pieces %q); the table says %q, %+v (pieces %q)",
				c.Name, c.Markup, text, got, pieces, c.Text, c.Spans, c.Pieces)
		}
	}
}
