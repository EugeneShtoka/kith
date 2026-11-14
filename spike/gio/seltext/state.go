// SPDX-License-Identifier: Unlicense OR MIT

package seltext

import (
	"image"
	"io"
	"strings"

	"gioui.org/f32"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
)

// State is one text's selection and clicks, kept across frames: a press starts a
// selection and a drag extends it; a press and release in place on a link clicks it.
// Ctrl+C copies the selection and Ctrl+A selects everything while the text has the
// focus, which a press gives it; losing the focus clears the selection. Secondary
// buttons are left to whatever is under the text.
type State struct {
	anchor, caret int // the selection's ends, bytes of the text; equal for none
	pressed       bool
	moved         bool
	pressAt       f32.Point
	clicked       []string

	text     string
	clusters []cluster
	placed   []placedShape
}

// Selection is the selected bytes of the text, from <= to; from == to for none.
func (s *State) Selection() (from, to int) {
	return min(s.anchor, s.caret), max(s.anchor, s.caret)
}

// SelectedText is the selected text.
func (s *State) SelectedText() string {
	from, to := s.Selection()
	if to > len(s.text) {
		return ""
	}
	return s.text[from:to]
}

// Clicked returns the next link clicked since the last call, if any.
func (s *State) Clicked() (string, bool) {
	if len(s.clicked) == 0 {
		return "", false
	}
	link := s.clicked[0]
	s.clicked = s.clicked[1:]
	return link, true
}

// remember keeps the frame's text and geometry for the next frame's events.
func (s *State) remember(text string, clusters []cluster, placed []placedShape) {
	if text != s.text {
		s.anchor, s.caret = 0, 0
	}
	s.text, s.clusters, s.placed = text, clusters, placed
}

func (s *State) update(gtx layout.Context) {
	for {
		ev, ok := gtx.Event(
			pointer.Filter{Target: s, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel},
			key.FocusFilter{Target: s},
			key.Filter{Focus: s, Name: "C", Required: key.ModShortcut},
			key.Filter{Focus: s, Name: "A", Required: key.ModShortcut},
		)
		if !ok {
			return
		}
		switch e := ev.(type) {
		case pointer.Event:
			s.pointer(gtx, e)
		case key.FocusEvent:
			if !e.Focus {
				s.anchor = s.caret
			}
		case key.Event:
			if e.State != key.Press {
				continue
			}
			switch e.Name {
			case "C":
				if t := s.SelectedText(); t != "" {
					gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(t))})
				}
			case "A":
				s.anchor, s.caret = 0, len(s.text)
			}
		}
	}
}

func (s *State) pointer(gtx layout.Context, e pointer.Event) {
	switch e.Kind {
	case pointer.Press:
		if !e.Buttons.Contain(pointer.ButtonPrimary) {
			return
		}
		gtx.Execute(key.FocusCmd{Tag: s})
		s.pressed, s.moved, s.pressAt = true, false, e.Position
		s.anchor = s.offsetAt(e.Position)
		s.caret = s.anchor
	case pointer.Drag:
		if !s.pressed {
			return
		}
		if d := e.Position.Sub(s.pressAt); d.X*d.X+d.Y*d.Y > 9 {
			s.moved = true
		}
		s.caret = s.offsetAt(e.Position)
	case pointer.Release, pointer.Cancel:
		if s.pressed && !s.moved && e.Kind == pointer.Release {
			if link := s.linkAt(e.Position); link != "" {
				s.clicked = append(s.clicked, link)
			}
		}
		s.pressed = false
	}
}

// offsetAt is the byte of the text nearest p: the start or the end of the cluster
// under it, whichever half p is in; past a line's end, the end of its last cluster.
func (s *State) offsetAt(p f32.Point) int {
	pt := image.Pt(int(p.X), int(p.Y))
	best, bestDist := 0, -1
	for _, c := range s.clusters {
		if pt.Y < c.box.Min.Y && bestDist >= 0 {
			break // lines are in order: a line below p ends the search
		}
		dy := 0
		switch {
		case pt.Y < c.box.Min.Y:
			dy = c.box.Min.Y - pt.Y
		case pt.Y >= c.box.Max.Y:
			dy = pt.Y - c.box.Max.Y + 1
		}
		dx := 0
		switch {
		case pt.X < c.box.Min.X:
			dx = c.box.Min.X - pt.X
		case pt.X >= c.box.Max.X:
			dx = pt.X - c.box.Max.X + 1
		}
		dist := dy*100000 + dx // a nearer line always wins over a nearer column
		if bestDist >= 0 && dist >= bestDist {
			continue
		}
		bestDist, best = dist, c.end
		if pt.X < (c.box.Min.X+c.box.Max.X)/2 {
			best = c.start
		}
	}
	return best
}

// linkAt is the link of the span under p, "" for none.
func (s *State) linkAt(p f32.Point) string {
	pt := image.Pt(int(p.X), int(p.Y))
	for _, ps := range s.placed {
		if ps.span.Link != "" && pt.In(image.Rectangle{Min: ps.offset, Max: ps.offset.Add(ps.size)}) {
			return ps.span.Link
		}
	}
	return ""
}

// area takes the pointer over size, with the text cursor, a hand over links.
func (s *State) area(gtx layout.Context, size image.Point) {
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	pointer.CursorText.Add(gtx.Ops)
	event.Op(gtx.Ops, s)
	for _, ps := range s.placed {
		if ps.span.Link == "" {
			continue
		}
		st := clip.Rect{Min: ps.offset, Max: ps.offset.Add(ps.size)}.Push(gtx.Ops)
		pointer.CursorPointer.Add(gtx.Ops)
		st.Pop()
	}
}
