package main

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// Words are a command when they start with one slash on one line: a known name takes its
// words after a space or colon, any other slash word is its own name.
func TestCommandOf(t *testing.T) {
	for _, tc := range []struct {
		body, name, arg string
		ok              bool
	}{
		{"/me waves", "/me", "waves", true},
		{"/me:waves", "/me", "waves", true},
		{"/upload", "/upload", "", true},
		{"/mention x", "/mention", "x", true}, // not /me
		{"/leave now", "/leave", "now", true},
		{"//me waves", "", "", false},
		{"/me\nwaves", "", "", false},
		{"/", "", "", false},
		{"hello /me", "", "", false},
	} {
		name, arg, ok := commandOf(tc.body)
		if name != tc.name || arg != tc.arg || ok != tc.ok {
			t.Errorf("%q: %q %q %v, want %q %q %v", tc.body, name, arg, ok, tc.name, tc.arg, tc.ok)
		}
	}
}

// A command the window has not got, an unknown slash word, or one missing its words is
// said and keeps them, sending nothing.
func TestACommandNotDoneKeepsItsWords(t *testing.T) {
	for _, tc := range []struct{ words, said string }{
		{"/leave", "/leave is in the TUI"},
		{"/frobnicate x", "/frobnicate is not a command here"},
		{"/me", "/me <what you are doing>"},
		{"/at 25:00 hi", `/at "25:00" is not a time`},
		{"/in 2h", "/in needs a time and a message"},
		{"/poll what? | yes", "/poll <question>"},
	} {
		u := &ui{}
		u.composer.SetText(tc.words)
		if !u.command(context.Background(), &u.composer, "", tc.words) {
			t.Errorf("%q: taken as a message", tc.words)
		}
		if !strings.HasPrefix(u.note, tc.said) || u.composer.Text() != tc.words {
			t.Errorf("%q: said %q, words %q; want %q said and the words kept", tc.words, u.note, u.composer.Text(), tc.said)
		}
	}
}

func TestScheduleTime(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		kind, arg string
		at        time.Time
		words     string
	}{
		{"in", "2h see you", now.Add(2 * time.Hour), "see you"},
		{"at", "11:00 hi there", time.Date(2026, 11, 15, 11, 0, 0, 0, time.UTC), "hi there"},
		{"at", "9am morning", time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC), "morning"}, // past: tomorrow
		{"at", "3:15pm tea", time.Date(2026, 11, 15, 15, 15, 0, 0, time.UTC), "tea"},
	} {
		at, words, err := scheduleTime(tc.kind, tc.arg, now)
		if err != nil || !at.Equal(tc.at) || words != tc.words {
			t.Errorf("/%s %s: %v %q %v, want %v %q", tc.kind, tc.arg, at, words, err, tc.at, tc.words)
		}
	}
	for _, arg := range []string{"-1h no", "soon no"} {
		if _, _, err := scheduleTime("in", arg, now); err == nil {
			t.Errorf("/in %s: no error", arg)
		}
	}
}

func TestPollOf(t *testing.T) {
	q, opts, multiple, ok := pollOf(" Lunch? | pizza | soup | any ")
	if !ok || q != "Lunch?" || !slices.Equal(opts, []string{"pizza", "soup"}) || !multiple {
		t.Errorf("got %q %q %v %v", q, opts, multiple, ok)
	}
	for _, arg := range []string{"Lunch? | pizza", "Lunch? | pizza | any", " | a | b", "q | a | | b"} {
		if _, _, _, ok := pollOf(arg); ok {
			t.Errorf("%q: read as a poll", arg)
		}
	}
}

// `/` at the start offers the window's commands by what is typed, until a space.
func TestCommandOffers(t *testing.T) {
	names := func(before string) []string {
		var out []string
		for _, c := range commandOffers(before) {
			out = append(out, c.put)
		}
		return out
	}
	if got := names("/"); len(got) != len(windowCommands) {
		t.Errorf("/: %q, want every command", got)
	}
	if got := names("/p"); !slices.Equal(got, []string{"/plain ", "/poll "}) {
		t.Errorf("/p: %q", got)
	}
	if got := names("/up"); !slices.Equal(got, []string{"/upload"}) {
		t.Errorf("/up: %q", got)
	}
	for _, before := range []string{"/me ", "//", "hi /p", ""} {
		if got := commandOffers(before); got != nil {
			t.Errorf("%q: offered %v", before, got)
		}
	}
}
