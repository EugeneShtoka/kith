package tui

import (
	"context"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
)

// An opener that fails at once (no handler for the scheme) is reported, not taken
// for success.
func TestOpeningALinkReportsAnOpenerThatFails(t *testing.T) {
	t.Parallel()

	cases := []struct {
		opener  string
		wantErr bool
	}{
		{"true", false},
		{"false", true},
		{"no-such-opener-kith", true},
	}
	for _, c := range cases {
		t.Run(c.opener, func(t *testing.T) {
			t.Parallel()
			m := New(context.Background(), apitest.Nop{}, config.Display{})
			m.prefs.external.open = c.opener

			opened, ok := m.openLinkCmd("https://example.org")().(openedMsg)
			if !ok {
				t.Fatal("openLinkCmd returned no openedMsg")
			}
			if (opened.err != nil) != c.wantErr {
				t.Errorf("err = %v, want error: %v", opened.err, c.wantErr)
			}
		})
	}
}
