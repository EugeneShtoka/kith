package main

import (
	"testing"

	"gioui.org/font"
	"gioui.org/widget/material"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

func TestBareLinksStopBeforePunctuation(t *testing.T) {
	body := "see https://example.org/a?b=1, and (https://example.net/x)."
	_, spans := drawn(domain.Message{Body: body}, body)
	var got []string
	for _, s := range spans {
		got = append(got, body[s.Start:s.End])
	}
	want := []string{"https://example.org/a?b=1", "https://example.net/x"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("links = %q, want %q", got, want)
	}
}

func TestStylesCutAtEveryEdge(t *testing.T) {
	u := &ui{th: material.NewTheme(), pal: themeOf(config.Config{}, config.Device{})}
	text := "plain bold code link"
	spans := []richtext.Span{
		{Start: 6, End: 15, Bold: true},
		{Start: 11, End: 15, Code: true},
		{Start: 16, End: 20, Link: true, Href: "https://example.org"},
	}
	st := u.styles(text, spans)
	var pieces []string
	for _, s := range st {
		pieces = append(pieces, s.Content)
	}
	want := []string{"plain ", "bold ", "code", " ", "link"}
	if len(pieces) != len(want) {
		t.Fatalf("pieces = %q, want %q", pieces, want)
	}
	for i := range want {
		if pieces[i] != want[i] {
			t.Fatalf("pieces = %q, want %q", pieces, want)
		}
	}
	if st[1].Font.Weight != font.Bold || st[2].Font.Typeface != "monospace" || st[2].Font.Weight != font.Bold {
		t.Error("bold and code do not both reach the piece under both")
	}
	if !st[4].Interactive || st[0].Interactive {
		t.Error("only the link is clickable")
	}
}

func TestOnlyWebAndMailLinksOpen(t *testing.T) {
	for href, opens := range map[string]bool{
		"https://example.org": true, "HTTP://example.org": true, "mailto:a@example.org": true,
		"file:///etc/passwd": false, "javascript:alert(1)": false, "ssh://host": false, "": false,
	} {
		if got := webOrMail(href); got != opens {
			t.Errorf("webOrMail(%q) = %v, want %v", href, got, opens)
		}
	}
}
