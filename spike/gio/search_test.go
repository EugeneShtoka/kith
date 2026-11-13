package main

import (
	"context"
	"strings"
	"testing"

	"gioui.org/widget/material"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A jump selects its message once read, waits while a page is on its way, and says the
// message is gone once the room's start is read without it.
func TestSeekSelectsWaitsOrSaysGone(t *testing.T) {
	msgs := []domain.Message{{ID: "$a", Body: "a"}, {ID: "$b", Body: "b"}, {ID: "$c", Body: "c"}}
	for _, tc := range []struct {
		name       string
		jump       domain.EventID
		loading    bool
		wantSel    domain.EventID
		wantJump   domain.EventID
		wantGone   bool
		wantInTime bool
	}{
		{"read", "$b", false, "$b", "", false, true},
		{"a page on its way", "$z", true, "", "$z", false, false},
		{"the room's start read", "$z", false, "", "", true, false},
	} {
		// A list never drawn, pinned to its end, as a room just opened is.
		u := &ui{timeline: timeline{msgs: msgs, shown: []int{0, 1, 2}, jump: tc.jump, loading: tc.loading}}
		u.timeline.list.ScrollToEnd = true
		u.seek(context.Background())
		if u.timeline.sel != tc.wantSel || u.timeline.jump != tc.wantJump {
			t.Errorf("%s: selected %q, still to jump to %q; want %q, %q", tc.name, u.timeline.sel, u.timeline.jump, tc.wantSel, tc.wantJump)
		}
		if gone := strings.Contains(u.note, "no longer cached"); gone != tc.wantGone {
			t.Errorf("%s: note %q", tc.name, u.note)
		}
		if tc.wantSel != "" && (u.timeline.list.Position.First != 0 || !u.timeline.list.Position.BeforeEnd) {
			t.Errorf("%s: not scrolled to it: %+v", tc.name, u.timeline.list.Position)
		}
		if (u.pane == paneTimeline) != tc.wantInTime {
			t.Errorf("%s: pane %v", tc.name, u.pane)
		}
	}
}

// A hit's excerpt marks exactly the runs kithd bracketed.
func TestSnippetMarksMatchedRuns(t *testing.T) {
	u := &ui{th: material.NewTheme(), pal: palette{text: rgb(0x202020), accent: rgb(0x3366ff)}}
	got := u.snippet("we " + domain.HighlightStart + "met" + domain.HighlightEnd + " at the " + domain.HighlightStart + "café" + domain.HighlightEnd)
	var b strings.Builder
	for _, s := range got {
		if s.Color == u.pal.accent {
			b.WriteString("[" + s.Content + "]")
		} else {
			b.WriteString(s.Content)
		}
	}
	if want := "we [met] at the [café]"; b.String() != want {
		t.Errorf("snippet = %q, want %q", b.String(), want)
	}
}
