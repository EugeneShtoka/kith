package main

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// heads is rows' headings without their times, to compare at a glance.
func heads(rows []historyRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.head)
	}
	return out
}

// The history lists each version oldest first, by how much it changed, then the
// deletion by whom where not the sender; with none kept it says why, or that the message
// as it is was all there was.
func TestHistoryRows(t *testing.T) {
	for _, tc := range []struct {
		what string
		h    historyView
		want []string
	}{
		{"reading", historyView{}, []string{"Reading its history…"}},
		{"failed", historyView{read: true, err: errors.New("gone")}, []string{"Could not read its history: gone"}},
		{"edited twice", historyView{read: true, msg: domain.Message{Edited: true}, revisions: []domain.Revision{
			{Body: "helo"}, {Body: "hello"}, {Body: "hi"}, {Body: "yo"},
		}}, []string{"Sent", "Edited +1", "Edited −3", "Edited reworded"}},
		{"edited, none kept", historyView{read: true, msg: domain.Message{Edited: true, Body: "now"}},
			[]string{"It was edited before its versions were being kept, and the server has nothing left of them."}},
		{"never edited", historyView{read: true, msg: domain.Message{Body: "hi"}}, []string{"Sent"}},
		{"deleted by its sender", historyView{read: true, msg: domain.Message{Sender: "@a", Redacted: true, RedactedBy: "@a"}},
			[]string{"What it said was not kept ([display.deleted] keep is off).", "Deleted"}},
		{"deleted by another, kept", historyView{
			read: true, msg: domain.Message{Sender: "@a", Body: "hi", Redacted: true, RedactedBy: "@a"},
			deletion: domain.Deletion{By: "@mod", Reason: "spam"},
		}, []string{"Sent", "Deleted by @mod: spam"}},
	} {
		if got := heads(historyRows(&tc.h)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.what, got, tc.want)
		}
	}
	at := time.Date(2026, 11, 15, 9, 5, 0, 0, time.Local)
	h := historyView{read: true, revisions: []domain.Revision{{Body: "hi", At: at}}}
	if got := historyRows(&h); got[0].head != "Sun 15 Nov 9:05 AM · Sent" || got[0].body != "hi" {
		t.Errorf("a version: %+v", got[0])
	}
}
