package matrix

import (
	"context"
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A message with no mentions stays an ordinary plain-text event.
func TestBuildMessagePlain(t *testing.T) {
	t.Parallel()

	got := buildMessage(domain.Draft{Body: "just text"}, "")
	if got.Body != "just text" {
		t.Errorf("Body = %q", got.Body)
	}
	if got.Format != "" || got.FormattedBody != "" {
		t.Errorf("a plain message should carry no HTML, got format=%q body=%q", got.Format, got.FormattedBody)
	}
	if got.Mentions != nil {
		t.Errorf("a plain message should carry no m.mentions, got %+v", got.Mentions)
	}
	if got.MsgType != event.MsgText {
		t.Errorf("MsgType = %q", got.MsgType)
	}
}

// A mention needs m.mentions (what the homeserver highlights on) plus the HTML pill.
func TestBuildMessageMention(t *testing.T) {
	t.Parallel()

	got := buildMessage(domain.Draft{
		Body:     "hi Alice, ready?",
		Mentions: []domain.Mention{{UserID: "@alice:example.org", Name: "Alice"}},
	}, "")
	if got.Body != "hi Alice, ready?" {
		t.Errorf("Body should stay plain for clients that ignore HTML, got %q", got.Body)
	}
	if got.Mentions == nil || len(got.Mentions.UserIDs) != 1 || got.Mentions.UserIDs[0] != "@alice:example.org" {
		t.Fatalf("m.mentions = %+v, want the mentioned MXID", got.Mentions)
	}
	if got.Format != event.FormatHTML {
		t.Errorf("Format = %q, want the HTML format", got.Format)
	}
	want := `hi <a href="https://matrix.to/#/@alice:example.org">Alice</a>, ready?`
	if got.FormattedBody != want {
		t.Errorf("FormattedBody =\n  %q\nwant\n  %q", got.FormattedBody, want)
	}
}

func TestBuildMessageReplyWithMention(t *testing.T) {
	t.Parallel()

	got := buildMessage(domain.Draft{
		Body:     "sure Bob",
		ReplyTo:  "$target:x",
		Mentions: []domain.Mention{{UserID: "@bob:x", Name: "Bob"}},
	}, "")
	if got.RelatesTo == nil || got.RelatesTo.GetReplyTo() != "$target:x" {
		t.Errorf("RelatesTo = %+v, want a reply to $target:x", got.RelatesTo)
	}
	if got.Mentions == nil || len(got.Mentions.UserIDs) != 1 {
		t.Errorf("a reply should still carry its mentions, got %+v", got.Mentions)
	}
}

// A mention whose name was deleted from the body must not ship.
func TestBuildMessageDropsStaleMentions(t *testing.T) {
	t.Parallel()

	got := buildMessage(domain.Draft{
		Body: "never mind",
		Mentions: []domain.Mention{
			{UserID: "@alice:x", Name: "Alice"}, // no longer in the body
		},
	}, "")
	if got.Mentions != nil {
		t.Errorf("a mention edited out of the body should not be sent, got %+v", got.Mentions)
	}
	if got.FormattedBody != "" {
		t.Errorf("no live mentions means no HTML, got %q", got.FormattedBody)
	}
}

// The body is escaped into HTML while the generated anchors survive.
func TestPillHTMLEscapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		mentions []domain.Mention
		want     string
	}{
		{
			name: "markup in the body is escaped",
			body: "<b>hi</b> Alice & co",
			mentions: []domain.Mention{
				{UserID: "@alice:x", Name: "Alice"},
			},
			want: `&lt;b&gt;hi&lt;/b&gt; <a href="https://matrix.to/#/@alice:x">Alice</a> &amp; co`,
		},
		{
			name: "markup in a display name is escaped inside the anchor",
			body: `hi <script>x</script> there`,
			mentions: []domain.Mention{
				{UserID: "@evil:x", Name: "<script>x</script>"},
			},
			want: `hi <a href="https://matrix.to/#/@evil:x">&lt;script&gt;x&lt;/script&gt;</a> there`,
		},
		{
			name: "a quote in an MXID cannot break out of the href",
			body: `hi Bob`,
			mentions: []domain.Mention{
				{UserID: `@b"ob:x`, Name: "Bob"},
			},
			want: `hi <a href="https://matrix.to/#/@b&#34;ob:x">Bob</a>`,
		},
		{
			name: "two people both get pills",
			body: "Alice and Bob",
			mentions: []domain.Mention{
				{UserID: "@alice:x", Name: "Alice"},
				{UserID: "@bob:x", Name: "Bob"},
			},
			want: `<a href="https://matrix.to/#/@alice:x">Alice</a> and <a href="https://matrix.to/#/@bob:x">Bob</a>`,
		},
		{
			name: "a short name does not claim a longer one's text",
			body: "Dan and Daniel",
			mentions: []domain.Mention{
				{UserID: "@dan:x", Name: "Dan"},
				{UserID: "@daniel:x", Name: "Daniel"},
			},
			want: `<a href="https://matrix.to/#/@dan:x">Dan</a> and <a href="https://matrix.to/#/@daniel:x">Daniel</a>`,
		},
		{
			name: "naming someone twice pills the first, leaving the second as text",
			body: "Alice, Alice!",
			mentions: []domain.Mention{
				{UserID: "@alice:x", Name: "Alice"},
			},
			want: `<a href="https://matrix.to/#/@alice:x">Alice</a>, Alice!`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := pillHTML(tc.body, tc.mentions)
			if got != tc.want {
				t.Errorf("pillHTML =\n  %q\nwant\n  %q", got, tc.want)
			}
			// No unescaped angle bracket may survive outside our anchors.
			if strings.Contains(got, "<script") {
				t.Errorf("unescaped markup survived: %q", got)
			}
		})
	}
}

// The pill we generate must be the pill we parse.
func TestPillRoundTrip(t *testing.T) {
	t.Parallel()

	content := buildMessage(domain.Draft{
		Body: "hi Alice and Bob",
		Mentions: []domain.Mention{
			{UserID: "@alice:example.org", Name: "Alice"},
			{UserID: "@bob:example.org", Name: "Bob"},
		},
	}, "")
	parsed := parseMentions(content.FormattedBody)
	if len(parsed) != 2 {
		t.Fatalf("parsed %d mentions from our own HTML, want 2: %+v", len(parsed), parsed)
	}
	byID := map[string]string{}
	for _, mention := range parsed {
		byID[mention.UserID] = mention.Name
	}
	if byID["@alice:example.org"] != "Alice" || byID["@bob:example.org"] != "Bob" {
		t.Errorf("round-tripped mentions = %+v", byID)
	}
}

// A message composed in a thread carries m.thread.
func TestBuildMessageInAThread(t *testing.T) {
	t.Parallel()

	got := buildMessage(domain.Draft{Body: "shipping now", ThreadRoot: "$root:x"}, "$latest:x")

	if got.RelatesTo == nil || got.RelatesTo.GetThreadParent() != "$root:x" {
		t.Fatalf("RelatesTo = %+v, want a thread rooted at $root:x", got.RelatesTo)
	}
	// The fallback reply points at the thread's newest message and is flagged.
	if got.RelatesTo.GetReplyTo() != "$latest:x" {
		t.Errorf("m.in_reply_to = %q, want the newest message in the thread", got.RelatesTo.GetReplyTo())
	}
	if !got.RelatesTo.IsFallingBack {
		t.Error("is_falling_back should be set: nobody replied to that message")
	}
	if got.RelatesTo.GetNonFallbackReplyTo() != "" {
		t.Error("a plain message in a thread must not read as a genuine reply")
	}
}

// A real reply inside a thread has both pointers and no fallback flag.
func TestBuildMessageReplyInsideAThread(t *testing.T) {
	t.Parallel()

	got := buildMessage(domain.Draft{Body: "which one?", ThreadRoot: "$root:x", ReplyTo: "$target:x"}, "$latest:x")

	if got.RelatesTo.GetThreadParent() != "$root:x" {
		t.Errorf("thread = %q, want $root:x", got.RelatesTo.GetThreadParent())
	}
	if got.RelatesTo.GetNonFallbackReplyTo() != "$target:x" {
		t.Errorf("reply target = %q, want $target:x as a genuine reply", got.RelatesTo.GetNonFallbackReplyTo())
	}
	if got.RelatesTo.IsFallingBack {
		t.Error("a reply someone actually made must not be marked as a fallback")
	}
}

// Regression: nil Mentions crashed the mention-ranking loop after the send went out
// (double sends via the queue; daemon exit from the scheduler goroutine).
func TestRecordingMentionsToleratesAMessageWithNone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := testCache(t)
	// The content buildMessage produces for a plain send and for a reply.
	for _, content := range []event.MessageEventContent{
		buildMessage(domain.Draft{Body: "no pills here"}, ""),
		buildMessage(domain.Draft{Body: "answering", ReplyTo: "$target"}, ""),
	} {
		if err := recordMentions(ctx, cache, "!a:x", &content); err != nil {
			t.Fatalf("recordMentions: %v", err)
		}
	}
	if got, err := cache.FrequentMentions(ctx, "!a:x", 5); err != nil || len(got) != 0 {
		t.Errorf("mentions = %v (%v) for messages that name nobody", got, err)
	}

	withPill := buildMessage(domain.Draft{
		Body:     "hi Dana",
		Mentions: []domain.Mention{{UserID: "@dana:x", Name: "Dana"}},
	}, "")
	if err := recordMentions(ctx, cache, "!a:x", &withPill); err != nil {
		t.Fatalf("recordMentions: %v", err)
	}
	if got, err := cache.FrequentMentions(ctx, "!a:x", 5); err != nil || len(got) != 1 || got[0] != "@dana:x" {
		t.Errorf("mentions = %v (%v), want Dana's one", got, err)
	}
}
