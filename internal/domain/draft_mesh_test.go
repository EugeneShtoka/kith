package domain_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// FoldDraft reads concurrent versions as the newest whole and each other's words after
// it: only what one added over words the newest had seen, else the whole of it; and
// over a version that cleared the draft, only what each added.
func TestFoldDraftKeepsEveryVersionsWords(t *testing.T) {
	t.Parallel()
	v := func(body, over string) domain.DraftVersion {
		return domain.DraftVersion{Draft: domain.StoredDraft{Body: body}, Over: over}
	}
	for _, tc := range []struct {
		name     string
		versions []domain.DraftVersion
		want     string
	}{
		{"one", []domain.DraftVersion{v("hi", "")}, "hi"},
		{"two appends over one base", []domain.DraftVersion{v("hi you", "hi"), v("hi there", "hi")}, "hi there\n\nyou"},
		{"a rewrite against an append", []domain.DraftVersion{v("hi you", "hi"), v("hello", "hi")}, "hello\n\nyou"},
		{"equal versions", []domain.DraftVersion{v("hi you", "hi"), v("hi you", "hi")}, "hi you"},
		// The older version's base was its own bridge's earlier version, which the newest
		// never saw: those words survive nowhere else.
		{"a base the newest never saw", []domain.DraftVersion{v("mine more", "mine"), v("theirs", "")}, "theirs\n\nmine more"},
		{"a send against an append", []domain.DraftVersion{v("", "sent"), v("sent more", "sent")}, "more"},
		{"an append against a send", []domain.DraftVersion{v("sent more", "sent"), v("", "sent")}, "more"},
		{"a send alone", []domain.DraftVersion{v("", "sent")}, ""},
	} {
		if got := domain.FoldDraft(tc.versions).Body; got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TakeDraft takes a draft moved elsewhere whole while nothing changed here, and keeps
// what changed here first otherwise.
func TestTakeDraftKeepsWhatChangedHere(t *testing.T) {
	t.Parallel()
	d := func(body string) domain.StoredDraft { return domain.StoredDraft{Body: body} }
	if got := domain.TakeDraft(d("hi"), d("hi"), d("bye")).Body; got != "bye" {
		t.Errorf("unchanged here: %q", got)
	}
	if got := domain.TakeDraft(d("hi mine"), d("hi"), d("hi theirs")).Body; got != "hi mine\n\ntheirs" {
		t.Errorf("both changed: %q", got)
	}
	if got := domain.TakeDraft(d("hi mine"), d("hi"), d("")).Body; got != "hi mine" {
		t.Errorf("sent elsewhere, typed here: %q", got)
	}
}
