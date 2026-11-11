package main

import (
	"context"
	"fmt"
	"image"
	"log"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// rail is kithd's rail for the window's profile: its rows, already ordered and
// filtered by kithd (domain.ShownRows), and the row the room list shows.
type rail struct {
	rows     []domain.RailRow
	holds    []map[domain.RoomID]bool // each row's rooms
	silenced map[domain.RoomID]bool   // a silent tag holds it: no unread counted
	sel      int
	btns     []widget.Clickable
	list     widget.List
}

// loadRail reads the rail, keeping the row the window is on when it is still there.
func (u *ui) loadRail(ctx context.Context) {
	served, err := u.backend.Rail(ctx)
	if err != nil {
		log.Printf("rail: %v", err)
		return
	}
	holds := make([]map[domain.RoomID]bool, len(served.Rows))
	for i, row := range served.Rows {
		holds[i] = make(map[domain.RoomID]bool, len(row.Rooms))
		for _, id := range row.Rooms {
			holds[i][id] = true
		}
	}
	silenced := map[domain.RoomID]bool{}
	for _, room := range served.Rooms {
		if room.Silenced {
			silenced[domain.RoomID(room.Facts.ID)] = true
		}
	}
	u.mu.Lock()
	sel := 0
	if old := u.rail.sel; old < len(u.rail.rows) {
		was := u.rail.rows[old]
		for i, row := range served.Rows {
			if row.Key == was.Key && row.Space == was.Space {
				sel = i
			}
		}
	}
	u.rail.rows, u.rail.holds, u.rail.silenced, u.rail.sel = served.Rows, holds, silenced, sel
	u.rail.btns = make([]widget.Clickable, len(served.Rows))
	u.rail.list.Axis = layout.Vertical
	u.mu.Unlock()
	u.w.Invalidate()
}

// inRow is whether the room list shows room: every room before the rail loads.
func (u *ui) inRow(room domain.RoomID) bool {
	if len(u.rail.rows) == 0 {
		return true
	}
	return u.rail.holds[u.rail.sel][room]
}

// rowUnread sums the unread of a row's rooms, silenced ones left out, as the desktop
// does (rail.ts rowUnread).
func (u *ui) rowUnread(i int) (count, mentions int) {
	for id := range u.rail.holds[i] {
		if u.rail.silenced[id] {
			continue
		}
		c, m := u.unread[id].Count(u.local)
		count, mentions = count+c, mentions+m
	}
	return count, mentions
}

func (u *ui) layoutRail(gtx C) D {
	r := &u.rail
	return material.List(u.th, &r.list).Layout(gtx, len(r.rows), func(gtx C, i int) D {
		row := r.rows[i]
		if r.btns[i].Clicked(gtx) {
			r.sel = i
			u.roomList.Position = layout.Position{}
		}
		count, mentions := u.rowUnread(i)
		item := func(gtx C) D {
			return r.btns[i].Layout(gtx, func(gtx C) D {
				if i == r.sel {
					size := image.Pt(gtx.Constraints.Max.X, gtx.Dp(34))
					paint.FillShape(gtx.Ops, u.pal.selection, clip.UniformRRect(image.Rectangle{Min: image.Pt(gtx.Dp(4), 0), Max: size}, gtx.Dp(6)).Op(gtx.Ops))
				}
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Left: unit.Dp(10), Right: unit.Dp(8), Top: unit.Dp(7), Bottom: unit.Dp(7)}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx C) D {
							label := row.Label
							if row.CountInLabel {
								label = fmt.Sprintf("%s %d", label, len(r.holds[i]))
							}
							l := material.Body2(u.th, label)
							l.MaxLines = 1
							l.Font.Weight = font.Bold
							if count == 0 {
								l.Font.Weight = font.Normal
								l.Color = u.pal.muted
							}
							return l.Layout(gtx)
						}),
						layout.Rigid(func(gtx C) D {
							if count == 0 {
								return D{}
							}
							return u.badge(gtx, fmt.Sprint(count), mentions > 0)
						}),
					)
				})
			})
		}
		if !row.SepAfter {
			return item(gtx)
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(item),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4), Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx, func(gtx C) D {
					size := image.Pt(gtx.Constraints.Max.X, gtx.Dp(1))
					paint.FillShape(gtx.Ops, u.pal.border, clip.Rect{Max: size}.Op())
					return D{Size: size}
				})
			}),
		)
	})
}
