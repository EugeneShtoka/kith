package main

import (
	"context"
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A link key on a message with several links asks which, as the desktop does; nav moves
// the choice and close drops it.
func TestALinkKeyAsksWhichOfSeveral(t *testing.T) {
	m := domain.Message{Body: "see https://a.example/x and matrix:r/room:example.org, then https://b.example."}
	want := []string{"https://a.example/x", "https://b.example", "matrix:r/room:example.org"}
	if got := messageLinks(m); !slices.Equal(got, want) {
		t.Fatalf("links %q, want %q", got, want)
	}
	u := &ui{}
	ctx := context.Background()
	if !u.takeLink(ctx, C{}, m, true) || u.linking == nil || !u.linking.copy || len(u.linking.links) != 3 {
		t.Fatalf("no chooser: %+v", u.linking)
	}
	for _, path := range []string{"nav.down", "nav.down", "nav.down", "nav.up"} {
		u.chooseLink(ctx, C{}, path)
	}
	if u.linking.at != 1 {
		t.Errorf("at %d after down×3 and up, want 1", u.linking.at)
	}
	if u.chooseLink(ctx, C{}, "timeline.reply") || !u.chooseLink(ctx, C{}, "picker.close") || u.linking != nil {
		t.Errorf("close left %+v", u.linking)
	}
	if u.takeLink(ctx, C{}, domain.Message{Body: "no links here"}, false) {
		t.Error("a message without links took the key")
	}
}
