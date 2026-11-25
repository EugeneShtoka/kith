package main

import (
	"slices"
	"testing"
)

// Copied files win over an image, an image over text; a uri-list sends only local files.
func TestWhatAPasteSends(t *testing.T) {
	for _, tc := range []struct {
		types []string
		want  string
	}{
		{[]string{"TARGETS", "text/uri-list", "x-special/gnome-copied-files", "image/png"}, "text/uri-list"},
		{[]string{"text/html", "image/jpeg", "image/png"}, "image/png"},
		{[]string{"UTF8_STRING", "text/plain"}, ""},
	} {
		if got := pasteType(tc.types); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.types, got, tc.want)
		}
	}
	if got := localFiles("file:///home/a/x%20y.png\r\n# a comment\nfile:///home/a/z.pdf\n"); !slices.Equal(got, []string{"/home/a/x y.png", "/home/a/z.pdf"}) {
		t.Errorf("files %q", got)
	}
	if got := localFiles("https://example.com/a.png\n"); got != nil {
		t.Errorf("a link read as files: %q", got)
	}
}
