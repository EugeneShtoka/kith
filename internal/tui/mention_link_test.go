package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A mention pill in the timeline links to the person it names, whichever drawing path
// its row takes: the plain LTR paint, the offset-mapped marks (RTL lines, formatting,
// tracked words, bare links), or a formatted body whose pill name was rewritten to an
// alias (which drops the sender's link span). Hyperlinks are on by default.
func TestTimelineMentionPillCarriesItsLink(t *testing.T) {
	t.Parallel()

	const pillHref = "matrix:u/dana:x"
	dana := []domain.Mention{{UserID: "@dana:x", Name: "Dana"}}
	for _, tc := range []struct {
		name string
		disp config.Display
		msg  domain.Message
		// want is the OSC 8 target the pill's row must carry.
		want string
	}{
		{
			name: "plain LTR body",
			msg:  domain.Message{Body: "ask Dana about it", Mentions: dana},
			want: pillHref,
		},
		{
			name: "plain LTR body with a bare link (marks path)",
			msg:  domain.Message{Body: "ask Dana about https://example.org", Mentions: dana},
			want: pillHref,
		},
		{
			name: "RTL body",
			msg:  domain.Message{Body: "שאל את Dana על זה", Mentions: dana},
			want: pillHref,
		},
		{
			name: "RTL body with a bare link",
			msg:  domain.Message{Body: "שאל את Dana על https://example.org", Mentions: dana},
			want: pillHref,
		},
		{
			name: "formatted pill, name unchanged: the sender's href",
			msg: domain.Message{
				Body: "ask Dana about it", Mentions: dana,
				HTML: `ask <a href="https://matrix.to/#/@dana:x">Dana</a> about it`,
			},
			want: "https://matrix.to/#/@dana:x",
		},
		{
			name: "formatted pill rewritten to an alias",
			disp: config.Display{Identities: []config.Identity{{Alias: "Dee", MXIDs: []string{"@dana:x"}}}},
			msg: domain.Message{
				Body: "ask Dana about it", Mentions: dana,
				HTML: `ask <a href="https://matrix.to/#/@dana:x">Dana</a> about it`,
			},
			want: pillHref,
		},
		{
			name: "formatted RTL pill rewritten to an alias",
			disp: config.Display{Identities: []config.Identity{{Alias: "Dee", MXIDs: []string{"@dana:x"}}}},
			msg: domain.Message{
				Body: "שאל את Dana על זה", Mentions: dana,
				HTML: `שאל את <a href="https://matrix.to/#/@dana:x">Dana</a> על זה`,
			},
			want: pillHref,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := sized(t, withRooms(t, New(context.Background(), apitest.Nop{}, tc.disp)))
			msg := tc.msg
			msg.ID, msg.RoomID, msg.Sender, msg.Timestamp = "$1", "!a:x", "@bob:x", at(1)
			m = loadPage(t, m, []domain.Message{msg})
			rows := strings.Join(m.layoutRows(), "\n")
			if !strings.Contains(rows, ansi.SetHyperlink(tc.want)) {
				t.Errorf("no OSC 8 link to %s on the pill:\n%q", tc.want, rows)
			}
		})
	}

	// Off means no link escapes at all.
	m := sized(t, withRooms(t, New(context.Background(), apitest.Nop{}, config.Display{Hyperlinks: new(false)})))
	m = loadPage(t, m, []domain.Message{{
		ID: "$1", RoomID: "!a:x", Sender: "@bob:x", Timestamp: at(1),
		Body: "שאל את Dana על זה", Mentions: dana,
	}})
	if rows := strings.Join(m.layoutRows(), "\n"); strings.Contains(rows, "\x1b]8;") {
		t.Errorf("hyperlinks off, yet the row links:\n%q", rows)
	}
}
