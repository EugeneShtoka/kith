package tui

import (
	"reflect"
	"testing"
)

func TestParsePollArg(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		in       string
		question string
		options  []string
		multiple bool
		ok       bool
	}{
		"question with two answers": {
			in: "lunch? | pizza | ramen", question: "lunch?", options: []string{"pizza", "ramen"}, ok: true,
		},
		"any marks multiple": {
			in: "pick | a | b | c | any", question: "pick", options: []string{"a", "b", "c"}, multiple: true, ok: true,
		},
		"empty is refused": {in: "", ok: false},
		"only question":    {in: "lunch?", ok: false},
		"only one answer":  {in: "lunch? | pizza", ok: false},
		"any alone is not an answer": {
			in: "pick | a | any", question: "pick", options: nil, ok: false,
		},
		"blank answer is refused":   {in: "pick | a | | b", ok: false},
		"blank question is refused": {in: " | a | b", ok: false},
		"extra whitespace trims": {
			in: "   q   |  a  |  b  ", question: "q", options: []string{"a", "b"}, ok: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			q, opts, multi, ok := parsePollArg(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if q != tc.question {
				t.Errorf("question = %q, want %q", q, tc.question)
			}
			if !reflect.DeepEqual(opts, tc.options) {
				t.Errorf("options = %v, want %v", opts, tc.options)
			}
			if multi != tc.multiple {
				t.Errorf("multiple = %v, want %v", multi, tc.multiple)
			}
		})
	}
}
