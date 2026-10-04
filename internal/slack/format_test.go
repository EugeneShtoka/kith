package slack

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// acmeNames is a workspace that knows Dana and #general, signed in as U1.
func acmeNames() names {
	people := map[string]string{"U1": "Me", "U2": "Dana"}
	channels := map[string]string{"C1": "general"}
	return names{
		team: "T1", me: "U1",
		user:    func(id string) string { return people[id] },
		channel: func(id string) string { return channels[id] },
	}
}

// A message's references read as words: people and channels by name, links by
// their words, Slack's escapes undone; markers around them format.
func TestSlackTextReadsAsWords(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		text, body, markup string
		mentioned          bool
	}{
		{text: "hi <@U2>", body: "hi @Dana"},
		{text: "hi <@U9|sam>", body: "hi @sam"},
		{text: "hi <@U9>", body: "hi @U9"},
		{text: "see <#C1>", body: "see #general"},
		{text: "see <#C7|random>", body: "see #random"},
		{text: "ping <@U1>", body: "ping @Me", mentioned: true},
		{text: "<!here> lunch", body: "@here lunch", mentioned: true},
		{text: "<!subteam^S1|@devs> look", body: "@devs look"},
		{text: "a &lt;b&gt; &amp; c", body: "a <b> & c"},
		{text: "<https://x.io|the site>", body: "the site", markup: `<a href="https://x.io">the site</a>`},
		{text: "<https://x.io>", body: "https://x.io", markup: `<a href="https://x.io">https://x.io</a>`},
		{text: "<mailto:d@x.io|d@x.io>", body: "d@x.io", markup: `<a href="mailto:d@x.io">d@x.io</a>`},
		{text: "*bold* <@U2>", body: "bold @Dana", markup: "<b>bold</b> @Dana"},
		{text: "*<@U2>*", body: "@Dana", markup: "<b>@Dana</b>"},
		{text: "_a &lt; b_", body: "a < b", markup: "<i>a &lt; b</i>"},
		{text: "`x &amp;&amp; y`", body: "x && y", markup: "<code>x &amp;&amp; y</code>"},
		{text: "not*bold*", body: "not*bold*"},
	} {
		got := render(tc.text, acmeNames())
		if got.body != tc.body || got.format.Markup() != tc.markup || got.mentioned != tc.mentioned {
			t.Errorf("render(%q) = %q %q mentioned=%v; want %q %q %v",
				tc.text, got.body, got.format.Markup(), got.mentioned, tc.body, tc.markup, tc.mentioned)
		}
		if !got.format.IsZero() && got.format.Text() != got.body {
			t.Errorf("render(%q): the formatting draws %q over the words %q", tc.text, got.format.Text(), got.body)
		}
	}
}

// A mention is kept with the words it reads as, a person's or a channel's.
func TestSlackMentionsNameWhomTheyMention(t *testing.T) {
	t.Parallel()
	got := render("<@U2> in <#C1>", acmeNames()).mentions
	want := []domain.Mention{
		{UserID: "slack:T1.U2", Name: "@Dana"},
		{RoomID: "slack:T1/C1", Name: "#general"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("mentions = %+v, want %+v", got, want)
	}
}

// A draft goes as mrkdwn: escaped, mentions of this workspace's people as <@U…>,
// Markdown in markers, links as Slack writes them; a plain draft keeps its words.
func TestDraftsGoAsMrkdwn(t *testing.T) {
	t.Parallel()
	dana := domain.Mention{UserID: "slack:T1.U2", Name: "@Dana"}
	other := domain.Mention{UserID: "slack:T2.U2", Name: "@Sam"}
	for _, tc := range []struct {
		draft domain.Draft
		want  string
	}{
		{domain.Draft{Body: "hi @Dana", Mentions: []domain.Mention{dana}}, "hi <@U2>"},
		{domain.Draft{Body: "hi @Sam", Mentions: []domain.Mention{other}}, "hi @Sam"},
		{domain.Draft{Body: "**bold** and *it*"}, "*bold* and _it_"},
		{domain.Draft{Body: "[site](https://x.io?a=1&b=2)"}, "<https://x.io?a=1&amp;b=2|site>"},
		{domain.Draft{Body: "a < b & c"}, "a &lt; b &amp; c"},
		{domain.Draft{Body: "`**x**`"}, "`**x**`"},
		{domain.Draft{Body: "**as typed**", Plain: true}, "**as typed**"},
	} {
		if got := composed(tc.draft, "T1"); got != tc.want {
			t.Errorf("composed(%q) = %q, want %q", tc.draft.Body, got, tc.want)
		}
	}
}
