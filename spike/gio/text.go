package main

import (
	"image"
	"image/color"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	gtext "gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"github.com/EugeneShtoka/kith/internal/emoji"
	"github.com/EugeneShtoka/kith/internal/themespec"
)

var shortcode = regexp.MustCompile(`:([a-z0-9_+-]+):`)

// shortcodes turns each :name: kith knows into its emoji, Slack's names first as for
// Slack's reactions (internal/slack reactionKey); unknown names stay as typed. Slack
// puts a code straight after a word ("aligned:pray:"), so only a code inside a word on
// both sides is left alone, as "a:b:c" in a link.
func shortcodes(body string) string {
	var out strings.Builder
	last := 0
	for _, at := range shortcode.FindAllStringSubmatchIndex(body, -1) {
		if at[0] > 0 && isWordByte(body[at[0]-1]) && at[1] < len(body) && isWordByte(body[at[1]]) {
			continue
		}
		e, ok := emoji.SlackNames[body[at[2]:at[3]]]
		if !ok {
			e, ok = emoji.ByName(body[at[2]:at[3]])
		}
		if ok {
			out.WriteString(body[last:at[0]])
			out.WriteString(e)
			last = at[1]
		}
	}
	out.WriteString(body[last:])
	return out.String()
}

func isWordByte(b byte) bool {
	return b == '_' || b == '/' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Local().Date()
	by, bm, bd := b.Local().Date()
	return ay == by && am == bm && ad == bd
}

// layoutDay is the rule a new day starts with, its date in the middle.
func (u *ui) layoutDay(gtx C, at time.Time) D {
	format := "Monday, January 2"
	if at.Local().Year() != time.Now().Year() {
		format = "Monday, January 2, 2006"
	}
	// A day's line is the stronger one, in the accent; a sender's run has the border's.
	line := u.pal.accent
	line.A = 0x99
	rule := func(gtx C) D {
		size := image.Pt(gtx.Constraints.Min.X, gtx.Dp(1))
		paint.FillShape(gtx.Ops, line, clip.Rect{Max: size}.Op())
		return D{Size: size}
	}
	return layout.Inset{Left: unit.Dp(14), Right: unit.Dp(16), Top: unit.Dp(14), Bottom: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, rule),
			layout.Rigid(func(gtx C) D {
				l := material.Caption(u.th, at.Local().Format(format))
				l.Color, l.Font.Weight = u.pal.muted, font.Bold
				return layout.Inset{Left: unit.Dp(10), Right: unit.Dp(10)}.Layout(gtx, l.Layout)
			}),
			layout.Flexed(1, rule),
		)
	})
}

// senderColors is the TUI's and the desktop's sender palette, slot by slot.
var senderColors = func() []color.NRGBA {
	var out []color.NRGBA
	for _, hex := range themespec.SenderColors(64, nil) {
		v, _ := strconv.ParseUint(hex[1:], 16, 32)
		out = append(out, rgb(uint32(v)))
	}
	return out
}()

// color is sender's color: kithd's slot for them, else the next free one in this
// window (not saved). shortcut: slots are keyed by identity group, looked up here by
// the sender's ID alone; a merged person's other IDs get their own slot.
func (t *timeline) color(sender string) color.NRGBA {
	slot, ok := t.slots[sender]
	if !ok {
		slot = 0
		for taken(t.slots, slot) {
			slot++
		}
		t.slots[sender] = slot
	}
	return senderColors[slot%len(senderColors)]
}

func taken(slots map[string]int, slot int) bool {
	for _, s := range slots {
		if s == slot {
			return true
		}
	}
	return false
}

// layoutNew is the line over the first message that came after the read receipt as the
// room opened (the desktop's unread line): "New" at its end, in the mention color.
func (u *ui) layoutNew(gtx C) D {
	return layout.Inset{Left: unit.Dp(14), Right: unit.Dp(16), Top: unit.Dp(10)}.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				size := image.Pt(gtx.Constraints.Max.X, gtx.Dp(1))
				paint.FillShape(gtx.Ops, u.pal.mention, clip.Rect{Max: size}.Op())
				return D{Size: size}
			}),
			layout.Rigid(func(gtx C) D {
				l := material.Caption(u.th, "New")
				l.Color, l.Font.Weight, l.Alignment = u.pal.mention, font.Bold, gtext.End
				return l.Layout(gtx)
			}),
		)
	})
}
