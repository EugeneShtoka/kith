package main

import (
	"image/color"
	"io"
	"log"
	"net/url"
	"os/exec"
	"regexp"
	"slices"
	"strings"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/widget"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
	"github.com/EugeneShtoka/kith/spike/gio/seltext"
)

// msgState is one message's widgets: its text (selectable, links clickable), and the
// tag its right-click is copied by.
type msgState struct {
	text  seltext.State
	media widget.Clickable
	image widget.Clickable
	reply widget.Clickable
	copy  int
}

var bareURL = regexp.MustCompile(`https?://[^\s<>"]+`)

// drawn is what m's text draws as: its formatting's text and spans, else the body with
// each bare http(s) URL made a link.
func drawn(m domain.Message, body string) (string, []richtext.Span) {
	if !m.Format.IsZero() && !m.Redacted {
		return m.Format.Text(), m.Format.Spans()
	}
	var spans []richtext.Span
	for _, at := range bareURL.FindAllStringIndex(body, -1) {
		end := at[1]
		for end > at[0] && strings.ContainsRune(".,;:!?)]}'", rune(body[end-1])) {
			end--
		}
		spans = append(spans, richtext.Span{Start: at[0], End: end, Link: true, Href: body[at[0]:end]})
	}
	return body, spans
}

// styles cuts text at every span edge and styles each piece by the spans over it, in
// fg unless a span colors it.
func (u *ui) styles(text string, spans []richtext.Span, fg color.NRGBA) []seltext.SpanStyle {
	cuts := []int{0, len(text)}
	for _, s := range spans {
		cuts = append(cuts, min(max(s.Start, 0), len(text)), min(max(s.End, 0), len(text)))
	}
	slices.Sort(cuts)
	cuts = slices.Compact(cuts)
	var out []seltext.SpanStyle
	for i := 0; i+1 < len(cuts); i++ {
		a, b := cuts[i], cuts[i+1]
		st := seltext.SpanStyle{Font: font.Font{Typeface: u.th.Face}, Size: u.th.TextSize, Color: fg, Content: text[a:b]}
		for _, s := range spans {
			if s.Start > a || s.End < b {
				continue
			}
			switch {
			case s.Code:
				st.Font.Typeface, st.Size, st.Background = "monospace", u.th.TextSize*0.9, u.pal.raised
			case s.Heading:
				st.Font.Weight, st.Size = font.Bold, u.th.TextSize*1.15
			}
			if s.Bold {
				st.Font.Weight = font.Bold
			}
			if s.Italic {
				st.Font.Style = font.Italic
			}
			st.Underline = st.Underline || s.Underline
			st.Strikethrough = st.Strikethrough || s.Strike
			if s.Quote || s.Spoiler {
				st.Color = u.pal.muted
			}
			if s.Link && s.Href != "" {
				st.Color, st.Link, st.Underline = u.pal.link, s.Href, true
			}
		}
		out = append(out, st)
	}
	return out
}

// layoutText draws a message's text, selectable, its links opening in the browser.
func (u *ui) layoutText(gtx C, st *msgState, m domain.Message, body string) D {
	for {
		link, ok := st.text.Clicked()
		if !ok {
			break
		}
		go openLink(link)
	}
	txt, spans := drawn(m, body)
	fg := u.pal.text
	if m.Redacted {
		fg = u.pal.muted
	}
	ts := seltext.Text(&st.text, u.th.Shaper, u.styles(txt, spans, fg)...)
	ts.Selection = u.pal.selection
	return ts.Layout(gtx)
}

// openLink opens a web or mail link in the person's browser. Links come from other
// people, so only those schemes go to xdg-open, never a file or a handler's.
func openLink(href string) {
	if !webOrMail(href) {
		log.Printf("not opening %q: only web and mail links open", href)
		return
	}
	if err := exec.Command("xdg-open", href).Run(); err != nil {
		log.Printf("opening %s: %v", href, err)
	}
}

func webOrMail(href string) bool {
	parsed, err := url.Parse(href)
	return err == nil && slices.Contains([]string{"http", "https", "mailto"}, strings.ToLower(parsed.Scheme))
}

// copyable lays w out under an area that copies txt on a right-click: an ancestor of
// w's own areas, so a selectable label or a link beneath still gets its clicks.
func (u *ui) copyable(gtx C, tag *int, txt string, w layout.Widget) D {
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: tag, Kinds: pointer.Press})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok && e.Buttons.Contain(pointer.ButtonSecondary) && txt != "" {
			gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(txt))})
			u.note = "Copied the message"
		}
	}
	m := op.Record(gtx.Ops)
	dims := w(gtx)
	call := m.Stop()
	defer clip.Rect{Max: dims.Size}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, tag)
	call.Add(gtx.Ops)
	return dims
}
