package signal_test

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

func style(kind signalv1.Style_Kind, start, length uint32) *signalv1.Style {
	return &signalv1.Style{Start: start, Length: length, Kind: kind}
}

// drawn reports whether f draws words in text with is.
func drawn(text string, f richtext.Formatted, words string, is func(richtext.Span) bool) bool {
	at := strings.Index(text, words)
	return at >= 0 && slices.ContainsFunc(f.Spans(), func(s richtext.Span) bool {
		return s.Start <= at && at+len(words) <= s.End && is(s)
	})
}

// styledMessage is Dana's message mentioning Ofer, styled as Signal counts: in UTF-16
// units over its text, where the emoji is two and the mention one U+FFFC.
func styledMessage() *signalv1.EventsResponse {
	ev := msg(dana, dana, 1700000000100, "😀 ￼, look: secret!")
	m := ev.GetMessage()
	m.Mentions = []*signalv1.Mention{{Start: 3, Length: 1, Aci: ofer, Name: "Ofer"}}
	m.Styles = []*signalv1.Style{
		style(signalv1.Style_KIND_ITALIC, 3, 1),
		style(signalv1.Style_KIND_BOLD, 6, 4),
		style(signalv1.Style_KIND_SPOILER, 12, 6),
		style(signalv1.Style_KIND_MONOSPACE, 40, 2), // past the text
	}
	return ev
}

// Signal's styles land on the words they covered once each mention is a name; one past
// the text draws nothing.
func TestSignalsStylesAreDrawnOverTheText(t *testing.T) {
	r := connected(t, styledMessage())
	m := r.messages(direct(dana))[0]
	if want := "😀 @Ofer, look: secret!"; m.Body != want {
		t.Fatalf("body %q, want %q", m.Body, want)
	}
	for words, is := range map[string]func(richtext.Span) bool{
		"@Ofer":  func(s richtext.Span) bool { return s.Italic },
		"look":   func(s richtext.Span) bool { return s.Bold },
		"secret": func(s richtext.Span) bool { return s.Spoiler },
	} {
		if !drawn(m.Body, m.Format, words, is) {
			t.Errorf("%q is not drawn as Signal styled it: %+v", words, m.Format.Spans())
		}
	}
	if slices.ContainsFunc(m.Format.Spans(), func(s richtext.Span) bool { return s.Code || s.Bold && s.Italic }) {
		t.Errorf("drawn beyond what Signal styled: %+v", m.Format.Spans())
	}
}

// An edit's styles are the message's from then on.
func TestAnEditsStylesReplaceTheMessages(t *testing.T) {
	ed := edit(dana, dana, 1700000000100, 1700000000200, "plainly struck")
	ed.GetEdit().Styles = []*signalv1.Style{style(signalv1.Style_KIND_STRIKETHROUGH, 8, 6)}
	r := connected(t, styledMessage(), ed)
	m := r.messages(direct(dana))[0]
	struck := func(s richtext.Span) bool { return s.Strike }
	if m.Body != "plainly struck" || !drawn(m.Body, m.Format, "struck", struck) || drawn(m.Body, m.Format, "plainly", struck) {
		t.Errorf("the edit kept %q %+v, want only \"struck\" struck through", m.Body, m.Format.Spans())
	}
	if drawn(m.Body, m.Format, "struck", func(s richtext.Span) bool { return s.Bold || s.Spoiler || s.Italic }) {
		t.Errorf("the message's old styles outlived the edit: %+v", m.Format.Spans())
	}
}

// A draft's Markdown goes as Signal's styles, counted over what is sent (the emoji two
// units, the mention one U+FFFC); a link's address follows its words, since Signal has
// no links, and styles after it move along. A draft sent as typed carries no styles.
// kith's own copy is drawn as sent.
func TestADraftsMarkdownGoesAsSignalsStyles(t *testing.T) {
	r := connected(t, msg(dana, dana, 1700000000100, "hi"))
	ctx := context.Background()
	draft := domain.Draft{
		Body:     "😀 **hi** @Dana, see [the site](https://x.io) and `code`",
		Mentions: []domain.Mention{{UserID: "signal:" + dana, Name: "@Dana"}},
	}
	if err := r.a.Send(ctx, direct(dana), draft); err != nil {
		t.Fatal(err)
	}
	s := r.sends()[0]
	if want := "😀 hi ￼, see the site (https://x.io) and code"; s.GetBody() != want {
		t.Fatalf("body %q, want %q", s.GetBody(), want)
	}
	if m := s.GetMentions(); len(m) != 1 || m[0].GetStart() != 6 {
		t.Errorf("mentions %v, want one at 6", m)
	}
	want := []string{"KIND_BOLD 3+2", "KIND_MONOSPACE 41+4"}
	var got []string
	for _, st := range s.GetStyles() {
		got = append(got, st.GetKind().String()+" "+strconv.Itoa(int(st.GetStart()))+"+"+strconv.Itoa(int(st.GetLength())))
	}
	if !slices.Equal(got, want) {
		t.Errorf("styles %v, want %v", got, want)
	}
	own := r.messages(direct(dana))[1]
	if own.Body != "😀 hi @Dana, see the site (https://x.io) and code" || !drawn(own.Body, own.Format, "hi", func(s richtext.Span) bool { return s.Bold }) {
		t.Errorf("kept %q %+v, want it as sent, drawn", own.Body, own.Format.Spans())
	}

	plain := domain.Draft{Body: "**as typed**", Plain: true}
	if err := r.a.Send(ctx, direct(dana), plain); err != nil {
		t.Fatal(err)
	}
	if p := r.sends()[1]; p.GetBody() != "**as typed**" || len(p.GetStyles()) != 0 {
		t.Errorf("a draft sent as typed went as %v", p)
	}
}
